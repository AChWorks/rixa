// SPDX-License-Identifier: MPL-2.0

package serverinstall

import (
	"context"
	"errors"
	"net/url"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

// Proves the native updater's new decision against independent physical
// PostgreSQL 18 databases, not a mocked or inferred editorial ledger. These
// databases are disposable; the test never touches a running Rixa service.
func TestNativeUpdatePreStopReadOnlyRealPostgres(t *testing.T) {
	adminDSN := os.Getenv("RIXA_TEST_POSTGRES_DSN")
	if adminDSN == "" {
		if os.Getenv("CI") != "" {
			t.Fatal("CI requires RIXA_TEST_POSTGRES_DSN for native pre-stop ledger proof")
		}
		t.Skip("set RIXA_TEST_POSTGRES_DSN to run native update ledger proof")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	admin, err := pgx.Connect(ctx, adminDSN)
	if err != nil {
		t.Fatal("cannot open disposable PostgreSQL test authority")
	}
	defer admin.Close(context.Background())

	namePrefix := "rixa_native_precheck_" + strconv.FormatInt(time.Now().UnixNano(), 36)
	controlDB, siteDB := namePrefix+"_c", namePrefix+"_a"
	for _, database := range []string{controlDB, siteDB} {
		if _, err := admin.Exec(ctx, "CREATE DATABASE "+database+" TEMPLATE template0 ENCODING 'UTF8'"); err != nil {
			t.Fatal("cannot create isolated PostgreSQL pre-stop test database")
		}
		t.Cleanup(func() {
			cleanupCtx, done := context.WithTimeout(context.Background(), 20*time.Second)
			defer done()
			conn, e := pgx.Connect(cleanupCtx, adminDSN)
			if e != nil {
				t.Error("cannot reconnect for isolated database cleanup")
				return
			}
			defer conn.Close(context.Background())
			if _, e = conn.Exec(cleanupCtx, "DROP DATABASE "+database+" WITH (FORCE)"); e != nil {
				t.Errorf("cannot remove isolated database %s", database)
			}
		})
	}
	dsnFor := func(database string) string {
		t.Helper()
		u, err := url.Parse(adminDSN)
		if err != nil {
			t.Fatal("bad isolated PostgreSQL administrative URL")
		}
		u.Path = "/" + database
		return u.String()
	}

	prior := &journal{Draft: validDraft()}
	prior.Draft.ControlDSN = dsnFor(controlDB)
	prior.Draft.SiteDSN = dsnFor(siteDB)
	if err := updateEditorialPreflight(ctx, prior); !errors.Is(err, ErrState) {
		t.Fatalf("an uninitialized site was incorrectly treated as safe: %v", err)
	}

	site, err := pgx.Connect(ctx, prior.Draft.SiteDSN)
	if err != nil {
		t.Fatal("cannot open isolated site database")
	}
	defer site.Close(context.Background())
	// The Rixa editorial-v2 published-release plan is represented by its
	// exact immutable ledger identities. This preflight needs no Editorial
	// row schema because it reads ONLY the migration ledger.
	if _, err = site.Exec(ctx, `
		CREATE SCHEMA rixa;
		CREATE TABLE rixa.schema_migrations (
			version integer PRIMARY KEY,
			identity text NOT NULL
		)
	`); err != nil {
		t.Fatal("cannot seed old editorial ledger")
	}
	for version, identity := range []string{"rixa-editorial-v1-20261006", "rixa-editorial-v2-20261006"} {
		if _, err = site.Exec(ctx, "INSERT INTO rixa.schema_migrations(version,identity) VALUES($1,$2)", version+1, identity); err != nil {
			t.Fatal("cannot seed old editorial ledger identity")
		}
	}
	if err := updateEditorialPreflight(ctx, prior); !errors.Is(err, errPreStopEditorialPending) {
		t.Fatalf("published v2-style ledger did not fail closed before stop: %v", err)
	}
	var versionCount int
	if err = site.QueryRow(ctx, "SELECT count(*) FROM rixa.schema_migrations").Scan(&versionCount); err != nil || versionCount != 2 {
		t.Fatalf("editorial preflight mutated old ledger count: %v count=%d", err, versionCount)
	}
	var grants bool
	if err = site.QueryRow(ctx, "SELECT to_regclass('rixa.site_grants') IS NOT NULL").Scan(&grants); err != nil || grants {
		t.Fatalf("editorial preflight performed schema migration: %v grants=%v", err, grants)
	}
	// A current ledger does NOT prove a candidate is compatible. In fact,
	// this deliberately lacks the full product tables; only the post-stop
	// candidate check can accept it. The early gate must not pretend to.
	if _, err = site.Exec(ctx, "INSERT INTO rixa.schema_migrations(version,identity) VALUES(3,$1)", "rixa-editorial-v3-20261031-site-grants"); err != nil {
		t.Fatal("cannot seed current editorial ledger")
	}
	if err := updateEditorialPreflight(ctx, prior); err != nil {
		t.Fatalf("current observer ledger must defer to candidate check: %v", err)
	}
	if err = site.QueryRow(ctx, "SELECT to_regclass('rixa.site_grants') IS NOT NULL").Scan(&grants); err != nil || grants {
		t.Fatalf("current ledger preflight wrongly installed schema: %v grants=%v", err, grants)
	}
	// Even an apparent current version is not accepted if an immutable
	// migration identity was tampered with. No schema repair is performed.
	if _, err = site.Exec(ctx, "UPDATE rixa.schema_migrations SET identity='tampered' WHERE version=2"); err != nil {
		t.Fatal("cannot stage disposable ledger-tamper observation")
	}
	if err = updateEditorialPreflight(ctx, prior); !errors.Is(err, ErrPrerequisite) || errors.Is(err, errPreStopEditorialPending) {
		t.Fatalf("incorrect immutable ledger classified as a compatible update: %v", err)
	}
	if _, err = site.Exec(ctx, "UPDATE rixa.schema_migrations SET identity=$1 WHERE version=2", "rixa-editorial-v2-20261006"); err != nil {
		t.Fatal("cannot reset isolated ledger test fixture")
	}

	// The unchanged physical-target preflight rejects another DSN naming the
	// exact same PostgreSQL database. No partial observation may be accepted.
	prior.Draft.ControlDSN = prior.Draft.SiteDSN
	if err := updateEditorialPreflight(ctx, prior); err == nil || errors.Is(err, errPreStopEditorialPending) {
		t.Fatalf("duplicate physical database target was misclassified: %v", err)
	}
	// Output and errors may never include the credential-bearing DSNs.
	if err := updateEditorialPreflight(ctx, prior); err != nil {
		if strings.Contains(err.Error(), adminDSN) || strings.Contains(err.Error(), prior.Draft.SiteDSN) {
			t.Fatal("native pre-stop error disclosed a database URL")
		}
	}
}
