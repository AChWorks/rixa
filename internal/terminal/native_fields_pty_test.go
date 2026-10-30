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

	"github.com/AChWorks/rixa/internal/serverinstall"
)

func TestNativePTYLaterListenerAndBackEdits(t *testing.T) {
	p := startPTY(t, "native-fields")
	position := p.await(t, 0, "1. Configure installation")
	p.send(t, "\r")
	position = p.await(t, position, "Control URL")
	p.send(t, "\x15https://control.rixa.test\r")
	position = p.await(t, position, "Site URL")
	p.send(t, "\x15https://site.rixa.test\r")
	position = p.await(t, position, "TLS listener")
	p.send(t, "\x150.0.0.0:443\r")
	position = p.await(t, position, "TLS certificate")
	p.send(t, strings.Repeat("\r", 9))
	position = p.await(t, position, "Draft saved privately.")

	// Reopen the saved draft, then change both earlier origins before their
	// later listener. Back must retain the newly entered work at every step.
	p.send(t, "1\r")
	position = p.await(t, position, "Control URL")
	p.send(t, "\x15https://control.rixa.test:9443\r")
	position = p.await(t, position, "Site URL")
	p.send(t, "\x15https://site.rixa.test:9443\r")
	position = p.await(t, position, "TLS listener")
	p.send(t, "\x1b")
	position = p.await(t, position, "https://site.rixa.test:9443")
	p.send(t, "\x1b")
	position = p.await(t, position, "https://control.rixa.test:9443")
	p.send(t, "\r\r")
	position = p.await(t, position, "TLS listener")
	p.send(t, "\x150.0.0.0:9443\r")
	position = p.await(t, position, "TLS certificate")
	p.send(t, "\x1b")
	position = p.await(t, position, "0.0.0.0:9443")
	p.send(t, "\r")
	position = p.await(t, position, "TLS certificate")
	p.send(t, strings.Repeat("\r", 9))
	p.await(t, position, "Draft saved privately.")
	p.send(t, "\x03")
	receipt := p.finish(t)
	if receipt["save_attempts"] != float64(2) || receipt["saves"] != float64(2) || receipt["rejected"] != float64(0) {
		t.Fatalf("incorrect save results: %#v", receipt)
	}
	if receipt["control_origin"] != "https://control.rixa.test:9443" || receipt["site_origin"] != "https://site.rixa.test:9443" || receipt["listen"] != "0.0.0.0:9443" {
		t.Fatalf("changed fields lost: %#v", receipt)
	}
	listeners, ok := receipt["saved_listeners"].([]any)
	if !ok || len(listeners) != 2 || listeners[0] != "0.0.0.0:443" || listeners[1] != "0.0.0.0:9443" {
		t.Fatalf("complete drafts did not reach the authoritative validator: %#v", receipt)
	}
	assertNativePTYNoEffectsOrSecrets(t, p, receipt)
}

func TestNativePTYFinalMismatchRefusedBeforeEffects(t *testing.T) {
	p := startPTY(t, "native-fields")
	position := p.await(t, 0, "1. Configure installation")
	p.send(t, "\r")
	position = p.await(t, position, "Control URL")
	p.send(t, "\x15https://control.rixa.test\r")
	position = p.await(t, position, "Site URL")
	p.send(t, "\x15https://site.rixa.test\r")
	position = p.await(t, position, "TLS listener")
	// Retain 8443 deliberately. Field-local validity permits forward editing,
	// but completing the draft must return to the offending origin without
	// saving, reviewing, applying, or losing the entered origins.
	p.send(t, strings.Repeat("\r", 10))
	p.await(t, position, "The HTTPS origin port must match the listener port.")
	p.await(t, position, "https://control.rixa.test")
	p.send(t, "\x03")
	receipt := p.finish(t)
	if receipt["save_attempts"] != float64(1) || receipt["saves"] != float64(0) || receipt["rejected"] != float64(1) {
		t.Fatalf("invalid completed draft was not refused: %#v", receipt)
	}
	if receipt["rejected_control_origin"] != "https://control.rixa.test" || receipt["rejected_site_origin"] != "https://site.rixa.test" || receipt["rejected_listen"] != "0.0.0.0:8443" {
		t.Fatalf("validator received different inputs: %#v", receipt)
	}
	assertNativePTYNoEffectsOrSecrets(t, p, receipt)
}

func assertNativePTYNoEffectsOrSecrets(t *testing.T, p *ptyCapture, receipt map[string]any) {
	t.Helper()
	if receipt["reviews"] != float64(0) || receipt["applies"] != float64(0) || receipt["removes"] != float64(0) {
		t.Fatalf("configuration caused unrelated effects: %#v", receipt)
	}
	for _, secret := range []string{"DB-CONTROL-CANARY", "DB-SITE-CANARY", "PASSWORD-CONTROL-CANARY", "PASSWORD-SITE-CANARY"} {
		if strings.Contains(p.text(), secret) {
			t.Fatalf("secret printed: %s", secret)
		}
	}
}

// Only the native UI and authoritative pure input validator execute here.
// The facade retains fake state; no native host actions or listeners run.
type nativeFieldsFacade struct {
	testFacade
	attempts       int
	rejected       int
	lastRejected   serverinstall.Draft
	savedListeners []string
}

func (f *nativeFieldsFacade) SaveDraft(ctx context.Context, d serverinstall.Draft) (serverinstall.Result, error) {
	f.attempts++
	if problems := serverinstall.ValidateDraft(d); len(problems) != 0 {
		f.rejected++
		f.lastRejected = d
		return serverinstall.Result{OperationID: d.OperationID, State: "draft", Code: "invalid-input", Problems: problems, Actions: []string{"configure"}}, serverinstall.ErrInput
	}
	f.savedListeners = append(f.savedListeners, d.Listen)
	return f.testFacade.SaveDraft(ctx, d)
}

func nativeFieldsPTYChild(t *testing.T) {
	f := &nativeFieldsFacade{testFacade: testFacade{draft: testDraft()}}
	err := Run(context.Background(), f, Options{Input: os.Stdin, Output: os.Stdout, OperationID: "pty-case", Version: "test"})
	if err != nil && !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	receipt := map[string]any{
		"save_attempts": f.attempts, "saves": f.saves, "rejected": f.rejected,
		"reviews": f.reviews, "applies": f.applies, "removes": f.removes,
		"control_origin": f.draft.ControlOrigin, "site_origin": f.draft.SiteOrigin, "listen": f.draft.Listen,
		"saved_listeners":         f.savedListeners,
		"rejected_control_origin": f.lastRejected.ControlOrigin, "rejected_site_origin": f.lastRejected.SiteOrigin, "rejected_listen": f.lastRejected.Listen,
	}
	b, err := json.Marshal(receipt)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(os.Getenv("RIXA_TERMINAL_PTY_RECEIPT"), b, 0600); err != nil {
		t.Fatal(err)
	}
}
