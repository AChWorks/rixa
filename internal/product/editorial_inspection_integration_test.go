// SPDX-License-Identifier: MPL-2.0

package product

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"
	"time"
)

func TestInspectEditorialMigrationsReadOnlyOnIndependentSites(t *testing.T) {
	adminDSN := os.Getenv("RIXA_TEST_POSTGRES_DSN")
	if adminDSN == "" {
		if os.Getenv("CI") != "" {
			t.Fatal("RIXA_TEST_POSTGRES_DSN is required for editorial inspection proof in CI")
		}
		t.Skip("set RIXA_TEST_POSTGRES_DSN to run read-only migration inspection")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	dsns := createTestDatabases(t, ctx, adminDSN)
	config := Config{
		Control: ControlConfig{DatabaseEnv: "RIXA_INSPECT_CONTROL"},
		Sites: []SiteConfig{
			{ID: "site-b", DatabaseEnv: "RIXA_INSPECT_B"},
			{ID: "disabled", DatabaseEnv: "RIXA_INSPECT_DISABLED", Disabled: true},
			{ID: "site-a", DatabaseEnv: "RIXA_INSPECT_A"},
		},
	}
	secrets := map[string]string{
		"RIXA_INSPECT_CONTROL": dsns["control"],
		"RIXA_INSPECT_A":       dsns["a"],
		"RIXA_INSPECT_B":       dsns["b"],
	}
	getenv := mapLookup(secrets)
	inspect := func() EditorialMigrationInspection {
		t.Helper()
		result, err := InspectEditorialMigrations(ctx, config, getenv)
		if err != nil {
			t.Fatal(err)
		}
		if result.Scope != "rixa-editorial-only" || result.Admission != "not-authorized" || len(result.Sites) != 2 ||
			result.Sites[0].ID != "site-a" || result.Sites[1].ID != "site-b" {
			t.Fatalf("inspection scope or enabled-site isolation changed: %#v", result)
		}
		encoded, err := json.Marshal(result)
		if err != nil || strings.Contains(string(encoded), "RIXA_INSPECT_") ||
			strings.Contains(string(encoded), dsns["a"]) || strings.Contains(string(encoded), dsns["b"]) {
			t.Fatal("inspection exposed a private database target or environment reference")
		}
		return result
	}

	// An empty installation has no Rixa schema. Inspection may describe the
	// candidate plan but must not initialize the database or imply admission.
	initial := inspect()
	for _, site := range initial.Sites {
		if site.State != "uninitialized" || site.Current != 0 || site.Target != 3 || len(site.Pending) != 3 {
			t.Fatalf("uninitialized state wrongly admitted as an upgrade: %#v", site)
		}
	}
	conn := editorialMigrationConn(t, ctx, dsns["a"])
	var created bool
	if err := conn.QueryRow(ctx, "SELECT to_regnamespace('rixa') IS NOT NULL").Scan(&created); err != nil || created {
		t.Fatalf("inspection created a Rixa schema: %v, present=%v", err, created)
	}
	_ = conn.Close(context.Background())

	seedEditorialV1(t, ctx, dsns["a"])
	v1 := inspect()
	if v1.Sites[0].Current != 1 || v1.Sites[0].State != "pending" ||
		len(v1.Sites[0].Pending) != 2 || v1.Sites[0].Pending[0] != editorialMigration2Identity ||
		v1.Sites[0].Pending[1] != editorialMigration3Identity {
		t.Fatalf("v1 pending plan not an exact prefix: %#v", v1.Sites[0])
	}
	assertEditorialLedger(t, ctx, dsns["a"], []editorialLedgerRow{{1, editorialMigrationIdentity}})

	// Independently populated site-a retains v2 while site-b is current v3.
	conn = editorialMigrationConn(t, ctx, dsns["a"])
	if _, err := conn.Exec(ctx, editorialMigration2SQL); err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Exec(ctx, "INSERT INTO rixa.schema_migrations(version,identity) VALUES(2,$1)", editorialMigration2Identity); err != nil {
		t.Fatal(err)
	}
	_ = conn.Close(context.Background())
	if err := migrateEditorial(ctx, dsns["b"], "site-b"); err != nil {
		t.Fatal(err)
	}
	v2 := inspect()
	if v2.Sites[0].State != "pending" || v2.Sites[0].Current != 2 ||
		len(v2.Sites[0].Pending) != 1 || v2.Sites[0].Pending[0] != editorialMigration3Identity {
		t.Fatalf("actual retained v2 ledger not observed: %#v", v2.Sites[0])
	}
	if v2.Sites[1].State != "current" || v2.Sites[1].Current != 3 || len(v2.Sites[1].Pending) != 0 {
		t.Fatalf("current independent site not observed: %#v", v2.Sites[1])
	}
	assertEditorialLedger(t, ctx, dsns["a"], []editorialLedgerRow{
		{1, editorialMigrationIdentity}, {2, editorialMigration2Identity},
	})
	conn = editorialMigrationConn(t, ctx, dsns["a"])
	var title string
	if err := conn.QueryRow(ctx, "SELECT title FROM rixa.content_revisions WHERE item_id=$1 AND revision=1", retainedEditorialItemID).Scan(&title); err != nil || title != "Retained title" {
		t.Fatalf("read-only inspection damaged retained content: title=%q err=%v", title, err)
	}
	var hasGrants bool
	if err := conn.QueryRow(ctx, "SELECT to_regclass('rixa.site_grants') IS NOT NULL").Scan(&hasGrants); err != nil || hasGrants {
		t.Fatalf("inspection migrated grant tables before approval: %v present=%v", err, hasGrants)
	}
	_ = conn.Close(context.Background())

	// Malformed, future or gapped ledgers must stop observation without a
	// partial success receipt or any migration effects.
	conn = editorialMigrationConn(t, ctx, dsns["a"])
	if _, err := conn.Exec(ctx, "INSERT INTO rixa.schema_migrations(version,identity) VALUES(4,'future-v4')"); err != nil {
		t.Fatal(err)
	}
	_ = conn.Close(context.Background())
	if result, err := InspectEditorialMigrations(ctx, config, getenv); !errors.Is(err, ErrConfiguration) || len(result.Sites) != 0 {
		t.Fatalf("unsupported future ledger admitted: %#v, %v", result, err)
	}
	assertEditorialLedger(t, ctx, dsns["a"], []editorialLedgerRow{
		{1, editorialMigrationIdentity}, {2, editorialMigration2Identity}, {4, "future-v4"},
	})

	// Physical target reuse is rejected by the existing Control/Site identity
	// fence, rather than being reported as a separate isolated site.
	secrets["RIXA_INSPECT_B"] = dsns["a"]
	if result, err := InspectEditorialMigrations(ctx, config, getenv); !errors.Is(err, ErrConfiguration) || len(result.Sites) != 0 {
		t.Fatalf("duplicate physical database was accepted: %#v %v", result, err)
	}

	// An existing product schema without its immutable ledger must not be
	// mistaken for a fresh installation whose migrations could be applied.
	missingLedger := createEditorialMigrationTestDatabase(t, ctx, adminDSN)
	conn = editorialMigrationConn(t, ctx, missingLedger)
	if _, err := conn.Exec(ctx, "CREATE SCHEMA rixa"); err != nil {
		t.Fatal(err)
	}
	_ = conn.Close(context.Background())
	missingConfig := Config{Control: config.Control, Sites: []SiteConfig{{ID: "site-a", DatabaseEnv: "RIXA_INSPECT_MISSING"}}}
	missingEnv := mapLookup(map[string]string{"RIXA_INSPECT_CONTROL": dsns["control"], "RIXA_INSPECT_MISSING": missingLedger})
	if result, err := InspectEditorialMigrations(ctx, missingConfig, missingEnv); !errors.Is(err, ErrConfiguration) || len(result.Sites) != 0 {
		t.Fatalf("site with missing migration ledger was admitted: %#v %v", result, err)
	}
	conn = editorialMigrationConn(t, ctx, missingLedger)
	if err := conn.QueryRow(ctx, "SELECT to_regclass('rixa.schema_migrations') IS NOT NULL").Scan(&created); err != nil || created {
		t.Fatalf("missing ledger inspection wrote a migration table: %v exists=%v", err, created)
	}
	_ = conn.Close(context.Background())
}
