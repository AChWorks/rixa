// SPDX-License-Identifier: MPL-2.0
package serverinstall

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func postgresTestDraft() PostgresDraft {
	return PostgresDraft{OperationID: "dedicated-pg", Port: 55432, DedicatedHost: true}
}
func postgresFrozenFixture() *postgresJournal {
	h := strings.Repeat("a", 64)
	j := &postgresJournal{Schema: postgresSchema, Draft: postgresTestDraft(), State: "applying", Stage: "executor", Code: "queued", Token: strings.Repeat("b", 64), ControlPassword: strings.Repeat("c", 64), SitePassword: strings.Repeat("d", 64), ReviewToken: strings.Repeat("e", 64), ReviewedAt: time.Now().UTC().Format(time.RFC3339Nano), ReviewedManagerSHA256: h, ManagerSHA256: h, Baseline: map[string]string{"base": "1.0"}, HostSHA256: h, KeySHA256: h, ListsSHA256: h, Helpers: map[string]string{"/usr/bin/apt-get": h, "/usr/bin/dpkg-query": h, "/usr/bin/dpkg-deb": h, "/usr/share/keyrings/debian-archive-keyring.gpg": h}, Packages: []PostgresPackage{{"postgresql-18", "18.6-1.pgdg13+1"}, {"postgresql-client-18", "18.6-1.pgdg13+1"}, {"postgresql-common", "290.pgdg13+1"}}, CommonPackages: []PostgresPackage{{"postgresql-common", "290.pgdg13+1"}}, ExecutorGeneration: 1, ExecutorAcknowledged: true, Attempts: map[string]string{"executor": "attempted"}}
	j.AssessmentID = postgresPublicPlan(j).Assessment.ID()
	j.Progress.PlanID = j.AssessmentID
	j.Frozen = postgresIntentDigest(j)
	return j
}
func preparePostgresState(t *testing.T, m *Manager) {
	t.Helper()
	if e := privateDir(m.postgresStateDir(), m.ownerUID, true); e != nil {
		t.Fatal(e)
	}
}
func TestPostgresDraftPersistsIndependentSecretsBeforeAnyHostEffect(t *testing.T) {
	m := testManager(t)
	ctx := context.Background()
	d := postgresTestDraft()
	r, e := m.SavePostgresDraft(ctx, d)
	if e != nil {
		t.Fatal(e)
	}
	j, e := m.loadPostgres(d.OperationID)
	if e != nil {
		t.Fatal(e)
	}
	if j.ControlPassword == j.SitePassword || !hashSyntax.MatchString(j.ControlPassword) || !hashSyntax.MatchString(j.SitePassword) {
		t.Fatal("generated credentials missing or reused")
	}
	b, _ := json.Marshal(r)
	for _, secret := range []string{j.Token, j.ControlPassword, j.SitePassword} {
		if bytes.Contains(b, []byte(secret)) {
			t.Fatal("public receipt contains private secret")
		}
	}
	info, e := os.Stat(m.postgresPath(d.OperationID))
	if e != nil || info.Mode().Perm() != 0600 {
		t.Fatal("secret journal not private")
	}
	dir, e := os.Stat(m.postgresStateDir())
	if e != nil || dir.Mode().Perm() != 0700 {
		t.Fatal("prerequisite directory not private")
	}
	d.Port = 55433
	if _, e = m.SavePostgresDraft(ctx, d); e != nil {
		t.Fatal(e)
	}
	changed, e := m.loadPostgres(d.OperationID)
	if e != nil || changed.ControlPassword != j.ControlPassword || changed.SitePassword != j.SitePassword || changed.Token != j.Token {
		t.Fatal("editing input reset retained secrets")
	}
	credentials := postgresCredentials(j)
	encoded, _ := json.Marshal(credentials)
	if string(encoded) != "{}" {
		t.Fatal("credential handoff serialized a public value")
	}
	for _, format := range []string{"%v", "%+v", "%#v", "%s"} {
		text := fmt.Sprintf(format, credentials)
		if strings.Contains(text, j.ControlPassword) || strings.Contains(text, "postgres://") {
			t.Fatal("credential formatting leaked")
		}
	}
}
func TestPostgresUsesNativeLifecycleLockAndAtomicReadonlyDiscovery(t *testing.T) {
	m := testManager(t)
	ctx := context.Background()
	d := postgresTestDraft()
	if _, e := m.SavePostgresDraft(ctx, d); e != nil {
		t.Fatal(e)
	}
	unlock, e := m.lock(ctx, false)
	if e != nil {
		t.Fatal(e)
	}
	defer unlock()
	for name, call := range map[string]func() error{
		"save":   func() error { _, e := m.SavePostgresDraft(ctx, d); return e },
		"apply":  func() error { _, e := m.ApplyPostgres(ctx, d.OperationID, ""); return e },
		"resume": func() error { _, e := m.ResumePostgres(ctx, d.OperationID); return e },
	} {
		if e := call(); !errors.Is(e, ErrBusy) {
			t.Fatalf("%s escaped lifecycle serialization: %v", name, e)
		}
	}
	r, e := m.PostgresStatus(ctx, d.OperationID)
	if e != nil || r.State != "draft" {
		t.Fatalf("readonly status acquired effect lock: %+v %v", r, e)
	}
	entries, e := m.DiscoverPostgres(ctx)
	if e != nil || len(entries) != 1 || entries[0].OperationID != d.OperationID || entries[0].ManagerPath != "" {
		t.Fatalf("readonly discovery changed/blocked draft: %+v %v", entries, e)
	}
}
func TestPostgresJournalRejectsAmbiguityRetargetingAndUnsafeState(t *testing.T) {
	for _, name := range []string{"duplicate", "unknown-field", "unknown-stage", "secret-retarget", "port-retarget", "plan-retarget", "out-of-order", "future-generation", "unsafe-mode", "symlink"} {
		t.Run(name, func(t *testing.T) {
			m := testManager(t)
			preparePostgresState(t, m)
			j := postgresFrozenFixture()
			if e := m.savePostgres(j); e != nil {
				t.Fatal(e)
			}
			path := m.postgresPath(j.Draft.OperationID)
			raw, e := os.ReadFile(path)
			if e != nil {
				t.Fatal(e)
			}
			switch name {
			case "duplicate":
				raw = append([]byte("{\"schema\":\"forged\","), raw[1:]...)
			case "unknown-field":
				raw = append([]byte("{\"command\":\"shell\","), raw[1:]...)
			case "unknown-stage":
				j.Attempts["root-shell"] = "attempted"
			case "secret-retarget":
				j.Token = strings.Repeat("f", 64)
			case "port-retarget":
				j.Draft.Port++
			case "plan-retarget":
				j.Packages[0].Version = "18.7-1.pgdg13+1"
			case "out-of-order":
				j.Attempts["server"] = "attempted"
			case "future-generation":
				j.ExecutorGeneration = 17
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
				raw, _ = json.Marshal(j)
			}
			if name != "unsafe-mode" && name != "symlink" {
				if e = os.WriteFile(path, raw, 0600); e != nil {
					t.Fatal(e)
				}
			}
			if _, e = m.loadPostgres(j.Draft.OperationID); e == nil {
				t.Fatal("unsafe prerequisite journal accepted")
			}
		})
	}
}
func TestPostgresDiscoveryNeverTreatsUnknownPrivateEntriesAsClean(t *testing.T) {
	for _, name := range []string{"unknown-file", "orphan-diagnostic", "dangling-owner", "frozen-manager-missing"} {
		t.Run(name, func(t *testing.T) {
			m := testManager(t)
			preparePostgresState(t, m)
			switch name {
			case "unknown-file":
				if e := os.WriteFile(filepath.Join(m.postgresStateDir(), "unknown"), []byte("data"), 0600); e != nil {
					t.Fatal(e)
				}
			case "orphan-diagnostic":
				if e := os.WriteFile(filepath.Join(m.postgresStateDir(), "unknown.diagnostic"), []byte("data"), 0600); e != nil {
					t.Fatal(e)
				}
			case "dangling-owner":
				if e := os.WriteFile(filepath.Join(m.postgresStateDir(), "owner"), []byte("unknown\n"), 0600); e != nil {
					t.Fatal(e)
				}
			case "frozen-manager-missing":
				if e := m.savePostgres(postgresFrozenFixture()); e != nil {
					t.Fatal(e)
				}
			}
			if _, e := m.DiscoverPostgres(context.Background()); e == nil {
				t.Fatal("unsafe discovery became clean-host evidence")
			}
		})
	}
}
func TestPostgresPackagePlanRefusesUpgradesRemovalsMetapackagesAndUnknownVersions(t *testing.T) {
	baseline := map[string]string{"base": "1.0"}
	valid := []byte("Reading state information...\nInst postgresql-common (290.pgdg13+1 apt.postgresql.org [all])\nInst postgresql-18 (18.6-1.pgdg13+1 apt.postgresql.org [amd64])\nConf postgresql-18 (18.6-1.pgdg13+1 apt.postgresql.org [amd64])\n")
	plan, e := parsePostgresAPTPlan(valid, baseline)
	if e != nil || len(plan) != 2 || plan[0].Name != "postgresql-18" {
		t.Fatalf("bounded new-package plan rejected: %+v %v", plan, e)
	}
	for _, line := range []string{
		"Inst base [1.0] (2.0 Debian [amd64])\n",
		"Inst base (1.0 Debian [amd64])\n",
		"Remv base [1.0]\n",
		"Inst postgresql (18+1 Debian [all])\n",
		"Inst postgresql-19 (19.0 Debian [amd64])\n",
		"Inst postgresql-18 (../unsafe [amd64])\n",
		"Inst postgresql-18 (18.6 origin [amd64])\nInst postgresql-18 (18.6 origin [amd64])\n",
	} {
		if _, e = parsePostgresAPTPlan([]byte(line), baseline); e == nil {
			t.Fatalf("unsafe package effect accepted: %q", line)
		}
	}
	if _, e = parsePostgresInventory([]byte("base\t1.0\tunpacked\n")); e == nil {
		t.Fatal("partially configured package treated as no effect")
	}
	if _, e = parsePostgresInventory([]byte("base:i386\t1.0\tinstalled\n")); e == nil {
		t.Fatal("unsupported foreign architecture silently adopted")
	}
}
func TestPostgresTransactionGuardBindsExactReviewedAuthenticatedFiles(t *testing.T) {
	dir := "/var/cache/rixa-postgres/example/archives"
	a := filepath.Join(dir, "a.deb")
	b := filepath.Join(dir, "b.deb")
	hash := strings.Repeat("a", 64)
	artifacts := map[string]postgresArtifact{a: {PostgresPackage{"a", "1.0"}, hash}, b: {PostgresPackage{"b", "2.0"}, hash}}
	expected := map[string]bool{"a": true, "b": true}
	for _, tc := range []struct {
		name, input string
		change      func(string) (PostgresPackage, string, error)
		want        bool
	}{
		{"exact", a + "\n" + b + "\n", nil, true},
		{"missing", a + "\n", nil, false},
		{"duplicate", a + "\n" + a + "\n", nil, false},
		{"unknown", a + "\n" + filepath.Join(dir, "foreign.deb") + "\n", nil, false},
		{"outside-cache", a + "\n/etc/passwd\n", nil, false},
		{"different-bytes", a + "\n" + b + "\n", func(p string) (PostgresPackage, string, error) {
			return artifacts[p].Package, strings.Repeat("b", 64), nil
		}, false},
		{"different-version", a + "\n" + b + "\n", func(p string) (PostgresPackage, string, error) {
			v := artifacts[p].Package
			v.Version = "9.0"
			return v, hash, nil
		}, false},
		{"read-unknown", a + "\n" + b + "\n", func(string) (PostgresPackage, string, error) { return PostgresPackage{}, "", context.DeadlineExceeded }, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			inspect := tc.change
			if inspect == nil {
				inspect = func(path string) (PostgresPackage, string, error) {
					return artifacts[path].Package, artifacts[path].SHA256, nil
				}
			}
			e := postgresGuardFiles([]byte(tc.input), artifacts, expected, dir, inspect)
			if (e == nil) != tc.want {
				t.Fatalf("transaction guard=%v want=%t", e, tc.want)
			}
		})
	}
}
func TestPostgresDedicatedAffirmationCannotApproveForeignServicesOrListeners(t *testing.T) {
	if !postgresBaseServices([]byte("ssh.service loaded active running SSH\nsystemd-journald.service loaded active running Journal\n")) {
		t.Fatal("supported base services rejected")
	}
	for _, s := range []string{"postgresql.service loaded active running PostgreSQL\n", "nginx.service loaded active running Web\n", "panel.service loaded active running Panel\n"} {
		if postgresBaseServices([]byte(s)) {
			t.Fatal("dedicated affirmation adopted foreign service")
		}
	}
	if !postgresBaseListeners([]byte("LISTEN 0 128 0.0.0.0:22 0.0.0.0:*\n")) {
		t.Fatal("supported SSH listener rejected")
	}
	for _, s := range []string{"LISTEN 0 128 0.0.0.0:80 0.0.0.0:*\n", "LISTEN 0 128 127.0.0.1:5432 0.0.0.0:*\n"} {
		if postgresBaseListeners([]byte(s)) {
			t.Fatal("foreign listener silently adopted")
		}
	}
}
func TestPostgresMainSuppressionMustActuallyBeIncludedLast(t *testing.T) {
	for _, name := range []string{"supported", "no-include", "override-after", "foreign-include", "existing-main"} {
		t.Run(name, func(t *testing.T) {
			m := testManager(t)
			b := newPostgresBackend(m)
			b.sysroot = t.TempDir()
			dir := b.path("/etc/postgresql-common")
			if e := os.MkdirAll(dir, 0755); e != nil {
				t.Fatal(e)
			}
			text := "# Package defaults\ninclude_dir '/etc/postgresql-common/createcluster.d'\n"
			if name == "no-include" {
				text = "# No include\n"
			}
			if name == "override-after" {
				text += "create_main_cluster = true\n"
			}
			if e := os.WriteFile(filepath.Join(dir, "createcluster.conf"), []byte(text), 0644); e != nil {
				t.Fatal(e)
			}
			if name == "foreign-include" {
				if e := os.Mkdir(filepath.Join(dir, "createcluster.d"), 0755); e != nil {
					t.Fatal(e)
				}
				if e := os.WriteFile(filepath.Join(dir, "createcluster.d", "unknown.conf"), []byte("create_main_cluster=true\n"), 0644); e != nil {
					t.Fatal(e)
				}
			}
			if name == "existing-main" {
				if e := os.MkdirAll(b.path("/etc/postgresql/18/main"), 0755); e != nil {
					t.Fatal(e)
				}
			}
			e := b.suppressMain(postgresFrozenFixture())
			if (e == nil) != (name == "supported") {
				t.Fatalf("main-cluster policy=%v case=%s", e, name)
			}
			if name == "supported" && !b.mainSuppressed(postgresFrozenFixture()) {
				t.Fatal("suppression was not positively observed")
			}
		})
	}
}
func TestPostgresHBARejectsAmbientOrNetworkTrust(t *testing.T) {
	good := []byte("local all postgres peer\nlocal all all peer\nhost all all 127.0.0.1/32 scram-sha-256\nhost all all ::1/128 scram-sha-256\nlocal replication all peer\nhost replication all 127.0.0.1/32 scram-sha-256\n")
	if !postgresHBA(good) {
		t.Fatal("explicit loopback/SCRAM and local administrative peer rejected")
	}
	for _, bad := range [][]byte{[]byte("host all all 0.0.0.0/0 trust\n"), bytes.ReplaceAll(good, []byte("scram-sha-256"), []byte("md5")), bytes.ReplaceAll(good, []byte("peer"), []byte("trust"))} {
		if postgresHBA(bad) {
			t.Fatal("unsafe authentication silently accepted")
		}
	}
	for _, key := range [][]byte{nil, []byte("untrusted key"), bytes.Repeat([]byte("x"), (64<<10)+1)} {
		if validatePGDGKey(key) == nil {
			t.Fatal("untrusted signing key accepted")
		}
	}
}
func TestPostgresUnknownExecutorStartAndForeignGuardNeverReplayEffects(t *testing.T) {
	m := testManager(t)
	preparePostgresState(t, m)
	j := postgresFrozenFixture()
	j.ExecutorAcknowledged = false
	if e := m.savePostgres(j); e != nil {
		t.Fatal(e)
	}
	r, e := m.ResumePostgres(context.Background(), j.Draft.OperationID)
	if !errors.Is(e, ErrState) || !reflect.DeepEqual(r.Actions, []string{"status"}) {
		t.Fatalf("unknown start invented continuation: %+v %v", r, e)
	}
	if e = m.RunPostgresPackageGuard(context.Background(), j.Draft.OperationID, "common", strings.NewReader("/untrusted.deb\n")); !errors.Is(e, ErrAuthority) {
		t.Fatalf("foreign guard context accepted: %v", e)
	}
	if e = m.RunPostgresPackageGuard(context.Background(), j.Draft.OperationID, "shell", strings.NewReader("")); !errors.Is(e, ErrInput) {
		t.Fatalf("guard widened fixed phase: %v", e)
	}
}
