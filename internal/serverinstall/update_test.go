// SPDX-License-Identifier: MPL-2.0
package serverinstall

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/AChWorks/rixa/internal/product"
)

func validUpdateDraft() UpdateDraft {
	return UpdateDraft{OperationID: "compatible-update", InstallationOperation: "first-install", ArchivePath: "/private/next.tar.gz", BundlePath: "/private/next.bundle.json", Release: "v9.8.7", SourceCommit: strings.Repeat("b", 40)}
}
func frozenUpdate() *updateJournal {
	d := validUpdateDraft()
	h := strings.Repeat("a", 64)
	return &updateJournal{Schema: updateSchema, Draft: d, State: "applying", Stage: "executor", Code: "queued", Frozen: updateDigest(d), ReviewToken: h, ParentSHA256: h, ActiveRelease: "v9.8.6", ConfigSHA256: h, EnvironmentSHA256: h, DatabaseTargets: map[string]string{"control": "1:100", "site-a": "1:101"}, ReviewedManagerSHA256: h, ManagerSHA256: h, HelperPath: "/usr/bin/gh", HelperSHA256: h, ArchiveSHA256: h, ExecutorGeneration: 1, ExecutorAcknowledged: true, Attempts: map[string]string{"executor": "attempted"}}
}
func installedParent(t *testing.T, m *Manager) *journal {
	t.Helper()
	d := validDraft()
	d.ControlPassword = ""
	d.SitePassword = ""
	h := strings.Repeat("a", 64)
	j := &journal{Schema: "rixa.native-install.v1", Draft: d, State: "installed", Stage: "postflight", Code: "installed", Frozen: draftDigest(d), ReviewToken: h, CertSHA256: h, KeySHA256: h, ArchiveSHA256: h, BinarySHA256: h, ManagerSHA256: h, ReviewedManagerSHA256: h, HelperPath: "/usr/bin/gh", HelperSHA256: h, ExecutorGeneration: 1, ExecutorAcknowledged: true, DatabaseTargets: map[string]string{"control": "1:100", "site-a": "1:101"}, UID: 1001, GID: 1001, ControlPrincipal: strings.Repeat("A", 26), SitePrincipal: strings.Repeat("B", 26), UnitSHA256: h, Attempts: map[string]string{"executor": "verified"}}
	for _, s := range []string{"payload", "account", "runtime-paths", "prepare", "migrate", "bootstrap-control", "bootstrap-site", "setup", "check", "service", "postflight"} {
		j.Attempts[s] = "verified"
	}
	if e := m.save(j); e != nil {
		t.Fatal(e)
	}
	if _, e := m.load(d.OperationID); e != nil {
		t.Fatalf("invalid parent fixture: %v", e)
	}
	return j
}
func TestUpdateDraftKeepsOriginalJournalAndCredentialsPrivate(t *testing.T) {
	m := testManager(t)
	ctx := context.Background()
	parent := installedParent(t, m)
	before, e := os.ReadFile(m.statePath(parent.Draft.OperationID))
	if e != nil {
		t.Fatal(e)
	}
	d := validUpdateDraft()
	r, e := m.SaveUpdateDraft(ctx, d)
	if e != nil {
		t.Fatal(e)
	}
	if r.OperationKind != "update" || r.InstallationOperation != d.InstallationOperation || r.Release != d.Release {
		t.Fatalf("bad update receipt: %+v", r)
	}
	got, e := m.LoadUpdateDraft(ctx, d.OperationID)
	if e != nil || got != d {
		t.Fatalf("draft changed: %v", e)
	}
	after, e := os.ReadFile(m.statePath(parent.Draft.OperationID))
	if e != nil || !bytes.Equal(before, after) {
		t.Fatal("draft rewrote immutable original installation")
	}
	info, e := os.Stat(m.updatePath(d.OperationID))
	if e != nil || info.Mode().Perm() != 0600 {
		t.Fatal("update journal not private")
	}
	b, _ := json.Marshal(r)
	if bytes.Contains(b, []byte("control-db-secret")) || bytes.Contains(b, []byte("site-db-secret")) {
		t.Fatal("receipt leaked original database credentials")
	}
	// A separately saved install operation cannot be overwritten by an update ID.
	d.OperationID = parent.Draft.OperationID
	d.InstallationOperation = "another-install"
	if _, e = m.SaveUpdateDraft(ctx, d); !errors.Is(e, ErrState) {
		t.Fatalf("install/update ID collision accepted: %v", e)
	}
}
func TestUpdateSharesInstallEffectLockAndStatusRemainsReadable(t *testing.T) {
	m := testManager(t)
	ctx := context.Background()
	d := validUpdateDraft()
	if _, e := m.SaveUpdateDraft(ctx, d); e != nil {
		t.Fatal(e)
	}
	unlock, e := m.lock(ctx, false)
	if e != nil {
		t.Fatal(e)
	}
	defer unlock()
	for name, call := range map[string]func() error{
		"install-save":  func() error { _, e := m.SaveDraft(ctx, validDraft()); return e },
		"update-save":   func() error { _, e := m.SaveUpdateDraft(ctx, d); return e },
		"update-resume": func() error { _, e := m.ResumeUpdate(ctx, d.OperationID); return e },
		"update-remove": func() error { _, e := m.RemoveUpdatedInstallation(ctx, d.OperationID); return e },
	} {
		if e := call(); !errors.Is(e, ErrBusy) {
			t.Fatalf("%s bypassed shared effect lock: %v", name, e)
		}
	}
	r, e := m.UpdateStatus(ctx, d.OperationID)
	if e != nil || r.State != "draft" {
		t.Fatalf("status blocked behind executor lock: %+v %v", r, e)
	}
}
func TestUpdateJournalRejectsUnsafeAmbiguousOrUnboundState(t *testing.T) {
	for _, name := range []string{"duplicate", "unknown-field", "unknown-stage", "retarget", "missing-parent", "missing-config", "missing-target", "bad-transport", "future-executor", "unsafe-mode", "symlink"} {
		t.Run(name, func(t *testing.T) {
			m := testManager(t)
			u := frozenUpdate()
			if e := m.saveUpdate(u); e != nil {
				t.Fatal(e)
			}
			path := m.updatePath(u.Draft.OperationID)
			b, e := os.ReadFile(path)
			if e != nil {
				t.Fatal(e)
			}
			switch name {
			case "duplicate":
				b = append([]byte("{\"schema\":\"forged\","), b[1:]...)
			case "unknown-field":
				b = append([]byte("{\"shell\":\"root\","), b[1:]...)
			case "unknown-stage":
				u.Attempts["shell"] = "attempted"
			case "retarget":
				u.Draft.SourceCommit = strings.Repeat("c", 40)
			case "missing-parent":
				u.ParentSHA256 = ""
			case "missing-config":
				u.ConfigSHA256 = ""
			case "missing-target":
				delete(u.DatabaseTargets, "site-a")
			case "bad-transport":
				u.TransportEnv = map[string]string{"PGPASSWORD": "secret"}
			case "future-executor":
				u.ExecutorGeneration = 17
			case "unsafe-mode":
				if e = os.Chmod(path, 0644); e != nil {
					t.Fatal(e)
				}
			case "symlink":
				if e = os.Remove(path); e != nil {
					t.Fatal(e)
				}
				if e = os.Symlink("/etc/passwd", path); e != nil {
					t.Fatal(e)
				}
			}
			if name != "duplicate" && name != "unknown-field" && name != "unsafe-mode" && name != "symlink" {
				b, _ = json.Marshal(u)
			}
			if name != "unsafe-mode" && name != "symlink" {
				if e = os.WriteFile(path, b, 0600); e != nil {
					t.Fatal(e)
				}
			}
			if _, e = m.loadUpdate(u.Draft.OperationID); e == nil {
				t.Fatal("unsafe or unbound update state accepted")
			}
		})
	}
}
func TestUpdateFenceRequiresKnownStartAndQuiescentExactUnit(t *testing.T) {
	for _, tc := range []struct {
		name, observed string
		ack            bool
		commandError   error
		want           bool
	}{
		{"unknown-start", "LoadState=not-found\nActiveState=inactive\nMainPID=0\nControlGroup=\n", false, ErrPrerequisite, false},
		{"active", "LoadState=loaded\nActiveState=active\nMainPID=120\nControlGroup=\n", true, nil, false},
		{"known-collected", "LoadState=not-found\nActiveState=inactive\nMainPID=0\nControlGroup=\n", true, ErrPrerequisite, true},
		{"transport-unknown", "LoadState=not-found\nActiveState=inactive\nMainPID=0\nControlGroup=\n", true, context.DeadlineExceeded, false},
		{"foreign-cgroup", "LoadState=loaded\nActiveState=failed\nMainPID=0\nControlGroup=/system.slice/foreign.service\n", true, nil, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := testManager(t)
			u := frozenUpdate()
			u.ExecutorAcknowledged = tc.ack
			m.run = func(_ context.Context, name string, args ...string) ([]byte, error) {
				if name != "/usr/bin/systemctl" || args[len(args)-1] != updateExecutorName(u) {
					t.Fatalf("unbound executor observation %s %v", name, args)
				}
				return []byte(tc.observed), tc.commandError
			}
			if got := m.updateExecutorFenced(context.Background(), u); got != tc.want {
				t.Fatalf("fence=%t want=%t", got, tc.want)
			}
		})
	}
}
func TestUpdateBindsLoadedExecStartAcrossUnitReplacement(t *testing.T) {
	m := testManager(t)
	parent := &journal{Draft: validDraft(), ArchiveSHA256: strings.Repeat("a", 64)}
	candidate := *parent
	candidate.ArchiveSHA256 = strings.Repeat("b", 64)
	if bytes.Equal(m.serviceBytes(parent), m.serviceBytes(&candidate)) {
		t.Fatal("unit identity did not change with candidate payload")
	}
	for _, tc := range []struct {
		name, exec string
		want       bool
	}{
		{"exact-candidate", "{ path=" + m.binaryPath(&candidate) + " ; argv[]=" + m.binaryPath(&candidate) + " run --config " + runtimeConfigPath + " ; ignore_errors=no ; }", true},
		{"stale-loaded-old", "{ path=" + m.binaryPath(parent) + " ; argv[]=" + m.binaryPath(parent) + " run --config " + runtimeConfigPath + " ; ignore_errors=no ; }", false},
		{"extra-argument", "{ path=" + m.binaryPath(&candidate) + " ; argv[]=" + m.binaryPath(&candidate) + " run --config " + runtimeConfigPath + " --unsafe ; }", false},
		{"missing-observation", "", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m.run = func(_ context.Context, _ string, args ...string) ([]byte, error) {
				if strings.Contains(strings.Join(args, " "), "--property=ExecStart") {
					return []byte(tc.exec), nil
				}
				return []byte("FragmentPath=" + unitPath + "\nDropInPaths=\nActiveState=inactive\nMainPID=0\n"), nil
			}
			e := m.loadedUpdateService(context.Background(), &candidate, false)
			if (e == nil) != tc.want {
				t.Fatalf("loaded candidate ownership=%v want=%t", e, tc.want)
			}
		})
	}
}
func TestOldManagerRemovalOrJournalChangeFencesQueuedUpdate(t *testing.T) {
	for _, change := range []string{"removed", "changed-installed-receipt"} {
		t.Run(change, func(t *testing.T) {
			m := testManager(t)
			parent := installedParent(t, m)
			u := frozenUpdate()
			original, e := os.ReadFile(m.statePath(parent.Draft.OperationID))
			if e != nil {
				t.Fatal(e)
			}
			u.ParentSHA256 = digest(original)
			// These are genuine v1 journal transitions/bytes that an old frozen
			// manager can write while no update executor holds the common lock.
			if change == "removed" {
				parent.State = "removed"
				parent.Stage = "remove"
				parent.Attempts["remove"] = "verified"
			} else {
				parent.Message = "Changed by another native owner operation."
			}
			if e = m.save(parent); e != nil {
				t.Fatal(e)
			}
			changed, e := os.ReadFile(m.statePath(parent.Draft.OperationID))
			if e != nil {
				t.Fatal(e)
			}
			if _, e = m.predecessor(u); !errors.Is(e, ErrState) {
				t.Fatalf("queued update adopted old-manager drift: %v", e)
			}
			final, e := os.ReadFile(m.statePath(parent.Draft.OperationID))
			if e != nil || !bytes.Equal(changed, final) {
				t.Fatal("reconciliation rewrote original journal")
			}
			if _, e = os.Stat(filepath.Join(m.stateRoot, "active-update")); !errors.Is(e, os.ErrNotExist) {
				t.Fatal("reconciliation recreated or activated an installation")
			}
		})
	}
}
func TestUpdateScopeAllowsPresentationButRejectsRetargeting(t *testing.T) {
	d := validDraft()
	base, e := product.ParseConfig(configBytes(d, strings.Repeat("A", 26), strings.Repeat("B", 26)))
	if e != nil {
		t.Fatal(e)
	}
	presentation := base
	presentation.Language = "fa"
	presentation.CatalogRoot = "/var/lib/rixa/locales"
	presentation.Resources.EditorialMaxOperations = 3
	if !supportedUpdateConfig(presentation, base) {
		t.Fatal("presentation/resource changes forced old generated config")
	}
	for _, tc := range []struct {
		name   string
		change func(*product.Config)
	}{
		{"listen", func(c *product.Config) { c.Listen = "0.0.0.0:9443" }},
		{"origin", func(c *product.Config) { c.Control.Origin = "https://other.rixa.test:8443" }},
		{"principal", func(c *product.Config) { c.Control.AdminPrincipal = strings.Repeat("C", 26) }},
		{"TLS", func(c *product.Config) { c.TLS.CertFile = "/other/cert.pem" }},
		{"extra-site", func(c *product.Config) { c.Sites = append(c.Sites, c.Sites[0]) }},
		{"media-root", func(c *product.Config) { c.Sites[0].MediaRoot = "/other/media" }},
		{"database-env", func(c *product.Config) { c.Sites[0].DatabaseEnv = "OTHER_DSN" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			actual := base
			actual.Sites = append([]product.SiteConfig(nil), base.Sites...)
			tc.change(&actual)
			if supportedUpdateConfig(actual, base) {
				t.Fatal("changed native scope accepted")
			}
		})
	}
}

