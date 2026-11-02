// SPDX-License-Identifier: MPL-2.0
package serverinstall

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func savedAddSiteParent(t *testing.T, m *Manager) *journal {
	t.Helper()
	parent := installedParent(t, m)
	if err := writeNew(filepath.Join(m.stateRoot, "installation"), []byte(parent.Draft.OperationID+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	return parent
}

func TestSaveAddSiteDraftDistinctPrivateOperationAndEdit(t *testing.T) {
	m := testManager(t)
	parent := savedAddSiteParent(t, m)
	d := addSiteDraftFixture()
	d.InstallationOperation = parent.Draft.OperationID
	result, err := m.SaveAddSiteDraft(context.Background(), d)
	if err != nil || result.Code != "draft-saved" || result.OperationKind != "add-site" ||
		result.OperationID != d.OperationID || result.ReviewToken != "" {
		t.Fatalf("Add Site draft save failed: %+v %v", result, err)
	}
	stored, err := m.LoadAddSiteDraft(context.Background(), d.OperationID)
	if err != nil || stored != d {
		t.Fatalf("private Add Site input changed: %+v %v", stored, err)
	}
	original, err := m.load(parent.Draft.OperationID)
	if err != nil || original.Draft.SiteBDSN != "" || original.Draft.SiteBOrigin != "" || original.SiteBPrincipal != "" {
		t.Fatalf("original site mutated: %v", err)
	}
	state, err := m.AddSiteStatus(context.Background(), d.OperationID)
	if err != nil || state.State != "draft" || state.ReviewToken != "" {
		t.Fatalf("Add Site draft status invalid: %+v %v", state, err)
	}
	d.SiteBPassword = "different-private-bootstrap-password"
	result, err = m.SaveAddSiteDraft(context.Background(), d)
	if err != nil || result.Code != "draft-saved" {
		t.Fatalf("cannot revise unfrozen private draft: %+v %v", result, err)
	}
	stored, err = m.LoadAddSiteDraft(context.Background(), d.OperationID)
	if err != nil || stored.SiteBPassword != d.SiteBPassword {
		t.Fatalf("revised private bootstrap credential not retained: %v", err)
	}
	if _, err = m.ReviewAddSite(context.Background(), d.OperationID); err == nil {
		t.Fatal("simulated host with no bound runtime was positively reviewed")
	}
	j, err := m.loadAddSite(d.OperationID)
	if err != nil || j.State != "blocked" || j.ReviewToken != "" {
		t.Fatalf("failed Review left stale authorization: %+v %v", j, err)
	}
	if _, err = m.LoadAddSiteDraft(context.Background(), d.OperationID); err != nil {
		t.Fatalf("blocked editable draft inaccessible: %v", err)
	}
}

func TestSaveAddSiteDraftRejectsCollisionAndCorruptStateWithoutEffects(t *testing.T) {
	m := testManager(t)
	parent := savedAddSiteParent(t, m)
	d := addSiteDraftFixture()
	d.InstallationOperation = parent.Draft.OperationID
	tests := []struct {
		name   string
		change func() error
	}{
		{"parent-operation", func() error { d.OperationID = parent.Draft.OperationID; return nil }},
		{"unknown-owner", func() error { d.InstallationOperation = "unrelated-install"; return nil }},
		{"invalid-site-host", func() error { d.SiteBOrigin = parent.Draft.SiteOrigin; return nil }},
		{"weak-password", func() error { d.SiteBPassword = "short"; return nil }},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			d = addSiteDraftFixture()
			d.InstallationOperation = parent.Draft.OperationID
			if err := tc.change(); err != nil {
				t.Fatal(err)
			}
			if _, err := m.SaveAddSiteDraft(context.Background(), d); err == nil {
				t.Fatal("unsafe Add Site draft was accepted")
			}
		})
	}
	d = addSiteDraftFixture()
	d.InstallationOperation = parent.Draft.OperationID
	if err := writeNew(filepath.Join(m.stateRoot, "pending-update"), []byte("other-update\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := m.SaveAddSiteDraft(context.Background(), d); !errors.Is(err, ErrState) {
		t.Fatalf("pending update was not fenced: %v", err)
	}
	if err := os.Remove(filepath.Join(m.stateRoot, "pending-update")); err != nil {
		t.Fatal(err)
	}
	// The same operation ID cannot be adopted across independent journals.
	d.OperationID = "existing-native-install"
	duplicate := *parent
	duplicate.Draft.OperationID = d.OperationID
	duplicate.Frozen = draftDigest(duplicate.Draft)
	if err := m.save(&duplicate); err != nil {
		t.Fatal(err)
	}
	if _, err := m.SaveAddSiteDraft(context.Background(), d); !errors.Is(err, ErrState) {
		t.Fatalf("installation operation collision accepted: %v", err)
	}
	d.OperationID = "new-addon"
	if err := os.Symlink(m.statePath(duplicate.Draft.OperationID), m.addSitePath(d.OperationID)); err != nil {
		t.Fatal(err)
	}
	if _, err := m.SaveAddSiteDraft(context.Background(), d); !errors.Is(err, ErrState) {
		t.Fatalf("symlinked existing Add Site journal adopted: %v", err)
	}
}

func TestReviewAddSiteRevokesOldTokenBeforeBlockedObservation(t *testing.T) {
	m := testManager(t)
	parent := savedAddSiteParent(t, m)
	d := addSiteDraftFixture()
	d.InstallationOperation = parent.Draft.OperationID
	j := reviewedAddSiteFixture()
	j.Draft = d
	if err := m.saveAddSite(j); err != nil {
		t.Fatal(err)
	}
	if _, err := m.ReviewAddSite(context.Background(), d.OperationID); err == nil {
		t.Fatal("unavailable real runtime was unexpectedly admitted")
	}
	observed, err := m.loadAddSite(d.OperationID)
	if err != nil || observed.State != "blocked" || observed.ReviewToken != "" {
		t.Fatalf("stale review token not durably revoked: %+v %v", observed, err)
	}
	raw, err := os.ReadFile(m.addSitePath(d.OperationID))
	if err != nil {
		t.Fatal(err)
	}
	var parsed map[string]any
	if err = json.Unmarshal(raw, &parsed); err != nil {
		t.Fatal(err)
	}
	if v := parsed["review_token"]; v != nil && v != "" {
		t.Fatal("stale review token remains stored")
	}
}

func TestAddSiteDraftInputCannotPersistSecretThroughResult(t *testing.T) {
	m := testManager(t)
	parent := savedAddSiteParent(t, m)
	d := addSiteDraftFixture()
	d.InstallationOperation = parent.Draft.OperationID
	res, err := m.SaveAddSiteDraft(context.Background(), d)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(res)
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{d.SiteBPassword, d.SiteBDSN, "separate-db-secret"} {
		if strings.Contains(string(raw), secret) {
			t.Fatalf("result leaked secret")
		}
	}
}

func TestAddSiteStatusBindsOriginalAndCompletedUpdateWithoutExposingToken(t *testing.T) {
	m := testManager(t)
	parent := savedAddSiteParent(t, m)
	parentBytes, err := os.ReadFile(m.statePath(parent.Draft.OperationID))
	if err != nil {
		t.Fatal(err)
	}
	j := reviewedAddSiteFixture()
	j.Draft.InstallationOperation = parent.Draft.OperationID
	j.ParentSHA256 = digest(parentBytes)
	if err := m.saveAddSite(j); err != nil {
		t.Fatal(err)
	}
	got, err := m.AddSiteStatus(context.Background(), j.Draft.OperationID)
	if err != nil || got.State != "ready" || got.ReviewToken != "" {
		t.Fatalf("unchanged parent not observed as reviewed, secret-free: %+v %v", got, err)
	}
	u := completeAddSiteUpdate(parent, digest(parentBytes), "compatible-active-update", "v0.7.0", "", "", parent.Draft.Release)
	if err := m.saveUpdate(u); err != nil {
		t.Fatal(err)
	}
	updateBytes, err := os.ReadFile(m.updatePath(u.Draft.OperationID))
	if err != nil {
		t.Fatal(err)
	}
	j.ActiveUpdate = u.Draft.OperationID
	j.ActiveUpdateSHA256 = digest(updateBytes)
	if err := m.saveAddSite(j); err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(m.stateRoot, "active-update")
	if err := writeNew(marker, []byte(u.Draft.OperationID+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	got, err = m.AddSiteStatus(context.Background(), j.Draft.OperationID)
	if err != nil || got.State != "ready" || got.ReviewToken != "" {
		t.Fatalf("exact completed update predecessor not observed: %+v %v", got, err)
	}
	u.Message = "different persisted update receipt"
	if err := m.saveUpdate(u); err != nil {
		t.Fatal(err)
	}
	got, err = m.AddSiteStatus(context.Background(), j.Draft.OperationID)
	if !errors.Is(err, ErrState) || got.State != "blocked" || got.Code != "predecessor-changed" || got.ReviewToken != "" {
		t.Fatalf("changed prior update receipt reported ready: %+v %v", got, err)
	}
	if err := os.Remove(marker); err != nil {
		t.Fatal(err)
	}
	got, err = m.AddSiteStatus(context.Background(), j.Draft.OperationID)
	if !errors.Is(err, ErrState) || got.State != "blocked" {
		t.Fatalf("removed active update marker reported ready: %+v %v", got, err)
	}
	// Restore the exact update bytes, but a changed parent remains forbidden.
	if err := os.WriteFile(m.updatePath(u.Draft.OperationID), updateBytes, 0600); err != nil {
		t.Fatal(err)
	}
	if err := writeNew(marker, []byte(u.Draft.OperationID+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	parent.Message = "changed original install receipt"
	if err := m.save(parent); err != nil {
		t.Fatal(err)
	}
	got, err = m.AddSiteStatus(context.Background(), j.Draft.OperationID)
	if !errors.Is(err, ErrState) || got.Code != "predecessor-changed" || got.ReviewToken != "" {
		t.Fatalf("changed original receipt reported ready: %+v %v", got, err)
	}
}

func TestAddSiteStatusRejectsPendingUpdateAfterReview(t *testing.T) {
	m := testManager(t)
	parent := savedAddSiteParent(t, m)
	raw, err := os.ReadFile(m.statePath(parent.Draft.OperationID))
	if err != nil {
		t.Fatal(err)
	}
	j := reviewedAddSiteFixture()
	j.Draft.InstallationOperation = parent.Draft.OperationID
	j.ParentSHA256 = digest(raw)
	if err = m.saveAddSite(j); err != nil {
		t.Fatal(err)
	}
	if err = writeNew(filepath.Join(m.stateRoot, "pending-update"), []byte("pending-next-update\n"), 0600); err != nil {
		t.Fatal(err)
	}
	got, err := m.AddSiteStatus(context.Background(), j.Draft.OperationID)
	if !errors.Is(err, ErrState) || got.State != "blocked" || got.Code != "update-pending" || got.ReviewToken != "" {
		t.Fatalf("pending update not fenced: %+v %v", got, err)
	}
}
