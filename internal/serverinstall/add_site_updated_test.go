// SPDX-License-Identifier: MPL-2.0
package serverinstall

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/AChWorks/rixa/internal/product"
)

func completeAddSiteUpdate(parent *journal, parentHash, operation, release, priorID, priorHash, precedingRelease string) *updateJournal {
	u := frozenUpdate()
	u.Draft.OperationID = operation
	u.Draft.InstallationOperation = parent.Draft.OperationID
	u.Draft.Release = release
	u.Draft.SourceCommit = strings.Repeat("b", 40)
	u.Frozen = updateDigest(u.Draft)
	u.State, u.Stage, u.Code = "installed", "commit", "updated"
	u.ActiveRelease = precedingRelease
	u.ParentSHA256 = parentHash
	u.PriorUpdate, u.PriorSHA256 = priorID, priorHash
	u.CandidateComponents = product.FoundationVersions()
	u.BinarySHA256, u.UnitSHA256 = strings.Repeat("c", 64), strings.Repeat("d", 64)
	u.DatabaseTargets = map[string]string{}
	for k, v := range parent.DatabaseTargets {
		u.DatabaseTargets[k] = v
	}
	for _, stage := range updateStages {
		u.Attempts[stage] = "verified"
	}
	u.Updated = "2026-11-02T00:00:00Z"
	return u
}

func addSiteChainFixtures(t *testing.T) (*journal, string, *updateJournal, *updateJournal, map[string][]byte) {
	t.Helper()
	m := testManager(t)
	parent := installedParent(t, m)
	rawParent, err := os.ReadFile(m.statePath(parent.Draft.OperationID))
	if err != nil {
		t.Fatal(err)
	}
	parentHash := digest(rawParent)
	first := completeAddSiteUpdate(parent, parentHash, "first-compatible-update", "v0.7.0", "", "", parent.Draft.Release)
	firstBytes, err := json.Marshal(first)
	if err != nil || !validUpdate(first) {
		t.Fatalf("invalid first completed fixture: %v", err)
	}
	second := completeAddSiteUpdate(parent, parentHash, "second-compatible-update", "v0.8.0",
		first.Draft.OperationID, digest(firstBytes), first.Draft.Release)
	secondBytes, err := json.Marshal(second)
	if err != nil || !validUpdate(second) {
		t.Fatalf("invalid second completed fixture: %v", err)
	}
	return parent, parentHash, first, second, map[string][]byte{
		first.Draft.OperationID:  firstBytes,
		second.Draft.OperationID: secondBytes,
	}
}

func testAddSiteUpdateReader(records map[string][]byte) addSiteUpdateReader {
	return func(id string) (*updateJournal, []byte, error) {
		b, ok := records[id]
		if !ok {
			return nil, nil, os.ErrNotExist
		}
		var u updateJournal
		if err := decode(b, &u); err != nil {
			return nil, nil, err
		}
		return &u, b, nil
	}
}

func TestCompletedAddSiteUpdateLineageAcceptsCompleteOneAndTwoUpdateHistories(t *testing.T) {
	parent, parentHash, first, second, records := addSiteChainFixtures(t)
	beforeParent, _ := json.Marshal(parent)
	beforeRecords := map[string][]byte{}
	for k, v := range records {
		beforeRecords[k] = bytes.Clone(v)
	}
	for _, id := range []string{first.Draft.OperationID, second.Draft.OperationID} {
		t.Run(id, func(t *testing.T) {
			var verified []string
			active, view, err := completedAddSiteUpdateLineage(parent, parentHash, id, testAddSiteUpdateReader(records), func(u *updateJournal) bool {
				verified = append(verified, u.Draft.OperationID)
				return true
			})
			if err != nil || active == nil || view == nil ||
				view.Draft.OperationID != parent.Draft.OperationID ||
				view.Draft.Release != active.Draft.Release || view.ArchiveSHA256 != active.ArchiveSHA256 ||
				view.BinarySHA256 != active.BinarySHA256 || view.UnitSHA256 != active.UnitSHA256 ||
				view.Draft.ControlDSN != parent.Draft.ControlDSN ||
				view.Draft.SiteDSN != parent.Draft.SiteDSN || siteBRequested(view.Draft) ||
				view.ControlPrincipal != parent.ControlPrincipal || view.SitePrincipal != parent.SitePrincipal ||
				!reflect.DeepEqual(view.DatabaseTargets, parent.DatabaseTargets) {
				t.Fatalf("completed compatible update not positively bound: %+v, %+v, %v", active, view, err)
			}
			if (id == first.Draft.OperationID && len(verified) != 1) ||
				(id == second.Draft.OperationID && len(verified) != 2) {
				t.Fatalf("missing bound chain ancestors: %+v", verified)
			}
		})
	}
	afterParent, _ := json.Marshal(parent)
	if !bytes.Equal(beforeParent, afterParent) || !reflect.DeepEqual(records, beforeRecords) {
		t.Fatal("read-only lineage validation mutated the retained journals or the parent")
	}
}

