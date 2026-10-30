// SPDX-License-Identifier: MPL-2.0
package serverinstall

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"

	"github.com/AChWorks/rixa/internal/product"
	"github.com/AChWorks/rixa/internal/releaseartifact"
)

var _ UpdateFacade = (*Manager)(nil)

func (m *Manager) SaveUpdateDraft(ctx context.Context, d UpdateDraft) (Result, error) {
	if e := m.authorize(); e != nil {
		return Result{}, e
	}
	if p := ValidateUpdateDraft(d); len(p) > 0 {
		return Result{OperationKind: "update", OperationID: d.OperationID, State: "draft", Code: "invalid-input", Problems: p, Actions: []string{"configure"}}, ErrInput
	}
	unlock, e := m.lock(ctx, true)
	if e != nil {
		return Result{}, e
	}
	defer unlock()
	if _, e = m.load(d.OperationID); e == nil {
		return Result{}, ErrState
	} else if !errors.Is(e, os.ErrNotExist) {
		return Result{}, e
	}
	if old, e := m.loadUpdate(d.OperationID); e == nil {
		if old.Frozen != "" || len(old.Attempts) > 0 {
			return updateResult(old), ErrState
		}
	} else if !errors.Is(e, os.ErrNotExist) {
		return Result{}, e
	}
	u := &updateJournal{Schema: updateSchema, Draft: d, State: "draft", Code: "draft-saved", Message: "Private update draft saved. Review the current installation and authenticated candidate before applying.", Attempts: map[string]string{}}
	if e = m.saveUpdate(u); e != nil {
		return Result{}, e
	}
	return updateResult(u), nil
}
func (m *Manager) LoadUpdateDraft(ctx context.Context, id string) (UpdateDraft, error) {
	unlock, e := m.lock(ctx, false)
	if e != nil {
		return UpdateDraft{}, e
	}
	defer unlock()
	u, e := m.loadUpdate(id)
	if e != nil {
		return UpdateDraft{}, e
	}
	return u.Draft, nil
}
func (m *Manager) ReviewUpdate(ctx context.Context, id string) (Result, error) {
	unlock, e := m.lock(ctx, false)
	if e != nil {
		return Result{}, e
	}
	defer unlock()
	u, e := m.loadUpdate(id)
	if e != nil {
		return Result{}, e
	}
	if u.Frozen != "" {
		return updateResult(u), ErrState
	}
	problems := m.reviewUpdate(ctx, u)
	u.State, u.Stage, u.Code, u.Message, u.ReviewToken = "ready", "review", "ready", "Compatible native update reviewed. Apply stages the authenticated candidate before stopping the owned service.", ""
	if len(problems) > 0 {
		u.State, u.Code, u.Message = "blocked", "prerequisite-required", "Correct the reported prerequisites and review this same update again."
	} else {
		token := make([]byte, 32)
		if _, e = rand.Read(token); e != nil {
			return Result{}, e
		}
		u.ReviewToken = hex.EncodeToString(token)
	}
	if e = m.saveUpdate(u); e != nil {
		return Result{}, e
	}
	r := updateResult(u)
	r.Problems = problems
	if len(problems) > 0 {
		return r, ErrPrerequisite
	}
	return r, nil
}
func (m *Manager) reviewUpdate(ctx context.Context, u *updateJournal) []Problem {
	problems := ValidateUpdateDraft(u.Draft)
	add := func(f, c, s string) { problems = append(problems, Problem{f, c, s}) }
	if len(problems) > 0 {
		return problems
	}
	pending, e := m.updateMarker("pending-update")
	if e != nil || (pending != "" && pending != u.Draft.OperationID) {
		add("installation_operation", "update-pending", "Inspect or resume the pending update before starting another update.")
		return problems
	}
	if runtime.GOOS != "linux" || runtime.GOARCH != "amd64" {
		add("host", "unsupported", "This native profile requires Linux amd64.")
		return problems
	}
	release, e := os.ReadFile("/etc/os-release")
	if e != nil || !strings.Contains("\n"+string(release), "\nID=debian\n") || !strings.Contains("\n"+string(release), "\nVERSION_ID=\"13\"\n") {
		add("host", "unsupported", "This native update profile requires Debian 13.")
		return problems
	}
	if _, e = os.Stat("/run/systemd/system"); e != nil {
		add("service", "systemd-required", "A running systemd system manager is required.")
		return problems
	}
	parentBytes, e := readPrivate(m.statePath(u.Draft.InstallationOperation), m.ownerUID, maxStateBytes)
	if e != nil {
		add("installation_operation", "installation-required", "Select the original retained installed native operation, including its private journal and frozen manager.")
		return problems
	}
	u.ParentSHA256 = digest(parentBytes)
	active, e := m.updateMarker("active-update")
	if e != nil {
		add("installation_operation", "active-receipt", "The active update receipt needs operator inspection.")
		return problems
	}
	u.PriorUpdate, u.PriorSHA256 = active, ""
	if active != "" {
		b, re := readPrivate(m.updatePath(active), m.ownerUID, maxStateBytes)
		if re != nil {
			add("installation_operation", "active-receipt", "The active update journal is unavailable; inspect retained management state.")
			return problems
		}
		u.PriorSHA256 = digest(b)
	}
	parent, e := m.activePredecessor(u)
	if e != nil {
		add("installation_operation", "installation-ownership", "The original installation, owner record or preserved frozen artifacts cannot be positively bound.")
		return problems
	}
	u.ActiveRelease = parent.Draft.Release
	if parent.Draft.Release == u.Draft.Release && parent.Draft.SourceCommit == u.Draft.SourceCommit {
		add("release", "already-active", "The selected source and release are already active; choose a different authenticated candidate.")
	}
	if e = m.checkOwnedService(parent); e != nil {
		add("service", "service-ownership", "The active payload, account, unit or private environment differs from its owning native operation.")
		return problems
	}
	if e = m.loadedServiceOwned(ctx, parent, true); e != nil {
		add("service", "service-ownership", "The loaded unit and active service process cannot be positively bound to the owning native payload.")
		return problems
	}
	config, environment, e := m.updateRuntimeEvidence(ctx, parent)
	if e != nil {
		add("installation_operation", "unsupported-current-scope", "Current config, TLS, environment or physical PostgreSQL18 targets differ from the supported owned native profile. Preserve them and reconcile the changed listener, origins, site scope, principals, roots, TLS or database credentials before updating.")
		return problems
	}
	if m.updateUnitFileState(ctx) != "enabled" {
		add("service", "service-enablement", "This native update profile requires the owned service to be enabled before review. Reconcile operator-changed enablement before updating.")
		return problems
	}
	u.ConfigSHA256, u.EnvironmentSHA256 = digest(config), digest(environment)
	u.DatabaseTargets = parent.DatabaseTargets
	image, e := currentManagerImage()
	if e != nil {
		add("host", "manager-path", "Restart this trusted manager from a root-owned non-writable path before reviewing an update.")
		return problems
	}
	u.ReviewedManagerSHA256 = digest(image)
	helper, e := exec.LookPath("gh")
	if e == nil {
		helper, e = filepath.EvalSymlinks(helper)
	}
	if e != nil || !trustedExecutable(helper) {
		add("archive", "helper-required", "Install the trusted official gh helper under root-owned non-writable parents.")
		return problems
	}
	helperHash, e := hashFile(helper)
	if e != nil {
		add("archive", "helper-required", "The trusted gh executable cannot be bounded and frozen.")
		return problems
	}
	u.HelperPath, u.HelperSHA256 = helper, helperHash
	u.TransportEnv = transportEnvironment()
	for _, v := range u.TransportEnv {
		if len(v) > 4096 || strings.ContainsAny(v, "\r\n\x00") {
			u.TransportEnv = nil
			add("archive", "invalid-transport", "Standard verification transport settings must be bounded and contain no newline/NUL.")
			return problems
		}
	}
	verified, e := m.verify(ctx, releaseartifact.Request{ArchivePath: u.Draft.ArchivePath, BundlePath: u.Draft.BundlePath, ReleaseTag: u.Draft.Release, SourceCommit: u.Draft.SourceCommit})
	if e != nil || verified.Status != "verified" {
		add("archive", "publisher-verification", "Publisher verification failed. Inspect the exact archive, bundle, release, source and trusted gh helper.")
		return problems
	}
	u.ArchiveSHA256 = verified.SHA256
	return problems
}

