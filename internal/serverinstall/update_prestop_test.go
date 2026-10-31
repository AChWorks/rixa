// SPDX-License-Identifier: MPL-2.0

package serverinstall

import (
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/AChWorks/rixa/internal/product"
)

// The inspector's real PostgreSQL-bound effects are exercised by the
// TestInspectEditorialMigrationsReadOnlyOnIndependentSites CI integration proof.
// These tests enforce the distinct manager rejection/journal boundary.
func TestNativeUpdatePreStopEditorialDecisionIsNegativeOnly(t *testing.T) {
	current := product.EditorialMigrationInspection{
		Scope: "rixa-editorial-only", Admission: "not-authorized",
		Sites: []product.EditorialMigrationSite{{ID: "site-a", State: "current", Current: 3, Target: 3, Pending: []string{}}},
	}
	if err := preStopEditorialDecision(current); err != nil {
		t.Fatalf("current editorial ledger must defer to post-stop candidate compatibility: %v", err)
	}

	// A manager that understands v3 recognizes v2 as requiring a native
	// schema-changing upgrade. It must refuse *before* disable or migration.
	pending := current
	pending.Sites = []product.EditorialMigrationSite{
		{ID: "site-a", State: "pending", Current: 2, Target: 3, Pending: []string{"rixa-editorial-v3-20261031-site-grants"}},
	}
	if err := preStopEditorialDecision(pending); !errors.Is(err, errPreStopEditorialPending) {
		t.Fatalf("retained v2 editorial pending schema not fenced: %v", err)
	}
	v1 := pending
	v1.Sites = []product.EditorialMigrationSite{
		{ID: "site-a", State: "pending", Current: 1, Target: 3, Pending: []string{"v2", "v3"}},
	}
	if err := preStopEditorialDecision(v1); !errors.Is(err, errPreStopEditorialPending) {
		t.Fatalf("retained v1 pending schema not fenced: %v", err)
	}

	for _, tc := range []struct {
		name string
		edit func(*product.EditorialMigrationInspection)
	}{
		{"different-scope", func(r *product.EditorialMigrationInspection) { r.Scope = "any-db" }},
		{"claiming-authorization", func(r *product.EditorialMigrationInspection) { r.Admission = "ready" }},
		{"no-sites", func(r *product.EditorialMigrationInspection) { r.Sites = nil }},
		{"extra-site", func(r *product.EditorialMigrationInspection) { r.Sites = append(r.Sites, r.Sites[0]) }},
		{"different-site", func(r *product.EditorialMigrationInspection) { r.Sites[0].ID = "site-b" }},
		{"uninitialized", func(r *product.EditorialMigrationInspection) {
			r.Sites[0].State = "uninitialized"
			r.Sites[0].Current = 0
		}},
		{"current-with-pending", func(r *product.EditorialMigrationInspection) { r.Sites[0].Pending = []string{"v4"} }},
		{"target-too-low", func(r *product.EditorialMigrationInspection) { r.Sites[0].Target = 0; r.Sites[0].Current = 0 }},
		{"ledger-forward", func(r *product.EditorialMigrationInspection) { r.Sites[0].Current = 4 }},
		{"pending-missing-identities", func(r *product.EditorialMigrationInspection) {
			r.Sites[0].State = "pending"
			r.Sites[0].Current = 2
			r.Sites[0].Pending = nil
		}},
		{"unknown-state", func(r *product.EditorialMigrationInspection) { r.Sites[0].State = "ready" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			report := current
			report.Sites = append([]product.EditorialMigrationSite(nil), current.Sites...)
			tc.edit(&report)
			if err := preStopEditorialDecision(report); !errors.Is(err, ErrState) {
				t.Fatalf("unsupported or corrupt observation was admitted: %v", err)
			}
		})
	}

	// An intentional pending state is distinct from an unavailable or unknown
	// observation. Only the positively known case can release a pending update
	// after separately proving prior service/ownership/TLS.
}

func TestNativeUpdateRejectedPreStopJournalMustProveNoMigrationIntent(t *testing.T) {
	m := testManager(t)
	u := frozenUpdate()
	h := strings.Repeat("a", 64)
	u.Attempts["executor"] = "verified"
	u.Attempts["payload"] = "verified"
	u.Attempts["stop"] = "attempted"
	u.BinarySHA256, u.UnitSHA256 = h, h
	u.CandidateComponents = product.FoundationVersions()
	u.Stage, u.State, u.Code = "stop", "rejected", "editorial-upgrade-pending-prior-uninterrupted"
	u.Message = "Known pending Rixa migration rejected before service stop; old service verified."

	if err := m.saveUpdate(u); err != nil {
		t.Fatalf("verified negative pre-stop admission did not persist: %v", err)
	}
	if read, err := m.loadUpdate(u.Draft.OperationID); err != nil || read.Code != u.Code || read.Stage != "stop" {
		t.Fatalf("pre-stop rejected journal invalid: result=%#v err=%v", read, err)
	}
	b, err := os.ReadFile(m.updatePath(u.Draft.OperationID))
	if err != nil || strings.Contains(string(b), "control-db-secret") || strings.Contains(string(b), "site-db-secret") {
		t.Fatal("update rejection persisted private runtime credentials")
	}

	for _, tc := range []struct {
		name string
		edit func(*updateJournal)
	}{
		{"no-authenticated-payload", func(u *updateJournal) { delete(u.Attempts, "payload") }},
		{"no-candidate-binary-identity", func(u *updateJournal) { u.BinarySHA256 = ""; u.UnitSHA256 = ""; u.CandidateComponents = nil }},
		{"service-already-stopped", func(u *updateJournal) { u.Attempts["stop"] = "verified" }},
		{"compatibility-was-attempted", func(u *updateJournal) { u.Attempts["stop"] = "verified"; u.Attempts["compatibility"] = "attempted" }},
		{"migration-was-attempted", func(u *updateJournal) {
			u.Attempts["stop"] = "verified"
			u.Attempts["compatibility"] = "verified"
			u.Attempts["migrate"] = "attempted"
		}},
		{"wrong-stage", func(u *updateJournal) { u.Stage = "compatibility" }},
		{"wrong-rejection-code", func(u *updateJournal) { u.Code = "candidate-incompatible-prior-restored" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			candidate := *u
			candidate.Attempts = make(map[string]string, len(u.Attempts))
			for key, value := range u.Attempts {
				candidate.Attempts[key] = value
			}
			tc.edit(&candidate)
			if validUpdate(&candidate) {
				t.Fatal("forged prior-uninterrupted update receipt accepted")
			}
		})
	}

	// Old positive post-stop restoration remains a valid historical receipt.
	u.Stage, u.Code = "compatibility", "candidate-incompatible-prior-restored"
	u.Attempts["stop"], u.Attempts["compatibility"] = "verified", "attempted"
	if err := m.saveUpdate(u); err != nil {
		t.Fatalf("old post-stop rejected result lost compatibility: %v", err)
	}
	if _, err := m.loadUpdate(u.Draft.OperationID); err != nil {
		t.Fatalf("old post-stop rejection no longer loads: %v", err)
	}
}
