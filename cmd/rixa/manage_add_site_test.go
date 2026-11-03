// SPDX-License-Identifier: MPL-2.0
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/AChWorks/rixa/internal/serverinstall"
)

type mockSiteInspector struct {
	serverinstall.Facade
	seen  serverinstall.AddSiteInspectionDraft
	calls int
}

func (s *mockSiteInspector) InspectAddSite(_ context.Context, d serverinstall.AddSiteInspectionDraft) (serverinstall.Result, error) {
	s.seen = d
	s.calls++
	return serverinstall.Result{OperationKind: "add-site-inspection", OperationID: d.InstallationOperation, State: "inspected", Code: "prerequisites-verified"}, nil
}

func TestManageAddSiteInspectionCLIRequiresPrivateSourceAndDoesNotExposeSecrets(t *testing.T) {
	input := serverinstall.AddSiteInspectionDraft{
		InstallationOperation: "native-original", SiteBOrigin: "https://site-b.example:8443",
		SiteBDSN:   "postgres://siteb:secret@127.0.0.1:5432/siteb?sslmode=disable",
		SiteBLogin: "new-admin", SiteBPassword: "test-secret-password",
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "private.json")
	data, err := json.Marshal(input)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	stub := &mockSiteInspector{}
	var out bytes.Buffer
	if err = manageWithFacade(context.Background(), []string{"add-site", "inspect", "--source", path}, nil, &out, stub); err != nil {
		t.Fatalf("inspection call failed: %v", err)
	}
	if stub.calls != 1 || stub.seen != input || !strings.Contains(out.String(), "prerequisites-verified") {
		t.Fatalf("CLI did not route validated private input: %+v %q", stub, out.String())
	}
	if bytes.Contains(out.Bytes(), []byte(input.SiteBDSN)) || bytes.Contains(out.Bytes(), []byte(input.SiteBPassword)) {
		t.Fatal("private add-site source leaked in output")
	}
	for _, args := range [][]string{
		{"add-site", "inspect"},
		{"add-site", "apply", "--source", path},
		{"add-site", "inspect", "--source", path, "--unexpected"},
		{"add-site", "inspect", "--source", path, "extra"},
	} {
		out.Reset()
		if err = manageWithFacade(context.Background(), args, nil, &out, stub); err == nil {
			t.Fatalf("invalid add-site action unexpectedly succeeded: %v", args)
		}
	}
	if stub.calls != 1 {
		t.Fatal("invalid direct action called inspector")
	}
	if err = os.Chmod(path, 0644); err != nil {
		t.Fatal(err)
	}
	if err = manageWithFacade(context.Background(), []string{"add-site", "inspect", "--source", path}, nil, &out, stub); err == nil {
		t.Fatal("world-readable draft accepted")
	}
	if stub.calls != 1 {
		t.Fatal("unsafe private draft reached inspector")
	}
}

type mockSitePreparer struct {
	mockSiteInspector
	saved                    serverinstall.AddSiteDraft
	saves, reviews, statuses int
}

func (s *mockSitePreparer) SaveAddSiteDraft(_ context.Context, d serverinstall.AddSiteDraft) (serverinstall.Result, error) {
	s.saved = d
	s.saves++
	return serverinstall.Result{OperationKind: "add-site", OperationID: d.OperationID, State: "draft", Code: "draft-saved"}, nil
}
func (s *mockSitePreparer) LoadAddSiteDraft(_ context.Context, _ string) (serverinstall.AddSiteDraft, error) {
	return s.saved, nil
}
func (s *mockSitePreparer) ReviewAddSite(_ context.Context, id string) (serverinstall.Result, error) {
	s.reviews++
	return serverinstall.Result{OperationKind: "add-site", OperationID: id, State: "ready", Code: "ready"}, nil
}
func (s *mockSitePreparer) AddSiteStatus(_ context.Context, id string) (serverinstall.Result, error) {
	s.statuses++
	return serverinstall.Result{OperationKind: "add-site", OperationID: id, State: "ready", Code: "ready"}, nil
}

func TestManageAddSitePreparationOnlyFacadeNeverAcquiresEffects(t *testing.T) {
	d := serverinstall.AddSiteDraft{
		OperationID: "new-site-addition",
		AddSiteInspectionDraft: serverinstall.AddSiteInspectionDraft{
			InstallationOperation: "original-install",
			SiteBOrigin:           "https://site-b.example:8443",
			SiteBDSN:              "postgres://siteb:db-secret@127.0.0.1:5432/siteb?sslmode=disable",
			SiteBLogin:            "site-b-admin", SiteBPassword: "secret-admin-password",
		},
	}
	raw, err := json.Marshal(d)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "add-site-draft.json")
	if err = os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	backend := &mockSitePreparer{}
	var out bytes.Buffer
	if err = manageWithFacade(context.Background(), []string{"add-site", "--help"}, nil, &out, backend); err != nil ||
		!strings.Contains(out.String(), "managed Apply/Resume") || backend.saves != 0 {
		t.Fatalf("preparation-only help was unavailable or dispatched effects: %v %q", err, out.String())
	}
	out.Reset()
	if err = manageWithFacade(context.Background(), []string{"add-site", "draft", "--source", path}, nil, &out, backend); err != nil {
		t.Fatalf("Add Site private draft CLI: %v", err)
	}
	if backend.saves != 1 || backend.saved != d || !strings.Contains(out.String(), "draft-saved") {
		t.Fatalf("private Add Site source not routed: %+v %q", backend, out.String())
	}
	if strings.Contains(out.String(), d.SiteBPassword) || strings.Contains(out.String(), d.SiteBDSN) {
		t.Fatal("Add Site draft emitted secrets")
	}
	out.Reset()
	if err = manageWithFacade(context.Background(), []string{"add-site", "review", "--operation", d.OperationID}, nil, &out, backend); err != nil || backend.reviews != 1 || !strings.Contains(out.String(), "ready") || strings.Contains(out.String(), "review_token") {
		t.Fatalf("review not routed: %v %q", err, out.String())
	}
	out.Reset()
	if err = manageWithFacade(context.Background(), []string{"add-site", "status", "--operation", d.OperationID}, nil, &out, backend); err != nil || backend.statuses != 1 || strings.Contains(out.String(), strings.Repeat("a", 64)) {
		t.Fatalf("status disclosed token or not routed: %v %q", err, out.String())
	}
	for _, args := range [][]string{
		{"add-site", "apply", "--operation", d.OperationID},
		{"add-site", "resume", "--operation", d.OperationID},
		{"add-site", "draft", "--source", path, "extra"},
		{"add-site", "draft", "--source", path, "--unrecognized"},
		{"add-site", "status", "--source", path},
		{"add-site", "review", "--operation", d.OperationID, "--source", path},
	} {
		out.Reset()
		if err = manageWithFacade(context.Background(), args, nil, &out, backend); err == nil {
			t.Fatalf("unavailable Add Site action accepted: %q", args)
		}
	}
	if backend.saves != 1 || backend.reviews != 1 || backend.statuses != 1 {
		t.Fatal("invalid Add Site CLI reached effects")
	}
	if err = os.Chmod(path, 0644); err != nil {
		t.Fatal(err)
	}
	if err = manageWithFacade(context.Background(), []string{"add-site", "draft", "--source", path}, nil, &out, backend); err == nil {
		t.Fatal("world-readable Add Site draft accepted")
	}
	if backend.saves != 1 {
		t.Fatal("unsafe private source reached backend")
	}
}