func TestCompletedAddSiteUpdateLineageRejectsUnknownRemovedOrAlteredUpdates(t *testing.T) {
	type change func(*journal, *updateJournal, *updateJournal, map[string][]byte)
	for _, tc := range []struct {
		name       string
		change     change
		intactFail bool
	}{
		{"wrong-original-parent-hash", func(_ *journal, _ *updateJournal, u *updateJournal, _ map[string][]byte) {
			u.ParentSHA256 = strings.Repeat("e", 64)
		}, false},
		{"different-installation", func(_ *journal, _ *updateJournal, u *updateJournal, _ map[string][]byte) {
			u.Draft.InstallationOperation = "unrelated-install"
		}, false},
		{"unverified-stage", func(_ *journal, _ *updateJournal, u *updateJournal, _ map[string][]byte) {
			u.Attempts["postflight"] = "attempted"
		}, false},
		{"active-update-interrupted", func(_ *journal, _ *updateJournal, u *updateJournal, _ map[string][]byte) {
			u.State = "reconciliation-required"
		}, false},
		{"active-update-rejected", func(_ *journal, _ *updateJournal, u *updateJournal, _ map[string][]byte) { u.State = "rejected" }, false},
		{"active-update-removed", func(_ *journal, _ *updateJournal, u *updateJournal, _ map[string][]byte) {
			u.State = "removed"
			u.Attempts["remove"] = "verified"
		}, false},
		{"forged-third-site-target", func(_ *journal, _ *updateJournal, u *updateJournal, _ map[string][]byte) {
			u.DatabaseTargets["site-b"] = "1:102"
		}, false},
		{"changed-control-target", func(_ *journal, _ *updateJournal, u *updateJournal, _ map[string][]byte) {
			u.DatabaseTargets["control"] = "9:200"
		}, false},
		{"changed-previous-release", func(_ *journal, _ *updateJournal, u *updateJournal, _ map[string][]byte) { u.ActiveRelease = "v6.5.4" }, false},
		{"missing-previous-update", func(_ *journal, _ *updateJournal, u *updateJournal, _ map[string][]byte) {
			u.PriorUpdate = "never-installed"
			u.PriorSHA256 = strings.Repeat("f", 64)
		}, false},
		{"changed-prior-update-hash", func(_ *journal, _ *updateJournal, u *updateJournal, _ map[string][]byte) {
			u.PriorSHA256 = strings.Repeat("f", 64)
		}, false},
		{"changed-first-release-only", func(_ *journal, f *updateJournal, u *updateJournal, r map[string][]byte) {
			f.ActiveRelease = "v0.0.0"
			b, _ := json.Marshal(f)
			r[f.Draft.OperationID] = b
			u.PriorSHA256 = digest(b) // The hash link remains valid; the release lineage does not.
		}, false},
		{"self-referential-prior", func(_ *journal, _ *updateJournal, u *updateJournal, _ map[string][]byte) {
			u.PriorUpdate = u.Draft.OperationID
		}, false},
		{"original-installation-removed", func(p *journal, _ *updateJournal, _ *updateJournal, _ map[string][]byte) {
			p.Attempts["remove"] = "attempted"
		}, false},
		{"manager-evidence-drift", func(_ *journal, _ *updateJournal, _ *updateJournal, _ map[string][]byte) {}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			parent, parentHash, first, active, records := addSiteChainFixtures(t)
			tc.change(parent, first, active, records)
			modified, _ := json.Marshal(active)
			records[active.Draft.OperationID] = modified
			intact := func(_ *updateJournal) bool { return !tc.intactFail }
			if _, _, err := completedAddSiteUpdateLineage(parent, parentHash, active.Draft.OperationID, testAddSiteUpdateReader(records), intact); !errors.Is(err, ErrState) {
				t.Fatalf("untrusted completed update admitted: %s %v", tc.name, err)
			}
		})
	}
}

