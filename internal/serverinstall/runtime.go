// SPDX-License-Identifier: MPL-2.0
package serverinstall

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"reflect"
	"strconv"
	"syscall"
	"time"

	"github.com/AChWorks/rixa/internal/product"
)

type runtimeTask struct {
	DatabaseTargets  map[string]string `json:"database_targets"`
	OperationID      string            `json:"operation_id"`
	UID              uint32            `json:"uid"`
	GID              uint32            `json:"gid"`
	Draft            Draft             `json:"draft"`
	CertPEM          []byte            `json:"cert_pem,omitempty"`
	KeyPEM           []byte            `json:"key_pem,omitempty"`
	ControlPrincipal string            `json:"control_principal,omitempty"`
	SitePrincipal    string            `json:"site_principal,omitempty"`
	SiteBPrincipal   string            `json:"site_b_principal,omitempty"`
}
type stageReceipt struct {
	Stage     string `json:"stage"`
	Principal string `json:"principal,omitempty"`
	Code      string `json:"code"`
}
type boundedOutput struct {
	// Keep the buffer private: promoted ReadFrom/WriteString would let
	// io.Copy/io.WriteString bypass Write and its child-output bound.
	buffer   bytes.Buffer
	exceeded bool
}

func (b *boundedOutput) Bytes() []byte  { return b.buffer.Bytes() }
func (b *boundedOutput) String() string { return b.buffer.String() }
func (b *boundedOutput) Len() int       { return b.buffer.Len() }
func (b *boundedOutput) Write(p []byte) (int, error) {
	if b.Len()+len(p) > 64<<10 {
		b.exceeded = true
		return 0, errors.New("bounded child output exceeded")
	}
	return b.buffer.Write(p)
}

// runtimeDatabaseBindingError distinguishes unavailable observations from
// changed physical bindings without rendering an underlying DSN, SQL or
// driver error into the root-owned retained diagnostic journal.
func runtimeDatabaseBindingError(observed, expected map[string]string, probeErr error) error {
	if probeErr != nil {
		return fmt.Errorf("%w: runtime database identity observation unavailable", ErrState)
	}
	if len(expected) == 0 || !reflect.DeepEqual(observed, expected) {
		return fmt.Errorf("%w: runtime database identity binding mismatch", ErrState)
	}
	return nil
}

