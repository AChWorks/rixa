// SPDX-License-Identifier: MPL-2.0
package serverinstall

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func completedScopedUpdate() *updateJournal {
	u := frozenUpdate()
	u.State, u.Stage, u.Code = "installed", "commit", "installed"
	u.BinarySHA256, u.UnitSHA256 = strings.Repeat("b", 64), strings.Repeat("c", 64)
	u.CandidateComponents = map[string]string{}
	for _, path := range updateComponentPaths {
		u.CandidateComponents[path] = "v1.0.0"
	}
	for _, stage := range updateStages {
		u.Attempts[stage] = "verified"
	}
	return u
}

func TestInstallationEffectsRejectChildChangedAfterStatus(t *testing.T) {
	for _, action := range []string{"remove", "resume"} {
		for _, transition := range []string{"pending", "active", "completion-cleanup", "removed-child"} {
			t.Run(action+"/"+transition, func(t *testing.T) {
				m := testManager(t)
				ctx := context.Background()
				parent := installedParent(t, m)
				commands := 0
				m.run = func(context.Context, string, ...string) ([]byte, error) {
					commands++
					return nil, ErrPrerequisite
				}
				// This private fixture has no live service. Its observational
				// status still binds to the original ID before the child exists.
				observed, e := m.Status(ctx, parent.Draft.OperationID)
				if e != nil && !errors.Is(e, ErrPrerequisite) {
					t.Fatal(e)
				}
				if observed.OperationID != parent.Draft.OperationID || observed.OperationKind == "update" {
					t.Fatalf("unexpected presentation binding: %+v", observed)
				}
				commands = 0
				call := m.RemoveInstallation
				if action == "resume" {
					call = m.ResumeInstallation
				}

				// Deterministically commit a CLI child change in the window
				// after presentation/gating and before the effect takes its lock.
				peer := *m
				unlock, e := peer.lock(ctx, false)
				if e != nil {
					t.Fatal(e)
				}
				defer func() { unlock() }()
				u := completedScopedUpdate()
				if transition == "pending" {
					u = frozenUpdate()
				}
				if transition == "removed-child" {
					u.State, u.Stage, u.Code = "removed", "remove", "removed"
					u.Attempts["remove"] = "verified"
				}
				if e = peer.saveUpdate(u); e != nil {
					t.Fatal(e)
				}
				if transition != "pending" {
					if e = writeAtomic(filepath.Join(m.stateRoot, "active-update"), []byte(u.Draft.OperationID+"\n"), 0600); e != nil {
						t.Fatal(e)
					}
				}
				if transition == "pending" || transition == "completion-cleanup" {
					if e = peer.claimUpdate(u.Draft.OperationID); e != nil {
						t.Fatal(e)
					}
				}
				if _, e = call(ctx, parent.Draft.OperationID); !errors.Is(e, ErrBusy) {
					t.Fatalf("strict effect bypassed CLI lifecycle lock: %v", e)
				}
				unlock()
				// Do not unlock twice: the deferred call is retained for fatal
				// setup paths, and becomes a no-op after this release.
				unlock = func() {}

				paths := []string{m.statePath(parent.Draft.OperationID), m.updatePath(u.Draft.OperationID)}
				before := make([][]byte, len(paths))
				for i, path := range paths {
					before[i], e = os.ReadFile(path)
					if e != nil {
						t.Fatal(e)
					}
				}
				r, e := call(ctx, parent.Draft.OperationID)
				if !errors.Is(e, ErrState) && !errors.Is(e, ErrBusy) {
					t.Fatalf("child transition accepted by installation-only effect: %+v %v", r, e)
				}
				if r.OperationKind == "update" || r.OperationID == u.Draft.OperationID {
					t.Fatalf("strict effect followed child: %+v", r)
				}
				if commands != 0 {
					t.Fatalf("strict effect reached %d native commands", commands)
				}
				for i, path := range paths {
					after, re := os.ReadFile(path)
					if re != nil || !bytes.Equal(before[i], after) {
						t.Fatalf("strict effect changed retained journal %s: %v", filepath.Base(path), re)
					}
				}
			})
		}
	}
}

func TestInstallationEffectsKeepOriginalScopeAndAuthority(t *testing.T) {
	for _, action := range []string{"remove", "resume"} {
		t.Run(action, func(t *testing.T) {
			m := testManager(t)
			parent := installedParent(t, m)
			parent.State, parent.Stage, parent.Code = "removed", "remove", "removed"
			parent.Attempts["remove"] = "verified"
			if e := m.save(parent); e != nil {
				t.Fatal(e)
			}
			call := m.RemoveInstallation
			if action == "resume" {
				call = m.ResumeInstallation
			}
			r, e := call(context.Background(), parent.Draft.OperationID)
			if (action == "remove" && e != nil) || (action == "resume" && !errors.Is(e, ErrState)) || r.OperationID != parent.Draft.OperationID || r.OperationKind == "update" {
				t.Fatalf("original operation changed semantics: %+v %v", r, e)
			}
			ctx, cancel := context.WithCancel(context.Background())
			cancel()
			if _, e = call(ctx, parent.Draft.OperationID); !errors.Is(e, context.Canceled) {
				t.Fatalf("canceled caller accepted: %v", e)
			}
			m.ownerUID++
			if _, e = call(context.Background(), parent.Draft.OperationID); !errors.Is(e, ErrAuthority) {
				t.Fatalf("operator authority bypassed: %v", e)
			}
		})
	}
}

func TestOrdinaryRemoveStillFollowsActiveUpdateReceipt(t *testing.T) {
	m := testManager(t)
	parent := installedParent(t, m)
	u := completedScopedUpdate()
	// An idempotently removed child proves routing without any service/filesystem
	// effects. The live child removal implementation is shared and unchanged.
	u.State, u.Stage, u.Code = "removed", "remove", "removed"
	u.Attempts["remove"] = "verified"
	if e := m.saveUpdate(u); e != nil {
		t.Fatal(e)
	}
	if e := writeAtomic(filepath.Join(m.stateRoot, "active-update"), []byte(u.Draft.OperationID+"\n"), 0600); e != nil {
		t.Fatal(e)
	}
	r, e := m.Remove(context.Background(), parent.Draft.OperationID)
	if e != nil || r.OperationKind != "update" || r.OperationID != u.Draft.OperationID || r.InstallationOperation != parent.Draft.OperationID {
		t.Fatalf("ordinary removal lost CLI convenience routing: %+v %v", r, e)
	}
}
