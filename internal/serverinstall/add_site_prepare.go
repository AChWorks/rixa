// SPDX-License-Identifier: MPL-2.0
package serverinstall

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"strings"
)

// SaveAddSiteDraft records a distinct private Add Site operation; it cannot
// adopt, replace or modify the original installed operation or an Update.
// Its sole effect is writing a 0600, root-owned manager journal.
func (m *Manager) SaveAddSiteDraft(ctx context.Context, d AddSiteDraft) (Result, error) {
	invalid := Result{OperationKind: "add-site", OperationID: d.OperationID,
		InstallationOperation: d.InstallationOperation, State: "draft", Code: "invalid-input"}
	if err := m.authorize(); err != nil {
		return invalid, err
	}
	if !validAddSiteDraft(d) || d.SiteBPassword == "" {
		invalid.Problems = []Problem{{Field: "operation_id", Code: "invalid-draft", Message: "Provide an independent operation ID and all five bounded Add Site fields."}}
		return invalid, ErrInput
	}
	unlock, err := m.lock(ctx, false)
	if err != nil {
		return invalid, err
	}
	defer unlock()
	activeSite, activeError := m.addSiteMarker("active-add-site")
	pendingSite, pendingError := m.addSiteMarker("pending-add-site")
	if activeError != nil || pendingError != nil || activeSite != "" || pendingSite != "" {
		return invalid, ErrBusy
	}
	owner, err := readPrivate(filepath.Join(m.stateRoot, "installation"), m.ownerUID, 128)
	if err != nil || string(owner) != d.InstallationOperation+"\n" {
		return invalid, ErrState
	}
	parent, err := m.load(d.InstallationOperation)
	if err != nil || parent.State != "installed" || parent.Frozen == "" || parent.Attempts["remove"] != "" {
		return invalid, ErrState
	}
	if problems := ValidateAddSiteInspection(parent.Draft, d.AddSiteInspectionDraft); len(problems) > 0 {
		invalid.Problems = problems
		return invalid, ErrInput
	}
	pending, err := m.updateMarker("pending-update")
	if err != nil || pending != "" {
		return invalid, ErrState
	}
	// Refuse colliding independent operation IDs, including invalid existing
	// records (a missing path is never inferred from a malformed file).
	if _, err := m.load(d.OperationID); !errors.Is(err, os.ErrNotExist) {
		return invalid, ErrState
	}
	if _, err := m.loadUpdate(d.OperationID); !errors.Is(err, os.ErrNotExist) {
		return invalid, ErrState
	}
	old, err := m.loadAddSite(d.OperationID)
	if err == nil {
		if old.Frozen != "" || len(old.Attempts) != 0 ||
			(old.State != "draft" && old.State != "blocked" && old.State != "ready") ||
			old.Draft.InstallationOperation != d.InstallationOperation {
			return addSiteResult(old), ErrState
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return invalid, ErrState
	}
	j := &addSiteJournal{Schema: addSiteSchema, Draft: d, State: "draft", Code: "draft-saved",
		Message:  "Private Add Site draft saved; Review rechecks the installed service without changing topology.",
		Attempts: map[string]string{}}
	if err := m.saveAddSite(j); err != nil {
		return invalid, err
	}
	return addSiteResult(j), nil
}

func (m *Manager) LoadAddSiteDraft(ctx context.Context, id string) (AddSiteDraft, error) {
	unlock, err := m.lockDraftRead(ctx, id)
	if err != nil {
		return AddSiteDraft{}, err
	}
	defer unlock()
	j, err := m.loadAddSite(id)
	if err != nil {
		return AddSiteDraft{}, err
	}
	if j.Frozen != "" {
		return AddSiteDraft{}, ErrState
	}
	return j.Draft, nil
}

// ReviewAddSite reuses the exact same lock-held admission as 'inspect'. A
// successful Review is *still non-applying*: no executor is queued, no service
// is changed, and later Apply must recheck this complete immutable evidence.
func (m *Manager) ReviewAddSite(ctx context.Context, id string) (Result, error) {
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
		return addSiteResult(j), ErrState
	}
	activeSite, activeError := m.addSiteMarker("active-add-site")
	pendingSite, pendingError := m.addSiteMarker("pending-add-site")
	if activeError != nil || pendingError != nil || activeSite != "" || pendingSite != "" {
		return addSiteResult(j), ErrBusy
	}
	// Re-review must revoke the previous token *durably* before any host
	// observation. A crash or uncertain probe must never leave stale consent.
	j.State, j.Stage, j.Code, j.ReviewToken = "blocked", "review", "review-in-progress", ""
	j.Message = "Review is incomplete; re-run Review before any later Apply."
	if err := m.saveAddSite(j); err != nil {
		return Result{}, err
	}
	inspected, inspectErr := m.inspectAddSiteLocked(ctx, j.Draft.AddSiteInspectionDraft)
	if inspectErr != nil {
		j.State, j.Stage, j.Code, j.ReviewToken = "blocked", "review", "prerequisite-required", ""
		j.Message = "Add Site prerequisites changed or are unverified. Inspect the reported fields and review this exact draft again."
		if err = m.saveAddSite(j); err != nil {
			return Result{}, err
		}
		result := addSiteResult(j)
		result.Problems = inspected.Problems
		return result, inspectErr
	}
	// Capture a single complete, host-bound evidence snapshot after the
	// positive no-effects admission. All future Apply implementations must
	// independently recheck these bindings before their first host effect.
	observed, err := m.observedAddSiteEvidence(ctx, j.Draft)
	if err != nil {
		return addSiteResult(j), err
	}
	token := make([]byte, 32)
	if _, err = rand.Read(token); err != nil {
		return Result{}, ErrState
	}
	observed.bind(j)
	j.ReviewToken = hex.EncodeToString(token)
	j.State, j.Stage, j.Code = "ready", "review", "ready"
	j.Message = "Durable Add Site prerequisites reviewed; no topology change has occurred. Apply requires this exact review token."
	if err = m.saveAddSite(j); err != nil {
		return Result{}, err
	}
	receipt := addSiteResult(j)
	receipt.ReviewToken = j.ReviewToken
	receipt.Actions = []string{"review", "apply", "status"}
	return receipt, nil
}

