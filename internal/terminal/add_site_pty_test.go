// SPDX-License-Identifier: MPL-2.0

//go:build linux

package terminal

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"

	"golang.org/x/sys/unix"
)

// The normal PTY test runner starts this process on a real Linux controlling
// terminal. Only the host-effect facade is simulated, not the terminal stack.
func runAddSitePTYChild(t *testing.T, mode string) {
	f := &addSiteTestFacade{draft: testAddSiteDraft()}
	f.ready = mode == "add-site-foreign-review" || mode == "add-site-unsaved"
	f.recovery = mode == "add-site-unknown"
	f.resumable = mode == "add-site-resume"
	f.installed = mode == "add-site-installed"
	f.failSaveOnce = mode == "add-site-save-error"
	f.validateNative = strings.HasPrefix(mode, "add-site-input-")
	f.operationError = mode == "add-site-input-operation-error"
	f.journalCollision = mode == "add-site-input-real-journal-collision"
	options := AddSiteOptions{
		Options: Options{
			Input: os.Stdin, Output: os.Stdout, OperationID: "pty-case",
			Version: "v0.8.0", Plain: mode == "add-site-plain",
			NoColor: mode == "add-site-no-color",
		},
		InstallationOperation: "owner-install",
	}
	if f.validateNative {
		prefill := testAddSiteDraft()
		switch mode {
		case "add-site-input-missing-origin":
			prefill.SiteBOrigin = ""
		case "add-site-input-missing-dsn":
			prefill.SiteBDSN = ""
		case "add-site-input-missing-login":
			prefill.SiteBLogin = ""
		case "add-site-input-missing-password":
			prefill.SiteBPassword = ""
		case "add-site-input-invalid-dsn":
			prefill.SiteBDSN = "not-a-postgres-url"
		case "add-site-input-invalid-login":
			prefill.SiteBLogin = "invalid login"
		case "add-site-input-invalid-password":
			prefill.SiteBPassword = "short"
		case "add-site-input-wrong-original":
			prefill.InstallationOperation = "unrelated-install"
		case "add-site-input-operation-collision":
			prefill.OperationID = prefill.InstallationOperation
		case "add-site-input-fresh-after-collision":
			prefill.OperationID = "fresh-operation-after-collision"
		}
		options.Options.OperationID = ""
		options.AddSitePrefill = &prefill
		options.InstallationOperation = ""
	}
	err := RunAddSite(context.Background(), f, options)
	if err != nil && !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	out := map[string]any{
		"saves": f.saves, "accepted": f.accepted, "reviews": f.reviews, "applies": f.applies,
		"resumes": f.resumes, "inspections": f.inspections, "token": f.token, "operation": f.draft.OperationID,
		"installation": f.draft.InstallationOperation,
		"origin":       f.draft.SiteBOrigin, "site_login": f.draft.SiteBLogin,
	}
	b, _ := json.Marshal(out)
	if err := os.WriteFile(os.Getenv("RIXA_TERMINAL_PTY_RECEIPT"), b, 0600); err != nil {
		t.Fatal(err)
	}
}

