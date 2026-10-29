// SPDX-License-Identifier: MPL-2.0

package product

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"regexp"
	goruntime "runtime"
	"strings"
	"sync"
	"time"

	"github.com/AChWorks/achrix/installation"
	"github.com/jackc/pgx/v5"
	"golang.org/x/sys/unix"
)

var (
	ErrSetupInput        = errors.New("invalid setup input or private target directory")
	ErrSetupBusy         = errors.New("another installation operation is active")
	ErrSetupStale        = errors.New("setup binding changed; inspect the retained operation")
	ErrSetupState        = errors.New("setup state unavailable or unsafe; inspect before continuing")
	setupOperationSyntax = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`)
)

// SetupRequest prepares one private configuration file. It does not install a
// database, migrate schema, bootstrap accounts, start a service or deploy Rixa.
// Configuration contains environment references, never resolved secret values.
type SetupRequest struct {
	OperationID   string
	TargetPath    string
	Configuration []byte
}

type SetupState string

const (
	SetupPlanned        SetupState = "planned"
	SetupAttempted      SetupState = "attempted"
	SetupVerified       SetupState = "verified"
	SetupFailed         SetupState = "failed"
	SetupReconciliation SetupState = "reconciliation-required"
)

// SetupResult is the common CLI/TUI/web backend result. The Plan's ready status
// covers only configuration preparation's declared subset, never deployment.
// Neither this result nor returned errors contains configuration, paths or DSNs.
type SetupResult struct {
	OperationID string             `json:"operation_id"`
	Scope       string             `json:"scope"`
	State       SetupState         `json:"state"`
	Code        string             `json:"code"`
	Plan        *installation.Plan `json:"plan,omitempty"`
	Guidance    map[string]string  `json:"guidance,omitempty"`
	Reconciled  bool               `json:"reconciled,omitempty"`
}

type setupJournal struct {
	Schema              string
	OperationID         string
	TargetSHA256        string
	Configuration       []byte
	ConfigurationSHA256 string
	Binding             installation.Binding
	PlanID              string
	State               SetupState
	Progress            installation.Progress
	Created             bool
}

func (j *setupJournal) valid() bool {
	if j.Schema != setupJournalSchema || !setupOperationSyntax.MatchString(j.OperationID) || len(j.Configuration) == 0 || len(j.Configuration) > 1<<20 || setupDigest(j.Configuration) != j.ConfigurationSHA256 || !setupHash(j.TargetSHA256) || !setupHash(j.PlanID) || j.Binding.OperationID != j.OperationID || j.Progress.PlanID != j.PlanID || !setupHash(j.Binding.CandidateSHA256) || !setupHash(j.Binding.PreconditionsSHA256) {
		return false
	}
	if _, err := ParseConfig(j.Configuration); err != nil {
		return false
	}
	switch j.State {
	case SetupPlanned, SetupAttempted, SetupVerified, SetupFailed, SetupReconciliation:
	default:
		return false
	}
	if j.State == SetupPlanned && len(j.Progress.Attempts) != 0 || j.State != SetupPlanned && len(j.Progress.Attempts) != 1 || j.Created && j.State != SetupVerified {
		return false
	}
	if len(j.Progress.Attempts) > 1 {
		return false
	}
	for _, a := range j.Progress.Attempts {
		if j.State == SetupVerified && a.State != installation.Verified || j.State != SetupVerified && a.State != installation.Attempted {
			return false
		}
		if a.RequirementID != "configuration-target" || a.State != installation.Attempted && a.State != installation.Verified {
			return false
		}
	}
	return true
}
func setupDigest(b []byte) string { s := sha256.Sum256(b); return hex.EncodeToString(s[:]) }
func setupHash(s string) bool {
	if len(s) != 64 {
		return false
	}
	_, err := hex.DecodeString(s)
	return err == nil && s == string(bytes.ToLower([]byte(s)))
}

var setupExecutable struct {
	sync.Once
	digest string
	err    error
}

func setupCandidate() (string, error) {
	setupExecutable.Do(func() {
		// /proc/self/exe pins the executing Linux image even if its pathname is replaced.
		f, e := os.Open("/proc/self/exe")
		if e != nil {
			setupExecutable.err = ErrSetupState
			return
		}
		defer f.Close()
		h := sha256.New()
		if _, e = io.Copy(h, f); e != nil {
			setupExecutable.err = ErrSetupState
			return
		}
		setupExecutable.digest = hex.EncodeToString(h.Sum(nil))
	})
	return setupExecutable.digest, setupExecutable.err
}

// AssessSetup performs read-only probes. It neither creates a journal nor binds
// a listener. Missing/unknown outcomes never produce partial executable actions.
func AssessSetup(ctx context.Context, request SetupRequest, lookup func(string) (string, bool)) (SetupResult, error) {
	return assessSetup(ctx, request, lookup, probeSetup)
}
func assessSetup(ctx context.Context, request SetupRequest, lookup func(string) (string, bool), probe setupProbe) (SetupResult, error) {
	result := setupResult(request.OperationID)
	d, c, err := prepareSetup(request)
	if err != nil {
		return setupError(result, err)
	}
	defer d.close()
	j, err := d.load(request.TargetPath)
	if err != nil {
		return setupError(result, err)
	}
	result, _, err = assessSetupOpen(ctx, d, c, request, lookup, probe, j)
	return result, err
}
func setupResult(id string) SetupResult {
	if !setupOperationSyntax.MatchString(id) {
		id = ""
	}
	return SetupResult{OperationID: id, Scope: "configuration-preparation", State: SetupPlanned, Code: "ok"}
}
func setupError(r SetupResult, err error) (SetupResult, error) {
	switch {
	case errors.Is(err, ErrSetupInput):
		r.Code = "invalid-input"
	case errors.Is(err, ErrSetupBusy):
		r.Code = "busy"
	case errors.Is(err, ErrSetupStale):
		r.Code = "stale-binding"
	default:
		err = ErrSetupState
		r.Code = "unsafe-state"
	}
	return r, err
}
func prepareSetup(r SetupRequest) (*setupDirectory, Config, error) {
	if !setupOperationSyntax.MatchString(r.OperationID) {
		return nil, Config{}, ErrSetupInput
	}
	c, err := ParseConfig(r.Configuration)
	if err != nil {
		return nil, Config{}, ErrSetupInput
	}
	d, err := openSetupDirectory(r.TargetPath)
	return d, c, err
}
func targetSetupDigest(target string, d *setupDirectory) string {
	var st unix.Stat_t
	if unix.Fstat(d.fd, &st) != nil {
		return ""
	}
	b, _ := json.Marshal(struct {
		Path   string
		Device uint64
		Inode  uint64
	}{target, uint64(st.Dev), st.Ino})
	return setupDigest(b)
}

type setupAssessment struct {
	plan         *installation.Plan
	profile      installation.Profile
	observations []installation.Observation
	support      []installation.Support
}

func assessSetupOpen(ctx context.Context, d *setupDirectory, c Config, r SetupRequest, lookup func(string) (string, bool), probe setupProbe, j *setupJournal) (SetupResult, *setupAssessment, error) {
	result := setupResult(r.OperationID)
	target := targetSetupDigest(r.TargetPath, d)
	candidate, err := setupCandidate()
	if err != nil {
		v, e := setupError(result, err)
		return v, nil, e
	}
	configHash := setupDigest(r.Configuration)
	if j != nil {
		result.State = j.State
		if j.OperationID != r.OperationID || j.TargetSHA256 != target || j.ConfigurationSHA256 != configHash || !bytes.Equal(j.Configuration, r.Configuration) || j.Binding.CandidateSHA256 != candidate {
			v, e := setupError(result, ErrSetupStale)
			return v, nil, e
		}
	}
	observations, databases := probe(ctx, c, lookup)
	actual, exists, readErr := d.read(filepath.Base(r.TargetPath), 1<<20)
	state := installation.Missing
	switch {
	case readErr != nil:
		state = installation.Unknown
	case exists && !bytes.Equal(actual, r.Configuration):
		state = installation.Unsupported
	case exists:
		state = installation.Satisfied
	}
	observations = append([]installation.Observation{{RequirementID: "configuration-target", State: state}}, observations...)
	profileID := c.HostingProfile
	if profileID == "" {
		profileID = HostingProfileDevelopment
	}
	preconditions, _ := json.Marshal(struct{ Config, Target, Databases string }{configHash, target, databases})
	profile := installation.Profile{Binding: installation.Binding{ProfileID: "rixa-config-" + strings.ReplaceAll(profileID, "_", "-"), OperationID: r.OperationID, CandidateSHA256: candidate, PreconditionsSHA256: setupDigest(preconditions)}, Requirements: []installation.Requirement{{ID: "configuration-target", Required: true}, {ID: "runtime-platform", Required: true}, {ID: "runtime-tls", Required: true}, {ID: "database-secrets", Required: true}, {ID: "database-targets", Required: true}, {ID: "private-storage", Required: true}}}
	support := []installation.Support{{RequirementID: "configuration-target", AdapterID: "rixa-private-config-create", Authorized: true}}
	progress := installation.Progress{}
	if j != nil {
		progress = j.Progress
	}
	var plan *installation.Plan
	if j == nil {
		plan, err = installation.Assess(profile, observations, support, progress)
	} else {
		original := profile
		original.Binding = j.Binding
		previous, priorErr := installation.Assess(original, observations, support, progress)
		if priorErr != nil || previous.ID() != j.PlanID {
			v, e := setupError(result, ErrSetupState)
			return v, nil, e
		}
		plan, err = previous.Recheck(profile, observations, support, progress)
	}
	if err != nil {
		v, e := setupError(result, ErrSetupStale)
		return v, nil, e
	}
	result.Plan = plan
	result.Guidance = setupGuidance(plan)
	if plan.Status() == installation.ReconciliationRequired {
		result.State = SetupReconciliation
		result.Code = "reconciliation-required"
	}
	if plan.Status() == installation.OperatorActionRequired {
		result.Code = "operator-action-required"
	}
	if plan.Status() == installation.UnsupportedPlan {
		result.Code = "unsupported"
	}
	return result, &setupAssessment{plan: plan, profile: profile, observations: observations, support: support}, nil
}

// ApplySetup records an attempt before the sole atomic no-overwrite effect and
// verifies exact bytes afterward. Re-entry never treats absence as non-application.
func ApplySetup(ctx context.Context, r SetupRequest, lookup func(string) (string, bool)) (SetupResult, error) {
	return applySetup(ctx, r, lookup, probeSetup, nil)
}
func applySetup(ctx context.Context, r SetupRequest, lookup func(string) (string, bool), probe setupProbe, afterEffect func() error) (SetupResult, error) {
	result := setupResult(r.OperationID)
	d, c, err := prepareSetup(r)
	if err != nil {
		return setupError(result, err)
	}
	defer d.close()
	unlock, err := d.lock()
	if err != nil {
		return setupError(result, err)
	}
	defer unlock()
	j, err := d.load(r.TargetPath)
	if err != nil {
		return setupError(result, err)
	}
	result, assessed, err := assessSetupOpen(ctx, d, c, r, lookup, probe, j)
	if err != nil {
		return result, err
	}
	// The probe is fresh under the target-wide lock. Retained plans have already
	// been Rechecked against these raw observations, not reconstructed outcomes.
	plan := assessed.plan
	if plan.Status() != installation.Ready && plan.Status() != installation.ChangesRequired {
		return result, nil
	}
	if ctx.Err() != nil {
		return setupError(result, ErrSetupState)
	}
	if j == nil {
		j = &setupJournal{Schema: setupJournalSchema, OperationID: r.OperationID, TargetSHA256: targetSetupDigest(r.TargetPath, d), Configuration: append([]byte(nil), r.Configuration...), ConfigurationSHA256: setupDigest(r.Configuration), Binding: plan.Binding(), PlanID: plan.ID(), State: SetupPlanned, Progress: installation.Progress{PlanID: plan.ID()}}
	}
	if len(plan.Actions()) == 0 {
		result.Reconciled = j.State == SetupAttempted || j.State == SetupFailed || j.State == SetupReconciliation
		j.State = SetupVerified
		j.Progress.Attempts = []installation.Attempt{{RequirementID: "configuration-target", State: installation.Verified}}
		if d.save(r.TargetPath, j) != nil {
			return setupError(result, ErrSetupState)
		}
		result.State = SetupVerified
		return result, nil
	}
	j.State = SetupAttempted
	j.Progress.Attempts = []installation.Attempt{{RequirementID: "configuration-target", State: installation.Attempted}}
	if d.save(r.TargetPath, j) != nil {
		return setupError(result, ErrSetupState)
	}
	result.State = SetupAttempted
	if err = d.createConfig(r.TargetPath, r.Configuration); err != nil {
		j.State = SetupFailed
		_ = d.save(r.TargetPath, j)
		result.State = SetupReconciliation
		result.Code = "reconciliation-required"
		return result, ErrSetupState
	}
	// An internal test seam models a process/acknowledgement loss after the actual
	// filesystem effect. It is not exposed to any CLI/web caller or adapter input.
	if afterEffect != nil {
		if afterEffect() != nil {
			return setupError(result, ErrSetupState)
		}
	}
	actual, exists, err := d.read(filepath.Base(r.TargetPath), 1<<20)
	if err != nil || !exists || !bytes.Equal(actual, r.Configuration) {
		j.State = SetupReconciliation
		_ = d.save(r.TargetPath, j)
		result.State = SetupReconciliation
		result.Code = "reconciliation-required"
		return result, ErrSetupState
	}
	j.State = SetupVerified
	j.Created = true
	j.Progress.Attempts[0].State = installation.Verified
	if d.save(r.TargetPath, j) != nil {
		result.State = SetupReconciliation
		return setupError(result, ErrSetupState)
	}
	result.State = SetupVerified
	assessed.observations[0].State = installation.Satisfied
	result.Plan, err = plan.Recheck(assessed.profile, assessed.observations, assessed.support, j.Progress)
	if err != nil {
		return setupError(result, ErrSetupStale)
	}
	result.Guidance = setupGuidance(result.Plan)
	return result, nil
}

// InspectSetup is passive: it never creates locks/journals or changes retained
// progress. ApplySetup performs any fresh probe/verification reconciliation.
func InspectSetup(targetPath, operationID string) (SetupResult, error) {
	result := setupResult(operationID)
	if !setupOperationSyntax.MatchString(operationID) {
		return setupError(result, ErrSetupInput)
	}
	d, err := openSetupDirectory(targetPath)
	if err != nil {
		return setupError(result, err)
	}
	defer d.close()
	j, err := d.load(targetPath)
	if err != nil {
		return setupError(result, err)
	}
	if j == nil {
		result.Code = "not-found"
		return result, nil
	}
	if j.OperationID != operationID || j.TargetSHA256 != targetSetupDigest(targetPath, d) {
		return setupError(result, ErrSetupStale)
	}
	result.State = j.State
	actual, exists, err := d.read(filepath.Base(targetPath), 1<<20)
	if err != nil || j.State == SetupVerified && (!exists || !bytes.Equal(actual, j.Configuration)) || j.State == SetupAttempted || j.State == SetupFailed || j.State == SetupReconciliation {
		result.State = SetupReconciliation
		result.Code = "reconciliation-required"
	}
	return result, nil
}

type setupProbe func(context.Context, Config, func(string) (string, bool)) ([]installation.Observation, string)

func probeSetup(parent context.Context, c Config, lookup func(string) (string, bool)) ([]installation.Observation, string) {
	if lookup == nil {
		lookup = os.LookupEnv
	}
	ctx, cancel := context.WithTimeout(parent, 15*time.Second)
	defer cancel()
	platform := installation.Satisfied
	if goruntime.GOOS != "linux" || goruntime.GOARCH != "amd64" {
		platform = installation.Unsupported
	}
	tlsState := installation.Satisfied
	if c.ValidateRuntimeFiles() != nil {
		tlsState = installation.Missing
	}
	storage := installation.Satisfied
	if validateDeclaredMediaRoots(c.Sites) != nil {
		storage = installation.Missing
	}
	for _, s := range c.Sites {
		if s.Disabled {
			continue
		}
		for _, path := range []string{s.MediaRoot, s.PublicRoot} {
			if path == "" {
				continue
			}
			var st unix.Stat_t
			if unix.Stat(path, &st) != nil || st.Uid != uint32(os.Geteuid()) || unix.Faccessat(unix.AT_FDCWD, path, unix.R_OK|unix.W_OK|unix.X_OK, unix.AT_EACCESS) != nil {
				storage = installation.Missing
			}
		}
	}
	secrets := installation.Satisfied
	refs := []string{c.Control.DatabaseEnv}
	for _, s := range c.Sites {
		if !s.Disabled {
			refs = append(refs, s.DatabaseEnv)
		}
	}
	database := installation.Satisfied
	identities := make([]string, 0, len(refs))
	seen := map[databaseTargetIdentity]bool{}
	for _, ref := range refs {
		dsn, ok := lookup(ref)
		if !ok || dsn == "" {
			secrets = installation.Missing
			database = installation.Unknown
			identities = append(identities, "unknown")
			continue
		}
		identity, state := probeSetupDatabase(ctx, dsn)
		if state == installation.Unsupported {
			database = installation.Unsupported
		} else if state != installation.Satisfied && database != installation.Unsupported {
			database = installation.Unknown
		}
		if state == installation.Satisfied {
			if seen[identity] {
				database = installation.Unsupported
			}
			seen[identity] = true
			identities = append(identities, identity.systemID+":"+identity.databaseOID)
		} else {
			identities = append(identities, "unknown")
		}
	}
	encoded, _ := json.Marshal(identities)
	return []installation.Observation{{RequirementID: "runtime-platform", State: platform}, {RequirementID: "runtime-tls", State: tlsState}, {RequirementID: "database-secrets", State: secrets}, {RequirementID: "database-targets", State: database}, {RequirementID: "private-storage", State: storage}}, setupDigest(encoded)
}
func probeSetupDatabase(parent context.Context, dsn string) (databaseTargetIdentity, installation.ObservedState) {
	cfg, err := safeDatabaseConnectionConfig(dsn)
	if err != nil {
		return databaseTargetIdentity{}, installation.Unsupported
	}
	ctx, cancel := context.WithTimeout(parent, databaseProbeTimeout)
	defer cancel()
	conn, err := pgx.ConnectConfig(ctx, cfg)
	if err != nil {
		return databaseTargetIdentity{}, installation.Unknown
	}
	defer func() {
		closeCtx, c := context.WithTimeout(context.WithoutCancel(parent), time.Second)
		defer c()
		_ = conn.Close(closeCtx)
	}()
	var identity databaseTargetIdentity
	var version int
	var encoding, fsync, fullPages, synchronous string
	err = conn.QueryRow(ctx, `SELECT control.system_identifier::text, database.oid::text, current_setting('server_version_num')::integer, current_setting('server_encoding'),current_setting('fsync'),current_setting('full_page_writes'),current_setting('synchronous_commit') FROM pg_control_system() AS control JOIN pg_database AS database ON database.datname=current_database()`).Scan(&identity.systemID, &identity.databaseOID, &version, &encoding, &fsync, &fullPages, &synchronous)
	if err != nil || identity.systemID == "" || identity.databaseOID == "" {
		return databaseTargetIdentity{}, installation.Unknown
	}
	if version < 180000 || version >= 190000 || encoding != "UTF8" || fsync != "on" || fullPages != "on" || synchronous != "on" {
		return databaseTargetIdentity{}, installation.Unsupported
	}
	return identity, installation.Satisfied
}
func setupGuidance(plan *installation.Plan) map[string]string {
	hints := map[string]string{"configuration-target": "Use the same operation and exact candidate; inspect an existing or uncertain target before continuing.", "runtime-platform": "Use the validated Linux amd64 executable.", "runtime-tls": "Provide the selected profile's matching valid certificate, protected key and configured administrator references.", "database-secrets": "Provide each configured database environment reference through protected operator input.", "database-targets": "Provide separate reachable PostgreSQL 18 UTF8 targets with durable commit settings; ask the provider for missing capabilities.", "private-storage": "Provide separate existing canonical private storage roots owned and accessible by the runtime user."}
	out := map[string]string{}
	for _, o := range plan.Outcomes() {
		if o.State != installation.Available {
			out[o.RequirementID] = hints[o.RequirementID]
		}
	}
	return out
}
