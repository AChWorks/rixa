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
