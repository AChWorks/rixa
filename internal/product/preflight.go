// SPDX-License-Identifier: MPL-2.0

package product

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

const databaseProbeTimeout = 3 * time.Second

type databaseTargetIdentity struct {
	systemID    string
	databaseOID string
}

type mediaRootIdentity struct {
	siteID    string
	canonical string
	info      os.FileInfo
}

type databaseConnector func(context.Context, *pgx.ConnConfig) (*pgx.Conn, error)

func validateRuntimeTargets(parent context.Context, runtime *RuntimeConfig) error {
	if runtime == nil {
		return ErrConfiguration
	}
	targets := make([]ResolvedDatabase, 0, len(runtime.Sites)+1)
	targets = append(targets, ResolvedDatabase{Kind: DatabaseControl, ID: "control", DSN: runtime.Control.DSN})
	for _, site := range runtime.Sites {
		if site.Disabled {
			continue
		}
		targets = append(targets, ResolvedDatabase{Kind: DatabaseSite, ID: site.ID, DSN: site.DSN})
	}
	if err := validateDatabaseTargets(parent, targets); err != nil {
		return err
	}
	return validateMediaRoots(runtime.Sites)
}

func validateDatabaseTargets(parent context.Context, targets []ResolvedDatabase) error {
	seen := make(map[databaseTargetIdentity]string, len(targets))
	for _, target := range targets {
		id, err := inspectDatabaseTarget(parent, target.DSN)
		if err != nil {
			return fmt.Errorf("database target %s: %w", target.ID, err)
		}
		if previous, duplicate := seen[id]; duplicate {
			return fmt.Errorf("%w: database targets %s and %s resolve to the same PostgreSQL database", ErrConfiguration, previous, target.ID)
		}
		seen[id] = target.ID
	}
	return nil
}

func inspectDatabaseTarget(parent context.Context, dsn string) (databaseTargetIdentity, error) {
	return inspectDatabaseTargetWithConnector(parent, dsn, pgx.ConnectConfig)
}

func inspectDatabaseTargetWithConnector(parent context.Context, dsn string, connect databaseConnector) (identity databaseTargetIdentity, err error) {
	cfg, configErr := safeDatabaseConnectionConfig(dsn)
	if configErr != nil {
		return identity, configErr
	}
	if connect == nil {
		return identity, ErrConfiguration
	}

	ctx, cancel := context.WithTimeout(parent, databaseProbeTimeout)
	defer cancel()

	conn, connectErr := connect(ctx, cfg)
	if connectErr != nil || conn == nil {
		return identity, safePreflightError(ctx, connectErr)
	}
	defer func() {
		closeCtx, closeCancel := context.WithTimeout(context.WithoutCancel(parent), time.Second)
		defer closeCancel()
		if closeErr := conn.Close(closeCtx); closeErr != nil && err == nil {
			// A close failure must never make a permanent probe rejection retryable.
			err = ErrUnavailable
		}
	}()

	queryErr := conn.QueryRow(ctx, `
		SELECT control.system_identifier::text, database.oid::text
		FROM pg_control_system() AS control
		JOIN pg_database AS database ON database.datname = current_database()
	`).Scan(&identity.systemID, &identity.databaseOID)
	if queryErr != nil {
		return databaseTargetIdentity{}, safePreflightError(ctx, queryErr)
	}
	if identity.systemID == "" || identity.databaseOID == "" {
		return databaseTargetIdentity{}, fmt.Errorf("%w: incomplete physical database identity", ErrDatabaseAdmission)
	}
	return identity, nil
}

// safeDatabaseConnectionConfig mirrors the released AChrix v0.3.0 PostgreSQL
// admission boundary before Rixa performs its additional physical-identity probe.
// The probe must never be the first operation to discover an unsafe remote DSN.
func safeDatabaseConnectionConfig(dsn string) (*pgx.ConnConfig, error) {
	if len(dsn) == 0 || len(dsn) > 4096 {
		return nil, ErrConfiguration
	}
	poolConfig, err := pgxpool.ParseConfig(dsn)
	if err != nil || poolConfig == nil || poolConfig.ConnConfig == nil {
		return nil, ErrConfiguration
	}
	cfg := poolConfig.ConnConfig
	if cfg.Database == "" || cfg.User == "" || !safeDatabaseHostTLS(cfg.Host, cfg.TLSConfig) {
		return nil, ErrConfiguration
	}
	for _, fallback := range cfg.Fallbacks {
		if fallback == nil || !safeDatabaseHostTLS(fallback.Host, fallback.TLSConfig) {
			return nil, ErrConfiguration
		}
	}
	// Keep the extra identity probe no more permissive than the bounded AChrix
	// database setup path.
	cfg.ConnectTimeout = time.Second
	return cfg, nil
}