func TestAddSitePTYConfigureReviewAndOneShotApply(t *testing.T) {
	p := startPTY(t, "add-site-full")
	pos := p.await(t, 0, "1. Configure new site")
	p.send(t, "\r")
	pos = p.await(t, pos, "Original installation operation")
	p.send(t, "\x15different-install\x1b")
	pos = p.await(t, pos, "Add Site management")
	if !strings.Contains(lastScreen(p.text()), "Unsaved edits:") {
		t.Fatal("abandoned Add Site edit did not fence Review/Apply")
	}
	p.send(t, "\r")
	pos = p.await(t, pos, "Original installation operation")
	p.send(t, "\x15owner-install\r")
	pos = p.await(t, pos, "New site-B URL")
	p.send(t, "\x15https://site-b.rixa.test:8443\r")
	pos = p.await(t, pos, "New site-B database URL")
	p.send(t, "\r")
	pos = p.await(t, pos, "New site-B administrator")
	p.send(t, "\r")
	pos = p.await(t, pos, "New site-B password")
	p.send(t, "\r")
	pos = p.await(t, pos, "Draft saved privately.")
	p.send(t, "2\r")
	pos = p.await(t, pos, "Result: inspected / prerequisites-verified")
	p.send(t, "\x1b")
	pos = p.await(t, pos, "Add Site management")
	p.send(t, "3\r")
	pos = p.await(t, pos, "State: ready")
	p.send(t, "4\r")
	pos = p.await(t, pos, "Start reviewed Add Site?")
	p.send(t, "WRONG\r")
	pos = p.await(t, pos, "Confirmation did not match")
	p.send(t, "\x1b")
	pos = p.await(t, pos, "Add Site management")
	p.send(t, "4\r")
	pos = p.await(t, pos, "Start reviewed Add Site?")
	p.send(t, "APPLY\r")
	p.await(t, pos, "State: applying")
	p.send(t, "\x03")
	receipt := p.finish(t)
	if receipt["saves"] != float64(1) || receipt["reviews"] != float64(1) ||
		receipt["applies"] != float64(1) || receipt["resumes"] != float64(0) ||
		receipt["inspections"] != float64(1) ||
		receipt["token"] != "exact-add-site-review-token" ||
		receipt["installation"] != "owner-install" {
		t.Fatalf("one-shot Add Site actions differed: %#v", receipt)
	}
	for _, secret := range []string{"PASSWORD-SECRET-CANARY", "DB-SECRET-CANARY", "exact-add-site-review-token"} {
		if strings.Contains(p.text(), secret) {
			t.Fatalf("private Add Site secret escaped: %s", secret)
		}
	}
	if !strings.Contains(p.text(), "\x1b[?1049l") {
		t.Fatal("alternate screen not restored")
	}
}

func TestAddSitePTYPlainNoColorHelpAndResize(t *testing.T) {
	for _, mode := range []string{"add-site-plain", "add-site-no-color"} {
		t.Run(mode, func(t *testing.T) {
			p := startPTY(t, mode)
			pos := p.await(t, 0, "Add Site management")
			p.send(t, "\x1bOP")
			pos = p.await(t, pos, "The original pinned TLS certificate")
			p.send(t, "\x1b")
			pos = p.await(t, pos, "Add Site management")
			if mode == "add-site-no-color" {
				if e := unix.IoctlSetWinsize(int(p.master.Fd()), unix.TIOCSWINSZ, &unix.Winsize{Col: 31, Row: 9}); e != nil {
					t.Fatal(e)
				}
				pos = p.await(t, pos, "Terminal too small")
				if e := unix.IoctlSetWinsize(int(p.master.Fd()), unix.TIOCSWINSZ, &unix.Winsize{Col: 100, Row: 30}); e != nil {
					t.Fatal(e)
				}
				pos = p.await(t, pos, "Add Site management")
			}
			p.send(t, "\x03")
			r := p.finish(t)
			if r["applies"] != float64(0) || r["saves"] != float64(0) || r["resumes"] != float64(0) {
				t.Fatalf("read-only interaction mutated backend: %#v", r)
			}
			if mode == "add-site-plain" && strings.Contains(p.text(), "\x1b[") {
				t.Fatal("plain Add Site menu emitted ANSI escape")
			}
			if mode == "add-site-no-color" && strings.Contains(p.text(), "\x1b[1;36m") {
				t.Fatal("no-color Add Site menu emitted semantic color")
			}
		})
	}
}

