// SPDX-License-Identifier: MPL-2.0

package main

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/AChWorks/rixa/internal/serverinstall"
)

type manageUpdateTestFacade struct {
	manageTestFacade
	update      serverinstall.UpdateDraft
	updateCalls []string
	updateToken string
}

func (f *manageUpdateTestFacade) receipt(id, state, code string) serverinstall.Result {
	return serverinstall.Result{OperationID: id, OperationKind: "update", InstallationOperation: f.update.InstallationOperation, ActiveRelease: "v0.4.0", Release: f.update.Release, State: state, Code: code, Actions: []string{"status"}}
}
func (f *manageUpdateTestFacade) SaveUpdateDraft(_ context.Context, d serverinstall.UpdateDraft) (serverinstall.Result, error) {
	f.update = d
	f.updateCalls = append(f.updateCalls, "draft")
	return f.receipt(d.OperationID, "draft", "saved"), nil
}
func (f *manageUpdateTestFacade) LoadUpdateDraft(context.Context, string) (serverinstall.UpdateDraft, error) {
	return f.update, nil
}
func (f *manageUpdateTestFacade) ReviewUpdate(_ context.Context, id string) (serverinstall.Result, error) {
	f.updateCalls = append(f.updateCalls, "review")
	r := f.receipt(id, "ready", "ready")
	r.ReviewToken = "exact-update-token"
	return r, nil
}
func (f *manageUpdateTestFacade) ApplyUpdate(_ context.Context, id, token string) (serverinstall.Result, error) {
	f.updateToken = token
	f.updateCalls = append(f.updateCalls, "apply")
	return f.receipt(id, "applying", "queued"), nil
}
func (f *manageUpdateTestFacade) ResumeUpdate(_ context.Context, id string) (serverinstall.Result, error) {
	f.updateCalls = append(f.updateCalls, "resume")
	return f.receipt(id, "applying", "resumed"), nil
}
func (f *manageUpdateTestFacade) UpdateStatus(_ context.Context, id string) (serverinstall.Result, error) {
	f.updateCalls = append(f.updateCalls, "status")
	r := f.receipt(id, "reconciliation-required", "activation-unknown")
	r.Problems = []serverinstall.Problem{{Field: "service", Code: "inspect", Message: "Inspect the retained active executable and service before resume."}}
	return r, serverinstall.ErrPrerequisite
}
func (f *manageUpdateTestFacade) RemoveUpdatedInstallation(_ context.Context, id string) (serverinstall.Result, error) {
	f.updateCalls = append(f.updateCalls, "remove")
	return f.receipt(id, "removed", "removed"), nil
}

func TestManageUpdateDirectLifecycleKeepsTypedBoundaries(t *testing.T) {
	draft := serverinstall.UpdateDraft{OperationID: "update-case", InstallationOperation: "owner-install", ArchivePath: "/private/candidate.tar.gz", BundlePath: "/private/candidate.tar.gz.sigstore.json", Release: "v0.5.0", SourceCommit: strings.Repeat("a", 40)}
	body, err := json.Marshal(draft)
	if err != nil {
		t.Fatal(err)
	}
	path := writeManageDraft(t, string(body), 0600)
	f := &manageUpdateTestFacade{}
	cases := [][]string{
		{"draft", "--source", path, "--operation", "update-case"},
		{"review", "--operation", "update-case"},
		{"apply", "--operation", "update-case", "--review-token", "exact-update-token"},
		{"resume", "--operation", "update-case"},
		{"remove", "--operation", "update-case", "--confirm-remove"},
	}
	for _, args := range cases {
		var out strings.Builder
		args = append(append([]string{"update"}, args...), "--non-interactive")
		if err := manageWithFacade(context.Background(), args, nil, &out, f); err != nil {
			t.Fatalf("%v: %v", args, err)
		}
		var receipt serverinstall.Result
		if err := json.Unmarshal([]byte(out.String()), &receipt); err != nil || receipt.OperationKind != "update" || receipt.InstallationOperation != "owner-install" {
			t.Fatalf("missing typed JSON: %v / %q", err, out.String())
		}
		if strings.Contains(out.String(), "\x1b") {
			t.Fatal("direct update emitted ANSI")
		}
	}
	if !reflect.DeepEqual(f.update, draft) || len(f.calls) != 0 || !reflect.DeepEqual(f.updateCalls, []string{"draft", "review", "apply", "resume", "remove"}) || f.updateToken != "exact-update-token" {
		t.Fatalf("mixed/changed dispatch: %#v", f)
	}
}