func safeDatabaseHostTLS(host string, tlsConfig *tls.Config) bool {
	ip := net.ParseIP(host)
	local := strings.HasPrefix(host, "/") || host == "localhost" || ip != nil && ip.IsLoopback()
	return local || tlsConfig != nil && !tlsConfig.InsecureSkipVerify && tlsConfig.ServerName != ""
}

// safePreflightError classifies ONLY read-only PostgreSQL identity probes.
// Driver errors (notably pgconn.ConnectError) can embed DSNs, usernames and
// passwords. Never wrap or format the underlying error in operator diagnostics.
// Unknown admission failures fail closed and do not trigger startup retries.
type databaseProbeOutcome uint8

const (
	databaseProbeUnavailable databaseProbeOutcome = iota
	databaseProbeCanceled
	databaseProbeAdmissionRejected
)

func safePreflightError(ctx context.Context, cause error) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if cause == nil {
		return fmt.Errorf("%w: missing database connection", ErrDatabaseAdmission)
	}
	// Retry only when every effective cause is transient. Cancellation stops
	// without retries, but must not hide a joined permanent admission denial.
	outcome, rejection := classifyDatabaseProbeCause(cause, 0)
	switch outcome {
	case databaseProbeUnavailable:
		return ErrUnavailable
	case databaseProbeCanceled:
		return context.Canceled
	default:
		return fmt.Errorf("%w: %s", ErrDatabaseAdmission, rejection)
	}
}

const maxDatabaseProbeErrorDepth = 32

// classifyDatabaseProbeCause never formats driver errors; those may contain
// credentials and DSNs. Unknown causes fail closed, including joined failures.
func classifyDatabaseProbeCause(cause error, depth int) (databaseProbeOutcome, string) {
	const unknown = "PostgreSQL connection or identity probe rejected"
	if cause == nil || depth >= maxDatabaseProbeErrorDepth {
		return databaseProbeAdmissionRejected, unknown
	}
	switch e := cause.(type) {
	case *pgconn.PgError:
		switch {
		case strings.HasPrefix(e.Code, "28"):
			return databaseProbeAdmissionRejected, "PostgreSQL authentication rejected"
		case e.Code == "42501":
			return databaseProbeAdmissionRejected, "PostgreSQL identity probe permission denied"
		case strings.HasPrefix(e.Code, "08"),
			e.Code == "57P03", // cannot_connect_now
			e.Code == "57P01", // admin_shutdown
			e.Code == "57P02", // crash_shutdown
			e.Code == "53300": // too_many_connections
			return databaseProbeUnavailable, ""
		default:
			return databaseProbeAdmissionRejected, "PostgreSQL rejected database identity probe"
		}
	case *tls.CertificateVerificationError, x509.UnknownAuthorityError,
		*x509.UnknownAuthorityError, x509.HostnameError, *x509.HostnameError,
		x509.CertificateInvalidError, *x509.CertificateInvalidError,
		x509.SystemRootsError, *x509.SystemRootsError,
		tls.RecordHeaderError, *tls.RecordHeaderError:
		return databaseProbeAdmissionRejected, "PostgreSQL TLS verification or protocol rejected"
	case *net.DNSError:
		if e.IsNotFound {
			return databaseProbeAdmissionRejected, "PostgreSQL host resolution rejected"
		}
		if e.IsTimeout || e.IsTemporary {
			return databaseProbeUnavailable, ""
		}
		return databaseProbeAdmissionRejected, "PostgreSQL host resolution rejected"
	case *net.OpError:
		outcome, rejection := classifyDatabaseProbeCause(e.Err, depth+1)
		if outcome == databaseProbeAdmissionRejected && rejection == unknown {
			return outcome, "PostgreSQL network operation rejected"
		}
		return outcome, rejection
	}
	// errors.Is/As matches any branch of errors.Join and cannot establish
	// recoverability. Permanent rejection outranks cancellation, which in
	// turn outranks transient availability.
	if many, ok := cause.(interface{ Unwrap() []error }); ok {
		children := many.Unwrap()
		if len(children) == 0 {
			return databaseProbeAdmissionRejected, unknown
		}
		firstRejection := ""
		hasCancellation := false
		for _, child := range children {
			outcome, category := classifyDatabaseProbeCause(child, depth+1)
			if outcome == databaseProbeAdmissionRejected && firstRejection == "" {
				firstRejection = category
			}
			if outcome == databaseProbeCanceled {
				hasCancellation = true
			}
		}
		if firstRejection != "" {
			return databaseProbeAdmissionRejected, firstRejection
		}
		if hasCancellation {
			return databaseProbeCanceled, ""
		}
		return databaseProbeUnavailable, ""
	}
	if single, ok := cause.(interface{ Unwrap() error }); ok {
		if inner := single.Unwrap(); inner != nil {
			return classifyDatabaseProbeCause(inner, depth+1)
		}
	}
	// pgconn.Timeout exposes its underlying context/net timeout via Unwrap.
	// pgconn.SafeToRetry only means no data was sent, not recoverability.
	if errors.Is(cause, context.Canceled) {
		return databaseProbeCanceled, ""
	}
	if errors.Is(cause, syscall.EACCES) || errors.Is(cause, syscall.EPERM) {
		return databaseProbeAdmissionRejected, "PostgreSQL network operation rejected"
	}
	if errors.Is(cause, context.DeadlineExceeded) {
		return databaseProbeUnavailable, ""
	}
	var netErr net.Error
	if errors.As(cause, &netErr) && netErr.Timeout() {
		return databaseProbeUnavailable, ""
	}
	if transientDatabaseTransportError(cause) {
		return databaseProbeUnavailable, ""
	}
	return databaseProbeAdmissionRejected, unknown
}

