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
	for _, database := range databases {
		if _, err := admin.Exec(ctx, "CREATE DATABASE "+database+" TEMPLATE template0 ENCODING 'UTF8'"); err != nil {
			t.Fatal("cannot create a separate disposable database")
		}
		t.Cleanup(func() {
			cleanup, finish := context.WithTimeout(context.Background(), 20*time.Second)
			defer finish()
			db, err := pgx.Connect(cleanup, adminDSN)
			if err != nil {
				t.Error("cannot open database cleanup connection")
				return
			}
			defer db.Close(context.Background())
			if _, err := db.Exec(cleanup, "DROP DATABASE "+database+" WITH (FORCE)"); err != nil {
				t.Errorf("cleanup of owned test database failed")
			}
		})
	}
	dsn := func(name string) string {
		u, err := url.Parse(adminDSN)
		if err != nil {
			t.Fatal("bad test PG URL")
		}
		u.Path = "/" + name
		return u.String()
	}
	d := validTwoSiteDraft()
	d.ControlDSN, d.SiteDSN, d.SiteBDSN = dsn(databases[0]), dsn(databases[1]), dsn(databases[2])
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
		state := filepath.Join(t.TempDir(), "bootstrap.json")
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
