// SPDX-License-Identifier: MPL-2.0

package serverinstall

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/AChWorks/rixa/internal/product"
	"github.com/jackc/pgx/v5"
)

// Uses only newly created databases with unique names on the disposable CI
// PostgreSQL 18 service. Never attaches to an existing product database.
func TestNativeOptionalTwoSitePhysicalMigrationAndBootstrap(t *testing.T) {
	adminDSN := os.Getenv("RIXA_TEST_POSTGRES_DSN")
	if adminDSN == "" {
		if os.Getenv("CI") != "" {
			t.Fatal("CI needs RIXA_TEST_POSTGRES_DSN for two-site native database proof")
		}
		t.Skip("set RIXA_TEST_POSTGRES_DSN to run disposable three-database proof")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	admin, err := pgx.Connect(ctx, adminDSN)
	if err != nil {
		t.Fatal("disposable PostgreSQL authority unavailable")
	}
	defer admin.Close(context.Background())

	prefix := "rixa_two_native_" + strconv.FormatInt(time.Now().UnixNano(), 36)
	databases := []string{prefix + "_c", prefix + "_a", prefix + "_b"}
	roles := []string{prefix + "_control", prefix + "_sitea", prefix + "_siteb"}
	passwords := []string{"Test-Control-Isolated-2026!", "Test-Site-A-Isolated-2026!", "Test-Site-B-Isolated-2026!"}
	// This disposable CI-only fixture owns all created databases and login
	// roles. No administrator login is passed into Native Review or Bootstrap.
	dsn := func(name, role, password string) string {
		u, err := url.Parse(adminDSN)
		if err != nil {
			t.Fatal("bad PostgreSQL test URL")
		}
		u.Path = "/" + name
		if role != "" {
			u.User = url.UserPassword(role, password)
		}
		return u.String()
	}
	for i, database := range databases {
		role := roles[i]
		name := pgx.Identifier{role}.Sanitize()
		if _, err := admin.Exec(ctx, "CREATE ROLE "+name+" LOGIN PASSWORD '"+passwords[i]+"'"); err != nil {
			t.Fatal("cannot create isolated disposable login role")
		}
		t.Cleanup(func() {
			cleanup, finish := context.WithTimeout(context.Background(), 20*time.Second)
			defer finish()
			db, err := pgx.Connect(cleanup, adminDSN)
			if err != nil {
				t.Error("cannot reopen role cleanup connection")
				return
			}
			defer db.Close(context.Background())
			if _, err := db.Exec(cleanup, "DROP ROLE "+name); err != nil {
				t.Error("isolated role cleanup failed: " + err.Error())
			}
		})
		dbName := pgx.Identifier{database}.Sanitize()
		if _, err := admin.Exec(ctx, "CREATE DATABASE "+dbName+" OWNER "+name+" TEMPLATE template0 ENCODING 'UTF8'"); err != nil {
			t.Fatal("cannot create separately owned disposable database")
		}
		t.Cleanup(func() {
			cleanup, finish := context.WithTimeout(context.Background(), 20*time.Second)
			defer finish()
			db, err := pgx.Connect(cleanup, adminDSN)
			if err != nil {
				t.Error("cannot reopen database cleanup connection")
				return
			}
			defer db.Close(context.Background())
			if _, err := db.Exec(cleanup, "DROP DATABASE "+dbName+" WITH (FORCE)"); err != nil {
				t.Error("disposable database cleanup failed: " + err.Error())
			}
		})
		if _, err := admin.Exec(ctx, "REVOKE CONNECT ON DATABASE "+dbName+" FROM PUBLIC"); err != nil {
			t.Fatal("cannot remove public database CONNECT")
		}
		// Grant only the read-only system-identity function required by the
		// existing native trusted physical-database admission. This is scoped
		// to the disposable DB, not a cluster-wide monitoring role.
		isolatedAdmin, err := pgx.Connect(ctx, dsn(database, "", ""))
		if err != nil {
			t.Fatal("cannot open disposable database as owner operator")
		}
		_, grantErr := isolatedAdmin.Exec(ctx, "GRANT EXECUTE ON FUNCTION pg_catalog.pg_control_system() TO "+name)
		closeErr := isolatedAdmin.Close(ctx)
		if grantErr != nil || closeErr != nil {
			t.Fatalf("cannot grant bounded physical-identity probe: %v %v", grantErr, closeErr)
		}
	}
	d := validTwoSiteDraft()
	d.ControlDSN, d.SiteDSN, d.SiteBDSN =
		dsn(databases[0], roles[0], passwords[0]),
		dsn(databases[1], roles[1], passwords[1]),
		dsn(databases[2], roles[2], passwords[2])
	if p := ValidateDraft(d); len(p) > 0 {
		t.Fatalf("isolated-role native draft validation failed: %+v", p)
	}
	// The after-install admission reuses the same real three-role PostgreSQL
	// contract without requiring the original bootstrap secrets to survive.
	installedBase := d
	installedBase.SiteBOrigin, installedBase.SiteBDSN = "", ""
	installedBase.SiteBLogin, installedBase.SiteBPassword = "", ""
	installedBase.ControlPassword, installedBase.SitePassword = "", ""
	proposed := AddSiteInspectionDraft{InstallationOperation: d.OperationID,
		SiteBOrigin: d.SiteBOrigin, SiteBDSN: d.SiteBDSN,
		SiteBLogin: d.SiteBLogin, SiteBPassword: d.SiteBPassword}
	if problems := ValidateAddSiteInspection(installedBase, proposed); len(problems) > 0 {
		t.Fatalf("physical after-install site admission rejected valid private inputs: %+v", problems)
	}
	if candidate := proposed.withInstallation(installedBase); candidate.ControlPassword != "" || candidate.SitePassword != "" {
		t.Fatal("the preflight reintroduced retired original administrator secrets")
	} else if afterTargets, err := databaseIdentities(ctx, candidate); err != nil || len(afterTargets) != 3 {
		t.Fatalf("actual after-install three-site-target admission failed: %v", err)
	}
	identities, err := databaseIdentities(ctx, d)
	if err != nil {
		t.Fatalf("real three-database native admission failed: %v", err)
	}
	if len(identities) != 3 || identities["control"] == identities["site-a"] || identities["site-a"] == identities["site-b"] || identities["control"] == identities["site-b"] {
		t.Fatal("physical database isolation not proven")
	}
	aliased := d
	aliased.SiteBDSN = d.SiteDSN
	if _, err := databaseIdentities(ctx, aliased); !errors.Is(err, ErrPrerequisite) {
		t.Fatalf("aliased physical site database was admitted: %v", err)
	}
	// Negative privilege tests mutate only fixture-owned DBs/roles. Restore
	// exact access before real product migrations and admin bootstraps.
	for _, test := range []struct {
		name, grant, revoke string
	}{
		{"foreign-connect", "GRANT CONNECT ON DATABASE " + pgx.Identifier{databases[1]}.Sanitize() +
			" TO " + pgx.Identifier{roles[0]}.Sanitize(),
			"REVOKE CONNECT ON DATABASE " + pgx.Identifier{databases[1]}.Sanitize() +
				" FROM " + pgx.Identifier{roles[0]}.Sanitize()},
		{"foreign-role-membership", "GRANT " + pgx.Identifier{roles[1]}.Sanitize() +
			" TO " + pgx.Identifier{roles[0]}.Sanitize(),
			"REVOKE " + pgx.Identifier{roles[1]}.Sanitize() +
				" FROM " + pgx.Identifier{roles[0]}.Sanitize()},
		{"public-connect-regrant", "GRANT CONNECT ON DATABASE " + pgx.Identifier{databases[1]}.Sanitize() +
			" TO PUBLIC", "REVOKE CONNECT ON DATABASE " + pgx.Identifier{databases[1]}.Sanitize() + " FROM PUBLIC"},
		{"elevated-login", "ALTER ROLE " + pgx.Identifier{roles[2]}.Sanitize() + " CREATEDB",
			"ALTER ROLE " + pgx.Identifier{roles[2]}.Sanitize() + " NOCREATEDB"},
		{"superuser-login", "ALTER ROLE " + pgx.Identifier{roles[2]}.Sanitize() + " SUPERUSER",
			"ALTER ROLE " + pgx.Identifier{roles[2]}.Sanitize() + " NOSUPERUSER"},
		{"createrole-login", "ALTER ROLE " + pgx.Identifier{roles[2]}.Sanitize() + " CREATEROLE",
			"ALTER ROLE " + pgx.Identifier{roles[2]}.Sanitize() + " NOCREATEROLE"},
		{"replication-login", "ALTER ROLE " + pgx.Identifier{roles[2]}.Sanitize() + " REPLICATION",
			"ALTER ROLE " + pgx.Identifier{roles[2]}.Sanitize() + " NOREPLICATION"},
		{"bypassrls-login", "ALTER ROLE " + pgx.Identifier{roles[2]}.Sanitize() + " BYPASSRLS",
			"ALTER ROLE " + pgx.Identifier{roles[2]}.Sanitize() + " NOBYPASSRLS"},
	} {
		t.Run(test.name, func(t *testing.T) {
			if _, err := admin.Exec(ctx, test.grant); err != nil {
				t.Fatalf("test grant failed: %v", err)
			}
			// Revoke even if the negative admission assertion fails.
			defer func() {
				if _, err := admin.Exec(ctx, test.revoke); err != nil {
					t.Errorf("test privilege restoration failed: %v", err)
				}
			}()
			if _, err := databaseIdentities(ctx, d); !errors.Is(err, errNativeRoleIsolation) {
				t.Fatalf("unsafe foreign database privilege admitted: %v", err)
			}
		})
	}
	// A non-login privileged role also exposes privilege through role membership;
	// admission must reject it even when it is not one of the three DB owners.
	t.Run("unrelated-privileged-role-membership", func(t *testing.T) {
		privileged := pgx.Identifier{prefix + "_privileged"}.Sanitize()
		if _, err := admin.Exec(ctx, "CREATE ROLE "+privileged+" NOLOGIN CREATEROLE"); err != nil {
			t.Fatalf("cannot create fixture-owned privileged role: %v", err)
		}
		defer func() {
			if _, err := admin.Exec(ctx, "DROP ROLE "+privileged); err != nil {
				t.Errorf("cannot remove fixture-owned privileged role: %v", err)
			}
		}()
		member := pgx.Identifier{roles[0]}.Sanitize()
		if _, err := admin.Exec(ctx, "GRANT "+privileged+" TO "+member); err != nil {
			t.Fatalf("fixture role membership failed: %v", err)
		}
		defer func() {
			if _, err := admin.Exec(ctx, "REVOKE "+privileged+" FROM "+member); err != nil {
				t.Errorf("fixture role membership restoration failed: %v", err)
			}
		}()
		if _, err := databaseIdentities(ctx, d); !errors.Is(err, errNativeRoleIsolation) {
			t.Fatalf("privileged role membership admitted: %v", err)
		}
	})
	if _, err := databaseIdentities(ctx, d); err != nil {
		t.Fatalf("safe PostgreSQL roles not admitted after privilege restoration: %v", err)
	}

	// Edit only the private media/public roots in the original native JSON
	// contract. A typed Config round-trip through json.Marshal is not a
	// valid wire encoding: time.Duration would be emitted as integer ns,
	// while product.ParseConfig expects strings such as "15s".
	var raw map[string]any
	if err := json.Unmarshal(configBytes(d, "", ""), &raw); err != nil {
		t.Fatal(err)
	}
	sites, ok := raw["sites"].([]any)
	if !ok || len(sites) != 2 {
		t.Fatal("native two-site document missing site inventory")
	}
	for i, entry := range sites {
		site, ok := entry.(map[string]any)
		if !ok {
			t.Fatal("invalid site document type")
		}
		for _, root := range []string{"media_root", "public_root"} {
			target := filepath.Join(t.TempDir(), fmt.Sprintf("%s-%d", root, i))
			if err := os.Mkdir(target, 0700); err != nil {
				t.Fatal(err)
			}
			site[root] = target
		}
	}
	encoded, err := json.Marshal(raw)
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := product.ParseConfig(encoded)
	if err != nil {
		t.Fatalf("native config must round-trip after temp roots: %v", err)
	}
	// Product-owned migrations run in all three physically isolated databases,
	// including real AChrix and Rixa tables, not only a hand-crafted ledger.
	if err := product.Migrate(ctx, cfg, databaseLookup(d)); err != nil {
		t.Fatalf("real three-database migration failed: %v", err)
	}
	editorial, err := product.InspectEditorialMigrations(ctx, cfg, databaseLookup(d))
	if err != nil || len(editorial.Sites) != 2 || preStopEditorialDecision(editorial) != nil {
		t.Fatalf("two independent current editorial ledgers not proven: %v", err)
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	principals := map[string]string{}
	for _, site := range []struct{ id, password string }{{"site-a", d.SitePassword}, {"site-b", d.SiteBPassword}} {
		root := t.TempDir()
		if err := os.Chmod(root, 0700); err != nil {
			t.Fatal(err)
		}
		state := filepath.Join(root, "bootstrap.json")
		res, err := product.ApplyBootstrapOperation(ctx, product.BootstrapOperationRequest{
			OperationID:   d.OperationID + "-bootstrap-" + site.id,
			StatePath:     state,
			Configuration: encoded,
			Scope:         "site:" + site.id,
			Login:         "site-admin",
			Password:      site.password,
		}, databaseLookup(d), logger)
		if err != nil || res.State != product.BootstrapVerified || res.Principal == "" {
			t.Fatalf("independent %s administrator bootstrap failed: %v", site.id, err)
		}
		principals[site.id] = res.Principal
	}
	if principals["site-a"] == principals["site-b"] {
		t.Fatal("independent site identities resolved to one principal")
	}
}
