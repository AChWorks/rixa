// SPDX-License-Identifier: MPL-2.0
package serverinstall

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func validDraft() Draft {
	return Draft{OperationID: "first-install", ArchivePath: "/private/runtime.tar.gz", BundlePath: "/private/runtime.bundle.json", Release: "v0.2.0", SourceCommit: strings.Repeat("a", 40),
		ControlOrigin: "https://control.rixa.test:8443", SiteOrigin: "https://site.rixa.test:8443", Listen: "0.0.0.0:8443",
		TLSCertPath: "/private/cert.pem", TLSKeyPath: "/private/key.pem",
		ControlDSN: "postgres://control:control-db-secret@127.0.0.1:5432/control?sslmode=disable", SiteDSN: "postgres://site:site-db-secret@127.0.0.1:5432/site?sslmode=disable",
		ControlLogin: "control-admin", SiteLogin: "site-admin", ControlPassword: "control-password-is-strong", SitePassword: "site-password-is-strong", Language: "en"}
}
func testManager(t *testing.T) *Manager {
	t.Helper()
	m := New()
	m.stateRoot = t.TempDir()
	if e := os.Chmod(m.stateRoot, 0700); e != nil {
		t.Fatal(e)
	}
	m.ownerUID = os.Geteuid()
	m.run = func(context.Context, string, ...string) ([]byte, error) {
		t.Fatal("unexpected host command")
		return nil, ErrState
	}
	return m
}
func TestDraftPreflightMatchesBootstrapAndSupportedAuthority(t *testing.T) {
	if p := ValidateDraft(validDraft()); len(p) > 0 {
		t.Fatalf("valid draft rejected: %+v", p)
	}
	cases := []struct {
		name, field string
		change      func(*Draft)
	}{
		{"short-password", "control_password", func(d *Draft) { d.ControlPassword = strings.Repeat("x", 14) }},
		{"upper-login", "site_login", func(d *Draft) { d.SiteLogin = "Admin" }},
		{"unicode-login", "site_login", func(d *Draft) { d.SiteLogin = "مدیر" }},
		{"wrong-port", "site_origin", func(d *Draft) { d.SiteOrigin = "https://site.rixa.test" }},
		{"invalid-port", "listen", func(d *Draft) { d.Listen = "0.0.0.0:hello" }},
		{"same-host", "site_origin", func(d *Draft) { d.SiteOrigin = d.ControlOrigin }},
		{"peer-credentials", "control_dsn", func(d *Draft) { d.ControlDSN = "postgres:///control?host=/var/run/postgresql" }},
		{"implicit-password", "site_dsn", func(d *Draft) { d.SiteDSN = "postgres://site@127.0.0.1:5432/site?sslmode=disable" }},
		{"implicit-port", "site_dsn", func(d *Draft) { d.SiteDSN = "postgres://site:password@127.0.0.1/site?sslmode=disable" }},
		{"remote-profile", "site_dsn", func(d *Draft) { d.SiteDSN = "postgres://site:password@db.example:5432/site?sslmode=verify-full" }},
		{"passfile", "site_dsn", func(d *Draft) { d.SiteDSN += "&passfile=/root/.pgpass" }},
		{"terminal-control-path", "archive", func(d *Draft) { d.ArchivePath = "/private/\x1b[31m" }},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			d := validDraft()
			c.change(&d)
			found := false
			for _, p := range ValidateDraft(d) {
				if p.Field == c.field {
					found = true
				}
				if strings.Contains(p.Message, d.ControlPassword) || strings.Contains(p.Message, d.SitePassword) {
					t.Fatal("credential leaked")
				}
			}
			if !found {
				t.Fatalf("missing field %s", c.field)
			}
		})
	}
	d := validDraft()
	d.ControlPassword = strings.Repeat("ژ", 15)
	if p := ValidateDraft(d); len(p) > 0 {
		t.Fatalf("Unicode password policy diverged: %+v", p)
	}
}
func TestPrivateDraftStateAndSecretFreeReceipts(t *testing.T) {
	m := testManager(t)
	d := validDraft()
	r, e := m.SaveDraft(context.Background(), d)
	if e != nil {
		t.Fatal(e)
	}
	i, e := os.Stat(m.statePath(d.OperationID))
	if e != nil || i.Mode().Perm() != 0600 {
		t.Fatal("private mode not retained")
	}
	b, _ := json.Marshal(r)
	for _, secret := range []string{d.ControlPassword, d.SitePassword, "control-db-secret", "site-db-secret"} {
		if bytes.Contains(b, []byte(secret)) {
			t.Fatal("public receipt contains credential")
		}
	}
	loaded, e := m.LoadDraft(context.Background(), d.OperationID)
	if e != nil || loaded.ControlPassword != d.ControlPassword {
		t.Fatal("protected valid input was lost")
	}
	for _, format := range []string{"%s", "%v", "%+v", "%#v"} {
		s := fmt.Sprintf(format, d)
		if strings.Contains(s, d.ControlPassword) || strings.Contains(s, d.ControlDSN) {
			t.Fatal("formatted credential")
		}
	}
	var logs bytes.Buffer
	slog.New(slog.NewJSONHandler(&logs, nil)).Info("draft", "value", d)
	if strings.Contains(logs.String(), d.SitePassword) || strings.Contains(logs.String(), d.SiteDSN) {
		t.Fatal("logged credential")
	}
}
func TestStatusReadsAtomicProgressWhileWriterOwnsLock(t *testing.T) {
	m := testManager(t)
	d := validDraft()
	if _, e := m.SaveDraft(context.Background(), d); e != nil {
		t.Fatal(e)
	}
	unlock, e := m.lock(context.Background(), false)
	if e != nil {
		t.Fatal(e)
	}
	defer unlock()
	r, e := m.Status(context.Background(), d.OperationID)
	if e != nil || r.State != "draft" {
		t.Fatalf("status blocked: %+v %v", r, e)
	}
	if _, e = m.SaveDraft(context.Background(), d); !errors.Is(e, ErrBusy) {
		t.Fatalf("competing writer not fenced: %v", e)
	}
}
func reviewedJournal(d Draft) *journal {
	return &journal{Schema: "rixa.native-install.v1", Draft: d, State: "ready", Stage: "review", Code: "ready", Attempts: map[string]string{}, ReviewToken: strings.Repeat("b", 64), ArchiveSHA256: strings.Repeat("c", 64), CertSHA256: strings.Repeat("d", 64), KeySHA256: strings.Repeat("e", 64), DatabaseTargets: map[string]string{"control": "1:2", "site-a": "1:3"}}
}
func TestTwoClientReviewTokenCannotApplyChangedDraft(t *testing.T) {
	m := testManager(t)
	d := validDraft()
	j := reviewedJournal(d)
	if e := m.save(j); e != nil {
		t.Fatal(e)
	}
	oldToken := j.ReviewToken
	d.SiteOrigin = "https://another.rixa.test:8443"
	if _, e := m.SaveDraft(context.Background(), d); e != nil {
		t.Fatal(e)
	}
	j = reviewedJournal(d)
	j.ReviewToken = strings.Repeat("f", 64)
	if e := m.save(j); e != nil {
		t.Fatal(e)
	}
	r, e := m.Apply(context.Background(), d.OperationID, oldToken)
	if !errors.Is(e, ErrState) || r.Code != "review-stale" || r.ReviewToken != "" {
		t.Fatalf("stale confirmation accepted: %+v %v", r, e)
	}
	r, e = m.Status(context.Background(), d.OperationID)
	if e != nil || r.ReviewToken != "" {
		t.Fatal("Status supplied an unreviewed confirmation token")
	}
	if _, e = os.Stat(filepath.Join(m.stateRoot, "installation")); !os.IsNotExist(e) {
		t.Fatal("stale apply produced installation effects")
	}
}
func TestJournalRejectsAmbiguityUnknownStagesAndUnsafeFiles(t *testing.T) {
	for _, kind := range []string{"duplicate", "unknown-stage", "bad-digest", "unsafe-mode", "symlink"} {
		t.Run(kind, func(t *testing.T) {
			m := testManager(t)
			d := validDraft()
			j := reviewedJournal(d)
			switch kind {
			case "unknown-stage":
				j.Attempts["shell"] = "attempted"
			case "bad-digest":
				j.ArchiveSHA256 = strings.Repeat("z", 64)
			}
			if e := m.save(j); e != nil {
				t.Fatal(e)
			}
			path := m.statePath(d.OperationID)
			switch kind {
			case "duplicate":
				b, _ := os.ReadFile(path)
				b = append([]byte("{\"schema\":\"forged\","), b[1:]...)
				if e := os.WriteFile(path, b, 0600); e != nil {
					t.Fatal(e)
				}
			case "unsafe-mode":
				_ = os.Chmod(path, 0644)
			case "symlink":
				_ = os.Remove(path)
				_ = os.Symlink("/etc/passwd", path)
			}
			if _, e := m.load(d.OperationID); e == nil {
				t.Fatal("unsafe/ambiguous journal accepted")
			}
		})
	}
}
func TestFrozenIdentitySurvivesPasswordErasureButRejectsRetarget(t *testing.T) {
	d := validDraft()
	before := draftDigest(d)
	d.ControlPassword, d.SitePassword = "", ""
	if draftDigest(d) != before {
		t.Fatal("credential retention rewrote frozen intent identity")
	}
	d.ControlOrigin = "https://changed.rixa.test:8443"
	if draftDigest(d) == before {
		t.Fatal("changed target did not invalidate identity")
	}
}
func TestPrivilegedParentsAndCreationDoNotFollowLinks(t *testing.T) {
	dir := t.TempDir()
	if trustedRootParent(filepath.Join(dir, "privileged")) {
		t.Fatal("writable temporary ancestor trusted for root mutation")
	}
	target := filepath.Join(dir, "secret")
	if e := os.WriteFile(target, []byte("original"), 0600); e != nil {
		t.Fatal(e)
	}
	link := filepath.Join(dir, "link")
	if e := os.Symlink(target, link); e != nil {
		t.Fatal(e)
	}
	if _, e := readRegular(link, 1024); e == nil {
		t.Fatal("symlink input followed")
	}
	if e := writeExpected(link, []byte("replacement"), 0600, os.Geteuid()); e == nil {
		t.Fatal("existing symlink accepted")
	}
	data, _ := os.ReadFile(target)
	if string(data) != "original" {
		t.Fatal("unrelated target modified")
	}
}
func TestFailureDiagnosticsRetainCauseAndOnlyMaskCredentials(t *testing.T) {
	m := testManager(t)
	j := reviewedJournal(validDraft())
	cause := fmt.Errorf("database host=127.0.0.1 name=control password=%s; initial=%s", "control-db-secret", j.Draft.ControlPassword)
	_, _ = m.fail(j, "stage-failed", "Inspect protected diagnostics.", cause)
	b, e := readPrivate(filepath.Join(m.stateRoot, j.Draft.OperationID+".diagnostic"), os.Geteuid(), 64<<10)
	if e != nil || !bytes.Contains(b, []byte("host=127.0.0.1")) || !bytes.Contains(b, []byte("name=control")) {
		t.Fatalf("useful diagnostics lost: %s %v", b, e)
	}
	if bytes.Contains(b, []byte("control-db-secret")) || bytes.Contains(b, []byte(j.Draft.ControlPassword)) {
		t.Fatal("actual credential was not redacted")
	}
}
