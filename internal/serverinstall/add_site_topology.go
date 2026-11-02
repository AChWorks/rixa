// SPDX-License-Identifier: MPL-2.0
package serverinstall

import (
	"bytes"
	"context"
	"reflect"
)

// topologyView derives an immutable two-site CURRENT view from the independent
// committed addition. Neither the original installation journal nor a completed
// update receipt is rewritten. Every caller must still prove runtime ownership
// before an effect; a marker alone cannot authorize an update or removal.
func (m *Manager) topologyView(original, current *journal) (*journal, error) {
	if original == nil || current == nil {
		return nil, ErrState
	}
	active, err := m.addSiteMarker("active-add-site")
	if err != nil {
		return nil, err
	}
	if active == "" {
		return current, nil
	}
	j, err := m.loadAddSite(active)
	if err != nil || j.State != "installed" || j.Draft.InstallationOperation != original.Draft.OperationID ||
		!validAddSiteJournal(j) || j.Attempts["commit"] != "verified" {
		return nil, ErrState
	}
	if j.Draft.SiteBPassword != "" || siteBRequested(original.Draft) {
		return nil, ErrState
	}
	raw, err := readPrivate(m.statePath(original.Draft.OperationID), m.ownerUID, maxStateBytes)
	if err != nil || digest(raw) != j.ParentSHA256 {
		return nil, ErrState
	}
	if j.ActiveUpdate != "" {
		raw, err = readPrivate(m.updatePath(j.ActiveUpdate), m.ownerUID, maxStateBytes)
		if err != nil || digest(raw) != j.ActiveUpdateSHA256 {
			return nil, ErrState
		}
		// The saved update is an immutable completed predecessor. Every later
		// update must independently validate its exact prior update chain.
		u, ue := m.loadUpdate(j.ActiveUpdate)
		if ue != nil || u.State != "installed" || u.Attempts["remove"] != "" {
			return nil, ErrState
		}
	}
	for _, site := range []string{"control", "site-a"} {
		if original.DatabaseTargets[site] != j.DatabaseTargets[site] {
			return nil, ErrState
		}
	}
	if current.Draft.SiteBOrigin != "" {
		return nil, ErrState
	}
	return addSiteView(current, j), nil
}

func (m *Manager) installedAddSiteStatus(ctx context.Context, j *addSiteJournal) (Result, error) {
	r := addSiteResult(j)
	parent, pe := m.load(j.Draft.InstallationOperation)
	if pe != nil {
		return r, pe
	}
	if parent.State == "removed" {
		r.State, r.Code, r.Message = "removed", "removed-retained",
			"The owned native service was removed; site-B and prior site data/configuration remain retained."
		r.Actions = []string{"status"}
		return r, nil
	}
	active, err := m.addSiteMarker("active-add-site")
	if err != nil || active != j.Draft.OperationID {
		return r, ErrState
	}
	pending, err := m.addSiteMarker("pending-add-site")
	if err != nil {
		return r, err
	}
	if pending != "" {
		r.Code, r.Message, r.Actions = "completion-cleanup-required",
			"The topology was committed but its pending claim remains; Resume can reconcile the recorded completion.",
			[]string{"resume", "status"}
		return r, nil
	}
	parent, current, err := m.addSitePredecessors(j)
	if err != nil {
		// A later compatible Update may own the active release. That Update
		// independently verifies the full two-site view.
		updated, ue := m.installationUpdate(j.Draft.InstallationOperation)
		if ue != nil {
			return r, ue
		}
		if updated != nil && updated.State == "removed" {
			r.State, r.Code, r.Message = "removed", "removed-retained",
				"The updated native service was removed; both site databases and configuration remain retained."
			return r, nil
		}
		if updated != nil && updated.State == "installed" {
			status, se := m.updateStatus(ctx, updated)
			r.Code, r.Message = status.Code, "Active native Update: "+status.Message
			if se != nil {
				r.Actions = []string{"status"}
			}
			return r, se
		}
		return r, ErrState
	}
	_, env, candidate, err := m.addSiteConfigState(j, parent)
	if err != nil || !bytes.Equal(env, environmentFile(j.Draft.withInstallation(parent.Draft))) {
		return r, ErrState
	}
	config, err := readPrivate(runtimeConfigPath, int(parent.UID), 1<<20)
	if err != nil || !bytes.Equal(config, candidate) {
		return r, ErrState
	}
	view := addSiteView(current, j)
	targets, err := databaseIdentities(ctx, view.Draft)
	if err != nil || !reflect.DeepEqual(targets, j.DatabaseTargets) ||
		m.checkOwnedService(view) != nil || m.loadedUpdateService(ctx, view, true) != nil ||
		m.updateUnitFileState(ctx) != "enabled" ||
		m.verifyCurrentSiteBTLS(ctx, parent, j.Draft.SiteBOrigin) != nil ||
		m.updateHTTPPostflight(ctx, view) != nil {
		r.Code, r.Message = "installed-unhealthy", "Site-B remains committed, but one or more runtime/identity proofs did not pass."
		return r, ErrPrerequisite
	}
	r.Code, r.Message = "healthy", "Both sites, the independent databases, frozen service, and TLS endpoints verified."
	return r, nil
}

func (m *Manager) addSiteManagementPending() (bool, error) {
	pending, err := m.addSiteMarker("pending-add-site")
	return pending != "", err
}

// Strongly reject a live topology modification in conventional installation,
// update or removal paths; stage-level effects are owned only by the Add Site
// executor and its own root-managed lock.
func (m *Manager) rejectPendingAddSite() error {
	busy, err := m.addSiteManagementPending()
	if err != nil {
		return err
	}
	if busy {
		return ErrBusy
	}
	return nil
}

func (m *Manager) addSitePreRemoveView(j *journal) (*journal, error) {
	if j == nil || j.State != "installed" {
		return nil, ErrState
	}
	return m.topologyView(j, j)
}
