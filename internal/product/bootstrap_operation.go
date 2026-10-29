// SPDX-License-Identifier: MPL-2.0

package product

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"path/filepath"
	"regexp"
	"time"
	"unicode/utf8"

	"github.com/AChWorks/achrix"
	"github.com/AChWorks/achrix/audit"
	"github.com/AChWorks/achrix/identity"
	"golang.org/x/sys/unix"
)

var (
	ErrBootstrapOperationInput = errors.New("invalid bootstrap operation input or private state directory")
	ErrBootstrapOperationState = errors.New("bootstrap operation state is unsafe or unavailable")
	ErrBootstrapOperationStale = errors.New("bootstrap operation binding changed")
	ErrBootstrapReconciliation = errors.New("bootstrap requires inspection and reconciliation")
	bootstrapLoginSyntax       = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,63}$`)
)

type BootstrapOperationState string

const (
	BootstrapPlanned        BootstrapOperationState = "planned"
	BootstrapAttempted      BootstrapOperationState = "attempted"
	BootstrapVerified       BootstrapOperationState = "verified"
	BootstrapConflict       BootstrapOperationState = "conflict"
	BootstrapReconciliation BootstrapOperationState = "reconciliation-required"
)

// BootstrapOperationRequest binds one initial account to one private, immutable
// draft and physical database. The caller must keep initial account creation
// quiesced. Password is transient input: never serialized into the journal.
type BootstrapOperationRequest struct {
	OperationID   string
	StatePath     string
	Configuration []byte
	Scope         string
	Login         string
	Password      string
}

// Default diagnostic formatting must not expose protected request input.
func (BootstrapOperationRequest) String() string         { return "BootstrapOperationRequest[private]" }
func (r BootstrapOperationRequest) GoString() string     { return r.String() }
func (r BootstrapOperationRequest) LogValue() slog.Value { return slog.StringValue(r.String()) }

// BootstrapOperationResult contains no password, session, DSN, draft or path.
// A verified result identifies the exact retained account, not a login adopted
// from an unrelated pre-existing account.
type BootstrapOperationResult struct {
	OperationID string                  `json:"operation_id"`
	Scope       string                  `json:"scope"`
	State       BootstrapOperationState `json:"state"`
	Code        string                  `json:"code"`
	Principal   string                  `json:"principal,omitempty"`
	Created     bool                    `json:"created,omitempty"`
	Reconciled  bool                    `json:"reconciled,omitempty"`
	Guidance    string                  `json:"guidance,omitempty"`
}

type bootstrapOperationJournal struct {
	Schema              string
	OperationID         string
	Scope               string
	Login               string
	ConfigurationSHA256 string
	TargetSHA256        string
	CandidateSHA256     string
	DatabaseIdentity    string
	State               BootstrapOperationState
	BaselineAbsent      bool
	ProofAttempted      bool
	Principal           string
	AttemptedAt         time.Time
}

func (j *bootstrapOperationJournal) valid() bool {
	if j.Schema != "rixa.bootstrap-operation.v1" || !setupOperationSyntax.MatchString(j.OperationID) ||
		!bootstrapLoginSyntax.MatchString(j.Login) || !setupHash(j.ConfigurationSHA256) ||
		!setupHash(j.TargetSHA256) || !setupHash(j.CandidateSHA256) ||
		!setupDatabaseSyntax.MatchString(j.DatabaseIdentity) || !validBootstrapScope(j.Scope) {
		return false
	}
	switch j.State {
	case BootstrapAttempted:
		return j.BaselineAbsent && j.Principal == "" && !j.AttemptedAt.IsZero()
	case BootstrapVerified:
		return j.BaselineAbsent && !j.ProofAttempted && accountIDSyntax.MatchString(j.Principal) && !j.AttemptedAt.IsZero()
	case BootstrapConflict:
		return j.Principal == "" && !j.ProofAttempted
	default:
		return false
	}
}

func validBootstrapScope(scope string) bool {
	if scope == "control" {
		return true
	}
	const prefix = "site:"
	return len(scope) > len(prefix) && scope[:len(prefix)] == prefix && siteIDSyntax.MatchString(scope[len(prefix):])
}

type bootstrapOperationAccounts interface {
	bootstrapAccounts
	Login(context.Context, string, string, string) (identity.Session, error)
	Logout(context.Context, string) error
}

type bootstrapOperationFactory func(context.Context, Config, string, string, *slog.Logger) (bootstrapOperationAccounts, func() error, error)
type bootstrapOperationProbe func(context.Context, Config, string, func(string) (string, bool)) (string, string, error)

// ApplyBootstrapOperation never repeats an uncertain account creation. A later
// process can reconcile a bound attempted account only by exact lookup plus
// successful intended-password authentication and temporary-session logout.
// An uncertain authentication/logout attempt is retained and is not replayed.
func ApplyBootstrapOperation(ctx context.Context, request BootstrapOperationRequest, lookup func(string) (string, bool), logger *slog.Logger) (BootstrapOperationResult, error) {
	return bootstrapOperation(ctx, request, lookup, logger, true, probeBootstrapOperation, newBootstrapOperationAccounts)
}

// InspectBootstrapOperation observes retained state and the bound account. It
// does not create accounts, issue sessions, change the journal or reset attempts.
// Password is ignored. Apply needs it only for a fresh/uncertain creation.
func InspectBootstrapOperation(ctx context.Context, request BootstrapOperationRequest, lookup func(string) (string, bool), logger *slog.Logger) (BootstrapOperationResult, error) {
	return bootstrapOperation(ctx, request, lookup, logger, false, probeBootstrapOperation, newBootstrapOperationAccounts)
}

func bootstrapOperation(ctx context.Context, r BootstrapOperationRequest, lookup func(string) (string, bool), logger *slog.Logger, apply bool, probe bootstrapOperationProbe, factory bootstrapOperationFactory) (result BootstrapOperationResult, err error) {
	result = BootstrapOperationResult{State: BootstrapPlanned, Code: "ok"}
	if !setupOperationSyntax.MatchString(r.OperationID) || !validBootstrapScope(r.Scope) || !bootstrapLoginSyntax.MatchString(r.Login) {
		return bootstrapOperationFailure(result, "invalid-input", ErrBootstrapOperationInput)
	}
	result.OperationID, result.Scope = r.OperationID, r.Scope
	c, parseErr := ParseConfig(r.Configuration)
	if parseErr != nil || !bootstrapScopeDeclared(c, r.Scope) || len(r.Password) > 1024 || !utf8.ValidString(r.Password) || utf8.RuneCountInString(r.Password) > 256 {
		return bootstrapOperationFailure(result, "invalid-input", ErrBootstrapOperationInput)
	}
	d, openErr := openSetupDirectory(r.StatePath)
	if openErr != nil {
		return bootstrapOperationFailure(result, "invalid-private-state", ErrBootstrapOperationInput)
	}
	defer d.close()
	unlock, lockErr := d.lock()
	if lockErr != nil {
		if errors.Is(lockErr, ErrSetupBusy) {
			return bootstrapOperationFailure(result, "busy", ErrSetupBusy)
		}
		return bootstrapOperationFailure(result, "unsafe-state", ErrBootstrapOperationState)
	}
	defer unlock()
	j, loadErr := loadBootstrapOperation(d, r.StatePath)
	if loadErr != nil {
		return bootstrapOperationFailure(result, "unsafe-state", ErrBootstrapOperationState)
	}
	candidate, candidateErr := setupCandidate()
	if candidateErr != nil {
		return bootstrapOperationFailure(result, "unsafe-state", ErrBootstrapOperationState)
	}
	target := targetSetupDigest(r.StatePath, d)
	if j != nil {
		result.State = j.State
		if j.OperationID != r.OperationID || j.Scope != r.Scope || j.Login != r.Login ||
			j.ConfigurationSHA256 != setupDigest(r.Configuration) || j.TargetSHA256 != target || j.CandidateSHA256 != candidate {
			return bootstrapOperationFailure(result, "stale-binding", ErrBootstrapOperationStale)
		}
	}
	if j == nil && !apply {
		return result, nil
	}
	if j == nil && r.Password == "" {
		return bootstrapOperationFailure(result, "password-required", ErrBootstrapOperationInput)
	}
	if j == nil && utf8.RuneCountInString(r.Password) < 15 {
		return bootstrapOperationFailure(result, "password-invalid", ErrBootstrapOperationInput)
	}
	dsn, physical, probeErr := probe(ctx, c, r.Scope, lookup)
	if probeErr != nil {
		return bootstrapOperationFailure(result, "observation-unavailable", ErrUnavailable)
	}
	if j != nil && j.DatabaseIdentity != physical {
		return bootstrapOperationFailure(result, "stale-database", ErrBootstrapOperationStale)
	}
	accounts, closeAccounts, startErr := factory(ctx, c, dsn, r.Login, logger)
	if startErr != nil {
		return bootstrapOperationFailure(result, "observation-unavailable", ErrUnavailable)
	}
	defer func() {
		if closeAccounts() != nil {
			result.Code = "shutdown-unavailable"
			result.Guidance = "Inspect this exact operation after the bootstrap runtime shuts down."
			err = errors.Join(err, ErrUnavailable)
		}
	}()
	if j != nil && j.State == BootstrapConflict {
		return bootstrapOperationFailure(result, "account-conflict", identity.ErrConflict)
	}
	account, lookupErr := accounts.LookupAccount(ctx, bootstrapPrincipal, r.Login)
	if j != nil && j.State == BootstrapVerified {
		if lookupErr != nil {
			if errors.Is(lookupErr, identity.ErrNotFound) {
				result.State = BootstrapReconciliation
				return bootstrapOperationFailure(result, "verified-account-missing", ErrBootstrapReconciliation)
			}
			return bootstrapOperationFailure(result, "observation-unavailable", ErrUnavailable)
		}
		if !validBootstrapAccount(account, r.Login) || account.ID != j.Principal {
			result.State = BootstrapReconciliation
			return bootstrapOperationFailure(result, "verified-account-drift", ErrBootstrapReconciliation)
		}
		result.Principal, result.Reconciled = j.Principal, true
		return result, nil
	}
	if j != nil {
		result.State = BootstrapReconciliation
		if j.ProofAttempted {
			return bootstrapOperationFailure(result, "authentication-attempt-unknown", ErrBootstrapReconciliation)
		}
		if lookupErr != nil {
			if errors.Is(lookupErr, identity.ErrNotFound) {
				return bootstrapOperationFailure(result, "attempted-account-absent", ErrBootstrapReconciliation)
			}
			return bootstrapOperationFailure(result, "observation-unavailable", ErrUnavailable)
		}
		if !apply {
			return bootstrapOperationFailure(result, "account-needs-authentication", ErrBootstrapReconciliation)
		}
		return reconcileBootstrapOperation(ctx, d, r, j, account, accounts, result)
	}
	j = &bootstrapOperationJournal{Schema: "rixa.bootstrap-operation.v1", OperationID: r.OperationID, Scope: r.Scope, Login: r.Login,
		ConfigurationSHA256: setupDigest(r.Configuration), TargetSHA256: target, CandidateSHA256: candidate, DatabaseIdentity: physical}
	if lookupErr == nil {
		j.State = BootstrapConflict
		if saveBootstrapOperation(d, r.StatePath, j) != nil {
			return bootstrapOperationFailure(result, "unsafe-state", ErrBootstrapOperationState)
		}
		result.State = BootstrapConflict
		return bootstrapOperationFailure(result, "account-conflict", identity.ErrConflict)
	}
	if !errors.Is(lookupErr, identity.ErrNotFound) {
		return bootstrapOperationFailure(result, "observation-unavailable", ErrUnavailable)
	}
	j.State, j.BaselineAbsent, j.AttemptedAt = BootstrapAttempted, true, time.Now().UTC()
	if saveBootstrapOperation(d, r.StatePath, j) != nil {
		return bootstrapOperationFailure(result, "unsafe-state", ErrBootstrapOperationState)
	}
	result.State = BootstrapAttempted
	account, createErr := accounts.CreateAccount(ctx, bootstrapPrincipal, r.Login, r.Password)
	if createErr == nil {
		if !validBootstrapAccount(account, r.Login) {
			return bootstrapOperationFailure(result, "invalid-account-result", ErrBootstrapReconciliation)
		}
		return verifyBootstrapOperation(d, r.StatePath, j, account, result, true)
	}
	if errors.Is(createErr, identity.ErrConflict) || errors.Is(createErr, identity.ErrInvalid) || errors.Is(createErr, achrix.ErrDenied) {
		j.State = BootstrapConflict
		if saveBootstrapOperation(d, r.StatePath, j) != nil {
			return bootstrapOperationFailure(result, "unsafe-state", ErrBootstrapOperationState)
		}
		result.State = BootstrapConflict
		return bootstrapOperationFailure(result, "account-conflict", identity.ErrConflict)
	}
	// The attempted effect is deliberately retained even when current lookup is
	// absent: a disconnected backend may still complete its original write.
	result.State = BootstrapReconciliation
	account, lookupErr = accounts.LookupAccount(context.WithoutCancel(ctx), bootstrapPrincipal, r.Login)
	if lookupErr != nil {
		return bootstrapOperationFailure(result, "create-outcome-unknown", ErrBootstrapReconciliation)
	}
	return reconcileBootstrapOperation(ctx, d, r, j, account, accounts, result)
}

func reconcileBootstrapOperation(ctx context.Context, d *setupDirectory, r BootstrapOperationRequest, j *bootstrapOperationJournal, account identity.Account, accounts bootstrapOperationAccounts, result BootstrapOperationResult) (BootstrapOperationResult, error) {
	result.State = BootstrapReconciliation
	if ctx.Err() != nil {
		return bootstrapOperationFailure(result, "account-needs-authentication", ErrBootstrapReconciliation)
	}
	if !validBootstrapAccount(account, r.Login) {
		return bootstrapOperationFailure(result, "account-proof-failed", ErrBootstrapReconciliation)
	}
	if r.Password == "" {
		return bootstrapOperationFailure(result, "password-required", ErrBootstrapReconciliation)
	}
	j.ProofAttempted = true
	if saveBootstrapOperation(d, r.StatePath, j) != nil {
		return bootstrapOperationFailure(result, "unsafe-state", ErrBootstrapOperationState)
	}
	session, authErr := accounts.Login(ctx, r.Login, r.Password, "")
	if authErr != nil {
		if errors.Is(authErr, identity.ErrAuthentication) || errors.Is(authErr, identity.ErrInvalid) || errors.Is(authErr, achrix.ErrDenied) {
			// These errors precede session creation in the public identity contract.
			j.ProofAttempted = false
			if saveBootstrapOperation(d, r.StatePath, j) != nil {
				return bootstrapOperationFailure(result, "unsafe-state", ErrBootstrapOperationState)
			}
			return bootstrapOperationFailure(result, "account-proof-failed", ErrBootstrapReconciliation)
		}
		return bootstrapOperationFailure(result, "authentication-attempt-unknown", ErrBootstrapReconciliation)
	}
	cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Second)
	cleanupErr := accounts.Logout(cleanupCtx, session.Token)
	cancel()
	session.Token, session.CSRF = "", ""
	if cleanupErr != nil {
		return bootstrapOperationFailure(result, "authentication-attempt-unknown", ErrBootstrapReconciliation)
	}
	j.ProofAttempted = false
	if session.Principal != achrix.Principal(account.ID) {
		if saveBootstrapOperation(d, r.StatePath, j) != nil {
			return bootstrapOperationFailure(result, "unsafe-state", ErrBootstrapOperationState)
		}
		return bootstrapOperationFailure(result, "account-proof-failed", ErrBootstrapReconciliation)
	}
	return verifyBootstrapOperation(d, r.StatePath, j, account, result, false)
}

func verifyBootstrapOperation(d *setupDirectory, path string, j *bootstrapOperationJournal, account identity.Account, result BootstrapOperationResult, created bool) (BootstrapOperationResult, error) {
	j.State, j.Principal, j.ProofAttempted = BootstrapVerified, account.ID, false
	if saveBootstrapOperation(d, path, j) != nil {
		result.State = BootstrapReconciliation
		return bootstrapOperationFailure(result, "receipt-write-unknown", ErrBootstrapOperationState)
	}
	result.State, result.Code, result.Principal, result.Created, result.Reconciled = BootstrapVerified, "ok", account.ID, created, !created
	result.Guidance = ""
	return result, nil
}

func bootstrapScopeDeclared(c Config, scope string) bool {
	if scope == "control" {
		return true
	}
	for _, site := range c.Sites {
		if !site.Disabled && scope == "site:"+site.ID {
			return true
		}
	}
	return false
}

func validBootstrapAccount(a identity.Account, login string) bool {
	return a.Login == login && accountIDSyntax.MatchString(a.ID) && a.Enabled && a.Revision >= 1
}

func bootstrapOperationFailure(r BootstrapOperationResult, code string, err error) (BootstrapOperationResult, error) {
	r.Code = code
	switch code {
	case "password-required":
		r.Guidance = "Provide the intended initial password through the protected secret input; do not change the bound login or operation."
	case "password-invalid":
		r.Guidance = "Use an initial password of 15 to 256 Unicode characters, at most 1024 UTF-8 bytes; its contents are not normalized."
	case "account-conflict":
		r.Guidance = "An existing or conflicting account cannot be adopted by a fresh bootstrap operation."
	case "authentication-attempt-unknown":
		r.Guidance = "Do not repeat authentication or account creation. Inspect the retained operation and original executor; temporary bootstrap sessions expire after one minute."
	case "stale-binding", "stale-database":
		r.Guidance = "Restore the exact approved draft, executing artifact, private state directory and physical database; do not replace or edit retained operation state."
	case "observation-unavailable":
		r.Guidance = "Restore database availability and inspect this exact operation; unavailable evidence is not a changed target or a failed creation."
	case "busy":
		r.Guidance = "Another operation owns this installation lock. Inspect it and wait for its verified completion."
	default:
		r.Guidance = "Inspect this exact retained operation and its account before continuing; do not create another operation to force a retry."
	}
	return r, err
}

func loadBootstrapOperation(d *setupDirectory, path string) (*bootstrapOperationJournal, error) {
	data, exists, err := d.read(filepath.Base(path), 64<<10)
	if err != nil || !exists {
		return nil, err
	}
	var j bootstrapOperationJournal
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if dec.Decode(&j) != nil || dec.Decode(new(any)) != io.EOF || !j.valid() {
		return nil, ErrBootstrapOperationState
	}
	return &j, nil
}
func saveBootstrapOperation(d *setupDirectory, path string, j *bootstrapOperationJournal) error {
	if !j.valid() {
		return ErrBootstrapOperationState
	}
	if _, _, err := d.read(filepath.Base(path), 64<<10); err != nil {
		return ErrBootstrapOperationState
	}
	data, err := json.Marshal(j)
	if err != nil {
		return ErrBootstrapOperationState
	}
	name, err := d.temporary(data)
	if err != nil {
		return ErrBootstrapOperationState
	}
	defer unix.Unlinkat(d.fd, name, 0)
	if unix.Renameat(d.fd, name, d.fd, filepath.Base(path)) != nil || unix.Fsync(d.fd) != nil {
		return ErrBootstrapOperationState
	}
	return nil
}

func probeBootstrapOperation(ctx context.Context, c Config, scope string, lookup func(string) (string, bool)) (string, string, error) {
	if err := validateDeclaredMediaRoots(c.Sites); err != nil {
		return "", "", ErrConfiguration
	}
	if _, err := c.DatabaseTargets(ctx, lookup); err != nil {
		return "", "", ErrUnavailable
	}
	target, err := c.BootstrapDatabase(scope, lookup)
	if err != nil {
		return "", "", ErrConfiguration
	}
	id, err := inspectDatabaseTarget(ctx, target.DSN)
	if err != nil {
		return "", "", ErrUnavailable
	}
	return target.DSN, id.systemID + ":" + id.databaseOID, nil
}

func newBootstrapOperationAccounts(ctx context.Context, c Config, dsn, login string, logger *slog.Logger) (bootstrapOperationAccounts, func() error, error) {
	if logger == nil {
		logger = slog.Default()
	}
	auditModule, err := audit.NewPostgres(dsn, audit.Config{MaxConns: c.Resources.Audit.MaxConns, MaxOperations: c.Resources.Audit.MaxOperations}, logger)
	if err != nil {
		return nil, nil, err
	}
	module, err := identity.NewPostgres(dsn, identity.Config{MaxConns: c.Resources.Identity.MaxConns, MaxOperations: c.Resources.Identity.MaxOperations, SessionLifetime: time.Minute}, logger)
	if err != nil {
		return nil, nil, err
	}
	base := bootstrapPolicy(login)
	policy := achrix.PolicyFunc(func(ctx context.Context, p achrix.Principal, capability, resource string) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if p == identity.PublicPrincipal && capability == identity.Authentication && resource == "" {
			return nil
		}
		return base.Authorize(ctx, p, capability, resource)
	})
	app, err := achrix.New(achrix.Config{StartupTimeout: c.StartupTimeout, ShutdownTimeout: c.ShutdownTimeout, Logger: logger}, policy, auditModule, module)
	if err != nil {
		return nil, nil, err
	}
	accountability, err := audit.NewService(app, auditModule)
	if err != nil {
		return nil, nil, err
	}
	service, err := identity.NewService(app, module, accountability)
	if err != nil {
		return nil, nil, err
	}
	closeApp := func() error {
		stop, cancel := context.WithTimeout(context.WithoutCancel(ctx), c.ShutdownTimeout)
		defer cancel()
		return app.Shutdown(stop)
	}
	start, cancel := context.WithTimeout(ctx, c.StartupTimeout)
	defer cancel()
	if err = app.Start(start); err == nil {
		err = app.Ready(start)
	}
	if err != nil {
		_ = closeApp()
		return nil, nil, err
	}
	return service, closeApp, nil
}
