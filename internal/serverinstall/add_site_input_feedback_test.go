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

// A syntactically valid, distinct operation ID can still identify an actual
// installation, Update or Add Site journal. Every denial must guide the TUI
// without revealing the kind/owner/contents of that existing private record.
func TestSaveAddSiteDraftRealJournalCollisionsReturnNonenumeratingOperationProblem(t *testing.T) {
	ctx := context.Background()
	tests := []struct {
		name string
		seed func(*testing.T, *Manager, *journal, string)
	}{
		{"installation", func(t *testing.T, m *Manager, parent *journal, id string) {
			duplicate := *parent
			duplicate.Draft.OperationID = id
			duplicate.Frozen = draftDigest(duplicate.Draft)
			if err := m.save(&duplicate); err != nil {
				t.Fatal(err)
			}
		}},
		{"update", func(t *testing.T, m *Manager, parent *journal, id string) {
			u := validUpdateDraft()
			u.OperationID, u.InstallationOperation = id, parent.Draft.OperationID
			if _, err := m.SaveUpdateDraft(ctx, u); err != nil {
				t.Fatal(err)
			}
		}},
		{"frozen-add-site", func(t *testing.T, m *Manager, parent *journal, id string) {
			j := installedAddSiteFixture()
			j.Draft.OperationID, j.Draft.InstallationOperation = id, parent.Draft.OperationID
			j.Frozen = addSiteDraftDigest(j.Draft)
			if err := m.saveAddSite(j); err != nil {
				t.Fatal(err)
			}
		}},
		{"incompatible-add-site", func(t *testing.T, m *Manager, _ *journal, id string) {
			j := addSiteJournalFixture()
			j.Draft.OperationID, j.Draft.InstallationOperation = id, "foreign-install"
			if err := m.saveAddSite(j); err != nil {
				t.Fatal(err)
			}
		}},
		{"corrupt-installation", func(t *testing.T, m *Manager, _ *journal, id string) {
			if err := writeNew(m.statePath(id), []byte("invalid private installation receipt"), 0600); err != nil {
				t.Fatal(err)
			}
		}},
		{"corrupt-update", func(t *testing.T, m *Manager, _ *journal, id string) {
			if err := writeNew(m.updatePath(id), []byte("invalid private update receipt"), 0600); err != nil {
				t.Fatal(err)
			}
		}},
		{"corrupt-add-site", func(t *testing.T, m *Manager, _ *journal, id string) {
			if err := writeNew(m.addSitePath(id), []byte("invalid private add-site receipt"), 0600); err != nil {
				t.Fatal(err)
			}
		}},
		{"symlinked-add-site", func(t *testing.T, m *Manager, parent *journal, id string) {
			if err := os.Symlink(m.statePath(parent.Draft.OperationID), m.addSitePath(id)); err != nil {
				t.Fatal(err)
			}
		}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			m := testManager(t)
			parent := savedAddSiteParent(t, m)
			d := addSiteDraftFixture()
			d.InstallationOperation = parent.Draft.OperationID
			d.OperationID = "colliding-other-operation"
			tc.seed(t, m, parent, d.OperationID)
			originalBytes, err := os.ReadFile(m.statePath(parent.Draft.OperationID))
			if err != nil {
				t.Fatal(err)
			}
			paths := []string{m.statePath(d.OperationID), m.updatePath(d.OperationID), m.addSitePath(d.OperationID)}
			type state struct {
				kind    os.FileMode
				content string
			}
			before := make(map[string]state)
			for _, path := range paths {
				fi, err := os.Lstat(path)
				if errors.Is(err, os.ErrNotExist) {
					continue
				}
				if err != nil {
					t.Fatal(err)
				}
				if fi.Mode()&os.ModeSymlink != 0 {
					dest, err := os.Readlink(path)
					if err != nil {
						t.Fatal(err)
					}
					before[path] = state{fi.Mode(), dest}
				} else {
					b, err := os.ReadFile(path)
					if err != nil {
						t.Fatal(err)
					}
					before[path] = state{fi.Mode(), string(b)}
				}
			}
			result, err := m.SaveAddSiteDraft(ctx, d)
			if !errors.Is(err, ErrState) || result.OperationKind != "add-site" ||
				result.OperationID != d.OperationID || len(result.Problems) != 1 ||
				result.Problems[0].Field != "operation_id" || result.Problems[0].Code != "operation-id-unavailable" {
				t.Fatalf("actual collision was not a safe guided ErrState: result=%+v err=%v", result, err)
			}
			message := result.Problems[0].Message
			if !strings.Contains(message, "Exit and reopen") || !strings.Contains(message, "--operation ID") ||
				strings.Contains(message, d.OperationID) || strings.Contains(message, "foreign-install") ||
				strings.Contains(message, parent.Draft.OperationID) {
				t.Fatal("colliding operation-ID guidance missing or enumerating")
			}
			public, err := json.Marshal(result)
			if err != nil {
				t.Fatal(err)
			}
			for _, secret := range []string{d.SiteBDSN, d.SiteBPassword, d.SiteBOrigin,
				"foreign-install", parent.Draft.OperationID + ".json", "invalid private"} {
				if strings.Contains(string(public), secret) {
					t.Fatal("collision result exposed private input or existing record")
				}
			}
			for _, path := range paths {
				fi, err := os.Lstat(path)
				prev, existed := before[path]
				if !existed {
					if !errors.Is(err, os.ErrNotExist) {
						t.Fatalf("created journal on rejected save: %s: %v", path, err)
					}
					continue
				}
				if err != nil || fi.Mode() != prev.kind {
					t.Fatalf("journal type/mode changed: %s: %v", path, err)
				}
				var content string
				if fi.Mode()&os.ModeSymlink != 0 {
					content, err = os.Readlink(path)
				} else {
					var b []byte
					b, err = os.ReadFile(path)
					content = string(b)
				}
				if err != nil || content != prev.content {
					t.Fatalf("journal mutated on rejected save: %s: %v", path, err)
				}
			}
			afterParent, err := os.ReadFile(m.statePath(parent.Draft.OperationID))
			if err != nil || string(afterParent) != string(originalBytes) {
				t.Fatal("original owner journal mutated")
			}
			d.OperationID = "fresh-unique-add-site"
			saved, err := m.SaveAddSiteDraft(ctx, d)
			if err != nil || saved.Code != "draft-saved" {
				t.Fatalf("fresh operation ID could not save: %+v %v", saved, err)
			}
		})
	}
}
