// SPDX-License-Identifier: MPL-2.0

package main

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/AChWorks/rixa/internal/serverinstall"
)

type postgresManageTestFacade struct {
	manageTestFacade
	callsPG []string
	draftPG serverinstall.PostgresDraft
	tokenPG string
}

func (p *postgresManageTestFacade) pgResult(id, state string) serverinstall.PostgresResult {
	return serverinstall.PostgresResult{Result: serverinstall.Result{OperationKind: "postgres", OperationID: id, State: state, Code: state, Actions: []string{"status"}}, Plan: &serverinstall.PostgresPlan{Profile: "clean-dedicated", Cluster: "rixa", Port: 55432, Packages: []serverinstall.PostgresPackage{{Name: "postgresql-18", Version: "exact-reviewed-version"}}}}
}
func (p *postgresManageTestFacade) SavePostgresDraft(_ context.Context, d serverinstall.PostgresDraft) (serverinstall.PostgresResult, error) {
	p.draftPG = d
	p.callsPG = append(p.callsPG, "draft")
	return p.pgResult(d.OperationID, "draft"), nil
}
func (p *postgresManageTestFacade) LoadPostgresDraft(context.Context, string) (serverinstall.PostgresDraft, error) {
	return p.draftPG, nil
}
func (p *postgresManageTestFacade) ReviewPostgres(_ context.Context, id string) (serverinstall.PostgresResult, error) {
	p.callsPG = append(p.callsPG, "review")
	r := p.pgResult(id, "ready")
	r.ReviewToken = "exact-pg-token"
	return r, nil
}
func (p *postgresManageTestFacade) ApplyPostgres(_ context.Context, id, token string) (serverinstall.PostgresResult, error) {
	p.callsPG = append(p.callsPG, "apply")
	p.tokenPG = token
	return p.pgResult(id, "applying"), nil
}
func (p *postgresManageTestFacade) ResumePostgres(_ context.Context, id string) (serverinstall.PostgresResult, error) {
	p.callsPG = append(p.callsPG, "resume")
	return p.pgResult(id, "applying"), nil
}
func (p *postgresManageTestFacade) PostgresStatus(_ context.Context, id string) (serverinstall.PostgresResult, error) {
	p.callsPG = append(p.callsPG, "status")
	r := p.pgResult(id, "reconciliation-required")
	r.Message = "Inspect the retained package/cluster outcome before resume."
	return r, serverinstall.ErrState
}
func (p *postgresManageTestFacade) DiscoverPostgres(context.Context) ([]serverinstall.PostgresDiscovery, error) {
	p.callsPG = append(p.callsPG, "discover")
	return nil, nil
}
func (p *postgresManageTestFacade) LoadPostgresCredentials(context.Context, string) (serverinstall.PostgresCredentials, error) {
	p.callsPG = append(p.callsPG, "PRIVATE-CREDENTIAL-HANDOFF")
	return serverinstall.PostgresCredentials{}, errors.New("private handoff is not a CLI verb")
}
func TestPostgresDirectTypedDispatchAndPrivateBoundary(t *testing.T) {
	path := writeManageDraft(t, `{"operation_id":"pg-case","port":55432,"dedicated_host":true}`, 0600)
	f := &postgresManageTestFacade{}
	for _, args := range [][]string{{"draft", "--source", path}, {"review", "--operation", "pg-case"}, {"apply", "--operation", "pg-case", "--review-token", "exact-pg-token"}, {"resume", "--operation", "pg-case"}, {"status", "--operation", "pg-case"}} {
		var out strings.Builder
		e := manageWithFacade(context.Background(), append(append([]string{"prerequisites"}, args...), "--non-interactive"), nil, &out, f)
		if args[0] == "status" {
			if !errors.Is(e, serverinstall.ErrState) || !strings.Contains(out.String(), "Inspect the retained package") {
				t.Fatal("lost actionable PG status")
			}
		} else if e != nil {
			t.Fatal(e)
		}
		if !strings.Contains(out.String(), "exact-reviewed-version") || strings.Contains(out.String(), "\x1b") {
			t.Fatalf("exact safe plan missing: %q", out.String())
		}
	}
	if !reflect.DeepEqual(f.callsPG, []string{"draft", "review", "apply", "resume", "status"}) || f.tokenPG != "exact-pg-token" || !f.draftPG.DedicatedHost || len(f.calls) != 0 {
		t.Fatalf("wrong PG dispatch: %#v", f)
	}
}
func TestPostgresDirectNeverPrintsCredentialsOrPrompts(t *testing.T) {
	source := writeManageDraft(t, `{"operation_id":"pg-case","control_dsn":"PRIVATE-CANARY"}`, 0600)
	for _, args := range [][]string{{"--non-interactive"}, {"credentials", "--operation", "pg-case"}, {"remove", "--operation", "pg-case"}, {"apply", "--operation", "pg-case"}, {"draft", "--source", source}, {"status", "--operation", "pg-case", "--review-token", "token"}, {"status", "--operation", "pg-case", "--plain"}} {
		f := &postgresManageTestFacade{}
		var out strings.Builder
		e := manageWithFacade(context.Background(), append([]string{"prerequisites"}, args...), nil, &out, f)
		if e == nil || len(f.callsPG) != 0 || len(f.calls) != 0 || out.Len() != 0 || strings.Contains(e.Error(), "PRIVATE-CANARY") {
			t.Fatalf("unsafe PG command: %v %#v", e, f)
		}
	}
	var out strings.Builder
	if e := manageWithFacade(context.Background(), []string{"prerequisites", "status", "--operation", "case"}, nil, &out, &manageTestFacade{}); e == nil {
		t.Fatal("legacy facade claimed PG support")
	}
}
