// SPDX-License-Identifier: MPL-2.0
package manageweb

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/AChWorks/rixa/internal/serverinstall"
)

type delayedStatusNative struct {
	*fakeNative
	reached chan struct{}
	once    sync.Once
}

func (f *delayedStatusNative) Status(ctx context.Context, id string) (serverinstall.Result, error) {
	r, e := f.fakeNative.Status(ctx, id)
	f.once.Do(func() { close(f.reached) })
	return r, e
}

type facadeWithoutStrictEffects struct{ serverinstall.Facade }

// The gate has already observed the original installation while body decoding
// waits. Only a strict effect call can reject the child appearing in that gap.
func TestConsoleStrictEffectsAfterDelayedBody(t *testing.T) {
	for _, action := range []string{"resume", "remove"} {
		t.Run(action, func(t *testing.T) {
			h := fixture(t, func(o *Options, _ *dependencies, f *fakeNative) {
				o.Scope.Actions = []string{"status", action}
				f.result.State = "reconciliation-required"
				f.result.Actions = []string{"status", action}
			})
			h.login(t)
			wrapped := &delayedStatusNative{fakeNative: h.backend, reached: make(chan struct{})}
			h.console.backend = wrapped
			reader, writer := io.Pipe()
			defer reader.Close()
			defer writer.Close()
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			request, _ := http.NewRequestWithContext(ctx, "POST", h.console.origin+"/admin/operation/"+action, reader)
			request.Header.Set("Origin", h.console.origin)
			request.Header.Set("X-Identity-Request", "1")
			request.Header.Set("X-CSRF-Token", h.csrf)
			request.Header.Set("Content-Type", "application/json")
			done := make(chan int, 1)
			go func() {
				response, err := h.client.Do(request)
				if err != nil {
					done <- 0
					return
				}
				_, _ = io.Copy(io.Discard, response.Body)
				_ = response.Body.Close()
				done <- response.StatusCode
			}()
			select {
			case <-wrapped.reached:
			case <-ctx.Done():
				t.Fatal("gate did not inspect installation")
			}
			h.backend.mutex.Lock()
			h.backend.result = serverinstall.Result{OperationID: "concurrent-child", OperationKind: "update", InstallationOperation: "fixture-install", State: "installed", Code: "completion-cleanup-required", Actions: []string{"resume", "remove", "status"}}
			h.backend.mutex.Unlock()
			body := "{}"
			if action == "remove" {
				body = `{"confirmation":"REMOVE"}`
			}
			if _, err := io.WriteString(writer, body); err != nil {
				t.Fatal("delayed body write")
			}
			_ = writer.Close()
			if code := <-done; code != 409 {
				t.Fatalf("scope drift HTTP status %d", code)
			}
			h.backend.mutex.Lock()
			defer h.backend.mutex.Unlock()
			if h.backend.calls["strict_"+action] != 1 || h.backend.calls[action] != 0 || h.backend.result.OperationID != "concurrent-child" {
				t.Fatal("effect bypassed strict native binding")
			}
		})
	}
}
func TestConsoleFailsClosedWithoutStrictInstallationEffects(t *testing.T) {
	for _, action := range []string{"resume", "remove"} {
		h := fixture(t, func(o *Options, _ *dependencies, f *fakeNative) {
			o.Scope.Actions = []string{"status", action}
			f.result.Actions = []string{"status", action}
		})
		h.console.backend = facadeWithoutStrictEffects{h.backend}
		h.login(t)
		body := "{}"
		if action == "remove" {
			body = `{"confirmation":"REMOVE"}`
		}
		code, _, _ := h.call(t, "POST", "/admin/operation/"+action, body, true)
		if code != 403 || h.backend.calls[action] != 0 {
			t.Fatal("missing strict seam fell back to generic effect")
		}
	}
}
func TestConsoleRetainsUpdateCleanupAccessUntilPositiveResume(t *testing.T) {
	h := fixture(t, func(o *Options, _ *dependencies, f *fakeNative) {
		o.Scope = Scope{Kind: "update", Operation: "fixture-update", Installation: "fixture-install", Actions: []string{"status", "resume"}}
		f.draft = serverinstall.Draft{OperationID: "fixture-install", Release: "v1.2.3"}
		f.result = serverinstall.Result{OperationKind: "update", OperationID: "fixture-update", InstallationOperation: "fixture-install", State: "installed", Code: "completion-cleanup-required", Message: "Run resume with the retained manager to reconcile the pending claim.", Actions: []string{"resume", "status"}}
		f.resumeResult = &serverinstall.Result{OperationKind: "update", OperationID: "fixture-update", InstallationOperation: "fixture-install", State: "installed", Code: "updated", Message: "Update completion verified.", Actions: []string{"status", "remove"}}
	})
	h.login(t)
	code, body, _ := h.call(t, "GET", "/admin/operation/status", "", false)
	var result response
	if code != 200 || json.Unmarshal(body, &result) != nil || result.AccessClosed || !strings.Contains(result.Message, "not confirmed") || !strings.Contains(result.TechnicalDetail, "pending claim") {
		t.Fatal("cleanup status claimed completion or lost repair guidance")
	}
	code, _, _ = h.call(t, "GET", "/console-auth/csrf", "", false)
	if code != 200 {
		t.Fatal("cleanup status revoked matching permit")
	}
	code, body, _ = h.call(t, "POST", "/admin/operation/resume", "{}", true)
	if code != 200 || json.Unmarshal(body, &result) != nil || !result.AccessClosed || h.backend.calls["resume"] != 1 || h.backend.calls["strict_resume"] != 0 {
		t.Fatal("explicit update Resume did not complete through UpdateFacade")
	}
	code, _, _ = h.call(t, "GET", "/console-auth/csrf", "", false)
	if code != 401 {
		t.Fatal("positive update completion retained session")
	}
}
func TestConsoleInstalledIsNotByItselfCompletion(t *testing.T) {
	for _, code := range []string{"installed-unhealthy", "completion-cleanup-required", "update-pending", "superseded", "installation-ownership"} {
		h := fixture(t, func(_ *Options, _ *dependencies, f *fakeNative) {
			f.result.State = "installed"
			f.result.Code = code
			f.result.Actions = []string{"status"}
		})
		h.login(t)
		status, body, _ := h.call(t, "GET", "/admin/operation/status", "", false)
		var result response
		if status != 200 || json.Unmarshal(body, &result) != nil || result.AccessClosed || !strings.Contains(result.Message, "not confirmed") {
			t.Fatal("retained installation was presented as completed")
		}
	}
	h := fixture(t, func(_ *Options, _ *dependencies, f *fakeNative) {
		f.result.State = "installed"
		f.result.Code = "healthy"
		f.result.Actions = []string{"status", "remove"}
	})
	h.login(t)
	code, body, _ := h.call(t, "GET", "/admin/operation/status", "", false)
	var result response
	if code != 200 || json.Unmarshal(body, &result) != nil || !result.AccessClosed {
		t.Fatal("positive initial completion did not close access")
	}
}

