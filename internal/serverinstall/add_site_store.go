// SPDX-License-Identifier: MPL-2.0
package serverinstall

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"time"
)

// addSiteSchema has its own ownership and effect ledger. The original install
// journal (and every completed update receipt) remains immutable. In
// particular, adding a site must never rewrite the original frozen Draft.
const addSiteSchema = "rixa.native-add-site.v1"

// AddSiteDraft is not an installation Draft: it has an independent operation
// identity and only the new site's credentials. These values are private.
type AddSiteDraft struct {
	OperationID string `json:"operation_id"`
	AddSiteInspectionDraft
}

func validAddSiteDraft(d AddSiteDraft) bool {
	if !operationSyntax.MatchString(d.OperationID) ||
		!operationSyntax.MatchString(d.InstallationOperation) ||
		d.OperationID == d.InstallationOperation ||
		d.SiteBOrigin == "" || d.SiteBDSN == "" || d.SiteBLogin == "" {
		return false
	}
	for _, v := range []string{d.SiteBOrigin, d.SiteBDSN, d.SiteBLogin, d.SiteBPassword} {
		if len(v) > 4096 || strings.ContainsAny(v, "\x00\r\n") {
			return false
		}
	}
	return true
}

func addSiteDraftDigest(d AddSiteDraft) string {
	// The password can be removed after a verified bootstrap without breaking
	// the immutable non-secret operation identity, as on native installation.
	d.SiteBPassword = ""
	b, _ := json.Marshal(d)
	return digest(b)
}

var addSiteStages = []string{
	"prepare", "migrate", "bootstrap-site-b", "stop", "activate", "check", "start", "postflight", "commit",
}

// addSiteJournal is purposefully disjoint from the original installation and
// Update journals. Predecessor hashes are immutable source-of-truth receipts;
// the active config/environment and physical database IDs are independently
// pinned. Unknown effects need stage-specific reconciliation, never replay.
type addSiteJournal struct {
	Schema                string            `json:"schema"`
	Draft                 AddSiteDraft      `json:"draft"`
	State                 string            `json:"state"`
	Stage                 string            `json:"stage,omitempty"`
	Code                  string            `json:"code"`
	Message               string            `json:"message,omitempty"`
	ReviewToken           string            `json:"review_token,omitempty"`
	Frozen                string            `json:"frozen,omitempty"`
	ParentSHA256          string            `json:"parent_sha256,omitempty"`
	ActiveUpdate          string            `json:"active_update,omitempty"`
	ActiveUpdateSHA256    string            `json:"active_update_sha256,omitempty"`
	ConfigSHA256          string            `json:"config_sha256,omitempty"`
	EnvironmentSHA256     string            `json:"environment_sha256,omitempty"`
	UnitSHA256            string            `json:"unit_sha256,omitempty"`
	ManagerSHA256         string            `json:"manager_sha256,omitempty"`
	ReviewedManagerSHA256 string            `json:"reviewed_manager_sha256,omitempty"`
	DatabaseTargets       map[string]string `json:"database_targets,omitempty"`
	SiteBPrincipal        string            `json:"site_b_principal,omitempty"`
	ExecutorGeneration    int               `json:"executor_generation,omitempty"`
	// Ordinary stage receipts belong to the last positively observed
	// executor, even while a newer generation is only queued.
	ExecutorVerifiedGeneration int               `json:"executor_verified_generation,omitempty"`
	ExecutorAcknowledged       bool              `json:"executor_acknowledged,omitempty"`
	Attempts                   map[string]string `json:"attempts"`
	Updated                    string            `json:"updated"`
}

