// SPDX-License-Identifier: MPL-2.0

package product

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"
)

const (
	editorialMigrationIdentity        = "rixa-editorial-v1-20261006"
	editorialMigration2Identity       = "rixa-editorial-v2-20261006"
	editorialMigrationLock      int64 = 74199742106
)

const editorialMigration2SQL = `ALTER TABLE rixa.content_items ADD COLUMN publication_intent_version bigint NOT NULL DEFAULT 1 CHECK (publication_intent_version >= 1);`

type editorialMigration struct {
	sql      string
	identity string
}

func editorialMigrationPlan() []editorialMigration {
	return []editorialMigration{
		{sql: editorialMigrationSQL, identity: editorialMigrationIdentity},
		{sql: editorialMigration2SQL, identity: editorialMigration2Identity},
	}
}

type editorialSchemaQuery interface {
	Query(context.Context, string, ...any) (pgx.Rows, error)
}

// Existing editorial state must have an exact nonempty ordered prefix of the
// immutable migration plan. Unknown, missing, reordered or changed entries
// fail closed so an older Rixa source never writes a newer schema.
func editorialLedgerVersion(ctx context.Context, q editorialSchemaQuery, plan []editorialMigration) (int, error) {
	rows, err := q.Query(ctx, "SELECT version,identity FROM rixa.schema_migrations ORDER BY version")
	if err != nil {
		return 0, err
	}
	defer rows.Close()

	count := 0
	for rows.Next() {
		var version int
		var identity string
		if err := rows.Scan(&version, &identity); err != nil {
			return 0, err
		}
		if count >= len(plan) || version != count+1 || identity != plan[count].identity {
			return 0, ErrConfiguration
		}
		count++
	}
	if err := rows.Err(); err != nil {
		return 0, err
	}
	if count == 0 {
		return 0, ErrConfiguration
	}
	return count, nil
}

const editorialMigrationSQL = `
CREATE TABLE rixa.content_items (
	id text PRIMARY KEY CHECK (char_length(id) = 26),
	kind text NOT NULL CHECK (kind IN ('post','page')),
	head_revision bigint NOT NULL CHECK (head_revision >= 1),
	publication_intent_revision bigint NULL CHECK (publication_intent_revision IS NULL OR publication_intent_revision >= 1),
	created_by text NOT NULL,
	created_at timestamptz NOT NULL
);

CREATE TABLE rixa.content_revisions (
	item_id text NOT NULL REFERENCES rixa.content_items(id) ON DELETE RESTRICT,
	revision bigint NOT NULL CHECK (revision >= 1),
	title text NOT NULL,
	body_html text NOT NULL,
	actor text NOT NULL,
	created_at timestamptz NOT NULL,
	operation_id text NOT NULL UNIQUE,
	PRIMARY KEY (item_id, revision)
);

CREATE TABLE rixa.content_media_refs (
	item_id text NOT NULL,
	revision bigint NOT NULL,
	asset_id text NOT NULL CHECK (char_length(asset_id) = 26),
	asset_revision bigint NOT NULL CHECK (asset_revision >= 1),
	PRIMARY KEY (item_id, revision, asset_id),
	FOREIGN KEY (item_id, revision) REFERENCES rixa.content_revisions(item_id, revision) ON DELETE RESTRICT
);

CREATE TABLE rixa.appearance_revisions (
	revision bigint PRIMARY KEY CHECK (revision >= 1),
	site_title text NOT NULL,
	site_description text NOT NULL,
	site_language text NOT NULL CHECK (site_language IN ('en','fa')),
	home_mode text NOT NULL CHECK (home_mode IN ('latest_posts','page')),
	home_page_id text NULL,
	header_show_title boolean NOT NULL,
	header_tagline text NOT NULL,
	footer_text text NOT NULL,
	theme text NOT NULL CHECK (theme IN ('light','dark')),
	actor text NOT NULL,
	created_at timestamptz NOT NULL,
	operation_id text NOT NULL UNIQUE
);

CREATE TABLE rixa.appearance_head (
	singleton boolean PRIMARY KEY DEFAULT true CHECK (singleton),
	head_revision bigint NOT NULL REFERENCES rixa.appearance_revisions(revision) ON DELETE RESTRICT
);

CREATE TABLE rixa.operations (
	operation_id text PRIMARY KEY,
	kind text NOT NULL,
	resource_id text NOT NULL,
	request_hash text NOT NULL CHECK (char_length(request_hash) = 64),
	result_revision bigint NOT NULL CHECK (result_revision >= 0),
	created_at timestamptz NOT NULL
);

CREATE INDEX content_revisions_item_recent_idx
	ON rixa.content_revisions(item_id, revision DESC);
CREATE INDEX content_media_refs_asset_idx
	ON rixa.content_media_refs(asset_id);
`

