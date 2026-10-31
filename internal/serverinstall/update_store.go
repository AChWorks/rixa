// SPDX-License-Identifier: MPL-2.0
package serverinstall

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"time"
)

const updateSchema = "rixa.native-update.v1"

var updateStages = []string{"payload", "stop", "compatibility", "migrate", "check", "activate", "start", "postflight", "commit"}

// updateJournal is deliberately separate from the immutable installation
// receipt. It records the exact predecessor and current configuration; it never
// carries replacement bootstrap passwords or reconstructs configuration.
type updateJournal struct {
	Schema                string            `json:"schema"`
	Draft                 UpdateDraft       `json:"draft"`
	State                 string            `json:"state"`
	Stage                 string            `json:"stage,omitempty"`
	Code                  string            `json:"code"`
	Message               string            `json:"message,omitempty"`
	ReviewToken           string            `json:"review_token,omitempty"`
	Frozen                string            `json:"frozen,omitempty"`
	ParentSHA256          string            `json:"parent_sha256,omitempty"`
	PriorUpdate           string            `json:"prior_update,omitempty"`
	PriorSHA256           string            `json:"prior_sha256,omitempty"`
	ActiveRelease         string            `json:"active_release,omitempty"`
	ConfigSHA256          string            `json:"config_sha256,omitempty"`
	EnvironmentSHA256     string            `json:"environment_sha256,omitempty"`
	DatabaseTargets       map[string]string `json:"database_targets,omitempty"`
	ReviewedManagerSHA256 string            `json:"reviewed_manager_sha256,omitempty"`
	ManagerSHA256         string            `json:"manager_sha256,omitempty"`
	HelperPath            string            `json:"helper_path,omitempty"`
	HelperSHA256          string            `json:"helper_sha256,omitempty"`
	TransportEnv          map[string]string `json:"transport_env,omitempty"`
	ArchiveSHA256         string            `json:"archive_sha256,omitempty"`
	CandidateComponents   map[string]string `json:"candidate_components,omitempty"`
	BinarySHA256          string            `json:"binary_sha256,omitempty"`
	UnitSHA256            string            `json:"unit_sha256,omitempty"`
	ExecutorGeneration    int               `json:"executor_generation,omitempty"`
	ExecutorAcknowledged  bool              `json:"executor_acknowledged,omitempty"`
	Attempts              map[string]string `json:"attempts"`
	Updated               string            `json:"updated"`
}

