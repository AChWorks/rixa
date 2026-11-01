// SPDX-License-Identifier: MPL-2.0

package serverinstall

import (
	"context"
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

func TestNativeUpdateStopHistoryGatesServiceEffects(t *testing.T) {
	// Strictly read-only systemd observations: any attempted disable, restart
	// or service mutation is an immediate test failure. This exercises the
	// actual gate used by executeUpdate's stop stage, including resumed state.
	active := "LoadState=loaded\nActiveState=active\nMainPID=123\nControlGroup=\n"
	quiescent := "LoadState=loaded\nActiveState=inactive\nMainPID=0\nControlGroup=\n"
	foreign := "LoadState=loaded\nActiveState=inactive\nMainPID=0\nControlGroup=/foreign.slice/worker.service\n"
	for _, tc := range []struct {
		name         string
		attempted    bool
		unitState    string
		observation  string
		wantMayStop  bool
		wantRejected bool
	}{
		{"first-active-may-check-ledger-then-stop", false, "enabled", active, true, false},
		{"first-already-quiescent-requires-reconciliation", false, "disabled", quiescent, false, true},
		{"resumed-prior-active-cannot-claim-uninterrupted", true, "enabled", active, false, true},
		{"resumed-reenabled-inactive-still-uncertain", true, "enabled", quiescent, false, true},
		{"resumed-disabled-but-active-still-uncertain", true, "disabled", active, false, true},
		{"resumed-foreign-cgroup-fails-closed", true, "disabled", foreign, false, true},
		{"resumed-confirmed-disabled-quiescent-no-replay", true, "disabled", quiescent, false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := testManager(t)
			observations := 0
			m.run = func(_ context.Context, cmd string, args ...string) ([]byte, error) {
				if cmd != "/usr/bin/systemctl" || len(args) == 0 || args[0] != "show" {
					t.Fatalf("stop-history gate attempted a service effect: %s %v", cmd, args)
				}
				observations++
				if strings.Contains(strings.Join(args, " "), "--property=UnitFileState") {
					return []byte(tc.unitState + "\n"), nil
				}
				return []byte(tc.observation), nil
			}
			mayStop, err := m.admitUpdateStop(context.Background(), tc.attempted)
			if mayStop != tc.wantMayStop || (errors.Is(err, ErrState)) != tc.wantRejected {
				t.Fatalf("incorrect stop-history decision: mayStop=%t error=%v", mayStop, err)
			}
			if observations == 0 {
				t.Fatal("stop-history gate did not observe systemd")
			}
		})
	}

	// The known pending manager-owned ledger can be rejected on an actual
	// first attempt; a manager-current ledger permits only the existing
	// post-stop candidate compatibility, never migration admission.
	for _, tc := range []struct {
		state   string
		current int
		pending []string
		wantErr bool
	}{
		{"pending", 2, []string{"rixa-editorial-v3-20261031-site-grants"}, true},
		{"current", 3, nil, false},
	} {
		report := product.EditorialMigrationInspection{
			Scope: "rixa-editorial-only", Admission: "not-authorized",
			Sites: []product.EditorialMigrationSite{{ID: "site-a", State: tc.state, Current: tc.current, Target: 3, Pending: tc.pending}},
		}
		err := preStopEditorialDecision(report)
		if errors.Is(err, errPreStopEditorialPending) != tc.wantErr {
			t.Fatalf("ledger state=%s incorrectly classified before stop: %v", tc.state, err)
		}
	}
	if got := strings.Join(updateStages, ","); got != "payload,stop,compatibility,migrate,check,activate,start,postflight,commit" {
		t.Fatalf("existing authenticated candidate stage order changed: %s", got)
	}
}

func TestNativeUpdateResumedStopNeverIssuesUninterruptedReceipt(t *testing.T) {
	for _, tc := range []struct {
		name string
		run  func(*Manager, *updateJournal) (Result, error)
	}{
		{"resumed-pending-ledger", func(m *Manager, u *updateJournal) (Result, error) {
			// This is an intentionally defensive direct invocation: even if a
			// future caller passes a pending-ledger error from a resumed stage,
			// the rejection function must not manufacture uninterrupted history.
			return m.rejectPendingEditorialBeforeStop(context.Background(), u, nil, errPreStopEditorialPending, true)
		}},
		{"resumed-uncertain-service-ownership", func(m *Manager, u *updateJournal) (Result, error) {
			return m.unresolvedPriorStop(u, ErrState)
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := testManager(t)
			u := frozenUpdate()
			h := strings.Repeat("a", 64)
			u.Stage = "stop"
			u.Attempts["executor"], u.Attempts["payload"], u.Attempts["stop"] = "verified", "verified", "attempted"
			u.BinarySHA256, u.UnitSHA256 = h, h
			u.CandidateComponents = product.FoundationVersions()
			if err := m.saveUpdate(u); err != nil {
				t.Fatalf("invalid retained stop-attempt fixture: %v", err)
			}
			if err := m.claimUpdate(u.Draft.OperationID); err != nil {
				t.Fatal(err)
			}

			result, err := tc.run(m, u)
			if !errors.Is(err, ErrState) || result.State != "reconciliation-required" || result.Code != "prior-stop-history-uncertain" {
				t.Fatalf("uncertain prior stop was incorrectly finalized: %+v err=%v", result, err)
			}
			if strings.Contains(result.Message, "No service stop") || strings.Contains(result.Message, "prior-uninterrupted") {
				t.Fatal("uncertain prior stop manufactured uninterrupted-service history")
			}
			retained, err := m.loadUpdate(u.Draft.OperationID)
			if err != nil || retained.State != "reconciliation-required" || retained.Attempts["stop"] != "attempted" ||
				retained.Attempts["migrate"] != "" || retained.Attempts["compatibility"] != "" || retained.Code != "prior-stop-history-uncertain" {
				t.Fatalf("uncertain attempt journal/history not preserved: %+v err=%v", retained, err)
			}
			pending, err := m.updateMarker("pending-update")
			if err != nil || pending != u.Draft.OperationID {
				t.Fatalf("uncertain stop cleared pending-update ownership: %q err=%v", pending, err)
			}
		})
	}
	// The positive post-stop restoration receipt and its distinct structure
	// remain covered by TestNativeUpdateRejectedPreStopJournalMustProveNoMigrationIntent,
	// and the active systemd disable acknowledgement by
	// TestDisableUpdateServiceReconcilesLostAcknowledgementByObservation.
}
