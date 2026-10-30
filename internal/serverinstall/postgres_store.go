// SPDX-License-Identifier: MPL-2.0
package serverinstall

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/AChWorks/achrix/installation"
)

const postgresSchema = "rixa.native-postgresql.v1"

var postgresStages = []string{"download", "common", "suppress-main", "server", "cluster", "start", "control-role", "site-role", "control-db", "site-db", "permissions", "verify"}

type postgresArtifact struct {
	Package PostgresPackage `json:"package"`
	SHA256  string          `json:"sha256"`
}
type postgresJournal struct {
	Schema                string                      `json:"schema"`
	Draft                 PostgresDraft               `json:"draft"`
	State                 string                      `json:"state"`
	Stage                 string                      `json:"stage,omitempty"`
	Code                  string                      `json:"code"`
	Message               string                      `json:"message,omitempty"`
	Token                 string                      `json:"token"`
	ControlPassword       string                      `json:"control_password"`
	SitePassword          string                      `json:"site_password"`
	ControlVerifier       string                      `json:"control_verifier"`
	SiteVerifier          string                      `json:"site_verifier"`
	ReviewToken           string                      `json:"review_token,omitempty"`
	ReviewedAt            string                      `json:"reviewed_at,omitempty"`
	ReviewedManagerSHA256 string                      `json:"reviewed_manager_sha256,omitempty"`
	ManagerSHA256         string                      `json:"manager_sha256,omitempty"`
	Frozen                string                      `json:"frozen,omitempty"`
	Baseline              map[string]string           `json:"baseline,omitempty"`
	HostSHA256            string                      `json:"host_sha256,omitempty"`
	Helpers               map[string]string           `json:"helpers,omitempty"`
	DpkgConfigSHA256      string                      `json:"dpkg_config_sha256,omitempty"`
	KeySHA256             string                      `json:"key_sha256,omitempty"`
	ListsSHA256           string                      `json:"lists_sha256,omitempty"`
	Packages              []PostgresPackage           `json:"packages,omitempty"`
	CommonPackages        []PostgresPackage           `json:"common_packages,omitempty"`
	Artifacts             map[string]postgresArtifact `json:"artifacts,omitempty"`
	ExecutorGeneration    int                         `json:"executor_generation,omitempty"`
	ExecutorAcknowledged  bool                        `json:"executor_acknowledged,omitempty"`
	Attempts              map[string]string           `json:"attempts"`
	PostgresTools         map[string]string           `json:"postgres_tools,omitempty"`
	PostgresGroups        []uint32                    `json:"postgres_groups,omitempty"`
	PostgresUID           uint32                      `json:"postgres_uid,omitempty"`
	PostgresGID           uint32                      `json:"postgres_gid,omitempty"`
	ClusterID             string                      `json:"cluster_id,omitempty"`
	DataIdentity          string                      `json:"data_identity,omitempty"`
	ConfigSHA256          string                      `json:"config_sha256,omitempty"`
	HBASHA256             string                      `json:"hba_sha256,omitempty"`
	BinarySHA256          string                      `json:"binary_sha256,omitempty"`
	DatabaseTargets       map[string]string           `json:"database_targets,omitempty"`
	AssessmentID          string                      `json:"assessment_id,omitempty"`
	Progress              installation.Progress       `json:"progress"`
	Updated               string                      `json:"updated"`
}