// This compares supported scope, not old generated bytes. Presentation language,
// catalogs, policy, resource limits and timeouts remain operator-owned values.
func supportedUpdateConfig(actual, owned product.Config) bool {
	if actual.HostingProfile != owned.HostingProfile || actual.Listen != owned.Listen || actual.TLS != owned.TLS || actual.Control != owned.Control || len(actual.Sites) != 1 || len(owned.Sites) != 1 {
		return false
	}
	a, b := actual.Sites[0], owned.Sites[0]
	a.PublicPolicy = product.PublicPolicyConfig{}
	b.PublicPolicy = product.PublicPolicyConfig{}
	return reflect.DeepEqual(a, b)
}
func (m *Manager) updateRuntimeEvidence(ctx context.Context, parent *journal) ([]byte, []byte, error) {
	if !trustedRootParent(runtimeRoot) || privateDir(runtimeRoot, int(parent.UID), false) != nil {
		return nil, nil, ErrState
	}
	config, e := readPrivate(runtimeConfigPath, int(parent.UID), 1<<20)
	if e != nil {
		return nil, nil, e
	}
	actual, e := product.ParseConfig(config)
	if e != nil {
		return nil, nil, e
	}
	owned, e := product.ParseConfig(configBytes(parent.Draft, parent.ControlPrincipal, parent.SitePrincipal))
	if e != nil || !supportedUpdateConfig(actual, owned) {
		return nil, nil, ErrState
	}
	env, e := readPrivate("/etc/rixa/service.env", 0, 16<<10)
	if e != nil || !bytes.Equal(env, environmentFile(parent.Draft)) {
		return nil, nil, ErrState
	}
	for _, v := range []struct{ path, hash string }{{actual.TLS.CertFile, parent.CertSHA256}, {actual.TLS.KeyFile, parent.KeySHA256}} {
		b, re := readPrivate(v.path, int(parent.UID), 1<<20)
		if re != nil || digest(b) != v.hash {
			return nil, nil, ErrState
		}
	}
	identities, e := databaseIdentities(ctx, parent.Draft)
	if e != nil || !reflect.DeepEqual(identities, parent.DatabaseTargets) {
		return nil, nil, ErrState
	}
	return config, env, nil
}
func (m *Manager) boundUpdateRuntime(ctx context.Context, u *updateJournal, parent *journal) error {
	config, env, e := m.updateRuntimeEvidence(ctx, parent)
	if e != nil || digest(config) != u.ConfigSHA256 || digest(env) != u.EnvironmentSHA256 || !reflect.DeepEqual(parent.DatabaseTargets, u.DatabaseTargets) {
		return ErrState
	}
	snapshot, e := readPrivate(m.updateConfigPath(u), m.ownerUID, 1<<20)
	if e != nil || digest(snapshot) != u.ConfigSHA256 || !bytes.Equal(snapshot, config) {
		return ErrState
	}
	return nil
}
func (m *Manager) ApplyUpdate(ctx context.Context, id, token string) (Result, error) {
	unlock, e := m.lock(ctx, false)
	if e != nil {
		return Result{}, e
	}
	defer unlock()
	u, e := m.loadUpdate(id)
	if e != nil {
		return Result{}, e
	}
	if u.Frozen != "" {
		return updateResult(u), nil
	}
	if u.State != "ready" || !hashSyntax.MatchString(token) || subtle.ConstantTimeCompare([]byte(token), []byte(u.ReviewToken)) != 1 {
		r := updateResult(u)
		r.ReviewToken = ""
		r.Code = "review-stale"
		r.Message = "Review the current update again and confirm that exact result."
		return r, ErrState
	}
	before := *u
	problems := m.reviewUpdate(ctx, u)
	if len(problems) > 0 || !sameUpdateEvidence(&before, u) {
		u.State, u.ReviewToken, u.Code, u.Message = "blocked", "", "review-evidence-changed", "The reviewed installation or candidate changed. Review again before any update effect."
		if e = m.saveUpdate(u); e != nil {
			return Result{}, e
		}
		r := updateResult(u)
		r.Problems = problems
		return r, ErrState
	}
	parent, e := m.activePredecessor(u)
	if e != nil {
		return updateResult(u), ErrState
	}
	config, e := readPrivate(runtimeConfigPath, int(parent.UID), 1<<20)
	if e != nil || digest(config) != u.ConfigSHA256 {
		return updateResult(u), ErrState
	}
	if e = writeExpected(m.updateConfigPath(u), config, 0600, m.ownerUID); e != nil {
		return updateResult(u), e
	}
	manager, e := m.snapshotManager()
	if e != nil {
		return updateResult(u), e
	}
	if filepath.Base(manager) != u.ReviewedManagerSHA256 {
		return updateResult(u), ErrManagerChanged
	}
	helper := &journal{HelperPath: u.HelperPath, HelperSHA256: u.HelperSHA256}
	if e = m.snapshotHelper(helper); e != nil {
		return updateResult(u), e
	}
	if e = writeExpected(filepath.Join(m.stateRoot, id+".update.transport.env"), transportFile(u.TransportEnv), 0600, m.ownerUID); e != nil {
		return updateResult(u), e
	}
	if e = m.claimUpdate(id); e != nil {
		return updateResult(u), e
	}
	u.ManagerSHA256 = filepath.Base(manager)
	u.Frozen = updateDigest(u.Draft)
	u.Stage = "executor"
	return m.queueUpdate(ctx, u)
}

