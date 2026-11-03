// SPDX-License-Identifier: MPL-2.0
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/AChWorks/rixa/internal/serverinstall"
	"github.com/AChWorks/rixa/internal/terminal"
)

func TestAddSiteMenuInputAndCapabilityRejectBeforeEffects(t *testing.T) {
	effects := &mockAddSiteEffects{}
	for _, args := range [][]string{
		{"add-site", "menu", "--non-interactive"},
		{"add-site", "--non-interactive"},
		{"add-site", "menu", "--operation", "site-b", "unparsed"},
		{"add-site", "menu", "--operation", "site-b", "--review-token", "secret"},
		{"add-site", "menu", "--source", "/private/not-a-menu-input"},
		{"add-site", "menu", "--confirm-remove"},
		{"add-site", "menu", "--unknown", "secret"},
	} {
		var out bytes.Buffer
		err := manageWithFacade(context.Background(), args, nil, &out, effects)
		if err == nil || out.Len() != 0 || effects.saves != 0 || effects.applies != 0 ||
			effects.resumes != 0 || effects.reviews != 0 {
			t.Fatalf("invalid/noninteractive Add Site menu invoked effects: args=%v err=%v output=%q", args, err, out.String())
		}
		if strings.Contains(err.Error(), "secret") || strings.Contains(err.Error(), "/private/not-a-menu-input") {
			t.Fatal("sensitive input leaked into CLI error")
		}
	}
	for _, limited := range []serverinstall.Facade{&mockSitePreparer{}, &mockSiteInspector{}} {
		var out bytes.Buffer
		err := manageWithFacade(context.Background(), []string{"add-site", "menu"}, nil, &out, limited)
		if err == nil || !strings.Contains(err.Error(), "not the interactive effects menu") || out.Len() != 0 {
			t.Fatalf("incomplete typed Add Site facade unexpectedly offered effects: %v", err)
		}
	}
}

func TestAddSiteMenuPrefillPrivacyAndTerminalBoundary(t *testing.T) {
	input := serverinstall.AddSiteDraft{OperationID: "addition-pty"}
	input.InstallationOperation = "original-operator"
	input.SiteBOrigin = "https://b.example:8443"
	input.SiteBDSN = "postgres://user:DB-PREFILL-SECRET@127.0.0.1:5432/b?sslmode=disable"
	input.SiteBLogin = "private-login"
	input.SiteBPassword = "PREFILL-PRIVATE-PASSWORD"
	file := filepath.Join(t.TempDir(), "add-site-private.json")
	raw, err := json.Marshal(input)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(file, raw, 0600); err != nil {
		t.Fatal(err)
	}
	f := &mockAddSiteEffects{}
	for _, args := range [][]string{
		{"add-site", "menu", "--prefill", file, "--installation", "other-original"},
		{"add-site", "menu", "--prefill", file, "--operation", "other-id"},
	} {
		var out bytes.Buffer
		if err := manageWithFacade(context.Background(), args, nil, &out, f); err == nil || out.Len() != 0 {
			t.Fatalf("conflicting Add Site prefill admitted: %v %q", args, out.String())
		}
	}
	var out bytes.Buffer
	err = manageWithFacade(context.Background(), []string{"add-site", "menu", "--prefill", file, "--operation", "addition-pty"}, nil, &out, f)
	if !errors.Is(err, terminal.ErrTerminal) || out.Len() != 0 {
		t.Fatalf("Add Site menu unexpectedly prompted without terminal: %v %q", err, out.String())
	}
	if f.applies != 0 || f.saves != 0 || f.reviews != 0 || f.resumes != 0 {
		t.Fatal("nonterminal prefill reached effects")
	}
	if err := os.Chmod(file, 0644); err != nil {
		t.Fatal(err)
	}
	out.Reset()
	err = manageWithFacade(context.Background(), []string{"add-site", "menu", "--prefill", file}, nil, &out, f)
	if err == nil || out.Len() != 0 || strings.Contains(err.Error(), input.SiteBDSN) {
		t.Fatalf("unsafe private Add Site prefill admitted or leaked: %v %q", err, out.String())
	}
}
