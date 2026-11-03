// SPDX-License-Identifier: MPL-2.0
package serverinstall

import (
	"context"
	"errors"
	"maps"
	"testing"
)

// The candidate is an effectful executable, including at the migrate stage.
// Refuse inconsistent native topology before checking or invoking that binary.
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
			j := &journal{
				Draft:           validDraft(),
				DatabaseTargets: map[string]string{"control": "1:100", "site-a": "1:101"},
			}
			if tc.twoSites {
				j.Draft.SiteBOrigin = "https://b.rixa.test:8443"
				j.DatabaseTargets["site-b"] = "1:102"
			}
			u := &updateJournal{Draft: validUpdateDraft(), DatabaseTargets: maps.Clone(j.DatabaseTargets)}
			if tc.alter != nil {
				tc.alter(u, j)
			}
			sites, err := candidateExpectedSites(u, j)
			if tc.want != 0 {
				if err != nil || sites != tc.want {
					t.Fatalf("legitimate candidate topology rejected: sites=%d err=%v", sites, err)
				}
				return
			}
			if sites != 0 || !errors.Is(err, ErrState) {
				t.Fatalf("inconsistent candidate topology admitted: sites=%d err=%v", sites, err)
			}
			for _, stage := range []string{"migrate", "check"} {
				if err := testManager(t).candidateCommand(context.Background(), u, j, stage); !errors.Is(err, ErrState) {
					t.Fatalf("%s did not fail closed before candidate execution: %v", stage, err)
				}
			}
		})
	}
}