// AddSiteStatus never returns its review token and is observational even
// while a separate executor holds the effect lock. It executes no mutation.
func (m *Manager) AddSiteStatus(ctx context.Context, id string) (Result, error) {
	// Atomic private journal reads remain safe while an effect executor owns
	// the exclusive manager lock; status must not block behind that lock.
	if err := m.authorize(); err != nil {
		return Result{}, err
	}
	if !operationSyntax.MatchString(id) {
		return Result{}, ErrInput
	}
	if err := privateDir(m.stateRoot, m.ownerUID, false); err != nil {
		return Result{}, err
	}
	j, err := m.loadAddSite(id)
	if err != nil {
		return Result{}, err
	}
	r := addSiteResult(j)
	r.ReviewToken = ""
	if j.State == "installed" {
		return m.installedAddSiteStatus(ctx, j)
	}
	if j.State == "applying" || j.State == "reconciliation-required" {
		out, err := m.run(ctx, "/usr/bin/systemctl", "show", "--property=ActiveState", "--value", addSiteExecutorName(j))
		if err == nil && (strings.TrimSpace(string(out)) == "active" || strings.TrimSpace(string(out)) == "activating") {
			r.Message = "The frozen Add Site executor is active; refresh retained stages."
			return r, nil
		}
		if m.addSiteResumable(ctx, j) {
			r.Actions = []string{"resume", "status"}
		}
		if j.State == "applying" {
			r.State, r.Code, r.Message = "reconciliation-required", "executor-not-active",
				"The Add Site executor is not active; inspect its exact retained stage and journal."
		}
		return r, nil
	}
	if j.State != "ready" {
		return r, nil
	}
	parent, err := readPrivate(m.statePath(j.Draft.InstallationOperation), m.ownerUID, maxStateBytes)
	if err != nil || digest(parent) != j.ParentSHA256 {
		r.State, r.Code, r.Message = "blocked", "predecessor-changed", "The original installation receipt changed; recheck the current state before Review."
		r.Actions = []string{"status"}
		return r, ErrState
	}
	active, err := m.updateMarker("active-update")
	if err != nil || active != j.ActiveUpdate {
		r.State, r.Code, r.Message = "blocked", "predecessor-changed", "The active update owner changed; recheck the current state before Review."
		r.Actions = []string{"status"}
		return r, ErrState
	}
	if active != "" {
		raw, readErr := readPrivate(m.updatePath(active), m.ownerUID, maxStateBytes)
		if readErr != nil || digest(raw) != j.ActiveUpdateSHA256 {
			r.State, r.Code, r.Message = "blocked", "predecessor-changed", "A completed update receipt changed; recheck the current state before Review."
			r.Actions = []string{"status"}
			return r, ErrState
		}
	}
	pending, err := m.updateMarker("pending-update")
	if err != nil || pending != "" {
		r.State, r.Code, r.Message = "blocked", "update-pending", "A managed update is pending; Add Site Review must be repeated when it completes."
		r.Actions = []string{"status"}
		return r, ErrState
	}
	// A stored predecessor marker is not sufficient evidence of continued
	// readiness: TLS, systemd, current config/environment, the actual binary
	// and physical database bindings can drift without altering any journal.
	if _, inspectErr := m.inspectAddSiteLocked(ctx, j.Draft.AddSiteInspectionDraft); inspectErr != nil {
		r.State, r.Code, r.Message = "blocked", "prerequisite-changed", "The current installed host, TLS or database isolation could not be verified; repeat Review."
		r.Actions = []string{"review", "status"}
		return r, ErrPrerequisite
	}
	observed, observeErr := m.observedAddSiteEvidence(ctx, j.Draft)
	if observeErr != nil || !observed.matches(j) {
		r.State, r.Code, r.Message = "blocked", "review-evidence-changed", "The current host or reviewed evidence differs from the saved Add Site receipt; repeat Review."
		r.Actions = []string{"review", "status"}
		return r, ErrState
	}
	r.Message = strings.TrimSpace(r.Message) + " Read-only host evidence remains current; Apply requires the token from the latest Review."
	return r, nil
}
