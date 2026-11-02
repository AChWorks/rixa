// SPDX-License-Identifier: MPL-2.0
package serverinstall

import (
	"context"
	"path/filepath"
	"reflect"
)

// completedAddSiteUpdate permits an observational Add Site preflight on an
// already completed compatible native update. It does not authorize any site
// mutation. A pending update, an interrupted executor, an untrusted ancestor
// or a changed current runtime is not equivalent to a completed update.
func (m *Manager) completedAddSiteUpdate(ctx context.Context, parent *journal, activeID string) (*journal, error) {
	if !operationSyntax.MatchString(activeID) || parent.State != "installed" || parent.Frozen == "" {
		return nil, ErrState
	}
	owner, err := readPrivate(filepath.Join(m.stateRoot, "installation"), m.ownerUID, 128)
	if err != nil || string(owner) != parent.Draft.OperationID+"\n" {
		return nil, ErrState
	}
	rawParent, err := readPrivate(m.statePath(parent.Draft.OperationID), m.ownerUID, maxStateBytes)
	if err != nil {
		return nil, ErrState
	}
	activeJournal, view, err := completedAddSiteUpdateLineage(parent, digest(rawParent), activeID,
		m.readCompletedAddSiteUpdate, m.updateManagerIntact)
	if err != nil {
		return nil, ErrState
	}
	// The current active journal binds the *actual current* runtime config,
	// environment, frozen private snapshot and all unchanged original physical
	// PostgreSQL targets. Historical update snapshots are checked by their
	// immutable chained receipts, never substituted for current config bytes.
	if err := m.boundUpdateRuntime(ctx, activeJournal, parent); err != nil {
		return nil, ErrState
	}
	return view, nil
}

// Read and decode one private journal from a single stable file descriptor.
// The exact read bytes are the same bytes the hash chain authenticates. Do
// not use a second independent load that could observe changed source bytes.
func (m *Manager) readCompletedAddSiteUpdate(id string) (*updateJournal, []byte, error) {
	if !operationSyntax.MatchString(id) {
		return nil, nil, ErrState
	}
	b, err := readPrivate(m.updatePath(id), m.ownerUID, maxStateBytes)
	if err != nil {
		return nil, nil, ErrState
	}
	var u updateJournal
	if decode(b, &u) != nil || u.Draft.OperationID != id || !validUpdate(&u) {
		return nil, nil, ErrState
	}
	return &u, b, nil
}

type addSiteUpdateReader func(string) (*updateJournal, []byte, error)

// completedAddSiteUpdateLineage is the bounded, pure chain verifier. The
// supplied reader is private validated storage in production; tests can supply
// controlled immutable receipts. There is no fallback to the source manager's
// *current* unit template, no use of an unverified partial update, and no
// separate ownership rule for the previously updated installation.
func completedAddSiteUpdateLineage(parent *journal, parentHash, activeID string,
	read addSiteUpdateReader, intact func(*updateJournal) bool,
) (*updateJournal, *journal, error) {
	if parent == nil || !hashSyntax.MatchString(parentHash) ||
		!operationSyntax.MatchString(activeID) || parent.State != "installed" ||
		parent.Attempts["remove"] != "" || parent.Frozen == "" ||
		read == nil || intact == nil {
		return nil, nil, ErrState
	}
	seen := map[string]bool{}
	id := activeID
	expectedHash := ""
	expectedRelease := ""
	var current *updateJournal
	// This is deliberately a bounded observation, not a generic recursive
	// journal walker. Abnormally deep or circular histories need reconciliation.
	for depth := 0; depth < 64; depth++ {
		if seen[id] || !operationSyntax.MatchString(id) {
			return nil, nil, ErrState
		}
		seen[id] = true
		u, raw, err := read(id)
		if err != nil || u == nil || len(raw) == 0 ||
			u.Draft.OperationID != id || !validUpdate(u) ||
			u.State != "installed" || u.Attempts["remove"] != "" ||
			u.Draft.InstallationOperation != parent.Draft.OperationID ||
			u.ParentSHA256 != parentHash ||
			!reflect.DeepEqual(u.DatabaseTargets, parent.DatabaseTargets) ||
			(expectedHash != "" && digest(raw) != expectedHash) ||
			(expectedRelease != "" && u.Draft.Release != expectedRelease) ||
			!intact(u) {
			return nil, nil, ErrState
		}
		if current == nil {
			current = u
		}
		if u.PriorUpdate == "" {
			if u.ActiveRelease != parent.Draft.Release {
				return nil, nil, ErrState
			}
			return current, updateView(parent, current), nil
		}
		if u.PriorSHA256 == "" || u.PriorUpdate == id {
			return nil, nil, ErrState
		}
		expectedRelease = u.ActiveRelease
		expectedHash = u.PriorSHA256
		id = u.PriorUpdate
	}
	return nil, nil, ErrState
}
