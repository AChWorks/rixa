// SPDX-License-Identifier: MPL-2.0

package product

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

func TestEditorialMigrationLedgerCompatibility(t *testing.T) {
	adminDSN := os.Getenv("RIXA_TEST_POSTGRES_DSN")
	if adminDSN == "" {
		if os.Getenv("CI") != "" {
			t.Fatal("RIXA_TEST_POSTGRES_DSN is required in CI")
		}
		t.Skip("set RIXA_TEST_POSTGRES_DSN to run PostgreSQL editorial migration proof")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	t.Run("fresh installs current ledger and rerun is idempotent", func(t *testing.T) {
		dsn := createEditorialMigrationTestDatabase(t, ctx, adminDSN)
		if err := migrateEditorial(ctx, dsn, "fresh-site"); err != nil {
			t.Fatalf("fresh editorial migration: %v", err)
		}
		assertEditorialLedger(t, ctx, dsn, []editorialLedgerRow{
			{1, editorialMigrationIdentity},
			{2, editorialMigration2Identity},
		})
		assertEditorialReady(t, ctx, dsn)

		if err := migrateEditorial(ctx, dsn, "fresh-site"); err != nil {
			t.Fatalf("idempotent editorial migration: %v", err)
		}
		assertEditorialLedger(t, ctx, dsn, []editorialLedgerRow{
			{1, editorialMigrationIdentity},
			{2, editorialMigration2Identity},
		})

		conn := editorialMigrationConn(t, ctx, dsn)
		defer conn.Close(context.Background())
		var appearanceCount, operationCount int
		if err := conn.QueryRow(ctx, "SELECT count(*) FROM rixa.appearance_revisions").Scan(&appearanceCount); err != nil {
			t.Fatal(err)
		}
		if err := conn.QueryRow(ctx, "SELECT count(*) FROM rixa.operations WHERE operation_id='bootstrap:appearance'").Scan(&operationCount); err != nil {
			t.Fatal(err)
		}
		if appearanceCount != 1 || operationCount != 1 {
			t.Fatalf("idempotent migration duplicated bootstrap state: appearance=%d operation=%d", appearanceCount, operationCount)
		}
	})

	t.Run("retained v1 state upgrades to v2 without data loss", func(t *testing.T) {
		dsn := createEditorialMigrationTestDatabase(t, ctx, adminDSN)
		seedEditorialV1(t, ctx, dsn)

		if err := migrateEditorial(ctx, dsn, "retained-site"); err != nil {
			t.Fatalf("retained v1 upgrade: %v", err)
		}
		assertEditorialLedger(t, ctx, dsn, []editorialLedgerRow{
			{1, editorialMigrationIdentity},
			{2, editorialMigration2Identity},
		})
		assertEditorialReady(t, ctx, dsn)

		conn := editorialMigrationConn(t, ctx, dsn)
		defer conn.Close(context.Background())
		var publicationIntent, publicationVersion int64
		var title, siteTitle string
		var operationCount int
		if err := conn.QueryRow(ctx, "SELECT publication_intent_revision,publication_intent_version FROM rixa.content_items WHERE id=$1", retainedEditorialItemID).Scan(&publicationIntent, &publicationVersion); err != nil {
			t.Fatal(err)
		}
		if err := conn.QueryRow(ctx, "SELECT title FROM rixa.content_revisions WHERE item_id=$1 AND revision=1", retainedEditorialItemID).Scan(&title); err != nil {
			t.Fatal(err)
		}
		if err := conn.QueryRow(ctx, "SELECT site_title FROM rixa.appearance_revisions WHERE revision=1").Scan(&siteTitle); err != nil {
			t.Fatal(err)
		}
		if err := conn.QueryRow(ctx, "SELECT count(*) FROM rixa.operations WHERE operation_id IN ('retained-content-op','retained-appearance-op')").Scan(&operationCount); err != nil {
			t.Fatal(err)
		}
		if publicationIntent != 1 || publicationVersion != 1 || title != "Retained title" || siteTitle != "Retained site" || operationCount != 2 {
			t.Fatalf("retained state changed during v2 upgrade: publication_intent=%d publication_version=%d title=%q site_title=%q operations=%d", publicationIntent, publicationVersion, title, siteTitle, operationCount)
		}

		if err := migrateEditorial(ctx, dsn, "retained-site"); err != nil {
			t.Fatalf("retained current rerun: %v", err)
		}
	})

	t.Run("wrong v1 identity fails before v2 effects", func(t *testing.T) {
		dsn := createEditorialMigrationTestDatabase(t, ctx, adminDSN)
		seedEditorialV1(t, ctx, dsn)
		conn := editorialMigrationConn(t, ctx, dsn)
		if _, err := conn.Exec(ctx, "UPDATE rixa.schema_migrations SET identity='wrong-v1' WHERE version=1"); err != nil {
			t.Fatal(err)
		}
		conn.Close(context.Background())

		if err := migrateEditorial(ctx, dsn, "wrong-v1"); !errors.Is(err, ErrConfiguration) {
			t.Fatalf("wrong v1 identity migration error = %v, want ErrConfiguration", err)
		}
		assertPublicationVersionColumnAbsent(t, ctx, dsn)
		assertEditorialNotReady(t, ctx, dsn)
	})

	t.Run("wrong v2 identity fails migration and readiness", func(t *testing.T) {
		dsn := createEditorialMigrationTestDatabase(t, ctx, adminDSN)
		if err := migrateEditorial(ctx, dsn, "wrong-v2"); err != nil {
			t.Fatal(err)
		}
		conn := editorialMigrationConn(t, ctx, dsn)
		if _, err := conn.Exec(ctx, "UPDATE rixa.schema_migrations SET identity='wrong-v2' WHERE version=2"); err != nil {
			t.Fatal(err)
		}
		conn.Close(context.Background())

		if err := migrateEditorial(ctx, dsn, "wrong-v2"); !errors.Is(err, ErrConfiguration) {
			t.Fatalf("wrong v2 identity migration error = %v, want ErrConfiguration", err)
		}
		assertEditorialNotReady(t, ctx, dsn)
	})

	t.Run("unknown future v3 fails migration and readiness", func(t *testing.T) {
		dsn := createEditorialMigrationTestDatabase(t, ctx, adminDSN)
		if err := migrateEditorial(ctx, dsn, "future-v3"); err != nil {
			t.Fatal(err)
		}
		conn := editorialMigrationConn(t, ctx, dsn)
		if _, err := conn.Exec(ctx, "INSERT INTO rixa.schema_migrations(version,identity) VALUES(3,'future-v3')"); err != nil {
			t.Fatal(err)
		}
		conn.Close(context.Background())

		if err := migrateEditorial(ctx, dsn, "future-v3"); !errors.Is(err, ErrConfiguration) {
			t.Fatalf("future v3 migration error = %v, want ErrConfiguration", err)
		}
		assertEditorialLedger(t, ctx, dsn, []editorialLedgerRow{
			{1, editorialMigrationIdentity},
			{2, editorialMigration2Identity},
			{3, "future-v3"},
		})
		assertEditorialNotReady(t, ctx, dsn)
	})

	t.Run("gapped v1 v3 ledger fails before inserting v2", func(t *testing.T) {
		dsn := createEditorialMigrationTestDatabase(t, ctx, adminDSN)
		seedEditorialV1(t, ctx, dsn)
		conn := editorialMigrationConn(t, ctx, dsn)
		if _, err := conn.Exec(ctx, "INSERT INTO rixa.schema_migrations(version,identity) VALUES(3,'future-v3')"); err != nil {
			t.Fatal(err)
		}
		conn.Close(context.Background())

		if err := migrateEditorial(ctx, dsn, "gapped"); !errors.Is(err, ErrConfiguration) {
			t.Fatalf("gapped migration error = %v, want ErrConfiguration", err)
		}
		assertEditorialLedger(t, ctx, dsn, []editorialLedgerRow{
			{1, editorialMigrationIdentity},
			{3, "future-v3"},
		})
		assertPublicationVersionColumnAbsent(t, ctx, dsn)
		assertEditorialNotReady(t, ctx, dsn)
	})
}

const retainedEditorialItemID = "AAAAAAAAAAAAAAAAAAAAAAAAAA"

type editorialLedgerRow struct {
	version  int
	identity string
}

func seedEditorialV1(t *testing.T, ctx context.Context, dsn string) {
	t.Helper()
	conn := editorialMigrationConn(t, ctx, dsn)
	defer conn.Close(context.Background())

	if _, err := conn.Exec(ctx, `
		CREATE SCHEMA rixa;
		CREATE TABLE rixa.schema_migrations (
			version integer PRIMARY KEY,
			identity text NOT NULL
		)
	`); err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Exec(ctx, editorialMigrationSQL); err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Exec(ctx, "INSERT INTO rixa.schema_migrations(version,identity) VALUES(1,$1)", editorialMigrationIdentity); err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Exec(ctx, `
		INSERT INTO rixa.content_items(id,kind,head_revision,publication_intent_revision,created_by,created_at)
		VALUES($1,'post',1,1,'retained-user',CURRENT_TIMESTAMP)
	`, retainedEditorialItemID); err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Exec(ctx, `
		INSERT INTO rixa.content_revisions(item_id,revision,title,body_html,actor,created_at,operation_id)
		VALUES($1,1,'Retained title','<p dir="auto">retained body</p>','retained-user',CURRENT_TIMESTAMP,'retained-content-op')
	`, retainedEditorialItemID); err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Exec(ctx, `
		INSERT INTO rixa.operations(operation_id,kind,resource_id,request_hash,result_revision,created_at)
		VALUES('retained-content-op','content.create',$1,$2,1,CURRENT_TIMESTAMP)
	`, retainedEditorialItemID, strings.Repeat("1", 64)); err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Exec(ctx, `
		INSERT INTO rixa.appearance_revisions(
			revision,site_title,site_description,site_language,home_mode,home_page_id,
			header_show_title,header_tagline,footer_text,theme,actor,created_at,operation_id
		) VALUES(1,'Retained site','Retained description','fa','latest_posts',NULL,true,'Retained tagline','Retained footer','dark','retained-user',CURRENT_TIMESTAMP,'retained-appearance-op')
	`); err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Exec(ctx, "INSERT INTO rixa.appearance_head(singleton,head_revision) VALUES(true,1)"); err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Exec(ctx, `
		INSERT INTO rixa.operations(operation_id,kind,resource_id,request_hash,result_revision,created_at)
		VALUES('retained-appearance-op','appearance.save','appearance',$1,1,CURRENT_TIMESTAMP)
	`, strings.Repeat("2", 64)); err != nil {
		t.Fatal(err)
	}
}

func createEditorialMigrationTestDatabase(t *testing.T, ctx context.Context, adminDSN string) string {
	t.Helper()
	admin := editorialMigrationConn(t, ctx, adminDSN)
	defer admin.Close(context.Background())

	name := "rixa_editorial_" + strconv.FormatInt(time.Now().UnixNano(), 36)
	if _, err := admin.Exec(ctx, "CREATE DATABASE "+name+" TEMPLATE template0 ENCODING 'UTF8'"); err != nil {
		t.Fatalf("create editorial migration database: %v", err)
	}
	t.Cleanup(func() {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		conn, err := pgx.Connect(cleanupCtx, adminDSN)
		if err != nil {
			t.Logf("editorial migration cleanup connect: %v", err)
			return
		}
		defer conn.Close(context.Background())
		if _, err := conn.Exec(cleanupCtx, "DROP DATABASE "+name+" WITH (FORCE)"); err != nil {
			t.Logf("drop editorial migration database %s: %v", name, err)
		}
	})
	u, err := url.Parse(adminDSN)
	if err != nil {
		t.Fatal(err)
	}
	u.Path = "/" + name
	return u.String()
}

func editorialMigrationConn(t *testing.T, ctx context.Context, dsn string) *pgx.Conn {
	t.Helper()
	conn, err := pgx.Connect(ctx, dsn)
	if err != nil {
		t.Fatalf("connect editorial migration database: %v", err)
	}
	return conn
}

func assertEditorialLedger(t *testing.T, ctx context.Context, dsn string, want []editorialLedgerRow) {
	t.Helper()
	conn := editorialMigrationConn(t, ctx, dsn)
	defer conn.Close(context.Background())
	rows, err := conn.Query(ctx, "SELECT version,identity FROM rixa.schema_migrations ORDER BY version")
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var got []editorialLedgerRow
	for rows.Next() {
		var row editorialLedgerRow
		if err := rows.Scan(&row.version, &row.identity); err != nil {
			t.Fatal(err)
		}
		got = append(got, row)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("editorial ledger = %#v, want %#v", got, want)
	}
}

func assertPublicationVersionColumnAbsent(t *testing.T, ctx context.Context, dsn string) {
	t.Helper()
	conn := editorialMigrationConn(t, ctx, dsn)
	defer conn.Close(context.Background())
	var count int
	if err := conn.QueryRow(ctx, `
		SELECT count(*) FROM information_schema.columns
		WHERE table_schema='rixa' AND table_name='content_items' AND column_name='publication_intent_version'
	`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatal("rejected migration applied publication_intent_version")
	}
}

func assertEditorialReady(t *testing.T, ctx context.Context, dsn string) {
	t.Helper()
	store, err := newEditorialStore(dsn)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.ready(ctx); err != nil {
		t.Fatalf("editorial readiness: %v", err)
	}
}

func assertEditorialNotReady(t *testing.T, ctx context.Context, dsn string) {
	t.Helper()
	store, err := newEditorialStore(dsn)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.ready(ctx); !errors.Is(err, ErrEditorialUnavailable) {
		t.Fatalf("editorial readiness error = %v, want ErrEditorialUnavailable", err)
	}
}
