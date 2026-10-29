// SPDX-License-Identifier: MPL-2.0

package main

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/AChWorks/rixa/internal/serverinstall"
	"github.com/AChWorks/rixa/internal/terminal"
	"golang.org/x/sys/unix"
)

type manageTestFacade struct {
	saved serverinstall.Draft
	calls []string
	token string
}

func (f *manageTestFacade) SaveDraft(_ context.Context, d serverinstall.Draft) (serverinstall.Result, error) {
	f.saved = d
	f.calls = append(f.calls, "draft")
	return serverinstall.Result{OperationID: d.OperationID, State: "draft", Code: "saved"}, nil
}
func (f *manageTestFacade) LoadDraft(context.Context, string) (serverinstall.Draft, error) {
	return f.saved, nil
}
func (f *manageTestFacade) Review(_ context.Context, id string) (serverinstall.Result, error) {
	f.calls = append(f.calls, "review")
	return serverinstall.Result{OperationID: id, State: "ready", Code: "ready", ReviewToken: "review-token-case"}, nil
}
func (f *manageTestFacade) Apply(_ context.Context, id string, token string) (serverinstall.Result, error) {
	f.token = token
	f.calls = append(f.calls, "apply")
	return serverinstall.Result{OperationID: id, State: "applying", Code: "queued"}, nil
}
func (f *manageTestFacade) Status(_ context.Context, id string) (serverinstall.Result, error) {
	f.calls = append(f.calls, "status")
	return serverinstall.Result{OperationID: id, State: "blocked", Code: "database-unavailable", Problems: []serverinstall.Problem{{Field: "control_dsn", Code: "database", Message: "Existing database is unavailable; inspect provider connectivity."}}}, serverinstall.ErrPrerequisite
}
func (f *manageTestFacade) Remove(_ context.Context, id string) (serverinstall.Result, error) {
	f.calls = append(f.calls, "remove")
	return serverinstall.Result{OperationID: id, State: "removed", Code: "removed"}, nil
}
func writeManageDraft(t *testing.T, body string, mode os.FileMode) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "draft.json")
	if e := os.WriteFile(path, []byte(body), mode); e != nil {
		t.Fatal(e)
	}
	return path
}

func TestManageDraftPrivateInputNeverBecomesOutput(t *testing.T) {
	path := writeManageDraft(t, `{"operation_id":"case","control_dsn":"postgres://user:PRIVATE-DSN-CANARY@127.0.0.1/db","control_password":"PRIVATE-PASSWORD-CANARY"}`, 0600)
	f := &manageTestFacade{}
	var out strings.Builder
	if e := manageWithFacade(context.Background(), []string{"draft", "--source", path, "--operation", "case", "--non-interactive"}, nil, &out, f); e != nil {
		t.Fatal(e)
	}
	if len(f.calls) != 1 || f.saved.ControlPassword != "PRIVATE-PASSWORD-CANARY" {
		t.Fatal("draft not delegated exactly")
	}
	if strings.Contains(out.String(), "PRIVATE-") {
		t.Fatal("private draft leaked")
	}
	var receipt serverinstall.Result
	if json.Unmarshal([]byte(out.String()), &receipt) != nil || receipt.State != "draft" {
		t.Fatalf("safe receipt missing: %q", out.String())
	}
}
func TestManageStatusFailureKeepsActionableJSON(t *testing.T) {
	f := &manageTestFacade{}
	var out strings.Builder
	e := manageWithFacade(context.Background(), []string{"status", "--operation", "case", "--non-interactive"}, nil, &out, f)
	if !errors.Is(e, serverinstall.ErrPrerequisite) || !strings.Contains(out.String(), "provider connectivity") {
		t.Fatalf("failure detail missing: %v / %q", e, out.String())
	}
	if strings.Contains(out.String(), "\x1b") {
		t.Fatal("direct JSON contains ANSI")
	}
}
func TestManageRemovalRequiresExplicitConfirmation(t *testing.T) {
	f := &manageTestFacade{}
	var out strings.Builder
	if e := manageWithFacade(context.Background(), []string{"remove", "--operation", "case"}, nil, &out, f); e == nil || len(f.calls) != 0 {
		t.Fatal("unconfirmed removal reached backend")
	}
	if e := manageWithFacade(context.Background(), []string{"remove", "--operation", "case", "--confirm-remove", "--non-interactive"}, nil, &out, f); e != nil || len(f.calls) != 1 || f.calls[0] != "remove" {
		t.Fatalf("confirmed removal: %v %#v", e, f.calls)
	}
}
func TestManageNoninteractiveMenuNeverPrompts(t *testing.T) {
	f := &manageTestFacade{}
	var out strings.Builder
	if e := manageWithFacade(context.Background(), []string{"--non-interactive"}, nil, &out, f); e == nil || len(f.calls) != 0 || out.Len() != 0 {
		t.Fatal("noninteractive menu opened")
	}
	if e := manageWithFacade(context.Background(), nil, nil, &out, f); !errors.Is(e, terminal.ErrTerminal) {
		t.Fatalf("missing terminal: %v", e)
	}
}
func TestManagePrivateDraftRejectsUnsafeAndMalformedSources(t *testing.T) {
	for _, body := range []string{`{"operation_id":"case","unknown":"SECRET-CANARY"}`, `{"operation_id":"case"} {}`, `{"control_password":"SECRET-CANARY"`} {
		path := writeManageDraft(t, body, 0600)
		if _, e := readManageDraft(path); e == nil || strings.Contains(e.Error(), "SECRET-CANARY") {
			t.Fatalf("unsafe parse: %v", e)
		}
	}
	public := writeManageDraft(t, `{"operation_id":"case"}`, 0644)
	if _, e := readManageDraft(public); e == nil {
		t.Fatal("public draft accepted")
	}
	private := writeManageDraft(t, `{"operation_id":"case"}`, 0600)
	link := filepath.Join(t.TempDir(), "link.json")
	if e := os.Symlink(private, link); e != nil {
		t.Fatal(e)
	}
	if _, e := readManageDraft(link); e == nil {
		t.Fatal("symlink accepted")
	}
	fifo := filepath.Join(t.TempDir(), "fifo")
	if e := unix.Mkfifo(fifo, 0600); e != nil {
		t.Fatal(e)
	}
	if _, e := readManageDraft(fifo); e == nil {
		t.Fatal("FIFO accepted")
	}
	if _, e := readManageDraft("relative.json"); e == nil {
		t.Fatal("relative path accepted")
	}
}
func TestManageFlagErrorsDoNotEchoSensitiveArguments(t *testing.T) {
	f := &manageTestFacade{}
	var out strings.Builder
	e := manageWithFacade(context.Background(), []string{"--bad=SECRET-CANARY"}, nil, &out, f)
	if e == nil || strings.Contains(e.Error(), "SECRET-CANARY") || strings.Contains(out.String(), "SECRET-CANARY") {
		t.Fatal("flag error echoed input")
	}
}

