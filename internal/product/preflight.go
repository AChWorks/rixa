// SPDX-License-Identifier: MPL-2.0

package product

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
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

func inspectDatabaseTarget(parent context.Context, dsn string) (identity databaseTargetIdentity, err error) {
	cfg, parseErr := pgx.ParseConfig(dsn)
	if parseErr != nil || cfg == nil || cfg.Database == "" || cfg.User == "" {
		return identity, ErrConfiguration
	}
	ctx, cancel := context.WithTimeout(parent, databaseProbeTimeout)
	defer cancel()

	conn, connectErr := pgx.ConnectConfig(ctx, cfg)
	if connectErr != nil {
		return identity, safePreflightError(ctx)
	}
	defer func() {
		closeCtx, closeCancel := context.WithTimeout(context.WithoutCancel(parent), time.Second)
		defer closeCancel()
		if closeErr := conn.Close(closeCtx); closeErr != nil {
			err = errors.Join(err, ErrUnavailable)
		}
	}()

	queryErr := conn.QueryRow(ctx, `
		SELECT control.system_identifier::text, database.oid::text
		FROM pg_control_system() AS control
		JOIN pg_database AS database ON database.datname = current_database()
	`).Scan(&identity.systemID, &identity.databaseOID)
	if queryErr != nil || identity.systemID == "" || identity.databaseOID == "" {
		return databaseTargetIdentity{}, safePreflightError(ctx)
	}
	return identity, nil
}

func safePreflightError(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return ErrUnavailable
}

func validateDeclaredMediaRoots(sites []SiteConfig) error {
	resolved := make([]ResolvedSite, 0, len(sites))
	for _, site := range sites {
		resolved = append(resolved, ResolvedSite{SiteConfig: site})
	}
	return validateMediaRoots(resolved)
}

func validateMediaRoots(sites []ResolvedSite) error {
	roots := make([]mediaRootIdentity, 0, len(sites))
	for _, site := range sites {
		if site.Disabled {
			continue
		}
		current, err := inspectMediaRoot(site.ID, site.MediaRoot)
		if err != nil {
			return fmt.Errorf("%w: media root for %s", ErrConfiguration, site.ID)
		}
		for _, existing := range roots {
			if os.SameFile(existing.info, current.info) ||
				pathsOverlap(existing.canonical, current.canonical) {
				return fmt.Errorf("%w: media roots for %s and %s are not exclusive", ErrConfiguration, existing.siteID, current.siteID)
			}
		}
		roots = append(roots, current)
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
