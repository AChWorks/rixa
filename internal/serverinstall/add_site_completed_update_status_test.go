// SPDX-License-Identifier: MPL-2.0
package serverinstall

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// Source-level two-release topology: the first Update finished before Add
// Site. A later Update binds the independently committed Add Site journal.
// All files are private test fixtures; no system service/DB is modified.
type addedSiteUpdateHistory struct {
	m                                              *Manager
	original                                       *journal
	beforeConfig, beforeEnv, afterConfig, afterEnv []byte
	first, second                                  *updateJournal
	addition                                       *addSiteJournal
	originalBytes, firstBytes, additionBytes       []byte
}

func makeAddedSiteUpdateHistory(t *testing.T) addedSiteUpdateHistory {
	t.Helper()
	m := testManager(t)
	original := savedAddSiteParent(t, m)
	originalBytes, err := os.ReadFile(m.statePath(original.Draft.OperationID))
	if err != nil {
		t.Fatal(err)
	}
	baseConfig := configBytesWithSiteB(original.Draft, original.ControlPrincipal, original.SitePrincipal, "")
	baseEnv := environmentFile(original.Draft)
	first := completeAddSiteUpdate(original, digest(originalBytes), "u1-before-add", "v0.7.0", "", "", original.Draft.Release)
	first.ConfigSHA256, first.EnvironmentSHA256 = digest(baseConfig), digest(baseEnv)
	if err := m.saveUpdate(first); err != nil {
		t.Fatal(err)
	}
	if err := writeNew(m.updateConfigPath(first), baseConfig, 0600); err != nil {
		t.Fatal(err)
	}
	firstBytes, err := os.ReadFile(m.updatePath(first.Draft.OperationID))
	if err != nil {
		t.Fatal(err)
	}
	j := installedAddSiteFixture()
	j.Draft.InstallationOperation = original.Draft.OperationID
	j.Frozen = addSiteDraftDigest(j.Draft)
	j.SiteBPrincipal = strings.Repeat("C", 26)
	j.ParentSHA256 = digest(originalBytes)
	j.ActiveUpdate, j.ActiveUpdateSHA256 = first.Draft.OperationID, digest(firstBytes)
	j.ConfigSHA256, j.EnvironmentSHA256 = first.ConfigSHA256, first.EnvironmentSHA256
	for _, site := range []string{"control", "site-a"} {
		j.DatabaseTargets[site] = original.DatabaseTargets[site]
	}
	if err := m.saveAddSite(j); err != nil {
		t.Fatal(err)
	}
	additionBytes, err := os.ReadFile(m.addSitePath(j.Draft.OperationID))
	if err != nil {
		t.Fatal(err)
	}
	// Actual Apply composes its candidate before it erases the initial
	// bootstrap password. The committed journal intentionally has no secret.
	proposed := j.Draft.AddSiteInspectionDraft
	proposed.SiteBPassword = "initial-test-bootstrap-pass"
	plan, err := composeAddSiteConfig(baseConfig, baseEnv, original, proposed, j.SiteBPrincipal)
	if err != nil {
		t.Fatal(err)
	}
	replayed, err := addSiteConfigCandidate(baseConfig, j, original)
	if err != nil || !bytes.Equal(replayed, plan.Configuration) {
		t.Fatalf("committed Add Site with erased bootstrap password cannot reconstruct its exact config: %v", err)
	}
	second := completeAddSiteUpdate(original, digest(originalBytes), "u2-after-add", "v0.8.0", first.Draft.OperationID, digest(firstBytes), first.Draft.Release)
	second.ActiveAddSiteSHA256 = digest(additionBytes)
	second.ConfigSHA256, second.EnvironmentSHA256 = digest(plan.Configuration), digest(plan.Environment)
	second.DatabaseTargets = map[string]string{}
	for k, v := range j.DatabaseTargets {
		second.DatabaseTargets[k] = v
	}
	if err := m.saveUpdate(second); err != nil {
		t.Fatal(err)
	}
	if err := writeNew(m.updateConfigPath(second), plan.Configuration, 0600); err != nil {
		t.Fatal(err)
	}
	if err := writeNew(filepath.Join(m.stateRoot, "active-add-site"), []byte(j.Draft.OperationID+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := writeNew(filepath.Join(m.stateRoot, "active-update"), []byte(first.Draft.OperationID+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	return addedSiteUpdateHistory{m, original, baseConfig, baseEnv, plan.Configuration, plan.Environment, first, second, j, originalBytes, firstBytes, additionBytes}
}

func (h addedSiteUpdateHistory) beforeU1Parent(t *testing.T) *journal {
	t.Helper()
	view, err := h.m.topologyView(h.original, h.original)
	if err != nil {
		t.Fatal(err)
	}
	return view
}
func (h addedSiteUpdateHistory) beforeU2Parent(t *testing.T) *journal {
	t.Helper()
	view, err := h.m.topologyView(h.original, updateView(h.original, h.first))
	if err != nil {
		t.Fatal(err)
	}
	return view
}

func TestCommittedAddSiteUpdateHealthUsesHistoricalAndCurrentEvidenceSeparately(t *testing.T) {
	h := makeAddedSiteUpdateHistory(t)
	if err := h.m.verifyUpdateActiveAddSite(h.first); err != nil {
		t.Fatalf("historical Update U1 not authenticated after Site-B commit: %v", err)
	}
	originalParent := h.beforeU1Parent(t)
	if err := matchUpdateRuntimeEvidence(h.first, originalParent, h.afterConfig, h.afterEnv, h.beforeConfig,
		h.addition, h.beforeConfig, h.afterConfig); err != nil {
		t.Fatalf("U1 status rejected a valid *separately committed* Add Site: %v", err)
	}
	if reflect.DeepEqual(originalParent.DatabaseTargets, h.first.DatabaseTargets) {
		t.Fatal("test did not reproduce historical two-target vs current three-target mismatch")
	}
	if digest(h.afterConfig) == h.first.ConfigSHA256 || digest(h.afterEnv) == h.first.EnvironmentSHA256 {
		t.Fatal("test did not reproduce old-hash vs expanded-config mismatch")
	}
	// The active serving release is U1 even though its predecessor remains
	// the original installation. A topology-aware status preserves both.
	viewU1 := updateView(originalParent, h.first)
	if !reflect.DeepEqual(viewU1, addSiteView(updateView(h.original, h.first), h.addition)) {
		t.Fatal("UpdateStatus and committed Add Site derive different predecessor/runtime identity")
	}
	if viewU1.Draft.SiteBOrigin != h.addition.Draft.SiteBOrigin ||
		viewU1.Draft.Release != h.first.Draft.Release ||
		viewU1.SiteBPrincipal != h.addition.SiteBPrincipal {
		t.Fatal("Status original/Update U1 would project different topology")
	}
	if err := h.m.verifyUpdateActiveAddSite(h.second); err != nil {
		t.Fatal(err)
	}
	projectedU2 := h.beforeU2Parent(t)
	if err := matchUpdateRuntimeEvidence(h.second, projectedU2, h.afterConfig, h.afterEnv, h.afterConfig, nil, nil, nil); err != nil {
		t.Fatalf("Update U2 cannot evaluate the exact current two-site runtime: %v", err)
	}
	viewU2 := updateView(projectedU2, h.second)
	if viewU2.Draft.Release != h.second.Draft.Release ||
		viewU2.Draft.SiteBOrigin != viewU1.Draft.SiteBOrigin ||
		viewU2.SiteBPrincipal != viewU1.SiteBPrincipal ||
		!reflect.DeepEqual(viewU2.DatabaseTargets, h.addition.DatabaseTargets) {
		t.Fatal("U2 lost the correctly committed three-target topology")
	}
	// The reconstructed current projections never rewrite historical journals.
	for _, file := range []struct {
		path string
		want []byte
	}{
		{h.m.statePath(h.original.Draft.OperationID), h.originalBytes},
		{h.m.updatePath(h.first.Draft.OperationID), h.firstBytes},
		{h.m.addSitePath(h.addition.Draft.OperationID), h.additionBytes},
	} {
		data, err := os.ReadFile(file.path)
		if err != nil || !bytes.Equal(data, file.want) {
			t.Fatalf("historical evidence modified by health observation: %s %v", file.path, err)
		}
	}
}

func TestCommittedAddSiteStatusRejectsEvidenceDrift(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(*addedSiteUpdateHistory, *journal, *updateJournal, *addSiteJournal, *[]byte, *[]byte, *[]byte)
	}{
		{"altered-current-config", func(_ *addedSiteUpdateHistory, _ *journal, _ *updateJournal, _ *addSiteJournal, config, _, _ *[]byte) {
			*config = []byte("operator override")
		}},
		{"altered-current-env", func(_ *addedSiteUpdateHistory, _ *journal, _ *updateJournal, _ *addSiteJournal, _, env, _ *[]byte) {
			*env = []byte("wrong credentials")
		}},
		{"altered-historical-config", func(_ *addedSiteUpdateHistory, _ *journal, _ *updateJournal, _ *addSiteJournal, _, _, snapshot *[]byte) {
			*snapshot = []byte("unrelated immutable Update snapshot")
		}},
		{"changed-control-database-identity", func(_ *addedSiteUpdateHistory, parent *journal, _ *updateJournal, _ *addSiteJournal, _, _, _ *[]byte) {
			parent.DatabaseTargets["control"] = "8:800"
		}},
		{"changed-site-b-database-identity", func(_ *addedSiteUpdateHistory, parent *journal, _ *updateJournal, _ *addSiteJournal, _, _, _ *[]byte) {
			parent.DatabaseTargets["site-b"] = "8:801"
		}},
		{"changed-committed-site-principal", func(_ *addedSiteUpdateHistory, parent *journal, _ *updateJournal, _ *addSiteJournal, _, _, _ *[]byte) {
			parent.SiteBPrincipal = strings.Repeat("Z", 26)
		}},
		{"forged-historical-hash", func(_ *addedSiteUpdateHistory, _ *journal, u *updateJournal, _ *addSiteJournal, _, _, _ *[]byte) {
			u.ConfigSHA256 = strings.Repeat("f", 64)
		}},
		{"different-update-owner", func(_ *addedSiteUpdateHistory, _ *journal, _ *updateJournal, j *addSiteJournal, _, _, _ *[]byte) {
			j.ActiveUpdate = "unrelated-update"
		}},
		{"unverified-topology-commit", func(_ *addedSiteUpdateHistory, _ *journal, _ *updateJournal, j *addSiteJournal, _, _, _ *[]byte) {
			j.Attempts["commit"] = "attempted"
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := makeAddedSiteUpdateHistory(t)
			parent := h.beforeU1Parent(t)
			u := *h.first
			addition := *h.addition
			// A changed map must not corrupt the separately saved fixture.
			parent.DatabaseTargets = map[string]string{}
			for k, v := range h.addition.DatabaseTargets {
				parent.DatabaseTargets[k] = v
			}
			addition.Attempts = map[string]string{}
			for k, v := range h.addition.Attempts {
				addition.Attempts[k] = v
			}
			config, env, snapshot := bytes.Clone(h.afterConfig), bytes.Clone(h.afterEnv), bytes.Clone(h.beforeConfig)
			tc.change(&h, parent, &u, &addition, &config, &env, &snapshot)
			if err := matchUpdateRuntimeEvidence(&u, parent, config, env, snapshot, &addition, h.beforeConfig, h.afterConfig); !errors.Is(err, ErrState) {
				t.Fatal("changed binding accepted with false HEALTHY")
			}
		})
	}
}

func TestUpdateAfterAddSitePinsExactReceiptAndRejectsChangedHistory(t *testing.T) {
	h := makeAddedSiteUpdateHistory(t)
	if err := h.m.verifyUpdateActiveAddSite(h.second); err != nil {
		t.Fatal(err)
	}
	// The independent Add Site owner is unchanged, and all preceding Update
	// bytes remain frozen. Newly completed U2 is bound to that exact receipt.
	h.addition.Message = "modified historical receipt"
	if err := h.m.saveAddSite(h.addition); err != nil {
		t.Fatal(err)
	}
	if err := h.m.verifyUpdateActiveAddSite(h.second); !errors.Is(err, ErrState) {
		t.Fatalf("U2 accepted a changed Add Site journal: %v", err)
	}
	// Exact current Add Site identity must also be positive before a
	// mutation is ever authorized for U2.
	h = makeAddedSiteUpdateHistory(t)
	h.second.ActiveAddSiteSHA256 = strings.Repeat("f", 64)
	if err := h.m.verifyUpdateActiveAddSite(h.second); !errors.Is(err, ErrState) {
		t.Fatalf("forged A1 hash admitted: %v", err)
	}
	h = makeAddedSiteUpdateHistory(t)
	h.first.Message = "changed immutable U1 receipt"
	if err := h.m.saveUpdate(h.first); err != nil {
		t.Fatal(err)
	}
	if _, err := h.m.topologyView(h.original, updateView(h.original, h.first)); !errors.Is(err, ErrState) {
		t.Fatalf("changed U1 historical chain admitted by Add Site: %v", err)
	}
}

func TestUpdateAfterAddSiteReviewMayBindButFrozenCannotDropTopologyPin(t *testing.T) {
	h := makeAddedSiteUpdateHistory(t)
	draft := *h.second
	draft.Frozen = ""
	draft.State = "draft"
	draft.Stage = ""
	draft.Attempts = map[string]string{}
	draft.ExecutorGeneration = 0
	draft.ExecutorAcknowledged = false
	draft.ManagerSHA256 = ""
	draft.BinarySHA256, draft.UnitSHA256 = "", ""
	draft.CandidateComponents = nil
	draft.ReviewToken = ""
	draft.DatabaseTargets = nil
	draft.ActiveAddSiteSHA256 = ""
	if err := h.m.verifyUpdateActiveAddSite(&draft); err != nil {
		t.Fatalf("unfrozen U2 cannot inspect committed A1: %v", err)
	}
	draft.Frozen = updateDigest(draft.Draft)
	draft.State = "applying"
	draft.Stage = "executor"
	draft.Attempts["executor"] = "attempted"
	draft.ExecutorGeneration = 1
	draft.DatabaseTargets = h.second.DatabaseTargets
	if err := h.m.verifyUpdateActiveAddSite(&draft); !errors.Is(err, ErrState) {
		t.Fatalf("unfrozen U2 escaped into effect without pinned A1 receipt: %v", err)
	}
	// The original native two-site path never creates an Add Site receipt.
	other := testManager(t)
	if err := other.verifyUpdateActiveAddSite(h.second); !errors.Is(err, ErrState) {
		t.Fatal("a foreign Add Site hash was accepted without an active marker")
	}
	uNative := *h.second
	uNative.ActiveAddSiteSHA256 = ""
	if err := other.verifyUpdateActiveAddSite(&uNative); err != nil {
		t.Fatalf("legitimate original native two-site update was rejected: %v", err)
	}
}

func TestPendingAddSiteUnifiesOriginalAndUpdateStatus(t *testing.T) {
	h := makeAddedSiteUpdateHistory(t)
	if err := writeNew(filepath.Join(h.m.stateRoot, "pending-add-site"), []byte(h.addition.Draft.OperationID+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	originalStatus, err := h.m.Status(context.Background(), h.original.Draft.OperationID)
	if err != nil || originalStatus.Code != "topology-pending" ||
		originalStatus.State != "reconciliation-required" {
		t.Fatalf("ordinary installation status falsely healthy: %+v %v", originalStatus, err)
	}
	updateStatus, err := h.m.UpdateStatus(context.Background(), h.first.Draft.OperationID)
	if err != nil || updateStatus.Code != "topology-pending" ||
		updateStatus.State != "reconciliation-required" {
		t.Fatalf("UpdateStatus disagrees with installation Status: %+v %v", updateStatus, err)
	}
	if err := h.m.verifyCommittedUpdateSiteBTLS(context.Background(), h.first, nil); !errors.Is(err, ErrState) {
		t.Fatal("unknown predecessor admitted without TLS proof")
	}
	wrongOrigin := h.beforeU1Parent(t)
	wrongOrigin.Draft.SiteBOrigin = "https://unexpected-site.rixa.test:8443"
	if err := h.m.verifyCommittedUpdateSiteBTLS(context.Background(), h.first, wrongOrigin); !errors.Is(err, ErrState) {
		t.Fatal("wrong Site-B SNI was admitted before leaf verification")
	}
}

// The erased bootstrap password is not a credential the operator must keep
// forever. This exception belongs only to a verified, committed Add Site;
// pre-commit validation still requires the original initial password.
func TestInstalledAddSiteConfigReplayDoesNotReintroduceBootstrapPassword(t *testing.T) {
	h := makeAddedSiteUpdateHistory(t)
	replay, err := addSiteConfigCandidate(h.beforeConfig, h.addition, h.original)
	if err != nil || !bytes.Equal(replay, h.afterConfig) {
		t.Fatalf("installed configuration cannot be reconstructed from durable journal: %v", err)
	}
	if bytes.Contains(replay, []byte("verified-bootstrapped-site")) ||
		bytes.Contains(h.afterEnv, []byte("verified-bootstrapped-site")) ||
		h.addition.Draft.SiteBPassword != "" {
		t.Fatal("validation-only password placeholder escaped to persisted topology")
	}
	old := *h.addition
	old.State = "applying"
	if _, err := addSiteConfigCandidate(h.beforeConfig, &old, h.original); !errors.Is(err, ErrState) {
		t.Fatal("pre-commit operation wrongly permitted missing bootstrap credentials")
	}
}