func TestAddSitePTYForeignReviewAndUnknownEffectsAreFenced(t *testing.T) {
	for _, mode := range []string{"add-site-foreign-review", "add-site-unknown"} {
		t.Run(mode, func(t *testing.T) {
			p := startPTY(t, mode)
			pos := p.await(t, 0, "Add Site management")
			if strings.Contains(lastScreen(p.text()), "Apply reviewed Add Site") ||
				strings.Contains(lastScreen(p.text()), "Resume retained Add Site") {
				t.Fatal("initial foreign review or unknown state exposed effects")
			}
			if mode == "add-site-unknown" {
				p.send(t, "\r")
				pos = p.await(t, pos, "reconciliation-required")
			} else {
				p.send(t, "4\r")
				pos = p.await(t, pos, "State: ready")
				if strings.Contains(lastScreen(p.text()), "Apply reviewed Add Site") {
					t.Fatal("Refresh adopted a foreign Add Site Review token")
				}
			}
			p.send(t, "\x03")
			r := p.finish(t)
			if r["applies"] != float64(0) || r["resumes"] != float64(0) {
				t.Fatalf("unapproved Add Site effect: %#v", r)
			}
		})
	}
}
func TestAddSitePTYBackendAuthorizedResume(t *testing.T) {
	p := startPTY(t, "add-site-resume")
	pos := p.await(t, 0, "1. Resume retained Add Site")
	p.send(t, "\r")
	pos = p.await(t, pos, "Resume the retained Add Site?")
	p.send(t, "RESUME\r")
	p.await(t, pos, "State: applying")
	p.send(t, "\x03")
	r := p.finish(t)
	if r["resumes"] != float64(1) || r["applies"] != float64(0) {
		t.Fatalf("native Resume did not respect positive backend action: %#v", r)
	}
}
func TestAddSitePTYUnsavedEditsNeverReachEffects(t *testing.T) {
	p := startPTY(t, "add-site-unsaved")
	pos := p.await(t, 0, "1. Configure new site")
	p.send(t, "\r")
	pos = p.await(t, pos, "Original installation operation")
	p.send(t, "\x15changed-install\x1b")
	pos = p.await(t, pos, "Unsaved edits:")
	screen := lastScreen(p.text())
	if strings.Contains(screen, "Review Add Site") || strings.Contains(screen, "Apply reviewed Add Site") {
		t.Fatalf("unsaved change enabled a backend effect: %s", screen)
	}
	p.send(t, "\x03")
	r := p.finish(t)
	if r["saves"] != float64(0) || r["applies"] != float64(0) ||
		r["reviews"] != float64(0) || r["installation"] != "owner-install" {
		t.Fatalf("unsaved Add Site draft reached backend: %#v", r)
	}
}

func TestAddSitePTYSaveErrorRedactsAllCredentialsAndAllowsCorrection(t *testing.T) {
	p := startPTY(t, "add-site-save-error")
	pos := p.await(t, 0, "1. Configure new site")
	p.send(t, "\r")
	pos = p.await(t, pos, "Original installation operation")
	p.send(t, strings.Repeat("\r", 5))
	pos = p.await(t, pos, "Not saved. Check operation ID and native Status")
	for _, secret := range []string{"PASSWORD-SECRET-CANARY", "DB-SECRET-CANARY"} {
		if strings.Contains(p.text(), secret) {
			t.Fatalf("sensitive Add Site save error reached terminal: %s", secret)
		}
	}
	p.send(t, "\r")
	p.await(t, pos, "Draft saved privately.")
	p.send(t, "\x03")
	r := p.finish(t)
	if r["saves"] != float64(2) || r["applies"] != float64(0) {
		t.Fatalf("retry after harmless Save failure failed: %#v", r)
	}
}

func TestAddSitePTYInspectRevokesLocalReviewConsent(t *testing.T) {
	p := startPTY(t, "add-site-full")
	pos := p.await(t, 0, "1. Configure new site")
	p.send(t, "\r")
	pos = p.await(t, pos, "Original installation operation")
	p.send(t, strings.Repeat("\r", 5))
	pos = p.await(t, pos, "Draft saved privately.")
	p.send(t, "3\r")
	pos = p.await(t, pos, "Apply reviewed Add Site")
	p.send(t, "2\r")
	pos = p.await(t, pos, "Result: inspected / prerequisites-verified")
	p.send(t, "\x1b")
	pos = p.await(t, pos, "Refresh Add Site status")
	if strings.Contains(lastScreen(p.text()), "Apply reviewed Add Site") {
		t.Fatal("read-only Inspect left a pre-inspection Apply token available")
	}
	p.send(t, "3\r")
	pos = p.await(t, pos, "Apply reviewed Add Site")
	p.send(t, "\x03")
	receipt := p.finish(t)
	if receipt["saves"] != float64(1) || receipt["inspections"] != float64(1) ||
		receipt["reviews"] != float64(2) || receipt["applies"] != float64(0) {
		t.Fatalf("Inspect was treated as consent or an effect: %#v", receipt)
	}
}

