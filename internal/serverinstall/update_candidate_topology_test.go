// SPDX-License-Identifier: MPL-2.0
package serverinstall

import (
	"context"
	"errors"
	"maps"
	"reflect"
	"testing"
)

// The candidate is an effectful executable, including at migrate.
// A topology failure must be distinguishable from checkPayload rejecting an
// absent binary, or removal of the execution-boundary guard would go undetected.
func TestCandidateCommandTopologyPreflight(t *testing.T) {
	for _, tc := range []struct {
		name     string
		twoSites bool
		alter    func(*updateJournal, *journal)
		want     int
	}{
		{"single-site", false, nil, 1},
		{"two-site", true, nil, 2},
		{"single-draft-three-targets", false, func(u *updateJournal, j *journal) {
			j.DatabaseTargets["site-b"] = "1:102"
			u.DatabaseTargets = maps.Clone(j.DatabaseTargets)
		}, 0},
		{"two-site-draft-two-targets", true, func(u *updateJournal, j *journal) {
			delete(j.DatabaseTargets, "site-b")
			u.DatabaseTargets = maps.Clone(j.DatabaseTargets)
		}, 0},
		{"two-site-wrong-key", true, func(u *updateJournal, j *journal) {
			delete(j.DatabaseTargets, "site-b")
			j.DatabaseTargets["site-c"] = "1:102"
			u.DatabaseTargets = maps.Clone(j.DatabaseTargets)
		}, 0},
		{"two-site-duplicate-database", true, func(u *updateJournal, j *journal) {
			j.DatabaseTargets["site-b"] = j.DatabaseTargets["site-a"]
			u.DatabaseTargets = maps.Clone(j.DatabaseTargets)
		}, 0},
		{"invalid-database-identity", false, func(u *updateJournal, j *journal) {
			j.DatabaseTargets["control"] = "invalid"
			u.DatabaseTargets = maps.Clone(j.DatabaseTargets)
		}, 0},
		{"reviewed-target-drift", true, func(u *updateJournal, _ *journal) {
			u.DatabaseTargets["site-b"] = "1:999"
		}, 0},
		{"wrong-parent-operation", false, func(u *updateJournal, _ *journal) {
			u.Draft.InstallationOperation = "other-install"
		}, 0},
		{"missing-journal-target", false, func(u *updateJournal, j *journal) {
			delete(j.DatabaseTargets, "control")
			u.DatabaseTargets = maps.Clone(j.DatabaseTargets)
		}, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			draft := validDraft()
			if tc.twoSites {
				draft = validTwoSiteDraft()
			}
			if p := ValidateDraft(draft); len(p) != 0 {
				t.Fatalf("invalid source installation fixture: %+v", p)
			}
			j := &journal{
				Draft:           draft,
				DatabaseTargets: map[string]string{"control": "1:100", "site-a": "1:101"},
			}
			if tc.twoSites {
				j.DatabaseTargets["site-b"] = "1:102"
			}
			u := &updateJournal{Draft: validUpdateDraft(), DatabaseTargets: maps.Clone(j.DatabaseTargets)}
			if tc.alter != nil {
				tc.alter(u, j)
			}
			sites, err := candidateExpectedSites(u, j)
			m := testManager(t)
			if tc.want != 0 {
				if err != nil || sites != tc.want {
					t.Fatalf("legitimate candidate topology rejected: sites=%d err=%v", sites, err)
				}
				// No payload is staged; plain ErrState from checkPayload proves
				// this valid topology passed the earlier candidate guard.
				for _, stage := range []string{"migrate", "check"} {
					if e := m.candidateCommand(context.Background(), u, j, stage); e != ErrState {
						t.Fatalf("valid %s topology failed before payload admission: %v", stage, e)
					}
				}
				return
			}
			if sites != 0 || !errors.Is(err, ErrState) || !errors.Is(err, errCandidateTopology) {
				t.Fatalf("inconsistent candidate topology admitted: sites=%d err=%v", sites, err)
			}
			for _, stage := range []string{"migrate", "check"} {
				err := m.candidateCommand(context.Background(), u, j, stage)
				if !errors.Is(err, ErrState) || !errors.Is(err, errCandidateTopology) {
					t.Fatalf("%s did not reject topology before payload admission/execution: %v", stage, err)
				}
			}
		})
	}
}

// Exercise the actual independently committed Add Site projection rather than
// an origin-only synthetic Draft. The committed bootstrap password is erased,
// while the site-B DSN, principal and physical database identities persist.
func TestCandidateCommandCommittedAddSiteTopologyPreflight(t *testing.T) {
	h := makeAddedSiteUpdateHistory(t)
	current := h.beforeU2Parent(t)
	if current.Draft.SiteBOrigin == "" || current.Draft.SiteBDSN == "" ||
		current.SiteBPrincipal == "" || current.Draft.SiteBPassword != "" ||
		!reflect.DeepEqual(current.DatabaseTargets, h.addition.DatabaseTargets) {
		t.Fatal("committed Add Site fixture is not a complete projected two-site topology")
	}
	sites, err := candidateExpectedSites(h.second, current)
	if err != nil || sites != 2 {
		t.Fatalf("committed Add Site topology rejected: sites=%d err=%v", sites, err)
	}
	// No executable payload is staged for this journal copy; even a
	// legitimate topology cannot launch a candidate in this test.
	noPayload := *current
	noPayload.ArchiveSHA256, noPayload.BinarySHA256 = "", ""
	for _, stage := range []string{"migrate", "check"} {
		if err := h.m.candidateCommand(context.Background(), h.second, &noPayload, stage); err != ErrState {
			t.Fatalf("valid committed Add Site %s failed before payload check: %v", stage, err)
		}
	}
	drift := noPayload
	drift.DatabaseTargets = maps.Clone(noPayload.DatabaseTargets)
	drift.DatabaseTargets["site-b"] = "9:999"
	for _, stage := range []string{"migrate", "check"} {
		err := h.m.candidateCommand(context.Background(), h.second, &drift, stage)
		if !errors.Is(err, ErrState) || !errors.Is(err, errCandidateTopology) {
			t.Fatalf("committed Add Site target drift escaped %s preflight: %v", stage, err)
		}
	}
}
