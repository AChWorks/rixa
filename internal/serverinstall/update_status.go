// SPDX-License-Identifier: MPL-2.0
package serverinstall

import (
	"context"
	"errors"
	"os"
	"strings"
)

// UpdateStatus remains observational while the bounded executor owns the
// effect lock. All journal and active receipt writes are atomic.
func (m *Manager) UpdateStatus(ctx context.Context, id string) (Result, error) {
	if e := m.authorize(); e != nil {
		return Result{}, e
	}
	if !operationSyntax.MatchString(id) {
		return Result{}, ErrInput
	}
	if e := privateDir(m.stateRoot, m.ownerUID, false); e != nil {
		return Result{}, e
	}
	u, e := m.loadUpdate(id)
	if errors.Is(e, os.ErrNotExist) {
		return Result{OperationKind: "update", OperationID: id, State: "not-installed", Code: "not-installed", Message: "This update has no saved draft.", Actions: []string{"configure", "status"}}, nil
	}
	if e != nil {
		return Result{}, e
	}
	return m.updateStatus(ctx, u)
}
func (m *Manager) updateStatus(ctx context.Context, u *updateJournal) (Result, error) {
	r := updateResult(u)
	r.ReviewToken = ""
	if u.State == "applying" || u.State == "reconciliation-required" {
		out, e := m.run(ctx, "/usr/bin/systemctl", "show", "--property=ActiveState", "--value", updateExecutorName(u))
		if e == nil && (strings.TrimSpace(string(out)) == "active" || strings.TrimSpace(string(out)) == "activating") {
			r.Message = "Update executor is active; refresh retained progress."
			return r, nil
		}
		if m.updateResumable(ctx, u) {
			r.Actions = []string{"resume", "status"}
		}
		if u.State == "applying" {
			r.State, r.Code, r.Message = "reconciliation-required", "executor-not-active", "Executor is no longer active. Inspect its journal and retained stage before further effects."
		}
		return r, nil
	}
	if u.State == "installed" {
		active, e := m.updateMarker("active-update")
		if e != nil {
			return r, e
		}
		if active != u.Draft.OperationID {
			r.Code, r.Message, r.Actions = "superseded", "A later update owns the active installation; this historical update is retained for evidence.", []string{"status"}
			return r, nil
		}
		parent, e := m.predecessor(u)
		if e != nil {
			r.Code = "installation-ownership"
			r.Actions = []string{"status"}
			return r, e
		}
		current := updateView(parent, u)
		pending, e := m.updateMarker("pending-update")
		if e != nil {
			return r, e
		}
		if pending == u.Draft.OperationID {
			r.Code, r.Message, r.Actions = "completion-cleanup-required", "Update is committed; run resume with its retained manager to reconcile the pending claim.", []string{"resume", "status"}
			return r, nil
		}
		if pending != "" {
			r.Code, r.Message, r.Actions = "update-pending", "A newer update is pending; inspect that operation before removal.", []string{"status"}
			return r, nil
		}
		if m.boundUpdateRuntime(ctx, u, parent) != nil || m.checkOwnedService(current) != nil || m.loadedUpdateService(ctx, current, true) != nil || m.updateUnitFileState(ctx) != "enabled" || m.updateHTTPPostflight(ctx, current) != nil {
			r.Code, r.Message, r.Actions = "installed-unhealthy", "Updated native installation is retained, but current runtime, service or TLS checks did not verify. Inspect its owned service and journal.", []string{"status"}
			if m.checkOwnedService(current) == nil && m.loadedUpdateService(ctx, current, false) == nil {
				r.Actions = []string{"status", "remove"}
			}
			return r, ErrPrerequisite
		}
		r.Code, r.Message = "healthy", "Authenticated active payload, retained configuration and physical targets, loaded service identity and strict TLS access controls are verified."
	}
	return r, nil
}
func (m *Manager) RemoveUpdatedInstallation(ctx context.Context, id string) (Result, error) {
	unlock, e := m.lock(ctx, false)
	if e != nil {
		return Result{}, e
	}
	defer unlock()
	u, e := m.loadUpdate(id)
	if e != nil {
		return Result{}, e
	}
	return m.removeUpdatedInstallation(ctx, u)
}
func (m *Manager) removeUpdatedInstallation(ctx context.Context, u *updateJournal) (Result, error) {
	if u.State == "removed" {
		return updateResult(u), nil
	}
	if u.State != "installed" || u.Attempts["remove"] != "" {
		return updateResult(u), ErrState
	}
	active, e := m.updateMarker("active-update")
	if e != nil || active != u.Draft.OperationID {
		return updateResult(u), ErrState
	}
	pending, e := m.updateMarker("pending-update")
	if e != nil || pending != "" {
		return updateResult(u), ErrBusy
	}
	parent, e := m.predecessor(u)
	if e != nil {
		return updateResult(u), e
	}
	current := updateView(parent, u)
	if e = m.checkOwnedService(current); e != nil {
		return updateResult(u), e
	}
	if e = m.loadedUpdateService(ctx, current, false); e != nil {
		return updateResult(u), e
	}
	u.Stage = "remove"
	u.Attempts["remove"] = "attempted"
	if e = m.saveUpdate(u); e != nil {
		return Result{}, e
	}
	if _, e = m.run(ctx, "/usr/bin/systemctl", "disable", "--now", "rixa.service"); e != nil {
		return m.updateFailure(u, "remove-outcome-unknown", "Removal stop/disable did not verify; retain files and inspect the owned service.", e)
	}
	if !m.unitQuiesced(ctx, "rixa.service", false) {
		return m.updateFailure(u, "remove-service-active", "The owned service cgroup is not verified quiescent; files were retained.", nil)
	}
	if e = m.checkOwnedService(current); e != nil {
		return m.updateFailure(u, "remove-ownership-drift", "Owned payload or unit changed; no file was removed.", e)
	}
	if e = os.Remove(unitPath); e != nil {
		return m.updateFailure(u, "remove-unit", "The owned unit could not be removed; inspect retained state.", e)
	}
	if e = os.Remove(m.binaryPath(current)); e != nil {
		return m.updateFailure(u, "remove-binary", "The exact active executable could not be removed; inspect retained state.", e)
	}
	if _, e = m.run(ctx, "/usr/bin/systemctl", "daemon-reload"); e != nil {
		return m.updateFailure(u, "remove-reload", "Service files were removed but systemd reload needs inspection.", e)
	}
	u.State, u.Code, u.Message = "removed", "removed", "Owned active service and executable removed. Databases, private configuration, Media, public files, original release, account and management evidence remain."
	u.Attempts["remove"] = "verified"
	if e = m.saveUpdate(u); e != nil {
		return Result{}, e
	}
	return updateResult(u), nil
}
func (m *Manager) installationUpdate(id string) (*updateJournal, error) {
	active, e := m.updateMarker("active-update")
	if e != nil || active == "" {
		return nil, e
	}
	u, e := m.loadUpdate(active)
	if e != nil {
		return nil, e
	}
	if u.Draft.InstallationOperation != id {
		return nil, nil
	}
	return u, nil
}
func (m *Manager) finishUpdateClaim(ctx context.Context, u *updateJournal) (Result, error) {
	image, e := currentManagerImage()
	if e != nil || digest(image) != u.ManagerSHA256 || !m.updateManagerIntact(u) {
		return updateResult(u), ErrManagerChanged
	}
	if !m.updateExecutorFenced(ctx, u) {
		return updateResult(u), ErrState
	}
	active, e := m.updateMarker("active-update")
	if e != nil || active != u.Draft.OperationID {
		return updateResult(u), ErrState
	}
	parent, e := m.predecessor(u)
	if e != nil {
		return updateResult(u), e
	}
	current := updateView(parent, u)
	if m.boundUpdateRuntime(ctx, u, parent) != nil || m.checkOwnedService(current) != nil || m.loadedUpdateService(ctx, current, true) != nil {
		return updateResult(u), ErrState
	}
	if e = m.clearUpdateClaim(u.Draft.OperationID); e != nil {
		return updateResult(u), e
	}
	return updateResult(u), nil
}
