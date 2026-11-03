// SPDX-License-Identifier: MPL-2.0
package serverinstall

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// A native Update active *before* the Add Site must survive a later topology
// expansion. The Add Site's journal pins that historical Update and the
// original installation while future Updates may change only the release view.
func TestAddSiteTopologyPreservesCompletedUpdatePredecessor(t *testing.T) {
	m := testManager(t)
	original := installedParent(t, m)
	originalBytes, err := os.ReadFile(m.statePath(original.Draft.OperationID))
	if err != nil {
		t.Fatal(err)
	}
	first := completeAddSiteUpdate(original, digest(originalBytes), "site-b-prior-update", "v0.7.0", "", "", original.Draft.Release)
	if err := m.saveUpdate(first); err != nil {
		t.Fatal(err)
	}
	firstBytes, err := os.ReadFile(m.updatePath(first.Draft.OperationID))
	if err != nil {
		t.Fatal(err)
	}
	j := installedAddSiteFixture()
	j.Draft.InstallationOperation = original.Draft.OperationID
	j.Frozen = addSiteDraftDigest(j.Draft)
	j.ParentSHA256 = digest(originalBytes)
	j.ActiveUpdate = first.Draft.OperationID
	j.ActiveUpdateSHA256 = digest(firstBytes)
	for _, site := range []string{"control", "site-a"} {
		j.DatabaseTargets[site] = original.DatabaseTargets[site]
	}
	if err := m.saveAddSite(j); err != nil {
		t.Fatal(err)
	}
	if err := writeNew(filepath.Join(m.stateRoot, "active-add-site"), []byte(j.Draft.OperationID+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	second := completeAddSiteUpdate(original, digest(originalBytes), "site-b-later-update", "v0.8.0", first.Draft.OperationID, digest(firstBytes), first.Draft.Release)
	// A subsequent update operates on the projected three-database topology.
	for key, val := range j.DatabaseTargets {
		second.DatabaseTargets[key] = val
	}
	if err := m.saveUpdate(second); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name    string
		current *journal
		release string
	}{
		{"initial-completed-predecessor", updateView(original, first), "v0.7.0"},
		{"later-completed-release", updateView(original, second), "v0.8.0"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			originalCopy := *original
			view, err := m.topologyView(original, tc.current)
			if err != nil || view.Draft.Release != tc.release ||
				view.Draft.ControlOrigin != original.Draft.ControlOrigin ||
				view.Draft.SiteOrigin != original.Draft.SiteOrigin ||
				view.Draft.SiteBOrigin != j.Draft.SiteBOrigin ||
				view.SiteBPrincipal != j.SiteBPrincipal ||
				!reflect.DeepEqual(view.DatabaseTargets, j.DatabaseTargets) ||
				view.Draft.SiteBPassword != "" {
				t.Fatalf("completed update lost or invented topology: %+v %v", view, err)
			}
			if !reflect.DeepEqual(original, &originalCopy) {
				t.Fatal("derived view mutated immutable original")
			}
		})
	}
	// Changing the exact active predecessor bytes must invalidate the Add
	// Site receipt even if its operation ID remains unchanged.
	first.Message = "altered completed update metadata"
	if err := m.saveUpdate(first); err != nil {
		t.Fatal(err)
	}
	if _, err := m.topologyView(original, updateView(original, first)); !errors.Is(err, ErrState) {
		t.Fatalf("changed committed Update admitted: %v", err)
	}
}

func TestAddSitePendingClaimFencesNativeLifecycleUntilReleased(t *testing.T) {
	m := testManager(t)
	id := "pending-site-b"
	if err := m.claimAddSite(id); err != nil {
		t.Fatal(err)
	}
	if err := m.rejectPendingAddSite(); !errors.Is(err, ErrBusy) {
		t.Fatalf("ordinary lifecycle not fenced: %v", err)
	}
	if err := m.clearAddSiteClaim(id); err != nil {
		t.Fatal(err)
	}
	if err := m.rejectPendingAddSite(); err != nil {
		t.Fatalf("clean topology still blocked: %v", err)
	}
	marker := filepath.Join(m.stateRoot, "pending-add-site")
	if err := os.WriteFile(marker, []byte(strings.Repeat("x", 130)), 0600); err != nil {
		t.Fatal(err)
	}
	if err := m.rejectPendingAddSite(); err == nil {
		t.Fatal("unparseable pending topology marker treated as absent")
	}
	if b, _ := os.ReadFile(marker); !bytes.Equal(b, []byte(strings.Repeat("x", 130))) {
		t.Fatal("read-only failed state check altered the suspicious marker")
	}
}
