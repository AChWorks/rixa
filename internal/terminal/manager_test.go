// SPDX-License-Identifier: MPL-2.0

package terminal

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/AChWorks/rixa/internal/serverinstall"
)

func TestSafeTextKeepsValuesInert(t *testing.T) {
	got := SafeText("site\x1b[2J\n\u202e.json / فارسی\u200c")
	if strings.ContainsAny(got, "\x1b\n") || strings.ContainsRune(got, '\u202e') {
		t.Fatalf("terminal control survived: %q", got)
	}
	if !strings.Contains(got, "فارسی\u200c") {
		t.Fatalf("ordinary UTF-8/ZWNJ changed: %q", got)
	}
}
func TestOnlyBackendActionsAreExposed(t *testing.T) {
	got := menuActions([]string{"status", "invented-action"})
	if len(got) != 3 || got[0].id != "status" || got[1].id != "details" || got[2].id != "exit" {
		t.Fatalf("unsupported menu: %#v", got)
	}
}
func TestPublicDiagnosticsRedactActualSecretsOnly(t *testing.T) {
	d := serverinstall.Draft{ControlDSN: "postgres://operator:secret@db.internal/database", ControlPassword: "password-canary-123"}
	got := publicError(errors.New("connection to db.internal failed: "+d.ControlDSN+"; "+d.ControlPassword), d)
	if strings.Contains(got, d.ControlDSN) || strings.Contains(got, d.ControlPassword) {
		t.Fatal("secret escaped")
	}
	if !strings.Contains(got, "db.internal failed") {
		t.Fatalf("useful diagnostic lost: %q", got)
	}
}
func TestMissingTerminalNeverPrompts(t *testing.T) {
	var out strings.Builder
	err := Run(context.Background(), &testFacade{}, Options{Output: &out})
	if !errors.Is(err, ErrTerminal) || out.Len() != 0 {
		t.Fatalf("nonterminal: err=%v out=%q", err, out.String())
	}
}

type testFacade struct {
	draft            serverinstall.Draft
	saves            int
	reviews          int
	applies          int
	removes          int
	firstSaveFailure bool
	ready            bool
	token            string
}

func (f *testFacade) SaveDraft(_ context.Context, d serverinstall.Draft) (serverinstall.Result, error) {
	f.saves++
	f.draft = d
	if f.firstSaveFailure && f.saves == 1 {
		return serverinstall.Result{OperationID: d.OperationID, State: "draft", Code: "unavailable", Actions: []string{"configure", "status"}}, errors.New("db.internal temporarily unavailable: " + d.ControlDSN)
	}
	return serverinstall.Result{OperationID: d.OperationID, State: "draft", Code: "saved", Actions: []string{"configure", "review", "status"}}, nil
}
func (f *testFacade) LoadDraft(context.Context, string) (serverinstall.Draft, error) {
	return f.draft, nil
}
func (f *testFacade) Review(context.Context, string) (serverinstall.Result, error) {
	f.reviews++
	return serverinstall.Result{OperationID: f.draft.OperationID, State: "ready", Code: "ready", ReviewToken: "review-token-case", Release: f.draft.Release, Actions: []string{"configure", "review", "apply", "status"}}, nil
}
func (f *testFacade) Apply(_ context.Context, _ string, token string) (serverinstall.Result, error) {
	f.token = token
	f.applies++
	return serverinstall.Result{OperationID: f.draft.OperationID, State: "applying", Code: "queued", Actions: []string{"status"}}, nil
}
func (f *testFacade) Status(context.Context, string) (serverinstall.Result, error) {
	if f.ready {
		return serverinstall.Result{OperationID: f.draft.OperationID, State: "ready", Code: "ready", ReviewToken: "another-client-token", Actions: []string{"configure", "review", "apply", "status"}}, nil
	}
	return serverinstall.Result{OperationID: f.draft.OperationID, State: "draft", Code: "saved", Actions: []string{"configure", "review", "status"}}, nil
}
func (f *testFacade) Remove(context.Context, string) (serverinstall.Result, error) {
	f.removes++
	return serverinstall.Result{OperationID: f.draft.OperationID, State: "removed", Code: "removed", Actions: []string{"status"}}, nil
}
func testDraft() serverinstall.Draft {
	return serverinstall.Draft{
		OperationID: "pty-case", ArchivePath: "/private/release.tar.gz", BundlePath: "/private/release.tar.gz.sigstore.json", Release: "v0.2.0", SourceCommit: strings.Repeat("0", 40),
		ControlOrigin: "https://control.rixa.test:8443", SiteOrigin: "https://site.rixa.test:8443", Listen: "0.0.0.0:8443", TLSCertPath: "/private/cert.pem", TLSKeyPath: "/private/key.pem",
		ControlDSN: "postgres://operator:DB-CONTROL-CANARY@127.0.0.1:5432/rixa_control?sslmode=disable", SiteDSN: "postgres://operator:DB-SITE-CANARY@127.0.0.1:5432/rixa_site?sslmode=disable",
		ControlLogin: "admin", SiteLogin: "admin", ControlPassword: "PASSWORD-CONTROL-CANARY-123", SitePassword: "PASSWORD-SITE-CANARY-123", Language: "en",
	}
}

func TestSmallViewportKeepsSelectedActionVisible(t *testing.T) {
	s := &session{full: true, width: 40, height: 12}
	actions := menuActions([]string{"configure", "review", "apply", "status", "remove"})
	for selected := range actions {
		lines := menuLines(s, "development", serverinstall.Result{OperationID: "case", State: "ready", Message: "Long detail", Problems: []serverinstall.Problem{{Field: "database", Message: "Needs attention"}}}, "A retained notice", testDraft(), actions, selected)
		if len(lines) > 8 {
			t.Fatalf("viewport overflow: %#v", lines)
		}
		found := false
		for _, line := range lines {
			if strings.HasPrefix(line, "> ") {
				found = true
			}
		}
		if !found {
			t.Fatalf("selected action hidden: %d %#v", selected, lines)
		}
	}
}

func (f *testFacade) Resume(_ context.Context, id string) (serverinstall.Result, error) {
	f.applies++
	return serverinstall.Result{OperationID: id, State: "applying", Code: "resumed", Actions: []string{"status"}}, nil
}

func TestWrappedDiagnosticsPreserveEveryRune(t *testing.T) {
	value := "Missing certificate detail / مسیر فارسی‌ آزمایشی / required suffix"
	lines := wrapLines([]string{value}, 7)
	if strings.Join(lines, "") != value {
		t.Fatal("diagnostic text was dropped or changed")
	}
	for _, line := range lines {
		if len([]rune(line)) > 7 {
			t.Fatal("diagnostic exceeded viewport bound")
		}
	}
}
