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
	editorialMigrationIdentity  = "rixa-editorial-v1-20261006"
	editorialMigration2Identity = "rixa-editorial-v2-20261006"
)

const editorialMigration2SQL = `ALTER TABLE rixa.content_items ADD COLUMN publication_intent_version bigint NOT NULL DEFAULT 1 CHECK (publication_intent_version >= 1);`

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

	if _, err = tx.Exec(ctx, `
		CREATE SCHEMA IF NOT EXISTS rixa;
		CREATE TABLE IF NOT EXISTS rixa.schema_migrations (
			version integer PRIMARY KEY,
			identity text NOT NULL
		)
	`); err != nil {
		return fmt.Errorf("editorial migration metadata: %w", err)
	}

	var identity string
	err = tx.QueryRow(ctx, "SELECT identity FROM rixa.schema_migrations WHERE version=1").Scan(&identity)
	switch {
	case err == nil && identity != editorialMigrationIdentity:
		return fmt.Errorf("%w: editorial migration identity mismatch", ErrConfiguration)
	case err == nil:
		// Already applied and identity-checked.
	case errors.Is(err, pgx.ErrNoRows):
		if _, err = tx.Exec(ctx, editorialMigrationSQL); err != nil {
			return fmt.Errorf("editorial migration 1: %w", err)
		}
		if _, err = tx.Exec(ctx, "INSERT INTO rixa.schema_migrations(version,identity) VALUES(1,$1)", editorialMigrationIdentity); err != nil {
			return fmt.Errorf("editorial migration record: %w", err)
		}
	default:
		return fmt.Errorf("editorial migration lookup: %w", err)
	}

	err = tx.QueryRow(ctx, "SELECT identity FROM rixa.schema_migrations WHERE version=2").Scan(&identity)
	switch {
	case err == nil && identity != editorialMigration2Identity:
		return fmt.Errorf("%w: editorial migration 2 identity mismatch", ErrConfiguration)
	case err == nil:
		// Already applied and identity-checked.
	case errors.Is(err, pgx.ErrNoRows):
		if _, err = tx.Exec(ctx, editorialMigration2SQL); err != nil {
			return fmt.Errorf("editorial migration 2: %w", err)
		}
		if _, err = tx.Exec(ctx, "INSERT INTO rixa.schema_migrations(version,identity) VALUES(2,$1)", editorialMigration2Identity); err != nil {
			return fmt.Errorf("editorial migration 2 record: %w", err)
		}
	default:
		return fmt.Errorf("editorial migration 2 lookup: %w", err)
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