func validAddSiteJournal(j *addSiteJournal) bool {
	if j == nil || j.Schema != addSiteSchema || !validAddSiteDraft(j.Draft) ||
		!codeSyntax.MatchString(j.Code) || len(j.Message) > 1024 || controlText(j.Message) ||
		j.ExecutorGeneration < 0 || j.ExecutorGeneration > 16 ||
		j.ExecutorVerifiedGeneration < 0 || j.ExecutorVerifiedGeneration > j.ExecutorGeneration || j.Attempts == nil {
		return false
	}
	if _, err := time.Parse(time.RFC3339Nano, j.Updated); err != nil {
		return false
	}
	switch j.State {
	case "draft", "ready", "blocked", "applying", "reconciliation-required", "installed":
	default:
		return false
	}
	for _, hash := range []string{j.ReviewToken, j.Frozen, j.ParentSHA256, j.ActiveUpdateSHA256, j.ConfigSHA256,
		j.EnvironmentSHA256, j.UnitSHA256, j.ManagerSHA256, j.ReviewedManagerSHA256} {
		if hash != "" && !hashSyntax.MatchString(hash) {
			return false
		}
	}
	if (j.ActiveUpdate == "") != (j.ActiveUpdateSHA256 == "") ||
		(j.ActiveUpdate != "" && !operationSyntax.MatchString(j.ActiveUpdate)) ||
		(j.Frozen != "" && j.Frozen != addSiteDraftDigest(j.Draft)) ||
		(j.ReviewToken != "" && j.State != "ready") {
		return false
	}
	// Once the separate site administrator has been durably bootstrapped,
	// the secret can be erased without changing the frozen draft identity.
	if j.Draft.SiteBPassword == "" && j.Attempts["bootstrap-site-b"] != "verified" {
		return false
	}
	if j.SiteBPrincipal != "" && !principalSyntax.MatchString(j.SiteBPrincipal) {
		return false
	}
	known := map[string]bool{"executor": true}
	for _, s := range addSiteStages {
		known[s] = true
	}
	if j.Stage != "" && j.Stage != "review" && !known[j.Stage] {
		return false
	}
	for key, value := range j.Attempts {
		if !known[key] || (value != "attempted" && value != "verified") {
			return false
		}
	}
	for n, stage := range addSiteStages {
		if j.Attempts[stage] == "" {
			continue
		}
		if j.ExecutorVerifiedGeneration == 0 {
			return false
		}
		for _, prev := range addSiteStages[:n] {
			if j.Attempts[prev] != "verified" {
				return false
			}
		}
	}
	// The independently bootstrapped site administrator is part of the
	// durable success receipt. It cannot be claimed before bootstrap, and
	// a verified bootstrap cannot have an unknown resulting principal.
	if (j.SiteBPrincipal != "" && j.Attempts["bootstrap-site-b"] != "verified") ||
		(j.Attempts["bootstrap-site-b"] == "verified" && j.SiteBPrincipal == "") {
		return false
	}
	if len(j.DatabaseTargets) > 0 {
		if len(j.DatabaseTargets) != 3 || j.DatabaseTargets["control"] == "" || j.DatabaseTargets["site-a"] == "" || j.DatabaseTargets["site-b"] == "" {
			return false
		}
		if j.DatabaseTargets["control"] == j.DatabaseTargets["site-a"] ||
			j.DatabaseTargets["site-a"] == j.DatabaseTargets["site-b"] ||
			j.DatabaseTargets["control"] == j.DatabaseTargets["site-b"] {
			return false
		}
		for _, id := range j.DatabaseTargets {
			if !databaseSyntax.MatchString(id) {
				return false
			}
		}
	}
	// A draft/review has never dispatched an executor. Refuse both an
	// arbitrary stage name and a review without the complete host bindings,
	// including the recorded 0644 systemd unit fingerprint.
	if j.Frozen == "" {
		if len(j.Attempts) != 0 || j.ExecutorGeneration != 0 || j.ExecutorVerifiedGeneration != 0 || j.ExecutorAcknowledged ||
			j.ManagerSHA256 != "" || j.State == "applying" ||
			j.State == "reconciliation-required" || j.State == "installed" {
			return false
		}
		switch j.State {
		case "draft":
			return j.Stage == ""
		case "blocked":
			return j.Stage == "" || j.Stage == "review"
		case "ready":
			return j.Stage == "review" && j.ReviewToken != "" &&
				j.ParentSHA256 != "" && j.ConfigSHA256 != "" &&
				j.EnvironmentSHA256 != "" && j.UnitSHA256 != "" &&
				j.ReviewedManagerSHA256 != "" && len(j.DatabaseTargets) == 3
		}
		return false
	}

	// Frozen is an explicit intent-before-effect boundary, not merely a
	// matching draft hash. Even a crash before systemd-run acknowledgement
	// must retain the executor attempt and a meaningful current stage. An
	// acknowledged queue may legitimately remain executor=attempted until
	// its child starts; never invent a verified start or completed stage.
	if j.State != "applying" && j.State != "reconciliation-required" && j.State != "installed" ||
		j.Stage == "" || j.Stage == "review" || j.Attempts["executor"] == "" ||
		j.ExecutorGeneration < 1 || j.ParentSHA256 == "" ||
		j.ConfigSHA256 == "" || j.EnvironmentSHA256 == "" || j.UnitSHA256 == "" ||
		j.ManagerSHA256 == "" || j.ReviewedManagerSHA256 != j.ManagerSHA256 ||
		len(j.DatabaseTargets) != 3 {
		return false
	}
	// The current generation may be only attempted while an earlier
	// verified generation owns retained ordinary stage receipts.
	switch j.Attempts["executor"] {
	case "attempted":
		// A queued child may terminate before it establishes its identity.
		// Multiple *acknowledged and positively quiesced* generations may be
		// retried without erasing any receipts from the last verified child.
		// The Resume gate, not this at-rest validator, proves every newer unit
		// was acknowledged and quiesced before a replacement is dispatched.
		if j.ExecutorVerifiedGeneration >= j.ExecutorGeneration {
			return false
		}
	case "verified":
		if j.ExecutorVerifiedGeneration != j.ExecutorGeneration || !j.ExecutorAcknowledged {
			return false
		}
	default:
		return false
	}
	if j.Stage == "executor" {
		// Before the first ordinary stage, no stage can claim a result.
		for _, stage := range addSiteStages {
			if j.Attempts[stage] != "" {
				return false
			}
		}
	} else {
		// Ordinary effects can begin only after the child has verified its
		// executor identity. The current stage must have a durable receipt,
		// and no *later* stage may have started (including after a crash).
		if j.ExecutorVerifiedGeneration == 0 || j.Attempts[j.Stage] == "" {
			return false
		}
		current := -1
		for i, stage := range addSiteStages {
			if stage == j.Stage {
				current = i
				break
			}
		}
		if current < 0 {
			return false
		}
		for _, stage := range addSiteStages[current+1:] {
			if j.Attempts[stage] != "" {
				return false
			}
		}
	}
	if j.State == "installed" {
		if j.Stage != "commit" || !j.ExecutorAcknowledged ||
			j.Attempts["executor"] != "verified" || j.ExecutorVerifiedGeneration != j.ExecutorGeneration || j.SiteBPrincipal == "" ||
			j.Draft.SiteBPassword != "" {
			return false
		}
		for _, stage := range addSiteStages {
			if j.Attempts[stage] != "verified" {
				return false
			}
		}
	}

	return true
}