func migrateEditorial(parent context.Context, dsn, siteID string) error {
	ctx, cancel := context.WithTimeout(parent, migrationTimeout)
	defer cancel()

	conn, err := pgx.Connect(ctx, dsn)
	if err != nil {
		return fmt.Errorf("editorial connect: %w", err)
	}
	defer conn.Close(context.Background())

	tx, err := conn.Begin(ctx)
	if err != nil {
		return fmt.Errorf("editorial migration begin: %w", err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()

	if _, err = tx.Exec(ctx, "SELECT pg_advisory_xact_lock($1)", editorialMigrationLock); err != nil {
		return fmt.Errorf("editorial migration lock: %w", err)
	}

	var schemaExists bool
	if err = tx.QueryRow(ctx, "SELECT to_regnamespace('rixa') IS NOT NULL").Scan(&schemaExists); err != nil {
		return fmt.Errorf("editorial migration schema lookup: %w", err)
	}

	plan := editorialMigrationPlan()
	version := 0
	if schemaExists {
		var ledgerExists bool
		if err = tx.QueryRow(ctx, "SELECT to_regclass('rixa.schema_migrations') IS NOT NULL").Scan(&ledgerExists); err != nil {
			return fmt.Errorf("editorial migration ledger lookup: %w", err)
		}
		if !ledgerExists {
			return fmt.Errorf("%w: editorial migration ledger missing", ErrConfiguration)
		}
		version, err = editorialLedgerVersion(ctx, tx, plan)
		if err != nil {
			if errors.Is(err, ErrConfiguration) {
				return fmt.Errorf("%w: editorial migration ledger is not a supported exact prefix", ErrConfiguration)
			}
			return fmt.Errorf("editorial migration ledger: %w", err)
		}
	} else {
		if _, err = tx.Exec(ctx, `
			CREATE SCHEMA rixa;
			CREATE TABLE rixa.schema_migrations (
				version integer PRIMARY KEY,
				identity text NOT NULL
			)
		`); err != nil {
			return fmt.Errorf("editorial migration metadata: %w", err)
		}
	}

	for i := version; i < len(plan); i++ {
		if _, err = tx.Exec(ctx, plan[i].sql); err != nil {
			return fmt.Errorf("editorial migration %d: %w", i+1, err)
		}
		if _, err = tx.Exec(ctx, "INSERT INTO rixa.schema_migrations(version,identity) VALUES($1,$2)", i+1, plan[i].identity); err != nil {
			return fmt.Errorf("editorial migration %d record: %w", i+1, err)
		}
	}

	version, err = editorialLedgerVersion(ctx, tx, plan)
	if err != nil {
		return fmt.Errorf("editorial migration ledger verification: %w", err)
	}
	if version != len(plan) {
		return fmt.Errorf("%w: editorial migration ledger is incomplete", ErrConfiguration)
	}

	var head int64
	err = tx.QueryRow(ctx, "SELECT head_revision FROM rixa.appearance_head WHERE singleton=true").Scan(&head)
	if errors.Is(err, pgx.ErrNoRows) {
		if !validPlainText(siteID, 120, false) {
			return fmt.Errorf("%w: site identity cannot initialize appearance", ErrConfiguration)
		}
		const bootstrapOperation = "bootstrap:appearance"
		if _, err = tx.Exec(ctx, `
			INSERT INTO rixa.appearance_revisions(
				revision,site_title,site_description,site_language,home_mode,home_page_id,
				header_show_title,header_tagline,footer_text,theme,actor,created_at,operation_id
			) VALUES(1,$1,'','en','latest_posts',NULL,true,'','','light','rixa.migrate',CURRENT_TIMESTAMP,$2)
		`, siteID, bootstrapOperation); err != nil {
			return fmt.Errorf("editorial appearance default: %w", err)
		}
		if _, err = tx.Exec(ctx, "INSERT INTO rixa.appearance_head(singleton,head_revision) VALUES(true,1)"); err != nil {
			return fmt.Errorf("editorial appearance head: %w", err)
		}
		if _, err = tx.Exec(ctx, `
			INSERT INTO rixa.operations(operation_id,kind,resource_id,request_hash,result_revision,created_at)
			VALUES($1,'appearance.initialize','appearance',$2,1,CURRENT_TIMESTAMP)
			ON CONFLICT (operation_id) DO NOTHING
		`, bootstrapOperation, strings.Repeat("0", 64)); err != nil {
			return fmt.Errorf("editorial appearance operation: %w", err)
		}
	} else if err != nil {
		return fmt.Errorf("editorial appearance lookup: %w", err)
	} else if head < 1 {
		return fmt.Errorf("%w: invalid appearance head", ErrConfiguration)
	}

	if err = tx.Commit(ctx); err != nil {
		return fmt.Errorf("editorial migration commit: %w", err)
	}
	return nil
}