func TestDisableUpdateServiceReconcilesLostAcknowledgementByObservation(t *testing.T) {
	m := testManager(t)
	ctx := context.Background()
	state := "enabled"
	disableCalls := 0
	m.run = func(_ context.Context, name string, args ...string) ([]byte, error) {
		if name != "/usr/bin/systemctl" {
			t.Fatalf("unexpected executable %q", name)
		}
		switch args[0] {
		case "disable":
			if strings.Join(args, " ") != "disable --now rixa.service" {
				t.Fatalf("unexpected disable %v", args)
			}
			disableCalls++
			state = "disabled"
			return nil, context.DeadlineExceeded // Effect happened; acknowledgement lost.
		case "show":
			if strings.Contains(strings.Join(args, " "), "--property=UnitFileState") {
				return []byte(state + "\n"), nil
			}
			return []byte("LoadState=loaded\nActiveState=inactive\nMainPID=0\nControlGroup=\n"), nil
		}
		t.Fatalf("unexpected command %v", args)
		return nil, ErrState
	}
	if e := m.disableUpdateService(ctx); !errors.Is(e, context.DeadlineExceeded) {
		t.Fatalf("lost ack assumed success: %v", e)
	}
	if !m.updateMigrationFenced(ctx) {
		t.Fatal("actual disabled/quiescent state not observed")
	}
	// Explicit continuation sees the completed effect. It never repeats the
	// unknown command or re-enables the old service after migration intent.
	if e := m.disableUpdateService(ctx); e != nil {
		t.Fatal(e)
	}
	if disableCalls != 1 {
		t.Fatalf("unknown disable was replayed %d times", disableCalls)
	}
}
func TestMigrationRequiresDisabledAndPositivelyQuiescentService(t *testing.T) {
	for _, tc := range []struct {
		name, enabled, observed string
		want                    bool
	}{
		{"disabled-quiescent", "disabled", "LoadState=loaded\nActiveState=inactive\nMainPID=0\nControlGroup=\n", true},
		{"reboot-unsafe-enabled", "enabled", "LoadState=loaded\nActiveState=inactive\nMainPID=0\nControlGroup=\n", false},
		{"still-running", "disabled", "LoadState=loaded\nActiveState=active\nMainPID=999\nControlGroup=\n", false},
		{"unknown-cgroup", "disabled", "LoadState=loaded\nActiveState=inactive\nMainPID=0\nControlGroup=/foreign.slice/x.service\n", false},
		{"masked-by-operator", "masked", "LoadState=loaded\nActiveState=inactive\nMainPID=0\nControlGroup=\n", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := testManager(t)
			m.run = func(_ context.Context, _ string, args ...string) ([]byte, error) {
				if args[0] != "show" {
					t.Fatal("fence check mutated service")
				}
				if strings.Contains(strings.Join(args, " "), "--property=UnitFileState") {
					return []byte(tc.enabled + "\n"), nil
				}
				return []byte(tc.observed), nil
			}
			if got := m.updateMigrationFenced(context.Background()); got != tc.want {
				t.Fatalf("migration fence=%t want=%t", got, tc.want)
			}
		})
	}
}

func TestOrdinaryResumeUsesPendingUpdateReceiptAndItsUnknownStartFence(t *testing.T) {
	m := testManager(t)
	parent := installedParent(t, m)
	u := frozenUpdate()
	u.ExecutorAcknowledged = false
	if e := m.saveUpdate(u); e != nil {
		t.Fatal(e)
	}
	if e := m.claimUpdate(u.Draft.OperationID); e != nil {
		t.Fatal(e)
	}
	r, e := m.Resume(context.Background(), parent.Draft.OperationID)
	if !errors.Is(e, ErrState) || r.OperationKind != "update" || r.OperationID != u.Draft.OperationID || r.InstallationOperation != parent.Draft.OperationID {
		t.Fatalf("ordinary resume bypassed pending update protocol: %+v %v", r, e)
	}
	if len(r.Actions) != 1 || r.Actions[0] != "status" {
		t.Fatal("unknown start offered an invented continuation")
	}
}
