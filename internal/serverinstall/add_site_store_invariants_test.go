// SPDX-License-Identifier: MPL-2.0
package serverinstall

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"reflect"
	"strings"
	"testing"
)

func applyingAddSiteFixture() *addSiteJournal {
	j := reviewedAddSiteFixture()
	j.State, j.Stage, j.Code = "applying", "executor", "queued"
	// Apply must consume the review token before retaining any executor intent.
	j.ReviewToken = ""
	j.Frozen = addSiteDraftDigest(j.Draft)
	j.ManagerSHA256 = j.ReviewedManagerSHA256
	j.ExecutorGeneration = 1
	j.Attempts["executor"] = "attempted"
	return j
}

// A queued executor is durably attempted before systemd-run, so its start may
// remain unknown. A verified executor may also have been interrupted before
// its first ordinary stage, or after a staged write. Those are not successes.
func TestAddSiteJournalAcceptsRealInterruptedExecutorReceipts(t *testing.T) {
	cases := []struct {
		name string
		edit func(*addSiteJournal)
	}{
		{"queued-unacknowledged", func(*addSiteJournal) {}},
		{"queued-acknowledged", func(j *addSiteJournal) { j.ExecutorAcknowledged = true }},
		{"executor-verified-before-stage", func(j *addSiteJournal) { j.Attempts["executor"] = "verified" }},
		{"first-stage-attempted", func(j *addSiteJournal) {
			j.ExecutorAcknowledged = true
			j.Attempts["executor"] = "verified"
			j.Stage = "prepare"
			j.Attempts["prepare"] = "attempted"
		}},
		{"second-stage-interrupted", func(j *addSiteJournal) {
			j.Attempts["executor"] = "verified"
			j.Stage = "migrate"
			j.Attempts["prepare"] = "verified"
			j.Attempts["migrate"] = "attempted"
			j.State = "reconciliation-required"
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			j := applyingAddSiteFixture()
			tc.edit(j)
			if !validAddSiteJournal(j) {
				t.Fatal("legitimate incomplete executor receipt was rejected")
			}
			m := testManager(t)
			if err := m.saveAddSite(j); err != nil {
				t.Fatalf("interrupted journal could not be retained: %v", err)
			}
			recovered, err := m.loadAddSite(j.Draft.OperationID)
			if err != nil || !reflect.DeepEqual(recovered, j) {
				t.Fatalf("interrupted journal not recovered: %v", err)
			}
			observed, err := m.AddSiteStatus(context.Background(), j.Draft.OperationID)
			if err != nil || observed.State != j.State || observed.Stage != j.Stage || observed.ReviewToken != "" {
				t.Fatalf("status misrepresented incomplete execution: %+v %v", observed, err)
			}
		})
	}
}