func ValidateUpdateDraft(d UpdateDraft) []Problem {
	var p []Problem
	if !operationSyntax.MatchString(d.OperationID) {
		p = append(p, Problem{"operation_id", "format", "Use 1–48 lowercase letters, numbers or hyphens, beginning with a letter or number."})
	}
	if !operationSyntax.MatchString(d.InstallationOperation) || d.InstallationOperation == d.OperationID {
		p = append(p, Problem{"installation_operation", "installation", "Choose the distinct original installed native operation ID."})
	}
	for _, f := range []struct{ name, value string }{{"archive", d.ArchivePath}, {"bundle", d.BundlePath}} {
		if !safePath(f.value) {
			p = append(p, Problem{f.name, "path", "Use one absolute canonical regular-file path without control characters."})
		}
	}
	if !releaseSyntax.MatchString(d.Release) || len(d.Release) > 128 {
		p = append(p, Problem{"release", "format", "Choose the exact versioned Rixa release tag."})
	}
	if !commitSyntax.MatchString(d.SourceCommit) {
		p = append(p, Problem{"source_commit", "format", "Use the exact 40-character lowercase source commit."})
	}
	return p
}
func updateDigest(d UpdateDraft) string        { b, _ := json.Marshal(d); return digest(b) }
func (m *Manager) updatePath(id string) string { return filepath.Join(m.stateRoot, id+".update.json") }
func (m *Manager) saveUpdate(u *updateJournal) error {
	u.Updated = time.Now().UTC().Format(time.RFC3339Nano)
	b, e := json.Marshal(u)
	if e != nil || len(b) > maxStateBytes || !validUpdate(u) {
		return ErrState
	}
	return writeAtomic(m.updatePath(u.Draft.OperationID), b, 0600)
}
func (m *Manager) loadUpdate(id string) (*updateJournal, error) {
	if !operationSyntax.MatchString(id) {
		return nil, ErrInput
	}
	b, e := readPrivate(m.updatePath(id), m.ownerUID, maxStateBytes)
	if e != nil {
		return nil, e
	}
	var u updateJournal
	if decode(b, &u) != nil || u.Draft.OperationID != id || !validUpdate(&u) {
		return nil, ErrState
	}
	return &u, nil
}
func validUpdate(u *updateJournal) bool {
	if u.Schema != updateSchema || len(ValidateUpdateDraft(u.Draft)) != 0 || !codeSyntax.MatchString(u.Code) || len(u.Message) > 1024 || controlText(u.Message) {
		return false
	}
	if _, e := time.Parse(time.RFC3339Nano, u.Updated); e != nil {
		return false
	}
	for _, h := range []string{u.Frozen, u.ReviewToken, u.ParentSHA256, u.PriorSHA256, u.ConfigSHA256, u.EnvironmentSHA256, u.ReviewedManagerSHA256, u.ManagerSHA256, u.HelperSHA256, u.ArchiveSHA256, u.BinarySHA256, u.UnitSHA256} {
		if h != "" && !hashSyntax.MatchString(h) {
			return false
		}
	}
	if u.PriorUpdate != "" && (!operationSyntax.MatchString(u.PriorUpdate) || u.PriorUpdate == u.Draft.OperationID || u.PriorSHA256 == "") {
		return false
	}
	if (u.PriorUpdate == "") != (u.PriorSHA256 == "") {
		return false
	}
	if u.HelperPath != "" && !safePath(u.HelperPath) {
		return false
	}
	if u.ActiveRelease != "" && (!releaseSyntax.MatchString(u.ActiveRelease) || len(u.ActiveRelease) > 128) {
		return false
	}
	for k, v := range u.TransportEnv {
		if !transportKeys[k] || len(v) > 4096 || strings.ContainsAny(v, "\r\n\x00") {
			return false
		}
	}
	if len(u.DatabaseTargets) != 0 && len(u.DatabaseTargets) != 2 {
		return false
	}
	for k, v := range u.DatabaseTargets {
		if (k != "control" && k != "site-a") || !databaseSyntax.MatchString(v) {
			return false
		}
	}
	known := map[string]bool{"executor": true, "remove": true}
	for _, s := range updateStages {
		known[s] = true
	}
	if u.Attempts == nil {
		return false
	}
	for k, v := range u.Attempts {
		if !known[k] || (v != "attempted" && v != "verified") {
			return false
		}
	}
	if u.Stage != "" && u.Stage != "review" && !known[u.Stage] {
		return false
	}
	// Stages are a single ordered contract. A partial receipt cannot skip
	// compatibility, migration, readiness or the durable activation boundary.
	for i, stage := range updateStages {
		if u.Attempts[stage] != "" {
			for _, previous := range updateStages[:i] {
				if u.Attempts[previous] != "verified" {
					return false
				}
			}
		}
	}
	if (u.BinarySHA256 == "") != (u.UnitSHA256 == "") {
		return false
	}
	if u.BinarySHA256 != "" {
		if !validUpdateComponents(u.CandidateComponents) {
			return false
		}
	} else if len(u.CandidateComponents) != 0 {
		return false
	}

	if u.ExecutorGeneration < 0 || u.ExecutorGeneration > 16 {
		return false
	}
	switch u.State {
	case "draft", "ready", "blocked", "applying", "reconciliation-required", "installed", "removed", "rejected":
	default:
		return false
	}
	if u.State == "rejected" {
		if u.BinarySHA256 == "" || u.UnitSHA256 == "" || u.Attempts["payload"] != "verified" || u.Attempts["migrate"] != "" {
			return false
		}
		switch u.Code {
		case "editorial-upgrade-pending-prior-uninterrupted":
			if u.Stage != "stop" || u.Attempts["stop"] != "attempted" || u.Attempts["compatibility"] != "" {
				return false
			}
		case "candidate-incompatible-prior-restored":
			if u.Stage != "compatibility" || u.Attempts["stop"] != "verified" || u.Attempts["compatibility"] != "attempted" {
				return false
			}
		default:
			return false
		}
	}
	if u.Frozen == "" {
		if u.Stage != "" && u.Stage != "review" {
			return false
		}
		if u.State == "ready" && (u.ReviewToken == "" || u.ParentSHA256 == "" || u.ConfigSHA256 == "" || u.EnvironmentSHA256 == "" || u.ReviewedManagerSHA256 == "" || u.HelperSHA256 == "" || u.ArchiveSHA256 == "" || len(u.DatabaseTargets) != 2) {
			return false
		}
		if u.State != "draft" && u.State != "ready" && u.State != "blocked" {
			return false
		}
		return len(u.Attempts) == 0 && u.ManagerSHA256 == "" && u.ExecutorGeneration == 0 && !u.ExecutorAcknowledged && u.BinarySHA256 == "" && u.UnitSHA256 == ""
	}
	if u.Stage == "" || u.Stage == "review" || u.Attempts["executor"] == "" || !safePath(u.HelperPath) {
		return false
	}
	if u.Frozen != updateDigest(u.Draft) || u.ParentSHA256 == "" || u.ConfigSHA256 == "" || u.EnvironmentSHA256 == "" || len(u.DatabaseTargets) != 2 || u.ManagerSHA256 == "" || u.HelperSHA256 == "" || u.ArchiveSHA256 == "" || u.ReviewedManagerSHA256 != u.ManagerSHA256 || u.ReviewToken == "" || u.ActiveRelease == "" || u.ExecutorGeneration < 1 {
		return false
	}
	if u.State == "draft" || u.State == "blocked" || u.State == "ready" {
		return false
	}
	if u.State == "installed" || u.State == "removed" {
		if u.BinarySHA256 == "" || u.UnitSHA256 == "" {
			return false
		}
		for _, s := range updateStages {
			if u.Attempts[s] != "verified" {
				return false
			}
		}
	}
	return u.State != "removed" || u.Attempts["remove"] == "verified"
}
func updateResult(u *updateJournal) Result {
	r := Result{OperationKind: "update", OperationID: u.Draft.OperationID, InstallationOperation: u.Draft.InstallationOperation, State: u.State, Stage: u.Stage, Code: u.Code, Message: u.Message, Release: u.Draft.Release, ActiveRelease: u.ActiveRelease, Actions: []string{"status"}}
	switch u.State {
	case "draft", "blocked":
		r.Actions = []string{"configure", "review", "status"}
	case "ready":
		r.Actions = []string{"configure", "review", "apply", "status"}
		r.ReviewToken = u.ReviewToken
	case "installed":
		if u.Attempts["remove"] == "" {
			r.Actions = []string{"status", "remove"}
		}
		r.ActiveRelease = u.Draft.Release
	case "removed":
		r.ActiveRelease = u.Draft.Release
	}
	return r
}
func (m *Manager) updateMarker(name string) (string, error) {
	b, e := readPrivate(filepath.Join(m.stateRoot, name), m.ownerUID, 128)
	if errors.Is(e, os.ErrNotExist) {
		return "", nil
	}
	if e != nil {
		return "", e
	}
	id := strings.TrimSuffix(string(b), "\n")
	if string(b) != id+"\n" || !operationSyntax.MatchString(id) {
		return "", ErrState
	}
	return id, nil
}
func (m *Manager) claimUpdate(id string) error {
	path := filepath.Join(m.stateRoot, "pending-update")
	if e := writeNew(path, []byte(id+"\n"), 0600); e != nil {
		found, re := m.updateMarker("pending-update")
		if re != nil || found != id {
			return ErrBusy
		}
	}
	return nil
}
func (m *Manager) clearUpdateClaim(id string) error {
	found, e := m.updateMarker("pending-update")
	if e != nil {
		return e
	}
	if found == "" {
		return nil
	}
	if found != id {
		return ErrState
	}
	if e = os.Remove(filepath.Join(m.stateRoot, "pending-update")); e != nil {
		return e
	}
	d, e := os.Open(m.stateRoot)
	if e != nil {
		return e
	}
	defer d.Close()
	return d.Sync()
}