// RuntimeStage is a deliberately fixed non-root child boundary. It receives
// secret-bearing typed input through a pipe, never command arguments or logs.
// It cannot select privileged commands or change any host management path.
func RuntimeStage(parent context.Context, args []string) error {
	if len(args) != 1 || os.Geteuid() == 0 {
		return ErrAuthority
	}
	var task runtimeTask
	input, e := io.ReadAll(io.LimitReader(os.Stdin, (3<<20)+1))
	if e != nil || len(input) > 3<<20 || decode(input, &task) != nil {
		return ErrInput
	}
	if !operationSyntax.MatchString(task.OperationID) || task.OperationID != task.Draft.OperationID || task.UID != uint32(os.Geteuid()) || task.GID != uint32(os.Getegid()) {
		return ErrInput
	}
	account, e := user.Lookup("rixa")
	if e != nil || account.Uid != strconv.Itoa(os.Geteuid()) || account.Gid != strconv.Itoa(os.Getegid()) {
		return ErrAuthority
	}
	if e = privateDir(runtimeRoot, os.Geteuid(), false); e != nil {
		return e
	}
	ctx, cancel := context.WithTimeout(parent, 90*time.Second)
	defer cancel()
	observed, e := databaseIdentities(ctx, task.Draft)
	if e = runtimeDatabaseBindingError(observed, task.DatabaseTargets, e); e != nil {
		return e
	}
	receipt := stageReceipt{Stage: args[0], Code: "verified"}
	switch args[0] {
	case "prepare":
		paths := []string{runtimeRoot + "/tls", "/var/lib/rixa/site-a", "/var/lib/rixa/site-a/media", "/var/lib/rixa/site-a/public"}
		if siteBRequested(task.Draft) {
			paths = append(paths, "/var/lib/rixa/site-b", "/var/lib/rixa/site-b/media", "/var/lib/rixa/site-b/public")
		}
		for _, p := range paths {
			if e = privateDir(p, os.Geteuid(), true); e != nil {
				return e
			}
		}
		if len(task.CertPEM) == 0 || len(task.KeyPEM) == 0 || len(task.CertPEM) > 1<<20 || len(task.KeyPEM) > 1<<20 {
			return ErrInput
		}
		if e = writeExpected(runtimeRoot+"/tls/cert.pem", task.CertPEM, 0600, os.Geteuid()); e != nil {
			return e
		}
		if e = writeExpected(runtimeRoot+"/tls/key.pem", task.KeyPEM, 0600, os.Geteuid()); e != nil {
			return e
		}
	case "migrate":
		c, e := product.ParseConfig(configBytes(task.Draft, "", ""))
		if e != nil {
			return e
		}
		if e = product.Migrate(ctx, c, databaseLookup(task.Draft)); e != nil {
			return fmt.Errorf("runtime stage %s failed: %w", args[0], e)
		}
	case "bootstrap-control", "bootstrap-site", "bootstrap-site-b":
		scope, login, password, path := "control", task.Draft.ControlLogin, task.Draft.ControlPassword, runtimeRoot+"/bootstrap-control.json"
		if args[0] == "bootstrap-site" {
			scope, login, password, path = "site:site-a", task.Draft.SiteLogin, task.Draft.SitePassword, runtimeRoot+"/bootstrap-site.json"
		} else if args[0] == "bootstrap-site-b" {
			if !siteBRequested(task.Draft) {
				return ErrInput
			}
			scope, login, password, path = "site:site-b", task.Draft.SiteBLogin, task.Draft.SiteBPassword, runtimeRoot+"/bootstrap-site-b.json"
		}
		r, e := product.ApplyBootstrapOperation(ctx, product.BootstrapOperationRequest{
			OperationID: task.OperationID + "-" + args[0], StatePath: path, Configuration: configBytes(task.Draft, "", ""), Scope: scope, Login: login, Password: password,
		}, databaseLookup(task.Draft), slog.New(slog.NewJSONHandler(os.Stderr, nil)))
		if e != nil {
			return fmt.Errorf("runtime stage %s failed: %w", args[0], e)
		}
		receipt.Principal = r.Principal
	case "setup":
		data := configBytesWithSiteB(task.Draft, task.ControlPrincipal, task.SitePrincipal, task.SiteBPrincipal)
		r, e := product.ApplySetup(ctx, product.SetupRequest{OperationID: task.OperationID + "-config", TargetPath: runtimeConfigPath, Configuration: data}, databaseLookup(task.Draft))
		if e != nil {
			return fmt.Errorf("runtime stage %s failed: %w", args[0], e)
		}
		if r.State != product.SetupVerified {
			return fmt.Errorf("%w: setup state=%s code=%s", ErrPrerequisite, r.State, r.Code)
		}
	case "check":
		c, e := product.ParseConfig(configBytesWithSiteB(task.Draft, task.ControlPrincipal, task.SitePrincipal, task.SiteBPrincipal))
		if e != nil {
			return e
		}
		resolved, e := c.ResolveRuntime(ctx, databaseLookup(task.Draft))
		if e != nil {
			return fmt.Errorf("runtime stage %s failed: %w", args[0], e)
		}
		runtime, e := product.BuildRuntime(resolved, slog.New(slog.NewJSONHandler(os.Stderr, nil)))
		if e != nil {
			return fmt.Errorf("runtime stage %s failed: %w", args[0], e)
		}
		if e = product.Check(ctx, runtime); e != nil {
			return fmt.Errorf("runtime stage %s failed: %w", args[0], e)
		}
	default:
		return ErrInput
	}
	return json.NewEncoder(os.Stdout).Encode(receipt)
}
func (m *Manager) childStage(ctx context.Context, j *journal, stage string) (stageReceipt, error) {
	task := runtimeTask{DatabaseTargets: j.DatabaseTargets, OperationID: j.Draft.OperationID, UID: j.UID, GID: j.GID, Draft: j.Draft, ControlPrincipal: j.ControlPrincipal, SitePrincipal: j.SitePrincipal, SiteBPrincipal: j.SiteBPrincipal}
	if stage == "prepare" {
		var e error
		task.CertPEM, e = readPrivate(m.tlsSnapshotPath(j, false), m.ownerUID, 1<<20)
		if e != nil {
			return stageReceipt{}, e
		}
		task.KeyPEM, e = readPrivate(m.tlsSnapshotPath(j, true), m.ownerUID, 1<<20)
		if e != nil {
			return stageReceipt{}, e
		}
		if digest(task.CertPEM) != j.CertSHA256 || digest(task.KeyPEM) != j.KeySHA256 {
			return stageReceipt{}, ErrState
		}
	}
	b, e := json.Marshal(task)
	if e != nil {
		return stageReceipt{}, ErrInput
	}
	executable := filepath.Join(managerRoot, j.ManagerSHA256)
	if !trustedExecutable(executable) {
		return stageReceipt{}, ErrAuthority
	}
	actual, e := hashFile(executable)
	if e != nil || actual != j.ManagerSHA256 {
		return stageReceipt{}, ErrState
	}
	childCtx, cancel := context.WithTimeout(ctx, 100*time.Second)
	defer cancel()
	cmd := exec.CommandContext(childCtx, executable, "manage", "internal-stage", stage)
	cmd.SysProcAttr = &syscall.SysProcAttr{Credential: &syscall.Credential{Uid: j.UID, Gid: j.GID, Groups: []uint32{j.GID}}}
	cmd.Stdin = bytes.NewReader(b)
	cmd.Env = []string{"PATH=/usr/sbin:/usr/bin:/sbin:/bin", "LANG=C.UTF-8", "LC_ALL=C.UTF-8", "HOME=/var/lib/rixa"}
	var out, diagnostic boundedOutput
	cmd.Stdout = &out
	cmd.Stderr = &diagnostic
	e = cmd.Run()
	if diagnostic.Len() > 0 {
		m.retainDiagnostic(j, diagnostic.String())
	}
	if e != nil || out.exceeded || diagnostic.exceeded {
		return stageReceipt{}, fmt.Errorf("runtime child %s: %w", stage, e)
	}
	var r stageReceipt
	if decode(out.Bytes(), &r) != nil || r.Stage != stage || r.Code != "verified" {
		return r, ErrState
	}
	return r, nil
}