// Revalidate both static and loaded ownership at every queue/resume boundary.
// In particular, an old frozen manager may have removed its exact original
// service while no update executor held the shared lock. That observation must
// stop this operation before it stages or recreates anything.
func (m *Manager) updateOwnedRuntime(ctx context.Context, u *updateJournal, parent *journal) error {
	if e := m.boundUpdateRuntime(ctx, u, parent); e != nil {
		return e
	}
	candidate := updateView(parent, u)
	if u.Attempts["activate"] == "verified" {
		if e := m.checkOwnedService(candidate); e != nil {
			return e
		}
		return m.loadedUpdateService(ctx, candidate, false)
	}
	if u.Attempts["activate"] == "attempted" {
		if m.checkOwnedService(parent) != nil && m.checkOwnedService(candidate) != nil {
			return ErrState
		}
		if !m.unitQuiesced(ctx, "rixa.service", false) {
			return ErrState
		}
		if m.loadedUpdateService(ctx, parent, false) != nil && m.loadedUpdateService(ctx, candidate, false) != nil {
			return ErrState
		}
		return nil
	}
	if e := m.checkOwnedService(parent); e != nil {
		return e
	}
	return m.loadedUpdateService(ctx, parent, false)
}

func (m *Manager) updateConfigPath(u *updateJournal) string {
	return filepath.Join(m.stateRoot, u.Draft.OperationID+".config."+u.ConfigSHA256)
}
