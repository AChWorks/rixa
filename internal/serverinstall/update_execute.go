// SPDX-License-Identifier: MPL-2.0
package serverinstall

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"syscall"
	"time"
)

func updateExecutorName(u *updateJournal) string {
	return "rixa-update-" + u.Draft.OperationID + "-" + strconv.Itoa(u.ExecutorGeneration) + ".service"
}
func (m *Manager) queueUpdate(ctx context.Context, u *updateJournal) (Result, error) {
	u.ExecutorGeneration++
	u.ExecutorAcknowledged = false
	u.State, u.Code, u.Message = "applying", "queued", "A fixed update executor is queued independently of this terminal; refresh status."
	u.Attempts["executor"] = "attempted"
	if e := m.saveUpdate(u); e != nil {
		return Result{}, e
	}
	_, e := m.run(ctx, "/usr/bin/systemd-run", "--unit="+strings.TrimSuffix(updateExecutorName(u), ".service"), "--property=Type=exec", "--property=RuntimeMaxSec=900", "--property=KillMode=control-group", "--collect", "--property=EnvironmentFile="+filepath.Join(m.stateRoot, u.Draft.OperationID+".update.transport.env"), "--setenv=PATH="+filepath.Join(managerRoot, "helpers", u.HelperSHA256)+":/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin", filepath.Join(managerRoot, u.ManagerSHA256), "manage", "internal-update-apply", u.Draft.OperationID)
	if e != nil {
		return m.updateFailure(u, "executor-acknowledgement-unknown", "Inspect the exact executor "+updateExecutorName(u)+" and its cgroup; an unknown start is never replayed.", e)
	}
	u.ExecutorAcknowledged = true
	if e = m.saveUpdate(u); e != nil {
		return Result{}, e
	}
	return updateResult(u), nil
}
func (m *Manager) updateExecutorFenced(ctx context.Context, u *updateJournal) bool {
	// Reuse the single native executor fence, with this operation's fixed unit name.
	if u.ExecutorGeneration < 1 || u.ExecutorGeneration > 16 || (!u.ExecutorAcknowledged && u.Attempts["executor"] != "verified") {
		return false
	}
	return m.unitQuiesced(ctx, updateExecutorName(u), true)
}
func (m *Manager) unitQuiesced(ctx context.Context, unit string, allowMissing bool) bool {
	out, e := m.run(ctx, "/usr/bin/systemctl", "show", "--property=LoadState", "--property=ActiveState", "--property=MainPID", "--property=ControlGroup", unit)
	p := properties(out)
	if e != nil && !(allowMissing && p["LoadState"] == "not-found" && errors.Is(e, ErrPrerequisite)) {
		return false
	}
	if p["LoadState"] != "loaded" && !(allowMissing && p["LoadState"] == "not-found") {
		return false
	}
	if (p["ActiveState"] != "inactive" && p["ActiveState"] != "failed") || p["MainPID"] != "0" {
		return false
	}
	if group := p["ControlGroup"]; group != "" {
		if group != "/system.slice/"+unit {
			return false
		}
		events, e := os.ReadFile(filepath.Join("/sys/fs/cgroup", group, "cgroup.events"))
		if e != nil || !strings.Contains("\n"+string(events), "\npopulated 0\n") {
			return false
		}
	}
	return true
}
func (m *Manager) inUpdateExecutor(u *updateJournal) bool {
	b, e := os.ReadFile("/proc/self/cgroup")
	if e != nil {
		return false
	}
	for _, line := range strings.Split(string(b), "\n") {
		f := strings.SplitN(line, ":", 3)
		if len(f) == 3 && f[2] == "/system.slice/"+updateExecutorName(u) {
			return true
		}
	}
	return false
}
func (m *Manager) updateManagerIntact(u *updateJournal) bool {
	for _, v := range []struct{ path, hash string }{{filepath.Join(managerRoot, u.ManagerSHA256), u.ManagerSHA256}, {filepath.Join(managerRoot, "helpers", u.HelperSHA256, "gh"), u.HelperSHA256}} {
		actual, e := hashFile(v.path)
		if e != nil || actual != v.hash || !trustedExecutable(v.path) {
			return false
		}
	}
	b, e := readPrivate(filepath.Join(m.stateRoot, u.Draft.OperationID+".update.transport.env"), m.ownerUID, 64<<10)
	return e == nil && string(b) == string(transportFile(u.TransportEnv))
}
func (m *Manager) updateResumable(ctx context.Context, u *updateJournal) bool {
	if u.Frozen == "" || u.ExecutorGeneration >= 16 || (u.State != "applying" && u.State != "reconciliation-required") || u.Stage == "remove" || !m.updateExecutorFenced(ctx, u) || !m.updateManagerIntact(u) {
		return false
	}
	pending, e := m.updateMarker("pending-update")
	if e != nil || pending != u.Draft.OperationID {
		return false
	}
	active, e := m.updateMarker("active-update")
	if e != nil || (active != u.PriorUpdate && !(active == u.Draft.OperationID && u.Attempts["commit"] == "attempted")) {
		return false
	}
	parent, e := m.predecessor(u)
	return e == nil && m.updateOwnedRuntime(ctx, u, parent) == nil
}
func (m *Manager) ResumeUpdate(ctx context.Context, id string) (Result, error) {
	unlock, e := m.lock(ctx, false)
	if e != nil {
		return Result{}, e
	}
	defer unlock()
	u, e := m.loadUpdate(id)
	if e != nil {
		return Result{}, e
	}
	return m.resumeUpdate(ctx, u)
}
func (m *Manager) resumeUpdate(ctx context.Context, u *updateJournal) (Result, error) {
	if u.State == "installed" {
		return m.finishUpdateClaim(ctx, u)
	}
	if !m.updateResumable(ctx, u) {
		return updateResult(u), ErrState
	}
	image, e := currentManagerImage()
	if e != nil || digest(image) != u.ManagerSHA256 {
		r := updateResult(u)
		r.Code = "manager-changed"
		r.Message = "Resume with the retained trusted manager image at " + filepath.Join(managerRoot, u.ManagerSHA256) + "; the executing manager changed."
		return r, ErrManagerChanged
	}
	return m.queueUpdate(ctx, u)
}