// Every invalid source case must fail both in-memory validation and when
// loaded from an exact private 0600 journal. Status must never emit a claimed
// applying/installed result for a forged or corrupted executor history.
func TestAddSiteJournalRejectsForgedFrozenExecutionOnLoadAndStatus(t *testing.T) {
	cases := []struct {
		name string
		base func() *addSiteJournal
		edit func(*addSiteJournal)
	}{
		{"frozen-applying-empty-stage", applyingAddSiteFixture, func(j *addSiteJournal) { j.Stage = "" }},
		{"frozen-applying-empty-attempts", applyingAddSiteFixture, func(j *addSiteJournal) { j.Attempts = map[string]string{} }},
		{"frozen-reconciliation-empty-attempts", applyingAddSiteFixture, func(j *addSiteJournal) {
			j.State = "reconciliation-required"
			j.Attempts = map[string]string{}
		}},
		{"frozen-executor-receipt-absent", applyingAddSiteFixture, func(j *addSiteJournal) { delete(j.Attempts, "executor") }},
		{"frozen-review-stage", applyingAddSiteFixture, func(j *addSiteJournal) { j.Stage = "review" }},
		{"frozen-zero-generation", applyingAddSiteFixture, func(j *addSiteJournal) { j.ExecutorGeneration = 0 }},
		{"acknowledged-without-generation", applyingAddSiteFixture, func(j *addSiteJournal) {
			j.ExecutorAcknowledged = true
			j.ExecutorGeneration = 0
		}},
		{"stage-without-receipt", applyingAddSiteFixture, func(j *addSiteJournal) {
			j.Stage = "prepare"
			j.Attempts["executor"] = "verified"
		}},
		{"advanced-stage-before-executor-verified", applyingAddSiteFixture, func(j *addSiteJournal) {
			j.Stage = "prepare"
			j.Attempts["prepare"] = "attempted"
		}},
		{"mismatched-later-stage", applyingAddSiteFixture, func(j *addSiteJournal) {
			j.Stage = "prepare"
			j.Attempts["executor"] = "verified"
			j.Attempts["prepare"] = "verified"
			j.Attempts["migrate"] = "attempted"
		}},
		{"executor-stage-with-later-attempt", applyingAddSiteFixture, func(j *addSiteJournal) {
			j.Attempts["executor"] = "verified"
			j.Attempts["prepare"] = "attempted"
		}},
		{"frozen-missing-reviewed-manager", applyingAddSiteFixture, func(j *addSiteJournal) { j.ReviewedManagerSHA256 = "" }},
		{"frozen-manager-differs-from-reviewed", applyingAddSiteFixture, func(j *addSiteJournal) { j.ManagerSHA256 = strings.Repeat("f", 64) }},
		{"frozen-missing-unit", applyingAddSiteFixture, func(j *addSiteJournal) { j.UnitSHA256 = "" }},
		{"frozen-missing-config", applyingAddSiteFixture, func(j *addSiteJournal) { j.ConfigSHA256 = "" }},
		{"frozen-missing-environment", applyingAddSiteFixture, func(j *addSiteJournal) { j.EnvironmentSHA256 = "" }},
		{"ready-with-executor-stage", reviewedAddSiteFixture, func(j *addSiteJournal) { j.Stage = "executor" }},
		{"ready-with-empty-stage", reviewedAddSiteFixture, func(j *addSiteJournal) { j.Stage = "" }},
		{"ready-missing-unit", reviewedAddSiteFixture, func(j *addSiteJournal) { j.UnitSHA256 = "" }},
		{"ready-missing-manager", reviewedAddSiteFixture, func(j *addSiteJournal) { j.ReviewedManagerSHA256 = "" }},
		{"draft-with-review-stage", addSiteJournalFixture, func(j *addSiteJournal) { j.Stage = "review" }},
		{"blocked-with-executor-stage", addSiteJournalFixture, func(j *addSiteJournal) { j.State, j.Stage = "blocked", "executor" }},
		{"installed-wrong-stage", installedAddSiteFixture, func(j *addSiteJournal) { j.Stage = "start" }},
		{"installed-not-acknowledged", installedAddSiteFixture, func(j *addSiteJournal) { j.ExecutorAcknowledged = false }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			j := tc.base()
			tc.edit(j)
			if validAddSiteJournal(j) {
				t.Fatal("invalid Add Site lifecycle state accepted by validator")
			}
			m := testManager(t)
			raw, err := json.Marshal(j)
			if err != nil {
				t.Fatal(err)
			}
			if err := writeAtomic(m.addSitePath(j.Draft.OperationID), raw, 0600); err != nil {
				t.Fatal(err)
			}
			before, err := os.ReadFile(m.addSitePath(j.Draft.OperationID))
			if err != nil {
				t.Fatal(err)
			}
			if _, err := m.loadAddSite(j.Draft.OperationID); !errors.Is(err, ErrState) {
				t.Fatalf("loadAddSite accepted impossible lifecycle state: %v", err)
			}
			status, err := m.AddSiteStatus(context.Background(), j.Draft.OperationID)
			if !errors.Is(err, ErrState) || status.State == "applying" || status.State == "installed" {
				t.Fatalf("AddSiteStatus disclosed invalid lifecycle as valid: %+v %v", status, err)
			}
			after, err := os.ReadFile(m.addSitePath(j.Draft.OperationID))
			if err != nil || !reflect.DeepEqual(before, after) {
				t.Fatalf("reading rejected journal changed retained evidence: %v", err)
			}
		})
	}
}
