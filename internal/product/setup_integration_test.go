// SPDX-License-Identifier: MPL-2.0

package product

import (
	"context"
	"encoding/json"
	"net/url"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/AChWorks/achrix/installation"
	"github.com/jackc/pgx/v5"
)

// This proves the actual preparation effect using provider-supplied PostgreSQL,
// TLS and storage. It deliberately makes no schema, account or service claim.
func TestSetupProviderSatisfiedPreparation(t *testing.T) {
	adminDSN := os.Getenv("RIXA_TEST_POSTGRES_DSN")
	if adminDSN == "" {
		if os.Getenv("CI") != "" {
			t.Fatal("RIXA_TEST_POSTGRES_DSN is required in CI")
		}
		t.Skip("set RIXA_TEST_POSTGRES_DSN for real setup prerequisite proof")
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	dsns := createTestDatabases(t, ctx, adminDSN)
	dir := t.TempDir()
	cert, key, _ := writeTestCertificate(t, dir, []string{"control.rixa.test", "a.rixa.test", "b.rixa.test"})
	mediaA, mediaB := filepath.Join(dir, "media-a"), filepath.Join(dir, "media-b")
	for _, root := range []string{mediaA, mediaB} {
		if err := os.Mkdir(root, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	candidate := fileConfig{
		HostingProfile: HostingProfileDirectTLS, Listen: "0.0.0.0:19443",
		TLS: TLSConfig{CertFile: cert, KeyFile: key}, Language: "en",
		Control: ControlConfig{Origin: "https://control.rixa.test:19443", DatabaseEnv: "RIXA_SETUP_CONTROL", AdminPrincipal: "AAAAAAAAAAAAAAAAAAAAAAAAAA"},
		Sites: []SiteConfig{
			{ID: "site-a", Origin: "https://a.rixa.test:19443", DatabaseEnv: "RIXA_SETUP_A", MediaRoot: mediaA, AdminPrincipal: "BBBBBBBBBBBBBBBBBBBBBBBBBB"},
			{ID: "site-b", Origin: "https://b.rixa.test:19443", DatabaseEnv: "RIXA_SETUP_B", MediaRoot: mediaB, AdminPrincipal: "CCCCCCCCCCCCCCCCCCCCCCCCCC"},
		},
	}
	data, err := json.Marshal(candidate)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ParseConfig(data); err != nil {
		t.Fatalf("invalid integration fixture: %v", err)
	}
	request := SetupRequest{OperationID: "provider-config", TargetPath: privateSetupIntegrationTarget(t), Configuration: data}
	secrets := map[string]string{"RIXA_SETUP_CONTROL": dsns["control"], "RIXA_SETUP_A": dsns["a"], "RIXA_SETUP_B": dsns["b"]}
	result, err := AssessSetup(ctx, request, mapLookup(secrets))
	if err != nil || result.Plan == nil || result.Plan.Status() != installation.ChangesRequired || len(result.Plan.Actions()) != 1 {
		t.Fatalf("provider-supplied preparation: result=%+v err=%v", result, err)
	}
	entries, err := os.ReadDir(filepath.Dir(request.TargetPath))
	if err != nil || len(entries) != 0 {
		t.Fatal("read-only assessment created private state")
	}
	result, err = ApplySetup(ctx, request, mapLookup(secrets))
	if err != nil || result.State != SetupVerified || result.Scope != "configuration-preparation" {
		t.Fatalf("real config preparation: result=%+v err=%v", result, err)
	}
	installed, err := os.ReadFile(request.TargetPath)
	if err != nil || string(installed) != string(data) {
		t.Fatal("configuration was not installed exactly")
	}
	if result, err = InspectSetup(request.TargetPath, request.OperationID); err != nil || result.State != SetupVerified {
		t.Fatalf("passive retained result: result=%+v err=%v", result, err)
	}
	for _, dsn := range dsns {
		conn, connectErr := pgx.Connect(ctx, dsn)
		if connectErr != nil {
			t.Fatal("test database inspection unavailable")
		}
		var schemas int
		queryErr := conn.QueryRow(ctx, "SELECT count(*) FROM pg_namespace WHERE nspname IN ('identity','audit','media','rixa')").Scan(&schemas)
		_ = conn.Close(ctx)
		if queryErr != nil || schemas != 0 {
			t.Fatal("configuration preparation installed database schema")
		}
	}

	t.Run("physical-alias", func(t *testing.T) {
		request.OperationID, request.TargetPath = "duplicate-database", privateSetupIntegrationTarget(t)
		changed := map[string]string{"RIXA_SETUP_CONTROL": dsns["control"], "RIXA_SETUP_A": dsns["control"], "RIXA_SETUP_B": dsns["b"]}
		assertSetupBlockedWithoutEffect(t, ctx, request, mapLookup(changed), installation.UnsupportedPlan)
	})
	t.Run("missing-secret", func(t *testing.T) {
		request.OperationID, request.TargetPath = "missing-secret", privateSetupIntegrationTarget(t)
		assertSetupBlockedWithoutEffect(t, ctx, request, func(string) (string, bool) { return "", false }, installation.OperatorActionRequired)
	})
	t.Run("incompatible-durability", func(t *testing.T) {
		request.OperationID, request.TargetPath = "unsafe-commit", privateSetupIntegrationTarget(t)
		u, parseErr := url.Parse(dsns["a"])
		if parseErr != nil {
			t.Fatal("test DSN is not a URL")
		}
		query := u.Query()
		query.Set("synchronous_commit", "off")
		u.RawQuery = query.Encode()
		changed := map[string]string{"RIXA_SETUP_CONTROL": dsns["control"], "RIXA_SETUP_A": u.String(), "RIXA_SETUP_B": dsns["b"]}
		assertSetupBlockedWithoutEffect(t, ctx, request, mapLookup(changed), installation.UnsupportedPlan)
		// A later missing secret must not hide an already verified incompatibility.
		delete(changed, "RIXA_SETUP_B")
		request.OperationID, request.TargetPath = "unsafe-and-missing", privateSetupIntegrationTarget(t)
		assertSetupBlockedWithoutEffect(t, ctx, request, mapLookup(changed), installation.UnsupportedPlan)
	})
}

func assertSetupBlockedWithoutEffect(t *testing.T, ctx context.Context, request SetupRequest, lookup func(string) (string, bool), status installation.PlanStatus) {
	t.Helper()
	result, err := ApplySetup(ctx, request, lookup)
	if err != nil || result.Plan == nil || result.Plan.Status() != status || len(result.Plan.Actions()) != 0 {
		t.Fatalf("blocked preparation: result=%+v err=%v", result, err)
	}
	if _, err = os.Lstat(request.TargetPath); !os.IsNotExist(err) {
		t.Fatal("blocked prerequisite created target configuration")
	}
}

func privateSetupIntegrationTarget(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.Chmod(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	return filepath.Join(dir, "rixa.json")
}
