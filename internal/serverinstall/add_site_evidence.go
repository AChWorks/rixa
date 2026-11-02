// SPDX-License-Identifier: MPL-2.0
package serverinstall

import (
	"context"
	"reflect"
)

// addSiteEvidence describes the exact immutable predecessor and current host
// state used for a preparation-only Review. The existing install/update state
// is never rewritten or made an implicit authorization to perform Apply.
type addSiteEvidence struct {
	ParentSHA256       string
	ActiveUpdate       string
	ActiveUpdateSHA256 string
	ConfigSHA256       string
	EnvironmentSHA256  string
	UnitSHA256         string
	ManagerSHA256      string
	DatabaseTargets    map[string]string
}

func (e addSiteEvidence) matches(j *addSiteJournal) bool {
	return j != nil &&
		j.ParentSHA256 != "" && j.ParentSHA256 == e.ParentSHA256 &&
		j.ActiveUpdate == e.ActiveUpdate && j.ActiveUpdateSHA256 == e.ActiveUpdateSHA256 &&
		j.ConfigSHA256 != "" && j.ConfigSHA256 == e.ConfigSHA256 &&
		j.EnvironmentSHA256 != "" && j.EnvironmentSHA256 == e.EnvironmentSHA256 &&
		j.UnitSHA256 != "" && j.UnitSHA256 == e.UnitSHA256 &&
		j.ReviewedManagerSHA256 != "" && j.ReviewedManagerSHA256 == e.ManagerSHA256 &&
		len(j.DatabaseTargets) == 3 && reflect.DeepEqual(j.DatabaseTargets, e.DatabaseTargets)
}

func (e addSiteEvidence) bind(j *addSiteJournal) {
	j.ParentSHA256, j.ActiveUpdate, j.ActiveUpdateSHA256 = e.ParentSHA256, e.ActiveUpdate, e.ActiveUpdateSHA256
	j.ConfigSHA256, j.EnvironmentSHA256, j.UnitSHA256 = e.ConfigSHA256, e.EnvironmentSHA256, e.UnitSHA256
	j.ReviewedManagerSHA256 = e.ManagerSHA256
	j.DatabaseTargets = make(map[string]string, len(e.DatabaseTargets))
	for k, v := range e.DatabaseTargets {
		j.DatabaseTargets[k] = v
	}
}

// observedAddSiteEvidence is called only under the native manager effect lock,
// after the full read-only InspectAddSite admission succeeded. It observes
// immutable on-disk bytes and private database identities. The current unit
// is a root-owned 0644 file; use recordedServiceUnit, not readPrivate.
// Actual live service/TLS/role isolation is separately established by the
// preceding inspect, and rechecked for Status without any effects.
func (m *Manager) observedAddSiteEvidence(ctx context.Context, d AddSiteDraft) (addSiteEvidence, error) {
	var observed addSiteEvidence
	parent, rawParent, err := m.readInstallationJournal(d.InstallationOperation)
	if err != nil || parent.State != "installed" || parent.Attempts["remove"] != "" {
		return observed, ErrState
	}
	observed.ParentSHA256 = digest(rawParent)
	active, err := m.updateMarker("active-update")
	if err != nil {
		return observed, ErrState
	}
	observed.ActiveUpdate = active
	current := parent
	if active != "" {
		u, raw, e := m.readCompletedAddSiteUpdate(active)
		if e != nil || u.State != "installed" || u.Attempts["executor"] != "verified" || u.Attempts["remove"] != "" ||
			u.ParentSHA256 != observed.ParentSHA256 || u.Draft.InstallationOperation != d.InstallationOperation {
			return observed, ErrState
		}
		observed.ActiveUpdateSHA256 = digest(raw)
		current = updateView(parent, u)
	}
	config, env, err := m.updateRuntimeEvidence(ctx, parent)
	if err != nil {
		return observed, ErrState
	}
	observed.ConfigSHA256, observed.EnvironmentSHA256 = digest(config), digest(env)
	unit, err := m.recordedServiceUnit(current)
	if err != nil {
		return observed, ErrState
	}
	observed.UnitSHA256 = digest(unit)
	targets, err := databaseIdentities(ctx, d.AddSiteInspectionDraft.withInstallation(parent.Draft))
	if err != nil || len(targets) != 3 || targets["control"] != parent.DatabaseTargets["control"] ||
		targets["site-a"] != parent.DatabaseTargets["site-a"] || targets["site-b"] == "" {
		return observed, ErrState
	}
	observed.DatabaseTargets = targets
	// currentManagerImage is bound to a root-owned trusted path and checks
	// executing bytes. A changed manager invalidates the prior Review.
	image, err := currentManagerImage()
	if err != nil {
		return observed, ErrManagerChanged
	}
	observed.ManagerSHA256 = digest(image)
	return observed, nil
}