func TestConsoleDistinctNativeDiagnosticsAndCredentialRedaction(t *testing.T) {
	const password = "db-canary/with space+&<secret>"
	const admin = "admin-canary/with space+&<secret>"
	dsn := "postgres://control:" + url.PathEscape(password) + "@127.0.0.1:5432/control?sslmode=disable"
	variants := []string{password, admin, url.QueryEscape(password), url.PathEscape(password), url.QueryEscape(admin), strings.TrimPrefix(url.UserPassword("", password).String(), ":"),
		base64.StdEncoding.EncodeToString([]byte(password)), base64.RawURLEncoding.EncodeToString([]byte(admin)),
		base64.StdEncoding.EncodeToString([]byte(dsn))}
	for _, tc := range []struct{ name, field, code, message string }{
		{"invalid-dsn", "control_dsn", "database-profile", "Use an explicit loopback TCP PostgreSQL URL with password, port, database and sslmode=disable."},
		{"pg-permissions", "control_dsn", "database-prerequisite", "Grant pg_control_system() identity permissions to the existing database role."},
		{"pg-schema", "control_dsn", "database-prerequisite", "Provide schema authority in both distinct existing databases before review."},
		{"trusted-manager", "host", "helper", "Install the trusted manager under root-owned non-writable ancestors: /opt/rixa/manager."},
		{"missing-verifier", "host", "helper", "Install gh under a trusted root-owned non-writable helper path and review again."},
		{"ownership", "runtime_user", "ownership", "The existing rixa account is not owned by this operation; inspect its owner instead of replacing it."},
		{"manager-drift", "host", "manager-changed", "Resume with the retained trusted manager image at /var/lib/rixa/managers/abcdef1234."},
		{"unhealthy-installed", "service", "installed-unhealthy", "Current service and strict TLS checks did not verify; inspect systemctl status rixa.service and its journal."},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := fixture(t, func(_ *Options, _ *dependencies, f *fakeNative) {
				f.draft = serverinstall.Draft{OperationID: "fixture-install", Release: "v1.2.3", ControlDSN: dsn, ControlPassword: admin}
				f.result.State = "blocked"
				f.result.Code = tc.code
			})
			h.login(t)
			parsed, _ := url.Parse(h.console.origin)
			session := h.client.Jar.Cookies(parsed)[0].Value
			all := append(append([]string{}, variants...), h.permit, h.csrf, session, strings.Repeat("b", 64))
			diagnostic := tc.message + " Supplied: " + dsn + " " + strings.Join(all, " | ") + " <img id=diagnostic-probe>"
			h.backend.mutex.Lock()
			h.backend.result.Message = diagnostic
			h.backend.result.ReviewToken = strings.Repeat("b", 64)
			h.backend.result.Problems = []serverinstall.Problem{{Field: tc.field, Code: tc.code, Message: diagnostic}}
			h.backend.mutex.Unlock()
			for _, path := range []string{"/admin/operation/status", "/admin/operation/"} {
				code, body, _ := h.call(t, "GET", path, "", false)
				if code != 200 {
					t.Fatal("diagnostic response unavailable")
				}
				if !bytes.Contains(body, []byte(tc.message)) {
					t.Fatal("distinct cause/repair instruction lost")
				}
				for _, secret := range all {
					if bytes.Contains(body, []byte(secret)) {
						t.Fatal("credential or token exposed in diagnostic")
					}
				}
				if bytes.Contains(body, []byte("<img id=diagnostic-probe>")) {
					t.Fatal("diagnostic became HTML markup")
				}
			}
		})
	}
}