// These cases exercise the actual input decoder, form field focus, native
// syntax admission (shared with SaveAddSiteDraft), and typed backend error
// responses. Only the filesystem/host backend is simulated.
func TestAddSitePTYMissingFieldsStayAtEditableFieldAndCorrectOnce(t *testing.T) {
	cases := []struct {
		mode, label, correction string
		fieldIndex              int
	}{
		{"add-site-input-missing-origin", "New site-B URL", "https://site-b.rixa.test:8443", 1},
		{"add-site-input-missing-dsn", "New site-B database URL", "postgres://b-user:DB-SECRET-CANARY@127.0.0.1:5432/bdb?sslmode=disable", 2},
		{"add-site-input-missing-login", "New site-B administrator", "b-admin", 3},
		{"add-site-input-missing-password", "New site-B password", "PASSWORD-SECRET-CANARY", 4},
	}
	for _, tc := range cases {
		t.Run(tc.mode, func(t *testing.T) {
			p := startPTY(t, tc.mode)
			pos := p.await(t, 0, "1. Configure new site")
			p.send(t, "\r")
			pos = p.await(t, pos, "Original installation operation")
			p.send(t, strings.Repeat("\r", tc.fieldIndex))
			pos = p.await(t, pos, tc.label)
			p.send(t, "\r")
			pos = p.await(t, pos, "This new Site-B field is required")
			// Validation must stop on the actual field: no backend Save
			// attempt or effect is possible until the local correction.
			if !strings.Contains(lastScreen(p.text()), tc.label) {
				t.Fatalf("missing-field validation did not keep focus on %s", tc.label)
			}
			p.send(t, tc.correction+"\r")
			if tc.fieldIndex < 4 {
				pos = p.await(t, pos, "Configure")
			}
			p.send(t, strings.Repeat("\r", 4-tc.fieldIndex))
			p.await(t, pos, "Draft saved privately.")
			p.send(t, "\x03")
			r := p.finish(t)
			if r["saves"] != float64(1) || r["accepted"] != float64(1) ||
				r["reviews"] != float64(0) || r["applies"] != float64(0) || r["resumes"] != float64(0) {
				t.Fatalf("invalid field was submitted or correction failed: %#v", r)
			}
			for _, secret := range []string{"DB-SECRET-CANARY", "PASSWORD-SECRET-CANARY"} {
				if strings.Contains(p.text(), secret) {
					t.Fatal("secret appeared in validation or PTY output")
				}
			}
		})
	}
}

func TestAddSitePTYNativeFieldErrorsRefocusAndCorrectOnce(t *testing.T) {
	cases := []struct {
		mode, focused, errorText, correction string
		fieldIndex                           int
	}{
		{"add-site-input-invalid-dsn", "New site-B database URL", "explicit loopback TCP PostgreSQL URL",
			"postgres://b-user:DB-SECRET-CANARY@127.0.0.1:5432/bdb?sslmode=disable", 2},
		{"add-site-input-invalid-login", "New site-B administrator", "login", "b-admin", 3},
		{"add-site-input-invalid-password", "New site-B password", "15 to 256 Unicode characters",
			"PASSWORD-SECRET-CANARY", 4},
		{"add-site-input-wrong-original", "Original installation operation", "eligible installed state",
			"owner-install", 0},
	}
	for _, tc := range cases {
		t.Run(tc.mode, func(t *testing.T) {
			p := startPTY(t, tc.mode)
			pos := p.await(t, 0, "1. Configure new site")
			p.send(t, "\r")
			pos = p.await(t, pos, "Original installation operation")
			p.send(t, strings.Repeat("\r", 5))
			// This error comes from the native Save/ValidateDraftFields
			// facade rather than the local required-field preflight.
			pos = p.await(t, pos, tc.errorText)
			if !strings.Contains(lastScreen(p.text()), tc.focused) {
				t.Fatalf("native Save did not refocus %s after denial: %s", tc.focused, tail(p.text(), 900))
			}
			p.send(t, "\x15"+tc.correction+"\r")
			p.send(t, strings.Repeat("\r", 4-tc.fieldIndex))
			p.await(t, pos, "Draft saved privately.")
			p.send(t, "\x03")
			r := p.finish(t)
			if r["saves"] != float64(2) || r["accepted"] != float64(1) ||
				r["reviews"] != float64(0) || r["applies"] != float64(0) || r["resumes"] != float64(0) ||
				r["installation"] != "owner-install" {
				t.Fatalf("native Save rejected/accepted incorrect effects: %#v", r)
			}
			for _, secret := range []string{"DB-SECRET-CANARY", "PASSWORD-SECRET-CANARY", "not-a-postgres-url"} {
				if strings.Contains(p.text(), secret) {
					t.Fatal("sensitive field leaked during correction")
				}
			}
		})
	}
}