// originalUpdateParent checks the immutable journal, preserved frozen artifacts,
// and original installation owner. The original account ID is used throughout.
func (m *Manager) originalUpdateParent(u *updateJournal) (*journal, error) {
	j, e := m.load(u.Draft.InstallationOperation)
	if e != nil {
		return nil, e
	}
	if j.State != "installed" || j.Frozen == "" {
		return nil, ErrState
	}
	b, e := readPrivate(m.statePath(j.Draft.OperationID), m.ownerUID, maxStateBytes)
	if e != nil {
		return nil, e
	}
	if u.ParentSHA256 != "" && digest(b) != u.ParentSHA256 {
		return nil, ErrState
	}
	owner, e := readPrivate(filepath.Join(m.stateRoot, "installation"), m.ownerUID, 128)
	if e != nil || string(owner) != j.Draft.OperationID+"\n" {
		return nil, ErrState
	}
	for _, v := range []struct{ path, hash string }{{filepath.Join(managerRoot, j.ManagerSHA256), j.ManagerSHA256}, {filepath.Join(m.helperDir(j), "gh"), j.HelperSHA256}} {
		h, he := hashFile(v.path)
		if he != nil || h != v.hash || !trustedExecutable(v.path) {
			return nil, ErrState
		}
	}
	return j, nil
}
func updateView(parent *journal, u *updateJournal) *journal {
	j := *parent
	j.Draft = parent.Draft
	j.Draft.Release = u.Draft.Release
	j.Draft.SourceCommit = u.Draft.SourceCommit
	j.Draft.ArchivePath = u.Draft.ArchivePath
	j.Draft.BundlePath = u.Draft.BundlePath
	j.ArchiveSHA256 = u.ArchiveSHA256
	j.BinarySHA256 = u.BinarySHA256
	j.UnitSHA256 = u.UnitSHA256
	return &j
}
func (m *Manager) predecessor(u *updateJournal) (*journal, error) {
	j, e := m.originalUpdateParent(u)
	if e != nil {
		return nil, e
	}
	if u.PriorUpdate == "" {
		return j, nil
	}
	b, e := readPrivate(m.updatePath(u.PriorUpdate), m.ownerUID, maxStateBytes)
	if e != nil || digest(b) != u.PriorSHA256 {
		return nil, ErrState
	}
	prior, e := m.loadUpdate(u.PriorUpdate)
	if e != nil || prior.State != "installed" || prior.Draft.InstallationOperation != u.Draft.InstallationOperation || prior.ParentSHA256 != u.ParentSHA256 {
		return nil, ErrState
	}
	return updateView(j, prior), nil
}
func (m *Manager) activePredecessor(u *updateJournal) (*journal, error) {
	active, e := m.updateMarker("active-update")
	if e != nil || active != u.PriorUpdate {
		return nil, ErrState
	}
	return m.predecessor(u)
}
func (m *Manager) updateFailure(u *updateJournal, code, message string, cause error) (Result, error) {
	if cause != nil {
		if parent, e := m.load(u.Draft.InstallationOperation); e == nil {
			diagnostic := *parent
			diagnostic.Draft = parent.Draft
			diagnostic.Draft.OperationID = u.Draft.OperationID
			m.retainDiagnostic(&diagnostic, "stage="+u.Stage+" code="+code+" cause="+cause.Error())
		}
	}
	u.State, u.Code, u.Message = "reconciliation-required", code, message
	if e := m.saveUpdate(u); e != nil {
		return Result{}, e
	}
	return updateResult(u), ErrState
}
func sameUpdateEvidence(a, b *updateJournal) bool {
	// Operational state and approval token are not prerequisite evidence.
	x, y := *a, *b
	x.State = ""
	y.State = ""
	x.Stage = ""
	y.Stage = ""
	x.Code = ""
	y.Code = ""
	x.Message = ""
	y.Message = ""
	x.ReviewToken = ""
	y.ReviewToken = ""
	x.Updated = ""
	y.Updated = ""
	return reflect.DeepEqual(x, y)
}
