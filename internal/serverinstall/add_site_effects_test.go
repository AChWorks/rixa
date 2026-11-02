// SPDX-License-Identifier: MPL-2.0
package serverinstall

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestAddSiteQueueRecordsUnknownExecutorBeforeDispatch(t *testing.T) {
	m := testManager(t)
	j := reviewedAddSiteFixture()
	j.State, j.Stage, j.Code = "applying", "executor", "queued"
	j.Frozen = addSiteDraftDigest(j.Draft)
	j.ReviewToken = ""
	j.ManagerSHA256 = j.ReviewedManagerSHA256
	var commands int
	m.run = func(_ context.Context, executable string, args ...string) ([]byte, error) {
		commands++
		if executable != "/usr/bin/systemd-run" ||
			!strings.Contains(strings.Join(args, " "), "internal-add-site-apply "+j.Draft.OperationID) {
			t.Fatal("unbounded or incorrect Add Site systemd executor")
		}
		recorded, err := m.loadAddSite(j.Draft.OperationID)
		if err != nil || recorded.State != "applying" ||
			recorded.Stage != "executor" || recorded.ExecutorAcknowledged ||
			recorded.Attempts["executor"] != "attempted" || recorded.ExecutorGeneration != 1 {
			t.Fatalf("executor dispatched without preceding durable intent: %+v %v", recorded, err)
		}
		return nil, ErrPrerequisite // start may have happened despite missing ack
	}
	r, err := m.queueAddSite(context.Background(), j)
	if !errors.Is(err, ErrState) || commands != 1 || r.Code != "executor-acknowledgement-unknown" {
		t.Fatalf("unknown executor incorrectly acknowledged: %+v %v", r, err)
	}
	recorded, err := m.loadAddSite(j.Draft.OperationID)
	if err != nil || recorded.State != "reconciliation-required" ||
		recorded.Attempts["executor"] != "attempted" || recorded.ExecutorAcknowledged {
		t.Fatal("unknown executor start was not retained as unresolved")
	}
}

func TestApplyAddSiteRejectsStaleOrForgedTokenBeforeHostEffects(t *testing.T) {
	m := testManager(t)
	j := reviewedAddSiteFixture()
	if err := m.saveAddSite(j); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(m.addSitePath(j.Draft.OperationID))
	if err != nil {
		t.Fatal(err)
	}
	for _, token := range []string{"", strings.Repeat("f", 64), strings.Repeat("a", 63)} {
		r, err := m.ApplyAddSite(context.Background(), j.Draft.OperationID, token)
		if !errors.Is(err, ErrState) || r.ReviewToken != "" || r.Code != "review-stale" {
			t.Fatalf("invalid token could dispatch effects: %+v %v", r, err)
		}
	}
	after, err := os.ReadFile(m.addSitePath(j.Draft.OperationID))
	if err != nil || !reflect.DeepEqual(before, after) {
		t.Fatal("failed confirmation mutated source receipt")
	}
	for _, name := range []string{"pending-add-site", "active-add-site"} {
		if _, err := os.Lstat(filepath.Join(m.stateRoot, name)); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("invalid confirmation claimed %s: %v", name, err)
		}
	}
}

func TestAddSiteClaimStrictIsolationAndCompletion(t *testing.T) {
	m := testManager(t)
	id := "topology-operation"
	if err := m.claimAddSite(id); err != nil {
		t.Fatal(err)
	}
	if err := m.claimAddSite(id); !errors.Is(err, ErrBusy) {
		t.Fatal("double start adopted a pending owner")
	}
	if err := m.clearAddSiteClaim("wrong"); !errors.Is(err, ErrState) {
		t.Fatal("foreign claim released")
	}
	if err := m.clearAddSiteClaim(id); err != nil {
		t.Fatal(err)
	}
	if err := writeNew(filepath.Join(m.stateRoot, "active-add-site"), []byte(id+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := m.claimAddSite("another"); !errors.Is(err, ErrState) {
		t.Fatal("existing topology replaced")
	}
}

func TestInstalledSiteTopologyIsDerivedWithoutMutatingParent(t *testing.T) {
	m := testManager(t)
	parent := savedAddSiteParent(t, m)
	raw, err := os.ReadFile(m.statePath(parent.Draft.OperationID))
	if err != nil {
		t.Fatal(err)
	}
	j := installedAddSiteFixture()
	j.Draft.InstallationOperation = parent.Draft.OperationID
	j.Frozen = addSiteDraftDigest(j.Draft)
	j.ParentSHA256 = digest(raw)
	for key := range j.DatabaseTargets {
		if key != "site-b" {
			j.DatabaseTargets[key] = parent.DatabaseTargets[key]
		}
	}
	if err := m.saveAddSite(j); err != nil {
		t.Fatal(err)
	}
	if err := writeNew(filepath.Join(m.stateRoot, "active-add-site"), []byte(j.Draft.OperationID+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	virtual, err := m.topologyView(parent, parent)
	if err != nil || virtual.Draft.SiteBOrigin != j.Draft.SiteBOrigin ||
		virtual.SiteBPrincipal != j.SiteBPrincipal || virtual.Draft.SiteBPassword != "" ||
		virtual.DatabaseTargets["site-b"] != j.DatabaseTargets["site-b"] {
		t.Fatalf("committed topology not derived: %+v %v", virtual, err)
	}
	if siteBRequested(parent.Draft) || parent.SiteBPrincipal != "" || len(parent.DatabaseTargets) != 2 {
		t.Fatal("immutable original installation was mutated")
	}
	j.ParentSHA256 = strings.Repeat("f", 64)
	if err := m.saveAddSite(j); err != nil {
		t.Fatal(err)
	}
	if _, err := m.topologyView(parent, parent); !errors.Is(err, ErrState) {
		t.Fatal("changed original lineage was admitted")
	}
}

func TestAddSiteStatusObservesActiveExecutorUnderEffectLock(t *testing.T) {
	m := testManager(t)
	j := applyingAddSiteFixture()
	if err := m.saveAddSite(j); err != nil {
		t.Fatal(err)
	}
	unlock, err := m.lock(context.Background(), false)
	if err != nil {
		t.Fatal(err)
	}
	defer unlock()
	m.run = func(_ context.Context, name string, args ...string) ([]byte, error) {
		if name != "/usr/bin/systemctl" || !strings.Contains(strings.Join(args, " "), addSiteExecutorName(j)) {
			t.Fatal("unexpected status effect")
		}
		return []byte("active\n"), nil
	}
	got, err := m.AddSiteStatus(context.Background(), j.Draft.OperationID)
	if err != nil || got.State != "applying" || got.Stage != j.Stage || got.ReviewToken != "" {
		t.Fatalf("status cannot observe live execution: %+v %v", got, err)
	}
}
