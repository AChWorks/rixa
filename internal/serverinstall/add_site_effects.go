// SPDX-License-Identifier: MPL-2.0
package serverinstall

import (
	"bytes"
	"context"
	"crypto/subtle"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

func addSiteExecutorName(j *addSiteJournal) string {
	return fmt.Sprintf("rixa-add-site-%s-%d.service", j.Draft.OperationID, j.ExecutorGeneration)
}
func (m *Manager) addSiteConfigPath(j *addSiteJournal) string {
	return filepath.Join(m.stateRoot, j.Draft.OperationID+".add-site.original-config")
}
func (m *Manager) addSiteMarker(name string) (string, error) { return m.updateMarker(name) }
func (m *Manager) claimAddSite(id string) error {
	active, err := m.addSiteMarker("active-add-site")
	if err != nil || active != "" {
		return ErrState
	}
	pending, err := m.addSiteMarker("pending-add-site")
	if err != nil || pending != "" {
		return ErrBusy
	}
	return writeNew(filepath.Join(m.stateRoot, "pending-add-site"), []byte(id+"\n"), 0600)
}
func (m *Manager) clearAddSiteClaim(id string) error {
	pending, err := m.addSiteMarker("pending-add-site")
	if err != nil || (pending != id && pending != "") {
		return ErrState
	}
	if pending == "" {
		return nil
	}
	if err = os.Remove(filepath.Join(m.stateRoot, "pending-add-site")); err != nil {
		return err
	}
	dir, err := os.Open(m.stateRoot)
	if err != nil {
		return err
	}
	defer dir.Close()
	return dir.Sync()
}

// addSitePredecessors authenticates the unchanged original receipt and the
// exact completed update (if any) at the topology-change boundary. It does not
// assume that pre-activation environment/config bytes survive after activation.
func (m *Manager) addSitePredecessors(j *addSiteJournal) (*journal, *journal, error) {
	if j == nil {
		return nil, nil, ErrState
	}
	parent, raw, err := m.readInstallationJournal(j.Draft.InstallationOperation)
	if err != nil || parent.State != "installed" || parent.Attempts["remove"] != "" ||
		parent.Frozen == "" || digest(raw) != j.ParentSHA256 {
		return nil, nil, ErrState
	}
	owner, err := readPrivate(filepath.Join(m.stateRoot, "installation"), m.ownerUID, 128)
	if err != nil || string(owner) != parent.Draft.OperationID+"\n" {
		return nil, nil, ErrState
	}
	current := parent
	active, err := m.updateMarker("active-update")
	if err != nil || active != j.ActiveUpdate {
		return nil, nil, ErrState
	}
	if active != "" {
		u, raw, re := m.readCompletedAddSiteUpdate(active)
		if re != nil || digest(raw) != j.ActiveUpdateSHA256 || u.ParentSHA256 != j.ParentSHA256 ||
			u.Draft.InstallationOperation != parent.Draft.OperationID ||
			!m.updateManagerIntact(u) {
			return nil, nil, ErrState
		}
		_, current, re = completedAddSiteUpdateLineage(parent, j.ParentSHA256, active, m.readCompletedAddSiteUpdate, m.updateManagerIntact)
		if re != nil {
			return nil, nil, ErrState
		}
	}
	return parent, current, nil
}
func addSiteView(current *journal, j *addSiteJournal) *journal {
	view := *current
	view.Draft = j.Draft.withInstallation(current.Draft)
	view.SiteBPrincipal = j.SiteBPrincipal
	view.DatabaseTargets = make(map[string]string, len(j.DatabaseTargets))
	for k, v := range j.DatabaseTargets {
		view.DatabaseTargets[k] = v
	}
	return &view
}
func (m *Manager) addSiteOriginal(j *addSiteJournal) ([]byte, error) {
	original, err := readPrivate(m.addSiteConfigPath(j), m.ownerUID, 1<<20)
	if err != nil || digest(original) != j.ConfigSHA256 {
		return nil, ErrState
	}
	return original, nil
}
func (m *Manager) addSiteConfigState(j *addSiteJournal, parent *journal) (config, env, candidate []byte, err error) {
	original, err := m.addSiteOriginal(j)
	if err != nil {
		return nil, nil, nil, err
	}
	candidate, err = addSiteConfigCandidate(original, j, parent)
	if err != nil {
		return nil, nil, nil, err
	}
	config, err = readPrivate(runtimeConfigPath, int(parent.UID), 1<<20)
	if err != nil {
		return nil, nil, nil, ErrState
	}
	env, err = readPrivate("/etc/rixa/service.env", 0, 16<<10)
	if err != nil {
		return nil, nil, nil, ErrState
	}
	previousEnv := environmentFile(parent.Draft)
	nextEnv := environmentFile(j.Draft.withInstallation(parent.Draft))
	if digest(previousEnv) != j.EnvironmentSHA256 ||
		(!bytes.Equal(config, original) && !bytes.Equal(config, candidate)) ||
		(!bytes.Equal(env, previousEnv) && !bytes.Equal(env, nextEnv)) {
		return nil, nil, nil, ErrState
	}
	return config, env, candidate, nil
}
func (m *Manager) addSiteExecutorFenced(ctx context.Context, j *addSiteJournal) bool {
	if j.ExecutorGeneration < 1 || j.ExecutorGeneration > 16 ||
		(!j.ExecutorAcknowledged && j.Attempts["executor"] != "verified") {
		return false
	}
	return m.unitQuiesced(ctx, addSiteExecutorName(j), true)
}
func (m *Manager) addSiteManagerIntact(j *addSiteJournal) bool {
	file := filepath.Join(managerRoot, j.ManagerSHA256)
	h, err := hashFile(file)
	return err == nil && h == j.ManagerSHA256 && trustedExecutable(file)
}

// ApplyAddSite is the only operator-confirmed topology-change admission. It
// consumes the exact Review token, reobserves *all* original host evidence,
// freezes an immutable source snapshot and then queues a detached executor.
func (m *Manager) ApplyAddSite(ctx context.Context, id, token string) (Result, error) {
	unlock, err := m.lock(ctx, false)
	if err != nil {
		return Result{}, err
	}
	defer unlock()
	j, err := m.loadAddSite(id)
	if err != nil {
		return Result{}, err
	}
	if j.Frozen != "" {
		return addSiteResult(j), nil
	}
	if j.State != "ready" || !hashSyntax.MatchString(token) ||
		subtle.ConstantTimeCompare([]byte(token), []byte(j.ReviewToken)) != 1 {
		r := addSiteResult(j)
		r.Code = "review-stale"
		r.Message = "Re-run Add Site Review and confirm its exact returned token."
		return r, ErrState
	}
	pending, pe := m.updateMarker("pending-update")
	active, ae := m.addSiteMarker("active-add-site")
	queued, qe := m.addSiteMarker("pending-add-site")
	if pe != nil || ae != nil || qe != nil || pending != "" || active != "" || queued != "" {
		return addSiteResult(j), ErrBusy
	}
	inspected, ie := m.inspectAddSiteLocked(ctx, j.Draft.AddSiteInspectionDraft)
	observed, oe := m.observedAddSiteEvidence(ctx, j.Draft)
	if ie != nil || oe != nil || !observed.matches(j) {
		j.State, j.Stage, j.ReviewToken = "blocked", "review", ""
		j.Code, j.Message = "review-evidence-changed", "The reviewed live host or predecessor differs. Repeat Review before effects."
		if err = m.saveAddSite(j); err != nil {
			return Result{}, err
		}
		r := addSiteResult(j)
		r.Problems = inspected.Problems
		return r, ErrState
	}
	parent, _, err := m.addSitePredecessors(j)
	if err != nil {
		return addSiteResult(j), err
	}
	config, env, err := m.updateRuntimeEvidence(ctx, parent)
	if err != nil || digest(config) != j.ConfigSHA256 || digest(env) != j.EnvironmentSHA256 {
		return addSiteResult(j), ErrState
	}
	if err = writeExpected(m.addSiteConfigPath(j), config, 0600, m.ownerUID); err != nil {
		return addSiteResult(j), err
	}
	manager, err := m.snapshotManager()
	if err != nil || filepath.Base(manager) != j.ReviewedManagerSHA256 {
		return addSiteResult(j), ErrManagerChanged
	}
	if err = m.claimAddSite(id); err != nil {
		return addSiteResult(j), err
	}
	j.ManagerSHA256 = filepath.Base(manager)
	j.ReviewToken = ""
	j.Frozen = addSiteDraftDigest(j.Draft)
	j.Stage = "executor"
	return m.queueAddSite(ctx, j)
}
func (m *Manager) queueAddSite(ctx context.Context, j *addSiteJournal) (Result, error) {
	j.ExecutorGeneration++
	j.ExecutorAcknowledged = false
	j.State, j.Code, j.Message = "applying", "queued", "The independent site-B executor is queued; inspect status for durable stages."
	j.Attempts["executor"] = "attempted"
	if err := m.saveAddSite(j); err != nil {
		return Result{}, err
	}
	_, err := m.run(ctx, "/usr/bin/systemd-run",
		"--unit="+strings.TrimSuffix(addSiteExecutorName(j), ".service"),
		"--property=Type=exec", "--property=RuntimeMaxSec=900",
		"--property=KillMode=control-group", "--collect",
		"--setenv=PATH=/usr/sbin:/usr/bin:/sbin:/bin",
		filepath.Join(managerRoot, j.ManagerSHA256), "manage", "internal-add-site-apply", j.Draft.OperationID)
	if err != nil {
		return m.addSiteFailure(j, "executor-acknowledgement-unknown", "Executor start acknowledgement is uncertain; never blindly replay the same generation.", err)
	}
	j.ExecutorAcknowledged = true
	if err = m.saveAddSite(j); err != nil {
		return Result{}, err
	}
	return addSiteResult(j), nil
}

func (m *Manager) addSiteResumable(ctx context.Context, j *addSiteJournal) bool {
	if j.Frozen == "" || j.ExecutorGeneration >= 16 ||
		(j.State != "applying" && j.State != "reconciliation-required") ||
		!m.addSiteManagerIntact(j) || !m.addSiteExecutorFenced(ctx, j) {
		return false
	}
	claim, err := m.addSiteMarker("pending-add-site")
	if err != nil || claim != j.Draft.OperationID {
		return false
	}
	if _, _, err = m.addSitePredecessors(j); err != nil {
		return false
	}
	if j.Attempts["postflight"] == "attempted" {
		return false
	}
	if j.Attempts["stop"] == "attempted" || j.Attempts["activate"] != "" && j.Attempts["start"] != "verified" {
		if !m.updateMigrationFenced(ctx) {
			return false
		}
	}
	if j.Attempts["start"] == "attempted" {
		// Only an already running and owned server can reconcile an unknown
		// start acknowledgement; never issue an unobserved restart.
		if m.updateUnitFileState(ctx) != "enabled" {
			return false
		}
	}
	return true
}
func (m *Manager) ResumeAddSite(ctx context.Context, id string) (Result, error) {
	unlock, err := m.lock(ctx, false)
	if err != nil {
		return Result{}, err
	}
	defer unlock()
	j, err := m.loadAddSite(id)
	if err != nil {
		return Result{}, err
	}
	image, err := currentManagerImage()
	if err != nil || digest(image) != j.ManagerSHA256 {
		r := addSiteResult(j)
		r.Code, r.Message = "manager-changed", "Resume with the exact retained Add Site manager executable."
		return r, ErrManagerChanged
	}
	if j.State == "installed" {
		if !m.addSiteExecutorFenced(ctx, j) {
			return addSiteResult(j), ErrState
		}
		active, ae := m.addSiteMarker("active-add-site")
		if ae != nil || active != id {
			return addSiteResult(j), ErrState
		}
		if err = m.clearAddSiteClaim(id); err != nil {
			return addSiteResult(j), err
		}
		return addSiteResult(j), nil
	}
	if !m.addSiteResumable(ctx, j) {
		return addSiteResult(j), ErrState
	}
	return m.queueAddSite(ctx, j)
}

func (m *Manager) addSiteFailure(j *addSiteJournal, code, message string, cause error) (Result, error) {
	if cause != nil {
		parent, err := m.load(j.Draft.InstallationOperation)
		if err == nil {
			copy := *parent
			copy.Draft.OperationID = j.Draft.OperationID
			copy.Draft.SiteBDSN = j.Draft.SiteBDSN
			copy.Draft.SiteBPassword = j.Draft.SiteBPassword
			m.retainDiagnostic(&copy, "add-site stage="+j.Stage+" code="+code+" cause="+cause.Error())
		}
	}
	j.State, j.Code, j.Message = "reconciliation-required", code, message
	if err := m.saveAddSite(j); err != nil {
		return Result{}, err
	}
	return addSiteResult(j), ErrState
}
