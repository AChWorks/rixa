// SPDX-License-Identifier: MPL-2.0

package product

import (
	"context"
	"errors"
	"fmt"
	"sort"

	"github.com/jackc/pgx/v5"
)

// EditorialMigrationInspection is a read-only observation of Rixa-owned site
// migration ledgers. It is not an admission contract for a native update: it
// neither inspects AChrix component ledgers nor proves backup/recovery safety.
type EditorialMigrationInspection struct {
	Scope     string                   `json:"scope"`
	Admission string                   `json:"admission"`
	Sites     []EditorialMigrationSite `json:"sites"`
}

type EditorialMigrationSite struct {
	ID      string   `json:"id"`
	State   string   `json:"state"`
	Current int      `json:"current"`
	Target  int      `json:"target"`
	Pending []string `json:"pending"`
}

// InspectEditorialMigrations observes only independent enabled site databases.
// Editorial ledger reads run in REPEATABLE READ, READ ONLY transactions;
// physical-target preflight uses separate read-only identity queries.
// Unknown/gapped/future migration identities fail closed; no data, journal,
// Media, service or configuration is changed.
func InspectEditorialMigrations(parent context.Context, config Config, getenv func(string) (string, bool)) (EditorialMigrationInspection, error) {
	result := EditorialMigrationInspection{
		Scope:     "rixa-editorial-only",
		Admission: "not-authorized",
		Sites:     []EditorialMigrationSite{},
	}
	targets, err := config.DatabaseTargets(parent, getenv)
	if err != nil {
		return EditorialMigrationInspection{}, err
	}
	for _, target := range targets {
		if target.Kind != DatabaseSite {
			continue
		}
		observed, err := inspectEditorialMigrationSite(parent, target)
		if err != nil {
			return EditorialMigrationInspection{}, err
		}
		result.Sites = append(result.Sites, observed)
	}
	sort.Slice(result.Sites, func(i, j int) bool { return result.Sites[i].ID < result.Sites[j].ID })
	return result, nil
}

func inspectEditorialMigrationSite(parent context.Context, target ResolvedDatabase) (EditorialMigrationSite, error) {
	result := EditorialMigrationSite{ID: target.ID, Target: len(editorialMigrationPlan()), Pending: []string{}}
	cfg, err := safeDatabaseConnectionConfig(target.DSN)
	if err != nil {
		return EditorialMigrationSite{}, fmt.Errorf("%w: site %s database configuration", ErrConfiguration, target.ID)
	}
	ctx, cancel := context.WithTimeout(parent, migrationTimeout)
	defer cancel()
	conn, err := pgx.ConnectConfig(ctx, cfg)
	if err != nil {
		return EditorialMigrationSite{}, fmt.Errorf("%w: site %s editorial inspection connection", ErrUnavailable, target.ID)
	}
	defer conn.Close(context.Background())
	tx, err := conn.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return EditorialMigrationSite{}, fmt.Errorf("%w: site %s editorial inspection transaction", ErrUnavailable, target.ID)
	}
	defer tx.Rollback(context.Background())

	var hasSchema, hasLedger bool
	if err := tx.QueryRow(ctx, "SELECT to_regnamespace('rixa') IS NOT NULL, to_regclass('rixa.schema_migrations') IS NOT NULL").Scan(&hasSchema, &hasLedger); err != nil {
		return EditorialMigrationSite{}, fmt.Errorf("%w: site %s editorial inspection metadata", ErrUnavailable, target.ID)
	}
	if !hasSchema {
		if hasLedger {
			return EditorialMigrationSite{}, fmt.Errorf("%w: site %s editorial metadata inconsistent", ErrConfiguration, target.ID)
		}
		result.State = "uninitialized"
	} else {
		if !hasLedger {
			return EditorialMigrationSite{}, fmt.Errorf("%w: site %s editorial ledger missing", ErrConfiguration, target.ID)
		}
		plan := editorialMigrationPlan()
		result.Current, err = editorialLedgerVersion(ctx, tx, plan)
		if err != nil {
			if errors.Is(err, ErrConfiguration) {
				return EditorialMigrationSite{}, fmt.Errorf("%w: site %s editorial ledger unsupported", ErrConfiguration, target.ID)
			}
			return EditorialMigrationSite{}, fmt.Errorf("%w: site %s editorial ledger unreadable", ErrUnavailable, target.ID)
		}
		result.State = "current"
		if result.Current < len(plan) {
			result.State = "pending"
		}
	}
	plan := editorialMigrationPlan()
	for _, migration := range plan[result.Current:] {
		result.Pending = append(result.Pending, migration.identity)
	}
	if err := tx.Commit(ctx); err != nil {
		return EditorialMigrationSite{}, fmt.Errorf("%w: site %s editorial inspection completion", ErrUnavailable, target.ID)
	}
	return result, nil
}