type rejectedDraftNative struct{ *fakeNative }

func (f rejectedDraftNative) SaveDraft(_ context.Context, d serverinstall.Draft) (serverinstall.Result, error) {
	return serverinstall.Result{OperationID: d.OperationID, State: "blocked", Code: "database-profile", Message: "Correct the loopback profile. Submitted " + d.ControlDSN + " " + d.ControlPassword}, serverinstall.ErrInput
}
func TestConsoleRejectedInputAndUnavailableDiagnosticContext(t *testing.T) {
	h := fixture(t, nil)
	h.console.backend = rejectedDraftNative{h.backend}
	h.login(t)
	dsn := "postgres://operator:new-db-secret@remote.invalid:5432/control?sslmode=disable"
	body, _ := json.Marshal(configureInput{ControlDSN: dsn, ControlPassword: "new-admin-secret"})
	code, data, _ := h.call(t, "POST", "/admin/operation/configure", string(body), true)
	if code != 400 || !bytes.Contains(data, []byte("Correct the loopback profile")) || bytes.Contains(data, []byte("new-db-secret")) || bytes.Contains(data, []byte("new-admin-secret")) {
		t.Fatal("rejected input lost guidance or leaked submitted credentials")
	}
	h.backend.mutex.Lock()
	h.backend.loadError = errors.New("private journal unavailable")
	h.backend.result.Message = "Native cause with unknown saved-private-secret"
	h.backend.mutex.Unlock()
	code, data, _ = h.call(t, "GET", "/admin/operation/status", "", false)
	if code != 200 || !bytes.Contains(data, []byte("Protected diagnostic context is unavailable")) || bytes.Contains(data, []byte("saved-private-secret")) {
		t.Fatal("missing protected context did not fail closed visibly")
	}
}
func TestDiagnosticBoundsAndStructuralPasswordExtraction(t *testing.T) {
	redact := diagnosticReplacer([]string{"host=127.0.0.1 password='libpq canary\\'secret' dbname=control"})
	out := redact("Inspect PostgreSQL permissions; libpq canary'secret")
	if strings.Contains(out, "canary") || !strings.Contains(out, "permissions") {
		t.Fatal("quoted supplied password not redacted")
	}
	redact = diagnosticReplacer([]string{"canary-secret"})
	out = redact(strings.Repeat("x", 4090) + "canary-secret" + "\u202e")
	if strings.Contains(out, "canary") || strings.ContainsRune(out, '\u202e') || len([]rune(out)) > 4097 {
		t.Fatal("diagnostic truncation/control bound")
	}
}

type failingInstalledStatus struct{ *fakeNative }

func (f failingInstalledStatus) Status(ctx context.Context, id string) (serverinstall.Result, error) {
	r, _ := f.fakeNative.Status(ctx, id)
	return r, serverinstall.ErrPrerequisite
}
func TestConsoleFailedInstalledInspectionAndFreshRemovalAccess(t *testing.T) {
	h := fixture(t, func(_ *Options, _ *dependencies, f *fakeNative) {
		f.result.State = "installed"
		f.result.Code = "healthy"
		f.result.Actions = []string{"status", "remove"}
	})
	h.console.backend = failingInstalledStatus{h.backend}
	h.login(t)
	code, body, _ := h.call(t, "GET", "/admin/operation/status", "", false)
	var result response
	if code != 503 || json.Unmarshal(body, &result) != nil || result.AccessClosed || !strings.Contains(result.Message, "not confirmed") {
		t.Fatal("failed current inspection was treated as positive completion")
	}
	fresh := fixture(t, func(o *Options, _ *dependencies, f *fakeNative) {
		o.Scope.Actions = []string{"status", "remove"}
		f.result.State = "installed"
		f.result.Code = "healthy"
		f.result.Actions = []string{"status", "remove"}
	})
	fresh.login(t)
	code, body, _ = fresh.call(t, "GET", "/admin/operation/status", "", false)
	if code != 200 || json.Unmarshal(body, &result) != nil || result.AccessClosed {
		t.Fatal("fresh status/removal permit closed on healthy status")
	}
	code, body, _ = fresh.call(t, "POST", "/admin/operation/remove", `{"confirmation":"REMOVE"}`, true)
	if code != 200 || json.Unmarshal(body, &result) != nil || !result.AccessClosed || fresh.backend.calls["strict_remove"] != 1 {
		t.Fatal("fresh strict removal did not close on completion")
	}
}
