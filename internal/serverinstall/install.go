// SPDX-License-Identifier: MPL-2.0
package serverinstall

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/user"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

var principalSyntax = regexp.MustCompile(`^[A-Z2-7]{26}$`)

// RunOperation is only the fixed systemd executor, never an arbitrary root
// action dispatcher. The unit owns its whole cgroup and survives SSH detachment.
func (m *Manager) RunOperation(ctx context.Context, id string) (Result, error) {
	// The enqueue caller briefly holds this same lock while systemd starts us.
	var unlock func()
	var e error
	waitCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	for {
		unlock, e = m.lock(waitCtx, false)
		if !errors.Is(e, ErrBusy) {
			break
		}
		select {
		case <-waitCtx.Done():
			return Result{}, waitCtx.Err()
		case <-time.After(50 * time.Millisecond):
		}
	}
	if e != nil {
		return Result{}, e
	}
	defer unlock()
	j, e := m.load(id)
	if e != nil {
		return Result{}, e
	}
	if j.State != "applying" || j.Frozen == "" || !m.inExecutor(j) {
		return result(j), ErrAuthority
	}
	image, e := currentManagerImage()
	if e != nil || digest(image) != j.ManagerSHA256 {
		return m.fail(j, "manager-drift", "The actual executing manager no longer matches the frozen image.", e)
	}
	actual, e := hashFile(filepath.Join(managerRoot, j.ManagerSHA256))
	if e != nil || actual != j.ManagerSHA256 {
		return m.fail(j, "manager-drift", "The retained trusted manager executable changed.")
	}
	owner, e := readPrivate(filepath.Join(m.stateRoot, "installation"), m.ownerUID, 128)
	if e != nil || string(owner) != id+"\n" {
		return m.fail(j, "installation-conflict", "The installation owner record does not match this operation.")
	}
	j.Attempts["executor"] = "verified"
	for _, stage := range []string{"payload", "account", "runtime-paths", "prepare", "migrate", "bootstrap-control", "bootstrap-site", "setup", "check", "service", "postflight"} {
		if e = ctx.Err(); e != nil {
			return m.fail(j, "interrupted", "Execution stopped; inspect the retained stage before continuation.")
		}
		if j.Attempts[stage] == "verified" {
			continue
		}
		j.Stage = stage
		if j.Attempts[stage] == "attempted" {
			// Only exact positive observations may reconcile an uncertain effect.
			if e = m.reconcile(ctx, j, stage); e != nil {
				return m.fail(j, "stage-outcome-unknown", "The "+stage+" stage needs operator reconciliation; inspect the executor journal and retained state.", e)
			}
		} else {
			j.Attempts[stage] = "attempted"
			j.Code = "stage-attempted"
			j.Message = "Executing " + stage + "."
			if e = m.save(j); e != nil {
				return Result{}, e
			}
			if e = m.execute(ctx, j, stage); e != nil {
				return m.fail(j, "stage-failed", "The "+stage+" stage did not verify. Inspect journalctl -u rixa-install-"+id+".service and retained diagnostics before another effect.", e)
			}
		}
		j.Attempts[stage] = "verified"
		j.Code = "stage-verified"
		if e = m.save(j); e != nil {
			return Result{}, e
		}
	}
	j.State, j.Code, j.Message = "installed", "installed", "Service installation and both initial administrator logins verified."
	j.Draft.ControlPassword, j.Draft.SitePassword = "", ""
	if e = m.save(j); e != nil {
		return Result{}, e
	}
	return result(j), nil
}
func (m *Manager) inExecutor(j *journal) bool {
	b, e := os.ReadFile("/proc/self/cgroup")
	if e != nil {
		return false
	}
	unit := "/" + executorName(j)
	for _, line := range strings.Split(string(b), "\n") {
		fields := strings.SplitN(line, ":", 3)
		if len(fields) == 3 && strings.HasSuffix(fields[2], unit) {
			return true
		}
	}
	return false
}
func (m *Manager) execute(ctx context.Context, j *journal, stage string) error {
	switch stage {
	case "payload":
		return m.installPayload(ctx, j)
	case "account":
		return m.createAccount(ctx, j)
	case "runtime-paths":
		return m.runtimePaths(j)
	case "prepare", "migrate", "bootstrap-control", "bootstrap-site", "setup", "check":
		r, e := m.childStage(ctx, j, stage)
		if e != nil {
			return e
		}
		if stage == "bootstrap-control" || stage == "bootstrap-site" {
			if !principalSyntax.MatchString(r.Principal) {
				return ErrState
			}
			if stage == "bootstrap-control" {
				j.ControlPrincipal = r.Principal
			} else {
				j.SitePrincipal = r.Principal
			}
		}
		return nil
	case "service":
		return m.installService(ctx, j)
	case "postflight":
		return m.postflight(ctx, j)
	default:
		return ErrState
	}
}
func (m *Manager) reconcile(ctx context.Context, j *journal, stage string) error {
	switch stage {
	case "payload":
		if e := m.checkPayload(j); e == nil {
			return nil
		}
		if _, e := os.Lstat(m.binaryPath(j)); errors.Is(e, os.ErrNotExist) {
			return m.installPayload(ctx, j)
		}
		return ErrState
	case "account":
		if e := m.observeAccount(j); e == nil {
			return nil
		}
		if j.UID == 0 {
			if _, e := user.Lookup("rixa"); e != nil {
				return m.createAccount(ctx, j)
			}
		}
		return ErrState
	case "runtime-paths":
		return m.runtimePaths(j)
	case "prepare", "migrate", "check":
		return m.execute(ctx, j, stage)
	case "bootstrap-control", "bootstrap-site", "setup":
		// These operations own durable attempted-effect records. The executor cgroup
		// and parent lock fence prior child work before any operator continuation.
		return m.execute(ctx, j, stage)
	case "service":
		if e := m.validateInstalled(ctx, j); e == nil {
			return nil
		}
		return m.installService(ctx, j)
	default:
		return ErrState
	}
}
func (m *Manager) installPayload(ctx context.Context, j *journal) error {
	dir, e := os.MkdirTemp(m.stateRoot, ".payload-")
	if e != nil {
		return ErrState
	}
	defer os.RemoveAll(dir)
	v, e := frozenInputs(ctx, j.Draft, dir)
	if e != nil || v.Status != "verified" || v.SHA256 != j.ArchiveSHA256 {
		m.retainDiagnostic(j, fmt.Sprintf("publisher code=%s field=%s detail=%s", v.Code, v.Field, v.Detail))
		return fmt.Errorf("%w: publisher code=%s field=%s", ErrPrerequisite, v.Code, v.Field)
	}
	for _, path := range []string{"/opt/rixa", releaseRoot} {
		if e = ownedPublicDir(path); e != nil {
			return e
		}
	}
	out, e := newPayloadStage(releaseRoot)
	if e != nil {
		return e
	}
	defer os.RemoveAll(filepath.Dir(out))
	binary, e := extractAuthenticated(filepath.Join(dir, "archive.tar.gz"), out, j.Draft)
	if e != nil {
		return e
	}
	j.BinarySHA256 = binary
	if e = m.save(j); e != nil {
		return e
	}
	if e = activatePayloadStage(out, releaseRoot, j.ArchiveSHA256); e != nil {
		return e
	}
	return m.checkPayload(j)
}
func (m *Manager) binaryPath(j *journal) string {
	return filepath.Join(releaseRoot, j.ArchiveSHA256, "rixa")
}
func (m *Manager) checkPayload(j *journal) error {
	if len(j.ArchiveSHA256) != 64 || len(j.BinarySHA256) != 64 {
		return ErrState
	}
	path := m.binaryPath(j)
	if !trustedExecutable(path) {
		return ErrState
	}
	h, e := hashFile(path)
	if e != nil || h != j.BinarySHA256 {
		return ErrState
	}
	return nil
}
func (m *Manager) createAccount(ctx context.Context, j *journal) error {
	if _, e := user.Lookup("rixa"); e == nil {
		return ErrState
	}
	if problem := unusedRuntimeGroup(user.LookupGroup); problem != nil {
		return fmt.Errorf("%w: %s", ErrState, problem.Message)
	}
	if _, e := m.run(ctx, "/usr/sbin/useradd", "--system", "--user-group", "--home-dir", "/var/lib/rixa", "--shell", "/usr/sbin/nologin", "--comment", "Rixa installation "+j.Draft.OperationID, "rixa"); e != nil {
		return e
	}
	return m.observeAccount(j)
}
func (m *Manager) observeAccount(j *journal) error {
	u, e := user.Lookup("rixa")
	if e != nil || u.HomeDir != "/var/lib/rixa" || u.Name != "Rixa installation "+j.Draft.OperationID {
		return ErrState
	}
	uid, e := intID(u.Uid)
	if e != nil {
		return e
	}
	gid, e := intID(u.Gid)
	if e != nil {
		return e
	}
	namedGroup, e := user.LookupGroup("rixa")
	if e != nil {
		return ErrState
	}
	memberships, e := u.GroupIds()
	if e != nil || !accountGroupMatches(namedGroup, u.Gid, j.GID, memberships) {
		return ErrState
	}
	b, e := os.ReadFile("/etc/passwd")
	if e != nil {
		return ErrState
	}
	found := false
	for _, line := range strings.Split(string(b), "\n") {
		f := strings.Split(line, ":")
		if len(f) == 7 && f[0] == "rixa" && f[2] == u.Uid && f[3] == u.Gid && f[6] == "/usr/sbin/nologin" {
			found = true
		}
	}
	if !found || j.UID != 0 && (j.UID != uid || j.GID != gid) {
		return ErrState
	}
	j.UID, j.GID = uid, gid
	return nil
}
func ownedDir(path string, uid, gid uint32, mode os.FileMode) error {
	if !trustedRootParent(path) {
		return ErrState
	}
	e := mkdirOwnedNew(path, mode, int(uid), int(gid))
	if e != nil && !errors.Is(e, os.ErrExist) {
		return ErrState
	}

	i, e := os.Lstat(path)
	if e != nil || !i.IsDir() || i.Mode().Perm() != mode || fileUID(i) != uid || fileGID(i) != gid {
		return ErrState
	}
	real, e := filepath.EvalSymlinks(path)
	if e != nil || real != path {
		return ErrState
	}
	return nil
}
func (m *Manager) runtimePaths(j *journal) error {
	if j.UID == 0 || j.GID == 0 {
		return ErrState
	}
	if e := ownedDir("/etc/rixa", 0, 0, 0755); e != nil {
		return e
	}
	if e := ownedDir(runtimeRoot, j.UID, j.GID, 0700); e != nil {
		return e
	}
	return ownedDir("/var/lib/rixa", j.UID, j.GID, 0700)
}
func (m *Manager) serviceBytes(j *journal) []byte {
	return []byte("[Unit]\nDescription=Rixa CMS\nStartLimitIntervalSec=60\nStartLimitBurst=3\nAfter=network-online.target\nWants=network-online.target\n\n[Service]\nType=exec\nUser=rixa\nGroup=rixa\nExecStart=" + m.binaryPath(j) + " run --config " + runtimeConfigPath + "\nEnvironmentFile=/etc/rixa/service.env\nWorkingDirectory=/var/lib/rixa\nUMask=0077\nRestart=on-failure\nRestartSec=5\nTimeoutStartSec=30\nTimeoutStopSec=30\nNoNewPrivileges=yes\nProtectSystem=strict\nProtectHome=yes\nPrivateTmp=yes\nReadWritePaths=/var/lib/rixa\nCapabilityBoundingSet=CAP_NET_BIND_SERVICE\nAmbientCapabilities=CAP_NET_BIND_SERVICE\nRestrictSUIDSGID=yes\nLockPersonality=yes\n\n[Install]\nWantedBy=multi-user.target\n")
}
func environmentFile(d Draft) []byte {
	quote := func(s string) string { return "\"" + strings.NewReplacer("\\", "\\\\", "\"", "\\\"").Replace(s) + "\"" }
	return []byte("RIXA_CONTROL_DSN=" + quote(d.ControlDSN) + "\nRIXA_SITE_DSN=" + quote(d.SiteDSN) + "\n")
}
func (m *Manager) installService(ctx context.Context, j *journal) error {
	if e := m.observeAccount(j); e != nil {
		return e
	}
	if e := m.checkPayload(j); e != nil {
		return e
	}
	if e := writeExpected("/etc/rixa/service.env", environmentFile(j.Draft), 0600, 0); e != nil {
		return ErrState
	}
	unit := m.serviceBytes(j)
	j.UnitSHA256 = digest(unit)
	if e := m.save(j); e != nil {
		return e
	}
	if e := writeExpected(unitPath, unit, 0644, 0); e != nil {
		return ErrState
	}
	if _, e := m.run(ctx, "/usr/bin/systemctl", "daemon-reload"); e != nil {
		return e
	}
	if e := m.loadedServiceOwned(ctx, j, false); e != nil {
		return e
	}
	if _, e := m.run(ctx, "/usr/bin/systemctl", "enable", "--now", "rixa.service"); e != nil {
		return e
	}
	readyCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	for {
		if e := m.validateInstalled(readyCtx, j); e == nil {
			return nil
		}
		select {
		case <-readyCtx.Done():
			return ErrPrerequisite
		case <-time.After(300 * time.Millisecond):
		}
	}
}
func (m *Manager) Remove(ctx context.Context, id string) (Result, error) {
	return m.remove(ctx, id, false)
}

