// SPDX-License-Identifier: MPL-2.0

package serverinstall

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"
)

func TestSaveAddSiteDraftReportsSafeEditableProblemsWithoutCreatingState(t *testing.T) {
	m := testManager(t)
	parent := savedAddSiteParent(t, m)
	canonical := addSiteDraftFixture()
	canonical.InstallationOperation = parent.Draft.OperationID
	cases := []struct {
		name, field, code string
		edit              func(*AddSiteDraft)
	}{
		{"missing-origin", "site_b_origin", "required", func(d *AddSiteDraft) { d.SiteBOrigin = "" }},
		{"missing-dsn", "site_b_dsn", "required", func(d *AddSiteDraft) { d.SiteBDSN = "" }},
		{"missing-login", "site_b_login", "required", func(d *AddSiteDraft) { d.SiteBLogin = "" }},
		{"missing-password", "site_b_password", "required", func(d *AddSiteDraft) { d.SiteBPassword = "" }},
		{"invalid-new-operation-id", "operation_id", "operation-id", func(d *AddSiteDraft) { d.OperationID = "BAD-ID" }},
		{"colliding-parent-operation-id", "operation_id", "operation-id", func(d *AddSiteDraft) { d.OperationID = d.InstallationOperation }},
		{"invalid-parent-syntax", "installation_operation", "installation-id", func(d *AddSiteDraft) { d.InstallationOperation = "WRONG ID" }},
		{"linebreak-dsn", "site_b_dsn", "format", func(d *AddSiteDraft) { d.SiteBDSN += "\ncanary" }},
		{"overlong-password", "site_b_password", "format", func(d *AddSiteDraft) { d.SiteBPassword = strings.Repeat("X", 4097) }},
		{"malformed-postgres-url", "site_b_dsn", "database-profile", func(d *AddSiteDraft) { d.SiteBDSN = "not-a-postgres-url" }},
		{"bad-https-origin", "site_b_origin", "origin", func(d *AddSiteDraft) { d.SiteBOrigin = "http://wrong.invalid:8443" }},
		{"weak-site-password", "site_b_password", "password", func(d *AddSiteDraft) { d.SiteBPassword = "short" }},
		{"invalid-site-login", "site_b_login", "login", func(d *AddSiteDraft) { d.SiteBLogin = "invalid login" }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			d := canonical
			tc.edit(&d)
			r, err := m.SaveAddSiteDraft(context.Background(), d)
			if !errors.Is(err, ErrInput) || r.State != "draft" || r.Code != "invalid-input" {
				t.Fatalf("invalid direct-native draft accepted: %+v %v", r, err)
			}
			matched := false
			for _, p := range r.Problems {
				if p.Field == tc.field && p.Code == tc.code {
					matched = true
				}
			}
			if !matched {
				t.Fatalf("missing actionable native field/code %s/%s; actual: %+v", tc.field, tc.code, r.Problems)
			}
			raw, err := json.Marshal(r)
			if err != nil {
				t.Fatal(err)
			}
			for _, secret := range []string{"separate-db-secret", "strong-site-b-password", "not-a-postgres-url", "canary"} {
				if strings.Contains(string(raw), secret) {
					t.Fatal("native result disclosed private field value")
				}
			}
			if _, err := os.Lstat(m.addSitePath(d.OperationID)); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("invalid native draft persisted: %v", err)
			}
		})
	}
}

func TestSaveAddSiteDraftParentMismatchNeverEnumeratesOwner(t *testing.T) {
	m := testManager(t)
	parent := savedAddSiteParent(t, m)
	d := addSiteDraftFixture()
	d.InstallationOperation = "unrelated-install"
	bad, err := m.SaveAddSiteDraft(context.Background(), d)
	if !errors.Is(err, ErrState) || len(bad.Problems) != 1 ||
		bad.Problems[0].Field != "installation_operation" || bad.Problems[0].Code != "original-not-eligible" {
		t.Fatalf("wrong original ID not mapped to guided, generic field: %+v %v", bad, err)
	}
	if strings.Contains(bad.Problems[0].Message, d.InstallationOperation) ||
		strings.Contains(bad.Problems[0].Message, parent.Draft.OperationID) {
		t.Fatal("parent diagnostic disclosed physical owner identity")
	}
	d.InstallationOperation = parent.Draft.OperationID
	if result, err := m.SaveAddSiteDraft(context.Background(), d); err != nil || result.Code != "draft-saved" {
		t.Fatalf("valid correction could not be saved: %+v %v", result, err)
	}
}

func TestAddSiteDraftInputUsesSameJournalRulesExceptErasedInstalledPassword(t *testing.T) {
	d := addSiteDraftFixture()
	if p := ValidateAddSiteDraftInput(d); len(p) != 0 || !validAddSiteDraft(d) {
		t.Fatalf("valid new draft diverged from persisted journal admission: %+v", p)
	}
	d.SiteBPassword = ""
	if validAddSiteDraft(d) == false || len(ValidateAddSiteDraftInput(d)) != 1 ||
		ValidateAddSiteDraftInput(d)[0].Field != "site_b_password" {
		t.Fatal("installed redacted journal must be valid, new draft must require password")
	}
}
