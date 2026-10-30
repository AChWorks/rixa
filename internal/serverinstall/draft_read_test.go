// SPDX-License-Identifier: MPL-2.0
package serverinstall

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func draftReaders(m *Manager) map[string]func(context.Context, string) error {
	return map[string]func(context.Context, string) error{
		"install": func(ctx context.Context, id string) error { _, e := m.LoadDraft(ctx, id); return e },
		"update":  func(ctx context.Context, id string) error { _, e := m.LoadUpdateDraft(ctx, id); return e },
	}
}

func TestDraftReadMissingStateAllowsFirstSave(t *testing.T) {
	m := testManager(t)
	m.stateRoot = filepath.Join(m.stateRoot, "not-created")
	d := validDraft()
	for name, read := range draftReaders(m) {
		if e := read(t.Context(), d.OperationID); !errors.Is(e, os.ErrNotExist) {
			t.Fatalf("%s missing state must be a missing draft: %v", name, e)
		}
		if _, e := os.Lstat(m.stateRoot); !errors.Is(e, os.ErrNotExist) {
			t.Fatalf("%s draft read created native state: %v", name, e)
		}
	}
	result, e := m.SaveDraft(t.Context(), d)
	if e != nil || result.State != "draft" || result.Code != "draft-saved" {
		t.Fatalf("ordinary first save: %+v %v", result, e)
	}
	loaded, e := m.LoadDraft(t.Context(), d.OperationID)
	if e != nil || !reflect.DeepEqual(loaded, d) {
		t.Fatalf("first saved private draft not retained: %v", e)
	}
	assertOwnedMode(t, m.stateRoot, 0700)
	assertOwnedMode(t, m.statePath(d.OperationID), 0600)
	assertOwnedMode(t, filepath.Join(m.stateRoot, ".lock"), 0600)
}

func TestDraftReadMissingStateRetainsRequestGates(t *testing.T) {
	for _, kind := range []string{"install", "update"} {
		t.Run(kind, func(t *testing.T) {
			m := testManager(t)
			m.stateRoot = filepath.Join(m.stateRoot, "not-created")
			read := draftReaders(m)[kind]
			if e := read(t.Context(), "../invalid"); !errors.Is(e, ErrInput) {
				t.Fatalf("invalid target: %v", e)
			}
			ctx, cancel := context.WithCancel(t.Context())
			cancel()
			if e := read(ctx, "first-install"); !errors.Is(e, context.Canceled) {
				t.Fatalf("canceled read: %v", e)
			}
			m.ownerUID++
			if e := read(t.Context(), "first-install"); !errors.Is(e, ErrAuthority) {
				t.Fatalf("unauthorized read: %v", e)
			}
			if _, e := os.Lstat(m.stateRoot); !errors.Is(e, os.ErrNotExist) {
				t.Fatalf("request gates created state: %v", e)
			}
		})
	}
}

func TestDraftReadRejectsUnsafeStateInsteadOfAbsence(t *testing.T) {
	for _, kind := range []string{"install", "update"} {
		for _, state := range []string{"mode", "file", "symlink", "dangling-link", "linked-parent", "missing-parent"} {
			t.Run(kind+"/"+state, func(t *testing.T) {
				m := testManager(t)
				root := m.stateRoot
				m.stateRoot = filepath.Join(root, "state")
				switch state {
				case "mode":
					if e := os.Mkdir(m.stateRoot, 0755); e != nil {
						t.Fatal(e)
					}
					if e := os.Chmod(m.stateRoot, 0755); e != nil {
						t.Fatal(e)
					}
				case "file":
					if e := os.WriteFile(m.stateRoot, []byte("preserve"), 0600); e != nil {
						t.Fatal(e)
					}
				case "symlink":
					if e := os.Symlink(root, m.stateRoot); e != nil {
						t.Fatal(e)
					}
				case "dangling-link":
					if e := os.Symlink(filepath.Join(root, "absent"), m.stateRoot); e != nil {
						t.Fatal(e)
					}
				case "missing-parent":
					m.stateRoot = filepath.Join(root, "absent-parent", "state")
				case "linked-parent":
					link := filepath.Join(root, "link")
					if e := os.Symlink(root, link); e != nil {
						t.Fatal(e)
					}
					m.stateRoot = filepath.Join(link, "absent")
				}
				if e := draftReaders(m)[kind](t.Context(), "first-install"); !errors.Is(e, ErrState) || errors.Is(e, os.ErrNotExist) {
					t.Fatalf("unsafe %s classified as missing draft: %v", state, e)
				}
				if state == "file" {
					if b, e := os.ReadFile(m.stateRoot); e != nil || string(b) != "preserve" {
						t.Fatalf("unsafe file changed: %v", e)
					}
				} else if _, e := os.Lstat(filepath.Join(m.stateRoot, ".lock")); !errors.Is(e, os.ErrNotExist) {
					t.Fatalf("unsafe state changed: %v", e)
				}
			})
		}
	}
}