func TestManageUpdateStatusFailurePreservesActionableJSON(t *testing.T) {
	f := &manageUpdateTestFacade{update: serverinstall.UpdateDraft{InstallationOperation: "owner-install", Release: "v0.5.0"}}
	var out strings.Builder
	err := manageWithFacade(context.Background(), []string{"update", "status", "--operation", "update-case", "--non-interactive"}, nil, &out, f)
	if !errors.Is(err, serverinstall.ErrPrerequisite) || !strings.Contains(out.String(), "reconciliation-required") || !strings.Contains(out.String(), "Inspect the retained active executable") {
		t.Fatalf("lost actionable result: %v / %q", err, out.String())
	}
	if !reflect.DeepEqual(f.updateCalls, []string{"status"}) || len(f.calls) != 0 {
		t.Fatal("status repeated effects")
	}
}

func TestManageUpdateRejectsInvalidInputsBeforeBackendEffects(t *testing.T) {
	metadata := writeManageDraft(t, `{"operation_id":"update-case","installation_operation":"owner-install"}`, 0600)
	credentials := writeManageDraft(t, `{"operation_id":"update-case","control_password":"UPDATE-PRIVATE-CANARY"}`, 0600)
	cases := [][]string{
		{"update", "--non-interactive"},
		{"update", "menu", "--non-interactive"},
		{"update", "draft", "--source", metadata, "--operation", "different"},
		{"update", "draft", "--source", credentials},
		{"update", "menu", "--prefill", credentials},
		{"update", "draft", "--source", metadata, "--installation", "owner-install"},
		{"update", "apply", "--operation", "update-case"},
		{"update", "remove", "--operation", "update-case"},
		{"update", "status", "--operation", "update-case", "--review-token", "token"},
		{"update", "status", "--operation", "update-case", "--confirm-remove"},
		{"update", "review", "--operation", "update-case", "--source", metadata},
		{"update", "status", "--operation", "update-case", "--prefill", metadata},
		{"update", "review", "--operation", "update-case", "--plain"},
		{"update", "review", "--operation", "update-case", "--no-color"},
		{"update", "status", "--operation", "update-case", "extra"},
		{"update", "--bad=UPDATE-PRIVATE-CANARY"},
		{"update", "internal-update-apply", "update-case"},
	}
	for _, args := range cases {
		t.Run(strings.Join(args[:2], "-"), func(t *testing.T) {
			f := &manageUpdateTestFacade{}
			var out strings.Builder
			err := manageWithFacade(context.Background(), args, nil, &out, f)
			if err == nil || out.Len() != 0 || len(f.calls) != 0 || len(f.updateCalls) != 0 {
				t.Fatalf("invalid input caused effects: %v %q %#v", err, out.String(), f)
			}
			if strings.Contains(err.Error(), "UPDATE-PRIVATE-CANARY") {
				t.Fatal("private JSON leaked")
			}
		})
	}
}
func TestManageUpdateOptionalFacadeAndHelp(t *testing.T) {
	var out strings.Builder
	old := &manageTestFacade{}
	if err := manageWithFacade(context.Background(), []string{"update", "status", "--operation", "case"}, nil, &out, old); err == nil || !strings.Contains(err.Error(), "compatible manager") || len(old.calls) != 0 {
		t.Fatalf("unsupported facade: %v", err)
	}
	f := &manageUpdateTestFacade{}
	if err := manageWithFacade(context.Background(), []string{"update", "--help"}, nil, &out, f); err != nil || !strings.Contains(out.String(), "installation_operation") || !strings.Contains(out.String(), "database rollback") || len(f.updateCalls) != 0 {
		t.Fatalf("missing help: %v / %q", err, out.String())
	}
}
