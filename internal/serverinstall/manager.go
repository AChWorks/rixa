// SPDX-License-Identifier: MPL-2.0
package serverinstall

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"net"
	"net/url"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/AChWorks/rixa/internal/product"
	"github.com/AChWorks/rixa/internal/releaseartifact"
	"github.com/jackc/pgx/v5"
)

const (
	runtimeRoot       = "/etc/rixa/runtime"
	runtimeConfigPath = runtimeRoot + "/rixa.json"
	unitPath          = "/etc/systemd/system/rixa.service"
	releaseRoot       = "/opt/rixa/releases"
	managerRoot       = "/opt/rixa-manager"
)

type Manager struct {
	stateRoot string
	ownerUID  int
	run       func(context.Context, string, ...string) ([]byte, error)
	verify    func(context.Context, releaseartifact.Request) (releaseartifact.Result, error)
}

func New() *Manager {
	return &Manager{stateRoot: "/var/lib/rixa-manager", ownerUID: 0, run: fixedCommand, verify: releaseartifact.Verify}
}
func fixedCommand(parent context.Context, name string, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(parent, 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Env = []string{"PATH=/usr/sbin:/usr/bin:/sbin:/bin", "LANG=C.UTF-8", "LC_ALL=C.UTF-8"}
	// No shell, inherited credentials, user-selected executable or unlimited output.
	var out boundedOutput
	cmd.Stdout = &out
	cmd.Stderr = &out
	e := cmd.Run()
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	if e != nil {
		return nil, ErrPrerequisite
	}
	return out.Bytes(), nil
}
func (m *Manager) SaveDraft(ctx context.Context, d Draft) (Result, error) {
	if e := m.authorize(); e != nil {
		return Result{}, e
	}
	if p := ValidateDraft(d); len(p) > 0 {
		return Result{OperationID: d.OperationID, State: "draft", Code: "invalid-input", Problems: p, Actions: []string{"configure"}}, ErrInput
	}
	unlock, e := m.lock(ctx, true)
	if e != nil {
		return Result{}, e
	}
	defer unlock()
	if old, e := m.load(d.OperationID); e == nil {
		if old.Frozen != "" || len(old.Attempts) != 0 {
			return result(old), ErrState
		}
	} else if !errors.Is(e, os.ErrNotExist) {
		return Result{}, e
	}
	j := &journal{Schema: "rixa.native-install.v1", Draft: d, State: "draft", Code: "draft-saved", Message: "Private draft saved. Review prerequisites before applying.", Attempts: map[string]string{}}
	if e = m.save(j); e != nil {
		return Result{}, e
	}
	return result(j), nil
}
func (m *Manager) LoadDraft(ctx context.Context, id string) (Draft, error) {
	unlock, e := m.lock(ctx, false)
	if e != nil {
		return Draft{}, e
	}
	defer unlock()
	j, e := m.load(id)
	if e != nil {
		return Draft{}, e
	}
	return j.Draft, nil
}
func (m *Manager) Review(ctx context.Context, id string) (Result, error) {
	unlock, e := m.lock(ctx, false)
	if e != nil {
		return Result{}, e
	}
	defer unlock()
	j, e := m.load(id)
	if e != nil {
		return Result{}, e
	}
	if j.Frozen != "" {
		return result(j), ErrState
	}
	p := m.review(ctx, j)
	j.State, j.Stage, j.Code, j.Message = "ready", "review", "ready", "Prerequisites verified. Apply performs the displayed native installation."
	if len(p) > 0 {
		j.State, j.Code, j.Message = "blocked", "prerequisite-required", "Correct the reported prerequisites and review this same draft again."
	}
	if e = m.save(j); e != nil {
		return Result{}, e
	}
	r := result(j)
	r.Problems = p
	if len(p) > 0 {
		return r, ErrPrerequisite
	}
	return r, nil
}
func (m *Manager) review(ctx context.Context, j *journal) []Problem {
	d := j.Draft
	p := ValidateDraft(d)
	add := func(f, c, s string) { p = append(p, Problem{f, c, s}) }
	if runtime.GOOS != "linux" || runtime.GOARCH != "amd64" {
		add("host", "unsupported", "This native profile requires Linux amd64.")
	}
	osRelease, e := os.ReadFile("/etc/os-release")
	if e != nil || !strings.Contains("\n"+string(osRelease), "\nID=debian\n") || !strings.Contains("\n"+string(osRelease), "\nVERSION_ID=\"13\"\n") {
		add("host", "unsupported", "This native profile requires Debian 13.")
	}
	if _, e = os.Stat("/run/systemd/system"); e != nil {
		add("service", "systemd-required", "A running systemd system manager is required.")
	}
	if _, e = m.run(ctx, "/usr/bin/systemctl", "show", "--property=Version"); e != nil {
		add("service", "systemd-unavailable", "The system service manager is unavailable.")
	}
	if _, e = user.Lookup("rixa"); e == nil && j.UID == 0 {
		add("runtime_user", "ownership", "An existing rixa account is not owned by this operation; inspect it instead of replacing it.")
	}
	for _, path := range []string{runtimeRoot, "/var/lib/rixa", releaseRoot, unitPath, unitPath + ".d", "/etc/rixa/service.env"} {
		if _, e = os.Lstat(path); e == nil {
			add("host", "existing-material", "Existing Rixa paths require the retained owning operation; first installation will not overwrite them.")
		} else if !errors.Is(e, os.ErrNotExist) {
			add("host", "path-unavailable", "A required installation path cannot be inspected.")
		}
	}
	cert, e1 := readRegular(d.TLSCertPath, 1<<20)
	key, e2 := readRegular(d.TLSKeyPath, 1<<20)
	if e1 != nil || e2 != nil {
		add("tls_cert", "tls-required", "Supply readable bounded regular TLS certificate/key files.")
	}
	if e1 == nil && e2 == nil {
		pair, e := tls.X509KeyPair(cert, key)
		if e != nil || len(pair.Certificate) == 0 {
			add("tls_key", "tls-pair", "The certificate and private key must form a valid TLS pair.")
		} else {
			leaf, e := x509.ParseCertificate(pair.Certificate[0])
			if e != nil || time.Now().Before(leaf.NotBefore) || !time.Now().Before(leaf.NotAfter) {
				add("tls_cert", "tls-validity", "The TLS certificate must be currently valid.")
			} else {
				for _, o := range []string{d.ControlOrigin, d.SiteOrigin} {
					u, _ := url.Parse(o)
					if u == nil || leaf.VerifyHostname(u.Hostname()) != nil {
						add("tls_cert", "tls-hostnames", "The certificate must cover both exact control and site hostnames.")
					}
				}
			}
		}
	}
	c, e := product.ParseConfig(configBytes(d, "", ""))
	if e == nil {
		targets, e := c.DatabaseTargets(ctx, databaseLookup(d))
		if e != nil {
			add("control_dsn", "database-unavailable", "Provide distinct reachable PostgreSQL databases with schema and physical-identity permissions.")
		} else {
			for _, t := range targets {
				pc, e := pgx.ParseConfig(t.DSN)
				if e != nil {
					add("control_dsn", "database-input", "The PostgreSQL connection cannot be parsed.")
					continue
				}
				pc.ConnectTimeout = time.Second
				conn, e := pgx.ConnectConfig(ctx, pc)
				if e != nil {
					add("control_dsn", "database-unavailable", "PostgreSQL cannot be reached.")
					continue
				}
				var version int
				e = conn.QueryRow(ctx, "SHOW server_version_num").Scan(&version)
				_ = conn.Close(ctx)
				if e != nil || version/10000 != 18 {
					add("control_dsn", "database-version", "This verified native profile requires PostgreSQL 18; no database major upgrade is performed.")
				}
			}
		}
	}
	if len(p) > 0 {
		return p
	}
	j.CertSHA256, j.KeySHA256 = digest(cert), digest(key)
	v, e := m.verify(ctx, releaseartifact.Request{ArchivePath: d.ArchivePath, BundlePath: d.BundlePath, ReleaseTag: d.Release, SourceCommit: d.SourceCommit})
	if e != nil || v.Status != "verified" {
		add("archive", v.Code, "Publisher verification failed. Inspect the selected archive/bundle/tag/source and trusted gh helper.")
	} else {
		j.ArchiveSHA256 = v.SHA256
	}
	if host, port, e := net.SplitHostPort(d.Listen); e == nil {
		l, e := net.Listen("tcp", net.JoinHostPort(host, port))
		if e != nil {
			add("listen", "occupied", "The selected listener is unavailable; choose a free port or inspect its owner.")
		} else {
			_ = l.Close()
		}
	}
	return p
}
func databaseLookup(d Draft) func(string) (string, bool) {
	return func(k string) (string, bool) {
		switch k {
		case "RIXA_CONTROL_DSN":
			return d.ControlDSN, true
		case "RIXA_SITE_DSN":
			return d.SiteDSN, true
		}
		return "", false
	}
}
func (m *Manager) Apply(ctx context.Context, id string) (Result, error) {
	unlock, e := m.lock(ctx, false)
	if e != nil {
		return Result{}, e
	}
	defer unlock()
	j, e := m.load(id)
	if e != nil {
		return Result{}, e
	}
	if j.Frozen != "" {
		return result(j), nil
	} // never blindly queue an uncertain prior write
	if j.State != "ready" {
		return result(j), ErrState
	}
	priorArchive, priorCert, priorKey := j.ArchiveSHA256, j.CertSHA256, j.KeySHA256
	if p := m.review(ctx, j); len(p) > 0 {
		r := result(j)
		r.Problems = p
		return r, ErrPrerequisite
	}
	if priorArchive != j.ArchiveSHA256 || priorCert != j.CertSHA256 || priorKey != j.KeySHA256 {
		return result(j), ErrState
	}
	for _, s := range []struct{ source, suffix, hash string }{{j.Draft.TLSCertPath, ".tls-cert", j.CertSHA256}, {j.Draft.TLSKeyPath, ".tls-key", j.KeySHA256}} {
		b, e := readRegular(s.source, 1<<20)
		if e != nil || digest(b) != s.hash {
			return result(j), ErrState
		}
		target := filepath.Join(m.stateRoot, id+s.suffix)
		if e = writeNew(target, b, 0600); e != nil {
			old, oe := readPrivate(target, m.ownerUID, 1<<20)
			if oe != nil || digest(old) != s.hash {
				return result(j), ErrState
			}
		}
	}
	ownerPath := filepath.Join(m.stateRoot, "installation")
	if e = writeNew(ownerPath, []byte(id+"\n"), 0600); e != nil {
		old, oe := readPrivate(ownerPath, m.ownerUID, 128)
		if oe != nil || string(old) != id+"\n" {
			return result(j), ErrState
		}
	}
	manager, e := m.snapshotManager()
	if e != nil {
		return result(j), e
	}
	j.ManagerSHA256 = filepath.Base(manager)
	j.Frozen = draftDigest(j.Draft)
	j.State, j.Stage, j.Code, j.Message = "applying", "executor", "queued", "Installation is supervised independently of this terminal. Refresh status."
	j.Attempts["executor"] = "attempted"
	if e = m.save(j); e != nil {
		return Result{}, e
	}
	_, e = m.run(ctx, "/usr/bin/systemd-run", "--unit=rixa-install-"+id, "--property=Type=exec", "--property=RuntimeMaxSec=600", "--property=KillMode=control-group", "--collect", manager, "manage", "internal-apply", id)
	if e != nil {
		j.State, j.Code, j.Message = "reconciliation-required", "executor-acknowledgement-unknown", "Inspect the retained operation and rixa-install-"+id+" unit; do not replay the start."
		_ = m.save(j)
		return result(j), ErrState
	}
	// The worker takes this same lock after systemd-run returns. Acceptance is not
	// inferred from the enqueue acknowledgement.
	return result(j), nil
}
func (m *Manager) snapshotManager() (string, error) {
	exe, e := os.Executable()
	if e != nil {
		return "", ErrState
	}
	exe, e = filepath.EvalSymlinks(exe)
	if e != nil || !trustedExecutable(exe) {
		return "", ErrAuthority
	}
	b, e := readRegular(exe, 64<<20)
	if e != nil {
		return "", e
	}
	if e = ownedPublicDir(managerRoot); e != nil {
		return "", e
	}
	target := filepath.Join(managerRoot, digest(b))
	if e = writeNew(target, b, 0755); e != nil {
		h, he := hashFile(target)
		if he != nil || h != digest(b) || !trustedExecutable(target) {
			return "", ErrState
		}
	}
	return target, nil
}
func trustedExecutable(p string) bool {
	for {
		i, e := os.Lstat(p)
		if e != nil || i.Mode()&os.ModeSymlink != 0 || fileUID(i) != 0 || i.Mode().Perm()&0022 != 0 {
			return false
		}
		if p == "/" {
			break
		}
		p = filepath.Dir(p)
	}
	return true
}
func ownedPublicDir(p string) error {
	if e := os.Mkdir(p, 0755); e != nil && !errors.Is(e, os.ErrExist) {
		return ErrState
	}
	i, e := os.Lstat(p)
	if e != nil || !i.IsDir() || fileUID(i) != 0 || i.Mode().Perm() != 0755 {
		return ErrState
	}
	real, e := filepath.EvalSymlinks(p)
	if e != nil || real != p {
		return ErrState
	}
	return nil
}
func (m *Manager) Status(ctx context.Context, id string) (Result, error) {
	if e := m.authorize(); e != nil {
		return Result{}, e
	}
	if !operationSyntax.MatchString(id) {
		return Result{}, ErrInput
	}
	if _, e := os.Lstat(m.stateRoot); errors.Is(e, os.ErrNotExist) {
		return Result{OperationID: id, State: "not-installed", Code: "not-installed", Message: "No native manager state exists for this host.", Actions: []string{"configure", "status"}}, nil
	}

	if e := privateDir(m.stateRoot, m.ownerUID, false); e != nil {
		return Result{}, e
	}
	j, e := m.load(id)
	if errors.Is(e, os.ErrNotExist) {
		return Result{OperationID: id, State: "not-installed", Code: "not-installed", Message: "This operation has no saved draft.", Actions: []string{"configure", "status"}}, nil
	}
	if e != nil {
		return Result{}, e
	}
	r := result(j)
	if j.State == "applying" || j.State == "reconciliation-required" {
		out, e := m.run(ctx, "/usr/bin/systemctl", "show", "--property=ActiveState", "--value", "rixa-install-"+id+".service")
		if e == nil && (strings.TrimSpace(string(out)) == "active" || strings.TrimSpace(string(out)) == "activating") {
			r.Message = "Installation executor is active; refresh retained progress."
			return r, nil
		}
		if j.State == "applying" {
			r.State = "reconciliation-required"
			r.Code = "executor-not-active"
			r.Message = "Executor is no longer active. Inspect its journal and retained stage before further effects."
		}
	}
	if j.State == "installed" {
		if e = m.validateInstalled(ctx, j); e != nil {
			r.Code = "installed-unhealthy"
			r.Message = "Retained installation exists, but current service/configuration/identity checks did not pass. Inspect systemctl status rixa.service and its journal."
			r.Actions = []string{"status"}
			return r, ErrPrerequisite
		}
		r.Code = "healthy"
		r.Message = "Owned service, active executable and local TLS administration endpoint are verified."
	}
	return r, nil
}
func intID(s string) (uint32, error) {
	n, e := strconv.ParseUint(s, 10, 32)
	if e != nil || n == 0 {
		return 0, ErrState
	}
	return uint32(n), nil
}
func (m *Manager) fail(j *journal, code, message string) (Result, error) {
	j.State, j.Code, j.Message = "reconciliation-required", code, message
	_ = m.save(j)
	return result(j), ErrState
}