func postgresDraftDigest(d PostgresDraft) string { b, _ := json.Marshal(d); return digest(b) }
func postgresRandom() (string, error) {
	b := make([]byte, 32)
	if _, e := rand.Read(b); e != nil {
		return "", e
	}
	return hex.EncodeToString(b), nil
}
func (m *Manager) postgresStateDir() string { return filepath.Join(m.stateRoot, "postgres") }
func (m *Manager) postgresPath(id string) string {
	return filepath.Join(m.postgresStateDir(), id+".json")
}
func (m *Manager) savePostgres(j *postgresJournal) error {
	j.Updated = time.Now().UTC().Format(time.RFC3339Nano)
	if !validPostgresJournal(j) {
		return ErrState
	}
	b, e := json.Marshal(j)
	if e != nil || len(b) > maxStateBytes {
		return ErrState
	}
	return writeAtomic(m.postgresPath(j.Draft.OperationID), b, 0600)
}
func (m *Manager) loadPostgres(id string) (*postgresJournal, error) {
	if !operationSyntax.MatchString(id) {
		return nil, ErrInput
	}
	if e := privateDir(m.postgresStateDir(), m.ownerUID, false); e != nil {
		return nil, e
	}
	b, e := readPrivate(m.postgresPath(id), m.ownerUID, maxStateBytes)
	if e != nil {
		return nil, e
	}
	var j postgresJournal
	if decode(b, &j) != nil || j.Draft.OperationID != id || !validPostgresJournal(&j) {
		return nil, ErrState
	}
	return &j, nil
}
func validPostgresJournal(j *postgresJournal) bool {
	if j.Schema != postgresSchema || len(ValidatePostgresDraft(j.Draft)) != 0 || !codeSyntax.MatchString(j.Code) || len(j.Message) > 1024 || controlText(j.Message) || j.Attempts == nil {
		return false
	}
	if _, e := time.Parse(time.RFC3339Nano, j.Updated); e != nil {
		return false
	}
	for _, v := range []string{j.Token, j.ControlPassword, j.SitePassword} {
		if !hashSyntax.MatchString(v) {
			return false
		}
	}
	if !validPostgresVerifier(j.ControlPassword, j.ControlVerifier) || !validPostgresVerifier(j.SitePassword, j.SiteVerifier) {
		return false
	}
	for _, v := range []string{j.ReviewToken, j.ReviewedManagerSHA256, j.ManagerSHA256, j.Frozen, j.HostSHA256, j.KeySHA256, j.ListsSHA256, j.ConfigSHA256, j.HBASHA256, j.BinarySHA256, j.AssessmentID, j.DpkgConfigSHA256} {
		if v != "" && !hashSyntax.MatchString(v) {
			return false
		}
	}
	known := map[string]bool{"executor": true}
	for _, s := range postgresStages {
		known[s] = true
	}
	if j.Stage != "" && j.Stage != "review" && !known[j.Stage] {
		return false
	}
	for s, v := range j.Attempts {
		if !known[s] || (v != "attempted" && v != "verified") {
			return false
		}
	}
	for i, s := range postgresStages {
		if j.Attempts[s] != "" {
			for _, p := range postgresStages[:i] {
				if j.Attempts[p] != "verified" {
					return false
				}
			}
		}
	}
	if j.ExecutorGeneration < 0 || j.ExecutorGeneration > 16 {
		return false
	}
	if len(j.Baseline) > 2048 || len(j.Packages) > 96 || len(j.CommonPackages) > 96 || len(j.Helpers) > 8 || len(j.Artifacts) > 96 || len(j.PostgresTools) > len(postgresToolPaths) {
		return false
	}
	for n, v := range j.Baseline {
		if !postgresPackageName.MatchString(n) || !postgresPackageVersion.MatchString(v) {
			return false
		}
	}
	for n, h := range j.Helpers {
		if !safePath(n) || !hashSyntax.MatchString(h) {
			return false
		}
	}
	if !validPostgresPackages(j.Packages) || !validPostgresPackages(j.CommonPackages) {
		return false
	}
	if len(j.Packages) > 0 {
		if !postgresPackageSubset(j.CommonPackages, j.Packages) || postgresPackageVersionFor(j.Packages, "postgresql-18") == "" || postgresPackageVersionFor(j.CommonPackages, "postgresql-common") == "" {
			return false
		}
	}
	for p, a := range j.Artifacts {
		if !safePath(p) || !hashSyntax.MatchString(a.SHA256) || !postgresPackageSubset([]PostgresPackage{a.Package}, j.Packages) {
			return false
		}
	}
	for path, h := range j.PostgresTools {
		found := false
		for _, known := range postgresToolPaths {
			if path == known {
				found = true
			}
		}
		if !found || !hashSyntax.MatchString(h) {
			return false
		}
	}
	if len(j.Progress.Attempts) > 1 {
		return false
	}
	for _, attempt := range j.Progress.Attempts {
		if attempt.RequirementID != "owned-postgresql18" || (attempt.State != installation.Attempted && attempt.State != installation.Verified) {
			return false
		}
	}
	if j.Frozen != "" && (j.AssessmentID == "" || j.Progress.PlanID != j.AssessmentID) {
		return false
	}
	if j.Frozen == "" && len(j.Progress.Attempts) > 0 {
		return false
	}
	if len(j.PostgresGroups) > 2 {
		return false
	}
	for _, g := range j.PostgresGroups {
		if g == 0 {
			return false
		}
	}
	if j.ClusterID != "" && !postgresNumber.MatchString(j.ClusterID) {
		return false
	}
	if j.DataIdentity != "" && !databaseSyntax.MatchString(j.DataIdentity) {
		return false
	}
	if len(j.DatabaseTargets) > 2 {
		return false
	}
	for n, v := range j.DatabaseTargets {
		if (n != "control" && n != "site-a") || !databaseSyntax.MatchString(v) {
			return false
		}
	}
	switch j.State {
	case "draft", "blocked", "ready", "applying", "reconciliation-required", "installed":
	default:
		return false
	}
	if j.Frozen == "" {
		if j.State != "draft" && j.State != "blocked" && j.State != "ready" {
			return false
		}
		if len(j.Attempts) > 0 || j.ManagerSHA256 != "" || j.ExecutorGeneration != 0 || j.ExecutorAcknowledged {
			return false
		}
	} else {
		if j.Frozen != postgresIntentDigest(j) || j.ManagerSHA256 == "" || j.ManagerSHA256 != j.ReviewedManagerSHA256 || j.ReviewToken == "" || j.HostSHA256 == "" || j.DpkgConfigSHA256 == "" || j.KeySHA256 == "" || j.ListsSHA256 == "" || len(j.Packages) == 0 || len(j.Helpers) == 0 || j.ExecutorGeneration < 1 || j.Attempts["executor"] == "" {
			return false
		}
		if j.State != "applying" && j.State != "reconciliation-required" && j.State != "installed" {
			return false
		}
	}
	if j.State == "ready" && (j.ReviewToken == "" || j.ReviewedManagerSHA256 == "" || j.HostSHA256 == "" || len(j.Packages) == 0 || j.ReviewedAt == "") {
		return false
	}
	if j.ReviewedAt != "" {
		if _, e := time.Parse(time.RFC3339Nano, j.ReviewedAt); e != nil {
			return false
		}
	}
	if j.Attempts["cluster"] == "verified" && (j.ClusterID == "" || j.PostgresUID == 0 || j.PostgresGID == 0 || j.DataIdentity == "" || j.ConfigSHA256 == "" || j.HBASHA256 == "" || j.BinarySHA256 == "") {
		return false
	}
	if j.State == "installed" {
		if len(j.Progress.Attempts) != 1 || j.Progress.Attempts[0].State != installation.Verified {
			return false
		}
		for _, s := range postgresStages {
			if j.Attempts[s] != "verified" {
				return false
			}
		}
		if len(j.DatabaseTargets) != 2 {
			return false
		}
	}
	return true
}
func postgresNames(j *postgresJournal) (cluster, control, site string) {
	h := digest([]byte(j.Draft.OperationID + ":" + j.Token))[:16]
	return "rixa" + h, "rixa_c_" + h, "rixa_s_" + h
}
func postgresPublicPlan(j *postgresJournal) *PostgresPlan {
	if len(j.Packages) == 0 {
		return nil
	}
	cluster, _, _ := postgresNames(j)
	p := &PostgresPlan{Profile: "debian13-amd64-native-postgresql18", Cluster: cluster, Port: j.Draft.Port, Packages: append([]PostgresPackage(nil), j.Packages...), CommonPackages: append([]PostgresPackage(nil), j.CommonPackages...),
		Effects:  []string{"Authenticate exact Debian/PGDG packages through scoped signed APT metadata.", "Install only reviewed new dependency versions; no upgrades or removals.", "Suppress implicit main-cluster creation before installing version-specific PostgreSQL18.", "Create one explicitly named loopback/password cluster and two isolated least-privilege application roles/databases."},
		Retained: []string{"PostgreSQL packages, cluster, databases and credentials remain after ordinary Rixa removal."}}
	candidate := j.ReviewedManagerSHA256
	if candidate == "" {
		return p
	}
	pre, _ := json.Marshal(struct {
		Draft            PostgresDraft
		Host             string
		Packages, Common []PostgresPackage
	}{j.Draft, j.HostSHA256, j.Packages, j.CommonPackages})
	profile := installation.Profile{Binding: installation.Binding{ProfileID: p.Profile, OperationID: j.Draft.OperationID, CandidateSHA256: candidate, PreconditionsSHA256: digest(pre)}, Requirements: []installation.Requirement{{ID: "owned-postgresql18", Required: true}}}
	state := installation.Missing
	if j.State == "installed" {
		state = installation.Satisfied
	}
	plan, e := installation.Assess(profile, []installation.Observation{{RequirementID: "owned-postgresql18", State: state}}, []installation.Support{{RequirementID: "owned-postgresql18", AdapterID: "rixa-native-postgresql18", Authorized: true}}, j.Progress)
	if e == nil {
		p.Assessment = plan
	}
	return p
}
func postgresResult(j *postgresJournal) PostgresResult {
	r := PostgresResult{Result: Result{OperationKind: "postgres-prerequisite", OperationID: j.Draft.OperationID, State: j.State, Stage: j.Stage, Code: j.Code, Message: j.Message, Actions: []string{"status"}}, Plan: postgresPublicPlan(j)}
	switch j.State {
	case "draft", "blocked":
		r.Actions = []string{"configure", "review", "status"}
	case "ready":
		r.Actions = []string{"configure", "review", "apply", "status"}
		r.ReviewToken = j.ReviewToken
	}
	return r
}
func (m *Manager) postgresOwner() (string, error) {
	b, e := readPrivate(filepath.Join(m.postgresStateDir(), "owner"), m.ownerUID, 128)
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
func (m *Manager) postgresFail(j *postgresJournal, code, message string, cause error) (PostgresResult, error) {
	if cause != nil {
		path := filepath.Join(m.postgresStateDir(), j.Draft.OperationID+".diagnostic")
		old, e := readPrivate(path, m.ownerUID, 64<<10)
		if e == nil || errors.Is(e, os.ErrNotExist) {
			text := string(old) + "\nstage=" + j.Stage + " code=" + code + " cause=" + cause.Error()
			for _, s := range []string{j.Token, j.ControlPassword, j.SitePassword, j.ControlVerifier, j.SiteVerifier} {
				text = strings.ReplaceAll(text, s, "[redacted]")
			}
			if len(text) > 64<<10 {
				text = text[len(text)-(64<<10):]
			}
			_ = writeAtomic(path, []byte(text), 0600)
		}
	}
	j.State, j.Code, j.Message = "reconciliation-required", code, message
	if e := m.savePostgres(j); e != nil {
		return PostgresResult{}, e
	}
	return postgresResult(j), ErrState
}
func (m *Manager) DiscoverPostgres(ctx context.Context) ([]PostgresDiscovery, error) {
	if e := m.authorize(); e != nil {
		return nil, e
	}
	if e := ctx.Err(); e != nil {
		return nil, e
	}
	if _, e := os.Lstat(m.stateRoot); errors.Is(e, os.ErrNotExist) {
		return nil, nil
	}
	if e := privateDir(m.stateRoot, m.ownerUID, false); e != nil {
		return nil, e
	}
	dir := m.postgresStateDir()
	if _, e := os.Lstat(dir); errors.Is(e, os.ErrNotExist) {
		return nil, nil
	}
	if e := privateDir(dir, m.ownerUID, false); e != nil {
		return nil, e
	}
	entries, e := os.ReadDir(dir)
	if e != nil || len(entries) > 256 {
		return nil, ErrState
	}
	var result []PostgresDiscovery
	diagnostics := map[string]bool{}
	records := map[string]bool{}
	for _, entry := range entries {
		name := entry.Name()
		if name == "owner" {
			if _, e = m.postgresOwner(); e != nil {
				return nil, e
			}
			continue
		}
		if strings.HasSuffix(name, ".diagnostic") {
			if !operationSyntax.MatchString(strings.TrimSuffix(name, ".diagnostic")) {
				return nil, ErrState
			}
			diagnostics[strings.TrimSuffix(name, ".diagnostic")] = true
			if _, e = readPrivate(filepath.Join(dir, name), m.ownerUID, 64<<10); e != nil {
				return nil, e
			}
			continue
		}
		if !strings.HasSuffix(name, ".json") || entry.IsDir() {
			return nil, ErrState
		}
		id := strings.TrimSuffix(name, ".json")
		j, e := m.loadPostgres(id)
		if e != nil {
			return nil, e
		}
		if j.Frozen != "" && !postgresManagerIntact(j) {
			return nil, ErrManagerChanged
		}
		cluster, _, _ := postgresNames(j)
		r := postgresResult(j)
		d := PostgresDiscovery{OperationID: id, State: j.State, Stage: j.Stage, Code: j.Code, Cluster: cluster, Port: j.Draft.Port, Actions: r.Actions, ManagerSHA256: j.ManagerSHA256}
		if j.ManagerSHA256 != "" {
			d.ManagerPath = filepath.Join(managerRoot, j.ManagerSHA256)
		}
		result = append(result, d)
		records[id] = true
	}
	for id := range diagnostics {
		if !records[id] {
			return nil, ErrState
		}
	}
	owner, e := m.postgresOwner()
	if e != nil {
		return nil, e
	}
	if owner != "" && !records[owner] {
		return nil, ErrState
	}
	sort.Slice(result, func(i, j int) bool { return result[i].OperationID < result[j].OperationID })
	return result, nil
}
func postgresManagerIntact(j *postgresJournal) bool {
	p := filepath.Join(managerRoot, j.ManagerSHA256)
	info, e := os.Lstat(p)
	if e != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0755 || fileUID(info) != 0 {
		return false
	}
	h, e := hashFile(p)
	return e == nil && h == j.ManagerSHA256 && trustedExecutable(p)
}
func (m *Manager) LoadPostgresCredentials(ctx context.Context, id string) (PostgresCredentials, error) {
	if e := m.authorize(); e != nil {
		return PostgresCredentials{}, e
	}
	if e := ctx.Err(); e != nil {
		return PostgresCredentials{}, e
	}
	j, e := m.loadPostgres(id)
	if e != nil {
		return PostgresCredentials{}, e
	}
	if j.State != "installed" {
		return PostgresCredentials{}, ErrState
	}
	b := newPostgresBackend(m)
	if e = b.verify(ctx, j); e != nil {
		return PostgresCredentials{}, e
	}
	return postgresCredentials(j), nil
}
