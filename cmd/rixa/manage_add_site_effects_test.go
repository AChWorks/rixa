// SPDX-License-Identifier: MPL-2.0
package main

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/AChWorks/rixa/internal/serverinstall"
)

type mockAddSiteEffects struct {
	mockSitePreparer
	applies, resumes int
	token            string
}

func (m *mockAddSiteEffects) ApplyAddSite(_ context.Context, id, token string) (serverinstall.Result, error) {
	m.applies++
	m.token = token
	return serverinstall.Result{OperationKind: "add-site", OperationID: id, State: "applying", Code: "queued"}, nil
}
func (m *mockAddSiteEffects) ResumeAddSite(_ context.Context, id string) (serverinstall.Result, error) {
	m.resumes++
	return serverinstall.Result{OperationKind: "add-site", OperationID: id, State: "applying", Code: "resumed"}, nil
}
func TestManageAddSiteEffectCLIRequiresReviewTokenAndExactOperation(t *testing.T) {
	backend := &mockAddSiteEffects{}
	id := "site-b-effect"
	token := strings.Repeat("a", 64)
	var out bytes.Buffer
	for _, args := range [][]string{
		{"add-site", "apply", "--operation", id},
		{"add-site", "apply", "--operation", id, "--review-token", ""},
		{"add-site", "apply", "--operation", id, "--review-token", token, "extra"},
		{"add-site", "resume", "--operation", id, "--review-token", token},
		{"add-site", "resume", "--operation", ""},
	} {
		out.Reset()
		if err := manageWithFacade(context.Background(), args, nil, &out, backend); err == nil {
			t.Fatalf("unvalidated effect accepted: %v", args)
		}
	}
	if backend.applies != 0 || backend.resumes != 0 {
		t.Fatal("unvalidated effect dispatched")
	}
	out.Reset()
	if err := manageWithFacade(context.Background(), []string{"add-site", "apply", "--operation", id, "--review-token", token}, nil, &out, backend); err != nil || backend.applies != 1 || backend.token != token || !strings.Contains(out.String(), "queued") || strings.Contains(out.String(), token) {
		t.Fatalf("bounded apply failed: %v %q", err, out.String())
	}
	out.Reset()
	if err := manageWithFacade(context.Background(), []string{"add-site", "resume", "--operation", id}, nil, &out, backend); err != nil || backend.resumes != 1 || !strings.Contains(out.String(), "resumed") {
		t.Fatalf("bounded resume failed: %v %q", err, out.String())
	}
}