func (m *Manager) addSitePath(id string) string {
	return filepath.Join(m.stateRoot, id+".add-site.json")
}

func (m *Manager) loadAddSite(id string) (*addSiteJournal, error) {
	if !operationSyntax.MatchString(id) {
		return nil, ErrInput
	}
	raw, err := readPrivate(m.addSitePath(id), m.ownerUID, maxStateBytes)
	if err != nil {
		return nil, err
	}
	var j addSiteJournal
	if decode(raw, &j) != nil || j.Draft.OperationID != id || !validAddSiteJournal(&j) {
		return nil, ErrState
	}
	return &j, nil
}

func (m *Manager) saveAddSite(j *addSiteJournal) error {
	if j == nil {
		return ErrState
	}
	j.Updated = time.Now().UTC().Format(time.RFC3339Nano)
	raw, err := json.Marshal(j)
	if err != nil || len(raw) > maxStateBytes || !validAddSiteJournal(j) {
		return ErrState
	}
	return writeAtomic(m.addSitePath(j.Draft.OperationID), raw, 0600)
}

func addSiteResult(j *addSiteJournal) Result {
	r := Result{OperationKind: "add-site", OperationID: j.Draft.OperationID,
		InstallationOperation: j.Draft.InstallationOperation, State: j.State, Stage: j.Stage,
		Code: j.Code, Message: j.Message}
	// Only a fresh Review result may disclose the one-use effect token.
	// Status and stage receipts never grant execution consent.
	switch j.State {
	case "draft", "blocked":
		r.Actions = []string{"review", "status"}
	case "ready":
		r.Actions = []string{"review", "apply", "status"}
	case "applying", "reconciliation-required":
		r.Actions = []string{"status"}
	case "installed":
		r.Actions = []string{"status"}
	default:
		r.Actions = []string{"status"}
	}
	return r
}
