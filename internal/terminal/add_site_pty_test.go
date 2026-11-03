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
	options := AddSiteOptions{
		Options: Options{
			Input: os.Stdin, Output: os.Stdout, OperationID: "pty-case",
			Version: "v0.8.0", Plain: mode == "add-site-plain",
			NoColor: mode == "add-site-no-color",
		},
		InstallationOperation: "owner-install",
	}
	err := RunAddSite(context.Background(), f, options)
	if err != nil && !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	out := map[string]any{
		"saves": f.saves, "reviews": f.reviews, "applies": f.applies,
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
	pos = p.await(t, pos, "private Add Site admission unavailable")
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
