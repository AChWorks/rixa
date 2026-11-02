// SPDX-License-Identifier: MPL-2.0
package serverinstall

import (
	"reflect"
	"strings"
	"testing"
)

func testAddSiteEvidence(j *addSiteJournal) addSiteEvidence {
	return addSiteEvidence{
		ParentSHA256:       j.ParentSHA256,
		ActiveUpdate:       j.ActiveUpdate,
		ActiveUpdateSHA256: j.ActiveUpdateSHA256,
		ConfigSHA256:       j.ConfigSHA256,
		EnvironmentSHA256:  j.EnvironmentSHA256,
		UnitSHA256:         j.UnitSHA256,
		ManagerSHA256:      j.ReviewedManagerSHA256,
		DatabaseTargets:    j.DatabaseTargets,
	}
}

func TestAddSiteEvidenceExactPreparedSnapshotBinding(t *testing.T) {
	reviewed := reviewedAddSiteFixture()
	observed := testAddSiteEvidence(reviewed)
	if !observed.matches(reviewed) {
		t.Fatal("matching current evidence refused")
	}
	copy := *reviewed
	copy.DatabaseTargets = map[string]string{}
	observed.bind(&copy)
	if !observed.matches(&copy) || !reflect.DeepEqual(copy.DatabaseTargets, reviewed.DatabaseTargets) {
		t.Fatal("captured review evidence did not bind exactly")
	}
	copy.DatabaseTargets["site-b"] = "3:230"
	if observed.DatabaseTargets["site-b"] == "3:230" || observed.matches(&copy) {
		t.Fatal("mutating a saved database binding changed the observed evidence")
	}
	cases := []struct {
		name   string
		mutate func(*addSiteJournal)
	}{
		{"original-parent", func(j *addSiteJournal) { j.ParentSHA256 = strings.Repeat("f", 64) }},
		{"active-update-marker", func(j *addSiteJournal) { j.ActiveUpdate = "unexpected-update" }},
		{"active-update-digest", func(j *addSiteJournal) { j.ActiveUpdateSHA256 = strings.Repeat("f", 64) }},
		{"runtime-config", func(j *addSiteJournal) { j.ConfigSHA256 = strings.Repeat("f", 64) }},
		{"private-env", func(j *addSiteJournal) { j.EnvironmentSHA256 = strings.Repeat("f", 64) }},
		{"loaded-unit", func(j *addSiteJournal) { j.UnitSHA256 = strings.Repeat("f", 64) }},
		{"running-manager", func(j *addSiteJournal) { j.ReviewedManagerSHA256 = strings.Repeat("f", 64) }},
		{"database-target", func(j *addSiteJournal) { j.DatabaseTargets["site-b"] = "3:230" }},
		{"missing-database", func(j *addSiteJournal) { delete(j.DatabaseTargets, "site-b") }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			j := *reviewed
			j.DatabaseTargets = make(map[string]string, len(reviewed.DatabaseTargets))
			for k, v := range reviewed.DatabaseTargets {
				j.DatabaseTargets[k] = v
			}
			tc.mutate(&j)
			if observed.matches(&j) {
				t.Fatal("stale Add Site evidence accepted")
			}
		})
	}
	// A completed managed update is supported only if *both* active receipt
	// identity and its private contents are identical to the reviewed bytes.
	reviewed.ActiveUpdate = "complete-update"
	reviewed.ActiveUpdateSHA256 = strings.Repeat("1", 64)
	if observed.matches(reviewed) {
		t.Fatal("unexpected active managed update accepted")
	}
	observed = testAddSiteEvidence(reviewed)
	if !observed.matches(reviewed) {
		t.Fatal("matching completed update lineage refused")
	}
	reviewed.ActiveUpdateSHA256 = strings.Repeat("2", 64)
	if observed.matches(reviewed) {
		t.Fatal("altered active update journal accepted")
	}
}