func TestAddSitePTYUnmatchedOperationErrorExplainsNoStateMutation(t *testing.T) {
	p := startPTY(t, "add-site-input-operation-error")
	pos := p.await(t, 0, "1. Configure new site")
	p.send(t, "\r")
	pos = p.await(t, pos, "Original installation operation")
	p.send(t, strings.Repeat("\r", 5))
	pos = p.await(t, pos, "Not saved. Check operation ID and native Status")
	if !strings.Contains(lastScreen(p.text()), "Back to edit") {
		t.Fatal("unmapped native operation failure blamed the password field")
	}
	p.send(t, "\x03")
	r := p.finish(t)
	if r["saves"] != float64(1) || r["accepted"] != float64(0) ||
		r["applies"] != float64(0) || r["reviews"] != float64(0) {
		t.Fatalf("native operation error silently saved state: %#v", r)
	}
}

func TestAddSitePTYNoneditableOperationIDReportsRestartGuidance(t *testing.T) {
	p := startPTY(t, "add-site-input-operation-collision")
	pos := p.await(t, 0, "1. Configure new site")
	p.send(t, "\r")
	pos = p.await(t, pos, "Original installation operation")
	p.send(t, strings.Repeat("\r", 5))
	pos = p.await(t, pos, "Exit and reopen with a new --operation ID")
	p.send(t, "\x03")
	r := p.finish(t)
	if r["saves"] != float64(1) || r["accepted"] != float64(0) ||
		r["applies"] != float64(0) {
		t.Fatalf("noneditable operation ID bypassed Native Save: %#v", r)
	}
}

// The PTY must handle the typed result actually emitted by Native
// SaveAddSiteDraft's real existing-journal collision branches (proven by
// serverinstall's filesystem tests), not only the local syntax-equality gate.
func TestAddSitePTYRealJournalCollisionGuidesRestartAndFreshID(t *testing.T) {
	p := startPTY(t, "add-site-input-real-journal-collision")
	pos := p.await(t, 0, "1. Configure new site")
	p.send(t, "\r")
	pos = p.await(t, pos, "Original installation operation")
	p.send(t, strings.Repeat("\r", 5))
	pos = p.await(t, pos, "Exit and reopen with a new --operation ID")
	screen := lastScreen(p.text())
	if !strings.Contains(screen, "Operation details") || strings.Contains(screen, "New site-B password") ||
		!strings.Contains(screen, "Changing a site field in this menu cannot change the operation ID") {
		t.Fatal("actual journal collision did not move focus to a dedicated restart notice")
	}
	p.send(t, "\r")
	pos = p.await(t, pos, "Add Site management")
	if strings.Contains(lastScreen(p.text()), "Apply reviewed Add Site") ||
		strings.Contains(lastScreen(p.text()), "Resume retained Add Site") {
		t.Fatal("failed Save exposed a Native effect")
	}
	p.send(t, "\x03")
	r := p.finish(t)
	if r["saves"] != float64(1) || r["accepted"] != float64(0) ||
		r["reviews"] != float64(0) || r["applies"] != float64(0) || r["resumes"] != float64(0) {
		t.Fatalf("existing journal collision mutated Native state: %#v", r)
	}
	for _, secret := range []string{"PASSWORD-SECRET-CANARY", "DB-SECRET-CANARY", "foreign-install", "/var/lib/rixa-manager"} {
		if strings.Contains(p.text(), secret) {
			t.Fatal("protected operation detail escaped to PTY")
		}
	}

	fresh := startPTY(t, "add-site-input-fresh-after-collision")
	pos = fresh.await(t, 0, "1. Configure new site")
	fresh.send(t, "\r")
	pos = fresh.await(t, pos, "Original installation operation")
	fresh.send(t, strings.Repeat("\r", 5))
	fresh.await(t, pos, "Draft saved privately.")
	fresh.send(t, "\x03")
	saved := fresh.finish(t)
	if saved["saves"] != float64(1) || saved["accepted"] != float64(1) ||
		saved["operation"] != "fresh-operation-after-collision" ||
		saved["reviews"] != float64(0) || saved["applies"] != float64(0) {
		t.Fatalf("new unique operation could not be saved safely: %#v", saved)
	}
	for _, secret := range []string{"PASSWORD-SECRET-CANARY", "DB-SECRET-CANARY"} {
		if strings.Contains(fresh.text(), secret) {
			t.Fatal("new operation's secret escaped to terminal")
		}
	}
}