// RunUpdateOperation is the sole fixed privileged update executor. Every stage
// uses the same native lock and a durable intent before its first effect.
func (m *Manager) RunUpdateOperation(ctx context.Context, id string) (Result, error) {
	wait, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	var unlock func()
	var e error
	for {
		unlock, e = m.lock(wait, false)
		if !errors.Is(e, ErrBusy) {
			break
		}
		select {
		case <-wait.Done():
			return Result{}, wait.Err()
		case <-time.After(50 * time.Millisecond):
		}
	}
	if e != nil {
		return Result{}, e
	}
	defer unlock()
	u, e := m.loadUpdate(id)
	if e != nil {
		return Result{}, e
	}
	if u.State != "applying" || u.Frozen == "" || !m.inUpdateExecutor(u) {
		return updateResult(u), ErrAuthority
	}
	image, e := currentManagerImage()
	if e != nil || digest(image) != u.ManagerSHA256 || !m.updateManagerIntact(u) {
		return m.updateFailure(u, "manager-drift", "The executing or retained update manager/helper no longer matches its frozen identity.", e)
	}
	pending, e := m.updateMarker("pending-update")
	if e != nil || pending != id {
		return m.updateFailure(u, "update-conflict", "The pending update owner does not match this executor.", e)
	}
	active, e := m.updateMarker("active-update")
	if e != nil || (active != u.PriorUpdate && !(active == id && u.Attempts["commit"] == "attempted")) {
		return m.updateFailure(u, "active-release-conflict", "The retained active release receipt changed; inspect ownership before continuing.", e)
	}
	parent, e := m.predecessor(u)
	if e != nil {
		return m.updateFailure(u, "installation-conflict", "The original installation or bound predecessor cannot be verified.", e)
	}
	if e = m.updateOwnedRuntime(ctx, u, parent); e != nil {
		return m.updateFailure(u, "runtime-evidence-changed", "Current configuration, TLS, environment or physical database identity changed; preserve the current state and reconcile it.", e)
	}
	u.Attempts["executor"] = "verified"
	for _, stage := range updateStages {
		if u.Attempts[stage] == "verified" {
			continue
		}
		if e = ctx.Err(); e != nil {
			return m.updateFailure(u, "interrupted", "Execution stopped; inspect the retained stage before continuation.", e)
		}
		u.Stage = stage
		// Each uncertain effect is reconciled by its stage-specific observations.
		// Candidate migration is the only schema mutation; immutable product ledgers
		// and a fenced previous executor permit its explicit forward continuation.
		attempted := u.Attempts[stage] == "attempted"
		if !attempted {
			u.Attempts[stage] = "attempted"
			u.Code, u.Message = "stage-attempted", "Executing "+stage+"."
			if e = m.saveUpdate(u); e != nil {
				return Result{}, e
			}
		}
		if e = m.executeUpdate(ctx, u, parent, stage, attempted); e != nil {
			// No database migration has been attempted. Restore the previous service
			// only from positive unchanged ownership, never from a timeout inference.
			if stage == "compatibility" && u.Attempts["migrate"] == "" {
				return m.rejectIncompatibleUpdate(ctx, u, parent, e)
			}
			return m.updateFailure(u, "stage-outcome-unknown", "The "+stage+" stage did not verify. Inspect journalctl -u "+updateExecutorName(u)+" and protected diagnostics before continuing.", e)
		}
		u.Attempts[stage] = "verified"
		u.Code = "stage-verified"
		if e = m.saveUpdate(u); e != nil {
			return Result{}, e
		}
	}
	u.State, u.Code, u.Message = "installed", "updated", "Authenticated candidate migration, readiness, owned service identity and TLS access controls verified. Original installation, configuration and data remain retained."
	if e = m.saveUpdate(u); e != nil {
		return Result{}, e
	}
	if e = m.clearUpdateClaim(id); e != nil {
		return updateResult(u), e
	}
	return updateResult(u), nil
}
func (m *Manager) executeUpdate(ctx context.Context, u *updateJournal, parent *journal, stage string, attempted bool) error {
	if e := m.boundUpdateRuntime(ctx, u, parent); e != nil {
		return e
	}
	candidate := updateView(parent, u)
	switch stage {
	case "payload":
		if u.BinarySHA256 != "" && m.checkPayload(candidate) == nil {
			return nil
		}
		return m.stageUpdatePayload(ctx, u, parent)
	case "stop":
		if e := m.checkOwnedService(parent); e != nil {
			return e
		}
		if e := m.loadedUpdateService(ctx, parent, false); e != nil {
			return e
		}
		return m.disableUpdateService(ctx)
	case "compatibility", "migrate", "check":
		if !m.updateMigrationFenced(ctx) {
			return ErrState
		}
		if e := m.checkOwnedService(parent); e != nil {
			return e
		}
		command := stage
		if stage == "compatibility" {
			command = "check"
		}
		return m.candidateCommand(ctx, u, candidate, command)
	case "activate":
		if !m.updateMigrationFenced(ctx) {
			return ErrState
		}
		if e := m.checkPayload(candidate); e != nil {
			return e
		}
		if e := m.checkOwnedService(candidate); e != nil {
			if e = m.checkOwnedService(parent); e != nil {
				return e
			}
			// Original service and candidate share the exact owned config/environment;
			// only the authenticated executable path changes.
			if e = writeAtomic(unitPath, m.serviceBytes(candidate), 0644); e != nil {
				return e
			}
		}
		if _, e := m.run(ctx, "/usr/bin/systemctl", "daemon-reload"); e != nil {
			return e
		}
		return m.loadedUpdateService(ctx, candidate, false)
	case "start":
		if e := m.checkOwnedService(candidate); e != nil {
			return e
		}
		if e := m.loadedUpdateService(ctx, candidate, false); e != nil {
			return e
		}
		if e := m.loadedUpdateService(ctx, candidate, true); e == nil {
			if m.updateUnitFileState(ctx) == "enabled" {
				return nil
			}
			if m.updateUnitFileState(ctx) != "disabled" {
				return ErrState
			}
			if _, e := m.run(ctx, "/usr/bin/systemctl", "enable", "rixa.service"); e != nil {
				return e
			}
			if m.updateUnitFileState(ctx) != "enabled" {
				return ErrState
			}
			return nil
		}
		if !m.unitQuiesced(ctx, "rixa.service", false) {
			return ErrState
		}
		if _, e := m.run(ctx, "/usr/bin/systemctl", "enable", "--now", "rixa.service"); e != nil {
			return e
		}
		deadline := time.Now().Add(30 * time.Second)
		for time.Now().Before(deadline) {
			if e := m.loadedUpdateService(ctx, candidate, true); e == nil && m.updateUnitFileState(ctx) == "enabled" {
				return nil
			}
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(250 * time.Millisecond):
			}
		}
		return ErrPrerequisite
	case "postflight":
		if m.updateUnitFileState(ctx) != "enabled" {
			return ErrState
		}
		if e := m.checkOwnedService(candidate); e != nil {
			return e
		}
		if e := m.loadedUpdateService(ctx, candidate, true); e != nil {
			return e
		}
		return m.updateHTTPPostflight(ctx, candidate)
	case "commit":
		if m.updateUnitFileState(ctx) != "enabled" {
			return ErrState
		}
		if e := m.checkOwnedService(candidate); e != nil {
			return e
		}
		if e := m.loadedUpdateService(ctx, candidate, true); e != nil {
			return e
		}
		active, e := m.updateMarker("active-update")
		if e != nil || (active != u.PriorUpdate && active != u.Draft.OperationID) {
			return ErrState
		}
		return writeAtomic(filepath.Join(m.stateRoot, "active-update"), []byte(u.Draft.OperationID+"\n"), 0600)
	default:
		return ErrInput
	}
}
func (m *Manager) stageUpdatePayload(ctx context.Context, u *updateJournal, parent *journal) error {
	dir, e := os.MkdirTemp(m.stateRoot, ".update-payload-")
	if e != nil {
		return e
	}
	defer os.RemoveAll(dir)
	d := parent.Draft
	d.Release = u.Draft.Release
	d.SourceCommit = u.Draft.SourceCommit
	d.ArchivePath = u.Draft.ArchivePath
	d.BundlePath = u.Draft.BundlePath
	verified, e := frozenInputs(ctx, d, dir)
	if e != nil || verified.Status != "verified" || verified.SHA256 != u.ArchiveSHA256 {
		return fmt.Errorf("%w: candidate publisher verification changed", ErrPrerequisite)
	}
	if e = ownedPublicDir(releaseRoot); e != nil {
		return e
	}
	out, e := newPayloadStage(releaseRoot)
	if e != nil {
		return e
	}
	defer os.RemoveAll(filepath.Dir(out))
	hash, components, e := extractUpdateAuthenticated(filepath.Join(dir, "archive.tar.gz"), out, d)
	if e != nil {
		return e
	}
	if u.BinarySHA256 != "" && u.BinarySHA256 != hash {
		return ErrState
	}
	if len(u.CandidateComponents) != 0 && !reflect.DeepEqual(u.CandidateComponents, components) {
		return ErrState
	}
	u.BinarySHA256 = hash
	u.CandidateComponents = components
	candidate := updateView(parent, u)
	u.UnitSHA256 = digest(m.serviceBytes(candidate))
	if e = m.saveUpdate(u); e != nil {
		return e
	}
	if e = activatePayloadStage(out, releaseRoot, u.ArchiveSHA256); e != nil {
		// The destination is never replaced. Only the already-recorded exact
		// authenticated binary may reconcile a previous no-replace activation.
		if m.checkPayload(updateView(parent, u)) != nil {
			return e
		}
	}
	return m.checkPayload(updateView(parent, u))
}
func (m *Manager) loadedUpdateService(ctx context.Context, j *journal, active bool) error {
	if e := m.loadedServiceOwned(ctx, j, active); e != nil {
		return e
	}
	b, e := m.run(ctx, "/usr/bin/systemctl", "show", "--property=ExecStart", "--value", "rixa.service")
	if e != nil {
		return e
	}
	s := string(b)
	path := m.binaryPath(j)
	if strings.Count(s, "path=") != 1 || strings.Count(s, "argv[]=") != 1 || !strings.Contains(s, "path="+path+" ;") || !strings.Contains(s, "argv[]="+path+" run --config "+runtimeConfigPath+" ;") {
		return ErrState
	}
	return nil
}
func (m *Manager) candidateCommand(parent context.Context, u *updateJournal, j *journal, stage string) error {
	if stage != "check" && stage != "migrate" {
		return ErrInput
	}
	if e := m.checkPayload(j); e != nil {
		return e
	}
	ctx, cancel := context.WithTimeout(parent, 120*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, m.binaryPath(j), stage, "--config", runtimeConfigPath)
	cmd.SysProcAttr = &syscall.SysProcAttr{Credential: &syscall.Credential{Uid: j.UID, Gid: j.GID, Groups: []uint32{j.GID}}}
	cmd.Env = []string{"PATH=/usr/sbin:/usr/bin:/sbin:/bin", "LANG=C.UTF-8", "LC_ALL=C.UTF-8", "HOME=/var/lib/rixa", "RIXA_CONTROL_DSN=" + j.Draft.ControlDSN, "RIXA_SITE_DSN=" + j.Draft.SiteDSN}
	cmd.Dir = "/var/lib/rixa"
	var out, diagnostic boundedOutput
	cmd.Stdout = &out
	cmd.Stderr = &diagnostic
	e := cmd.Run()
	if diagnostic.Len() > 0 || e != nil {
		copy := *j
		copy.Draft = j.Draft
		copy.Draft.OperationID = u.Draft.OperationID
		m.retainDiagnostic(&copy, "candidate "+stage+": "+diagnostic.String())
	}
	if e != nil || out.exceeded || diagnostic.exceeded {
		return fmt.Errorf("%w: candidate %s did not verify", ErrPrerequisite, stage)
	}
	if stage == "check" {
		var receipt struct {
			Status     string            `json:"status"`
			Sites      int               `json:"sites"`
			MultiSite  bool              `json:"multisite"`
			Core       string            `json:"achrix"`
			Components map[string]string `json:"achrix_components"`
		}
		if decode(out.Bytes(), &receipt) != nil || !validCandidateCheckReceipt(receipt.Status, receipt.Sites, receipt.Core, receipt.Components, u.CandidateComponents) {
			return ErrState
		}
	}
	return nil
}
func (m *Manager) rejectIncompatibleUpdate(ctx context.Context, u *updateJournal, parent *journal, cause error) (Result, error) {
	// This is only a pre-migration service restoration. It is not a database or
	// post-migration binary rollback. Every old ownership binding must still hold.
	if u.Attempts["migrate"] != "" || m.boundUpdateRuntime(ctx, u, parent) != nil || m.checkOwnedService(parent) != nil || m.loadedUpdateService(ctx, parent, false) != nil || !m.unitQuiesced(ctx, "rixa.service", false) {
		return m.updateFailure(u, "compatibility-recovery-required", "Candidate compatibility did not verify. Prior serving state cannot be safely restored automatically; inspect ownership and retained evidence.", cause)
	}
	if _, e := m.run(ctx, "/usr/bin/systemctl", "enable", "--now", "rixa.service"); e != nil {
		return m.updateFailure(u, "prior-service-outcome-unknown", "Candidate compatibility failed before migration. Prior service start needs operator inspection.", e)
	}
	verified := false
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		if m.loadedUpdateService(ctx, parent, true) == nil && m.updateUnitFileState(ctx) == "enabled" && m.updateHTTPPostflight(ctx, parent) == nil {
			verified = true
			break
		}
		select {
		case <-ctx.Done():
			return m.updateFailure(u, "prior-service-unverified", "Prior serving state requires inspection; no migration was attempted.", ctx.Err())
		case <-time.After(250 * time.Millisecond):
		}
	}
	if !verified {
		return m.updateFailure(u, "prior-service-unverified", "Candidate compatibility failed before migration. Inspect the prior service; no migration was attempted.", cause)
	}
	u.State, u.Code, u.Message = "rejected", "candidate-incompatible-prior-restored", "The candidate did not prove existing-schema compatibility. No migration was attempted; the exact prior service and TLS access controls were positively restored. Use a new reviewed compatible candidate."
	if e := m.saveUpdate(u); e != nil {
		return Result{}, e
	}
	if e := m.clearUpdateClaim(u.Draft.OperationID); e != nil {
		return updateResult(u), e
	}
	return updateResult(u), ErrPrerequisite
}

func (m *Manager) updateUnitFileState(ctx context.Context) string {
	b, e := m.run(ctx, "/usr/bin/systemctl", "show", "--property=UnitFileState", "--value", "rixa.service")
	if e != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}

// Called only after exact static/loaded service ownership is established.
// Lost disable acknowledgements remain unknown; explicit fenced continuation
// observes disabled+quiescent rather than replaying the disable blindly.
func (m *Manager) disableUpdateService(ctx context.Context) error {
	state := m.updateUnitFileState(ctx)
	if state != "enabled" && state != "disabled" {
		return ErrState
	}
	if state == "disabled" && m.unitQuiesced(ctx, "rixa.service", false) {
		return nil
	}
	if _, e := m.run(ctx, "/usr/bin/systemctl", "disable", "--now", "rixa.service"); e != nil {
		return e
	}
	if !m.updateMigrationFenced(ctx) {
		return ErrState
	}
	return nil
}
func (m *Manager) updateMigrationFenced(ctx context.Context) bool {
	return m.updateUnitFileState(ctx) == "disabled" && m.unitQuiesced(ctx, "rixa.service", false)
}