func transientDatabaseTransportError(err error) bool {
	return errors.Is(err, syscall.ECONNREFUSED) || errors.Is(err, syscall.ECONNRESET) ||
		errors.Is(err, syscall.ECONNABORTED) || errors.Is(err, syscall.ETIMEDOUT) ||
		errors.Is(err, syscall.EHOSTUNREACH) || errors.Is(err, syscall.ENETUNREACH) ||
		errors.Is(err, syscall.ENETDOWN) || errors.Is(err, io.EOF) ||
		errors.Is(err, io.ErrUnexpectedEOF) || errors.Is(err, pgconn.ErrConnClosed)
}

func validateDeclaredMediaRoots(sites []SiteConfig) error {
	resolved := make([]ResolvedSite, 0, len(sites))
	for _, site := range sites {
		resolved = append(resolved, ResolvedSite{SiteConfig: site})
	}
	return validateMediaRoots(resolved)
}

func validateMediaRoots(sites []ResolvedSite) error {
	roots := make([]mediaRootIdentity, 0, len(sites)*2)
	add := func(siteID, kind, root string) error {
		current, err := inspectMediaRoot(siteID+":"+kind, root)
		if err != nil {
			return fmt.Errorf("%w: %s root for %s", ErrConfiguration, kind, siteID)
		}
		for _, existing := range roots {
			if os.SameFile(existing.info, current.info) ||
				pathsOverlap(existing.canonical, current.canonical) {
				return fmt.Errorf("%w: private roots %s and %s are not exclusive", ErrConfiguration, existing.siteID, current.siteID)
			}
		}
		roots = append(roots, current)
		return nil
	}
	for _, site := range sites {
		if site.Disabled {
			continue
		}
		if err := add(site.ID, "media", site.MediaRoot); err != nil {
			return err
		}
		if site.PublicRoot != "" {
			if err := add(site.ID, "public", site.PublicRoot); err != nil {
				return err
			}
		}
	}
	return nil
}

func inspectMediaRoot(siteID, path string) (mediaRootIdentity, error) {
	if path == "" || !filepath.IsAbs(path) || filepath.Clean(path) != path || path == string(filepath.Separator) {
		return mediaRootIdentity{}, ErrConfiguration
	}
	canonical, err := filepath.EvalSymlinks(path)
	if err != nil || canonical != path {
		return mediaRootIdentity{}, ErrConfiguration
	}
	info, err := os.Stat(canonical)
	if err != nil || !info.IsDir() || info.Mode().Perm() != 0o700 {
		return mediaRootIdentity{}, ErrConfiguration
	}
	return mediaRootIdentity{siteID: siteID, canonical: canonical, info: info}, nil
}

func pathsOverlap(a, b string) bool {
	if a == b {
		return true
	}
	return pathContains(a, b) || pathContains(b, a)
}

func pathContains(parent, child string) bool {
	relative, err := filepath.Rel(parent, child)
	if err != nil || relative == "." || relative == ".." {
		return false
	}
	return !strings.HasPrefix(relative, ".."+string(filepath.Separator))
}