// RemoveInstallation removes only the original installation, never the active
// child update that the ordinary CLI Remove method may follow.
func (m *Manager) RemoveInstallation(ctx context.Context, id string) (Result, error) {
	return m.remove(ctx, id, true)
}

func (m *Manager) remove(ctx context.Context, id string, installationOnly bool) (Result, error) {
	unlock, e := m.lock(ctx, false)
	if e != nil {
		return Result{}, e
	}
	defer unlock()
	if pending, pe := m.updateMarker("pending-update"); pe != nil {
		return Result{}, pe
	} else if pending != "" {
		return Result{}, ErrBusy
	}
	if u, ue := m.installationUpdate(id); ue != nil {
		return Result{}, ue
	} else if u != nil {
		if installationOnly {
			return Result{}, ErrState
		}
		return m.removeUpdatedInstallation(ctx, u)
	}
	j, e := m.load(id)
	if e != nil {
		return Result{}, e
	}
	if j.State == "removed" {
		return result(j), nil
	}
	if j.State != "installed" || j.Attempts["remove"] != "" {
		return result(j), ErrState
	}
	if e = m.checkOwnedService(j); e != nil {
		return result(j), e
	}
	if e = m.loadedServiceOwned(ctx, j, false); e != nil {
		return result(j), e
	}
	j.Stage = "remove"
	j.Attempts["remove"] = "attempted"
	if e = m.save(j); e != nil {
		return Result{}, e
	}
	if _, e = m.run(ctx, "/usr/bin/systemctl", "disable", "--now", "rixa.service"); e != nil {
		return m.fail(j, "remove-outcome-unknown", "Removal stop/disable did not verify; retain files and inspect the service.")
	}
	out, e := m.run(ctx, "/usr/bin/systemctl", "show", "--property=ActiveState", "--value", "rixa.service")
	if e != nil || strings.TrimSpace(string(out)) != "inactive" {
		return m.fail(j, "remove-service-active", "The owned service is not verified inactive; executables were retained.")
	}
	if e = m.checkOwnedService(j); e != nil {
		return m.fail(j, "remove-ownership-drift", "Owned executable/unit changed; no file was removed.")
	}
	if e = os.Remove(unitPath); e != nil {
		return m.fail(j, "remove-unit", "The unit could not be removed; inspect retained state.")
	}
	if e = os.Remove(m.binaryPath(j)); e != nil {
		return m.fail(j, "remove-binary", "The exact executable could not be removed; inspect retained state.")
	}
	if _, e = m.run(ctx, "/usr/bin/systemctl", "daemon-reload"); e != nil {
		return m.fail(j, "remove-reload", "Service files were removed but systemd reload needs inspection.")
	}
	j.State, j.Code, j.Message = "removed", "removed", "Owned service and executable removed. Databases, private configuration, Media, account and retained evidence remain."
	j.Attempts["remove"] = "verified"
	if e = m.save(j); e != nil {
		return Result{}, e
	}
	return result(j), nil
}