func TestManagePrefillIsPrivateAndNeverPrinted(t *testing.T) {
	path := writeManageDraft(t, `{"operation_id":"case","archive":"/private/archive","control_password":"PREFILL-SECRET-CANARY"}`, 0600)
	var out strings.Builder
	e := manageWithFacade(context.Background(), []string{"menu", "--prefill", path}, nil, &out, &manageTestFacade{})
	if !errors.Is(e, terminal.ErrTerminal) || strings.Contains(out.String(), "PREFILL-SECRET") {
		t.Fatalf("prefill handling: %v %q", e, out.String())
	}
	e = manageWithFacade(context.Background(), []string{"status", "--operation", "case", "--prefill", path}, nil, &out, &manageTestFacade{})
	if e == nil {
		t.Fatal("prefill accepted for a direct operation")
	}
}
func TestManageHelpHasRealCommands(t *testing.T) {
	var out strings.Builder
	e := manageWithFacade(context.Background(), []string{"--help"}, nil, &out, &manageTestFacade{})
	if e != nil || !strings.Contains(out.String(), "--prefill") || !strings.Contains(out.String(), "--confirm-remove") {
		t.Fatalf("help: %v %q", e, out.String())
	}
}

func (f *manageTestFacade) Resume(_ context.Context, id string) (serverinstall.Result, error) {
	f.calls = append(f.calls, "resume")
	return serverinstall.Result{OperationID: id, State: "applying", Code: "resumed"}, nil
}

func TestManageApplyRequiresAndCarriesExplicitReviewToken(t *testing.T) {
	f := &manageTestFacade{}
	var out strings.Builder
	e := manageWithFacade(context.Background(), []string{"apply", "--operation", "case"}, nil, &out, f)
	if e == nil || len(f.calls) != 0 {
		t.Fatal("apply without explicit reviewed token reached backend")
	}
	e = manageWithFacade(context.Background(), []string{"apply", "--operation", "case", "--review-token", "opaque-reviewed-token"}, nil, &out, f)
	if e != nil || f.token != "opaque-reviewed-token" {
		t.Fatalf("exact token not forwarded: %v %#v", e, f)
	}
}
func TestManageResumeUsesSeparateBackendAction(t *testing.T) {
	f := &manageTestFacade{}
	var out strings.Builder
	e := manageWithFacade(context.Background(), []string{"resume", "--operation", "case"}, nil, &out, f)
	if e != nil || len(f.calls) != 1 || f.calls[0] != "resume" {
		t.Fatalf("resume not delegated: %v %#v", e, f.calls)
	}
}