func TestInspectAddSiteRefusesPendingOrUntrustedActiveUpdateBeforeHostEffects(t *testing.T) {
	for _, tc := range []struct {
		name, marker, content string
	}{
		{"pending-update", "pending-update", "future-update\n"},
		{"unknown-active", "active-update", "orphaned-update\n"},
		{"corrupt-active", "active-update", "bad/operation\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := testManager(t)
			parent := installedParent(t, m)
			if err := writeNew(filepath.Join(m.stateRoot, "installation"), []byte(parent.Draft.OperationID+"\n"), 0600); err != nil {
				t.Fatal(err)
			}
			if err := writeNew(filepath.Join(m.stateRoot, tc.marker), []byte(tc.content), 0600); err != nil {
				t.Fatal(err)
			}
			_, proposed := candidateInspection()
			r, err := m.InspectAddSite(context.Background(), proposed)
			if !errors.Is(err, ErrState) || r.Code != "update-ownership" ||
				r.State != "blocked" || r.ReviewToken != "" {
				t.Fatalf("incomplete/unknown active update admitted: %+v %v", r, err)
			}
		})
	}
}

func TestCompletedAddSiteUpdateLineageReadsRealPrivateJournalFiles(t *testing.T) {
	m := testManager(t)
	parent := installedParent(t, m)
	beforeParent, err := os.ReadFile(m.statePath(parent.Draft.OperationID))
	if err != nil {
		t.Fatal(err)
	}
	first := completeAddSiteUpdate(parent, digest(beforeParent), "first-disk-update", "v0.7.0", "", "", parent.Draft.Release)
	if err := m.saveUpdate(first); err != nil {
		t.Fatal(err)
	}
	firstRaw, err := os.ReadFile(m.updatePath(first.Draft.OperationID))
	if err != nil {
		t.Fatal(err)
	}
	second := completeAddSiteUpdate(parent, digest(beforeParent), "second-disk-update", "v0.8.0",
		first.Draft.OperationID, digest(firstRaw), first.Draft.Release)
	if err := m.saveUpdate(second); err != nil {
		t.Fatal(err)
	}
	active, view, err := completedAddSiteUpdateLineage(parent, digest(beforeParent), second.Draft.OperationID,
		m.readCompletedAddSiteUpdate, func(_ *updateJournal) bool { return true })
	if err != nil || active.Draft.Release != "v0.8.0" || view.Draft.Release != "v0.8.0" {
		t.Fatalf("protected complete disk history rejected: %+v %+v %v", active, view, err)
	}
	if raw, err := os.ReadFile(m.statePath(parent.Draft.OperationID)); err != nil || !bytes.Equal(raw, beforeParent) {
		t.Fatal("original installed journal was modified by inspection")
	}
	// Ownership, mode, symlinks and malformed content all fail closed even
	// when the corresponding in-memory update metadata was once trusted.
	path := m.updatePath(second.Draft.OperationID)
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name string
		set  func() error
	}{
		{"world-readable", func() error { return os.Chmod(path, 0644) }},
		{"forged-unknown-field", func() error {
			return os.WriteFile(path, append([]byte("{\"unexpected\":\"x\","), raw[1:]...), 0600)
		}},
		{"invalid-json", func() error { return os.WriteFile(path, []byte("{"), 0600) }},
		{"symlinked-journal", func() error {
			if err := os.Remove(path); err != nil {
				return err
			}
			return os.Symlink(m.updatePath(first.Draft.OperationID), path)
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// Each negative case uses a fresh file reconstruction except
			// the symlink case. These files exist only inside test TempDir.
			if err := os.Remove(path); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, raw, 0600); err != nil {
				t.Fatal(err)
			}
			if err := tc.set(); err != nil {
				t.Fatal(err)
			}
			if _, _, err := m.readCompletedAddSiteUpdate(second.Draft.OperationID); !errors.Is(err, ErrState) {
				t.Fatalf("unsafe protected update file accepted: %v", err)
			}
		})
	}
}
