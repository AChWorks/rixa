// SPDX-License-Identifier: MPL-2.0

package product

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

// This opt-in fixture serves a pinned Rixa binary with three unique disposable
// PostgreSQL databases. It never deploys, provisions or changes a real site.
func TestControlAccountBrowserFixture(t *testing.T) {
	if os.Getenv("RIXA_CONTROL_ACCOUNT_OBSERVATION") != "1" {
		t.Skip("set RIXA_CONTROL_ACCOUNT_OBSERVATION=1 for real Chromium/PostgreSQL acceptance")
	}
	binary := os.Getenv("RIXA_CONTROL_ACCOUNT_BINARY")
	root := os.Getenv("RIXA_CONTROL_ACCOUNT_ROOT")
	dsn := os.Getenv("RIXA_TEST_POSTGRES_DSN")
	if !filepath.IsAbs(binary) || !filepath.IsAbs(root) || root == "/" || dsn == "" {
		t.Fatal("requires an absolute built binary, new private root and disposable PostgreSQL admin DSN")
	}
	digest := browserExecutableDigest(t, binary)
	if err := os.Mkdir(root, 0o700); err != nil {
		t.Fatalf("refusing to reuse account-browser fixture root: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 12*time.Minute)
	defer cancel()
	dsns := createTestDatabases(t, ctx, dsn)
	port := strconv.Itoa(freePort(t))
	cert, key, roots := writeTestCertificate(t, root, []string{"control.localhost", "a.localhost", "b.localhost"})
	t.Cleanup(func() { _ = os.Remove(key) })
	catalogRoot := filepath.Join(root, "catalogs")
	if err := os.Mkdir(catalogRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	raw := fileConfig{
		Listen:   net.JoinHostPort("127.0.0.1", port),
		TLS:      TLSConfig{CertFile: cert, KeyFile: key},
		Language: "en", StartupTimeout: "15s", ShutdownTimeout: "10s",
		Control: ControlConfig{Origin: "https://control.localhost:" + port, DatabaseEnv: "RIXA_UPGRADE_CONTROL"},
	}
	for i, name := range []string{"a", "b"} {
		mediaRoot := filepath.Join(root, name+"-media")
		publicRoot := filepath.Join(root, name+"-public")
		for _, path := range []string{mediaRoot, publicRoot} {
			if err := os.Mkdir(path, 0o700); err != nil {
				t.Fatal(err)
			}
		}
		raw.Sites = append(raw.Sites, SiteConfig{
			ID: "site-" + name, Origin: "https://" + name + ".localhost:" + port,
			DatabaseEnv: []string{"RIXA_UPGRADE_A", "RIXA_UPGRADE_B"}[i],
			MediaRoot:   mediaRoot, PublicRoot: publicRoot, PublicPolicy: testPublicPolicy(),
		})
	}
	raw.Sites = append(raw.Sites, SiteConfig{
		ID: "disabled", Origin: "https://disabled.localhost:" + port,
		DatabaseEnv: "RIXA_DISABLED", MediaRoot: filepath.Join(root, "disabled-media"), Disabled: true,
	})
	env := []string{"PATH=" + os.Getenv("PATH"), "LANG=C"}
	for name, value := range map[string]string{
		"RIXA_UPGRADE_CONTROL": dsns["control"],
		"RIXA_UPGRADE_A":       dsns["a"],
		"RIXA_UPGRADE_B":       dsns["b"],
	} {
		env = append(env, name+"="+value)
	}
	configPath := filepath.Join(root, "rixa.json")
	writeBrowserConfig(t, configPath, raw, catalogRoot)
	retainedCommand(t, ctx, binary, env, "migrate", "--config", configPath)
	for i, scope := range []string{"control", "site:site-a", "site:site-b"} {
		output := retainedCommand(t, ctx, binary, append(append([]string{}, env...), "RIXA_UPGRADE_PASSWORD="+[]string{testControlPassword, testSiteAPassword, testSiteBPassword}[i]),
			"bootstrap-admin", "--config", configPath, "--scope", scope,
			"--login", []string{"control-admin", "site-a-admin", "site-b-admin"}[i],
			"--password-env", "RIXA_UPGRADE_PASSWORD")
		var account map[string]any
		if json.Unmarshal(output, &account) != nil || account["created"] != true {
			t.Fatal("real account-browser fixture bootstrap failed")
		}
		id, ok := account["principal"].(string)
		if !ok || !accountIDSyntax.MatchString(id) {
			t.Fatal("real account-browser fixture principal is invalid")
		}
		if i == 0 {
			raw.Control.AdminPrincipal = id
		} else {
			raw.Sites[i-1].AdminPrincipal = id
		}
	}
	writeBrowserConfig(t, configPath, raw, catalogRoot)
	retainedCommand(t, ctx, binary, env, "check", "--config", configPath)
	baseline := snapshotRetainedRows(t, ctx, dsns)
	identityBefore := snapshotControlIdentity(t, ctx, dsns)
	files := snapshotRetainedFiles(t, raw.Sites[:2])
	configBytes, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	phase := filepath.Join(root, "runtime")
	if err := os.Mkdir(phase, 0o700); err != nil {
		t.Fatal(err)
	}
	stop := startRetainedBaseline(t, ctx, binary, env, configPath, phase)
	httpClient := newRetainedHTTP(t, ctx, raw.Listen, port, roots)
	waitBrowserReady(t, httpClient)
	receipt := map[string]string{
		"schema": "rixa.control-account-browser.v1", "binary_sha256": digest,
		"control": raw.Control.Origin, "site_a": raw.Sites[0].Origin, "site_b": raw.Sites[1].Origin,
	}
	ready, err := json.MarshalIndent(receipt, "", "  ")
	if err != nil || os.WriteFile(filepath.Join(root, "ready.json"), append(ready, '\n'), 0o600) != nil {
		t.Fatal("cannot publish private account-browser readiness")
	}
	t.Logf("RIXA_CONTROL_ACCOUNT_READY receipt=%s sha256=%s", filepath.Join(root, "ready.json"), digest)
	waitBrowserMarker(t, ctx, filepath.Join(root, "finish.request"))
	resultBytes, err := os.ReadFile(filepath.Join(root, "result.json"))
	if err != nil || len(resultBytes) > 4096 {
		t.Fatal("browser result absent or oversized")
	}
	var result struct {
		Schema           string            `json:"schema"`
		BinarySHA256     string            `json:"binary_sha256"`
		Checks           []string          `json:"checks"`
		AccountIDs       map[string]string `json:"account_ids"`
		MutationAttempts map[string]int    `json:"mutation_attempts"`
		InjectedCommits  int               `json:"injected_commits"`
	}
	if json.Unmarshal(resultBytes, &result) != nil ||
		result.Schema != receipt["schema"] || result.BinarySHA256 != digest ||
		!exactControlAccountChecks(result.Checks) ||
		len(result.AccountIDs) != 2 || !accountIDSyntax.MatchString(result.AccountIDs["site-a"]) ||
		!accountIDSyntax.MatchString(result.AccountIDs["site-b"]) ||
		len(result.MutationAttempts) != 2 || result.MutationAttempts["browser-account-a"] != 2 ||
		result.MutationAttempts["browser-account-b"] != 1 || result.InjectedCommits != 1 {
		t.Fatal("browser result missing exact checks or persisted identity references")
	}
	stop()
	if browserExecutableDigest(t, binary) != digest {
		t.Fatal("browser altered its pinned Rixa binary")
	}
	if after, err := os.ReadFile(configPath); err != nil || !bytes.Equal(configBytes, after) {
		t.Fatal("account-browser changed product configuration")
	}
	if !equalSelectedSiteFiles(files, snapshotRetainedFiles(t, raw.Sites[:2])) {
		t.Fatal("account-browser changed Media originals or publication files")
	}
	verifyControlBrowserAccounts(t, ctx, dsns, identityBefore, result.AccountIDs, map[string]string{
		"control": raw.Control.AdminPrincipal,
		"a":       raw.Sites[0].AdminPrincipal,
		"b":       raw.Sites[1].AdminPrincipal,
	})
	afterRows := snapshotRetainedRows(t, ctx, dsns)
	if len(afterRows) != len(baseline) {
		t.Fatal("browser changed the database table inventory")
	}
	for key, before := range baseline {
		scope, table, ok := strings.Cut(key, "/")
		if !ok {
			t.Fatal("invalid retained snapshot key")
		}
		// Only independent account/credential creation, login sessions and
		// their own atomic Audit records are allowed to change.
		allow := strings.Contains(table, "\"identity\".\"sessions\"") || strings.Contains(table, "\"audit\".\"records\"")
		// The whole-table hashes include the expected new rows; their precise
		// deltas (including unchanged administrator credentials) are verified
		// by verifyControlBrowserAccounts.
		if scope != "control" && (table == "\"identity\".\"accounts\"" || table == "\"identity\".\"credentials\"") {
			continue
		}
		if !allow && afterRows[key] != before {
			t.Fatalf("browser modified out-of-scope DB table %s", key)
		}
	}
	t.Log("RIXA_CONTROL_ACCOUNT_BROWSER real_https_postgres=true scope_isolated=true result_success_conflict_unknown=true unchanged_media_public_config=true")
}

// Check all unique required labels, in the expected order.
func exactControlAccountChecks(actual []string) bool {
	expected := []string{
		"control-login-cookie-scope",
		"site-list-explicit-selection",
		"en-ltr-keyboard-responsive",
		"site-a-create-acknowledged",
		"duplicate-conflict-no-unknown-replay",
		"fa-rtl-selected-site",
		"site-b-committed-lost-ack-no-replay",
		"identity-reconciliation-and-cross-site-denial",
		"disabled-unknown-forged-site-denials",
		"no-browser-errors-no-webstorage",
	}
	if len(actual) != len(expected) {
		return false
	}
	for i := range expected {
		if actual[i] != expected[i] {
			return false
		}
	}
	return true
}

// Captures each entire row, keyed by its immutable account ID. Only a digest
// of the credential row is retained; passwords/hashes never enter CI logs or
// browser receipts.
type controlIdentityRows struct {
	accounts    map[string][32]byte
	credentials map[string][32]byte
}

func snapshotControlIdentity(t *testing.T, ctx context.Context, dsns map[string]string) map[string]controlIdentityRows {
	t.Helper()
	result := make(map[string]controlIdentityRows, len(dsns))
	for _, scope := range []string{"control", "a", "b"} {
		conn, err := pgx.Connect(ctx, dsns[scope])
		if err != nil {
			t.Fatalf("Identity snapshot connection unavailable: %s: %v", scope, err)
		}
		read := func(table, idColumn string) map[string][32]byte {
			t.Helper()
			rows, err := conn.Query(ctx, fmt.Sprintf("SELECT %s,row_to_json(t)::text FROM identity.%s t", idColumn, table))
			if err != nil {
				t.Fatalf("Identity snapshot query %s/%s: %v", scope, table, err)
			}
			defer rows.Close()
			values := make(map[string][32]byte)
			for rows.Next() {
				var id, serialized string
				if err = rows.Scan(&id, &serialized); err != nil || id == "" {
					t.Fatalf("Identity snapshot scan %s/%s: %v", scope, table, err)
				}
				if _, exists := values[id]; exists {
					t.Fatalf("duplicate Identity row key in %s/%s", scope, table)
				}
				values[id] = sha256.Sum256([]byte(serialized))
			}
			if err = rows.Err(); err != nil {
				t.Fatalf("Identity snapshot iteration %s/%s: %v", scope, table, err)
			}
			return values
		}
		result[scope] = controlIdentityRows{
			accounts:    read("accounts", "id"),
			credentials: read("credentials", "account_id"),
		}
		_ = conn.Close(ctx)
	}
	return result
}

func verifyControlBrowserAccounts(t *testing.T, ctx context.Context, dsns map[string]string, before map[string]controlIdentityRows, expectedIDs, adminIDs map[string]string) {
	t.Helper()
	after := snapshotControlIdentity(t, ctx, dsns)
	for _, scope := range []string{"control", "a", "b"} {
		original, current := before[scope], after[scope]
		adminID := adminIDs[scope]
		if len(original.accounts) != 1 || len(original.credentials) != 1 ||
			!accountIDSyntax.MatchString(adminID) ||
			len(original.accounts) != len(original.credentials) ||
			original.accounts[adminID] == ([32]byte{}) || original.credentials[adminID] == ([32]byte{}) {
			t.Fatalf("Identity fixture expected one complete bootstrap administrator in %s", scope)
		}
		insertedID := ""
		newLogin := ""
		if scope == "a" {
			insertedID, newLogin = expectedIDs["site-a"], "browser-account-a"
		}
		if scope == "b" {
			insertedID, newLogin = expectedIDs["site-b"], "browser-account-b"
		}
		added := 0
		if insertedID != "" {
			added = 1
		}
		if len(current.accounts) != len(original.accounts)+added ||
			len(current.credentials) != len(original.credentials)+added {
			t.Fatalf("unexpected Identity account/credential row count in %s", scope)
		}
		for id, digest := range original.accounts {
			if current.accounts[id] != digest {
				t.Fatalf("existing Identity account row changed in %s", scope)
			}
		}
		for id, digest := range original.credentials {
			if current.credentials[id] != digest {
				t.Fatalf("existing Identity credential row changed in %s", scope)
			}
		}
		for id := range current.accounts {
			if _, exists := original.accounts[id]; !exists && id != insertedID {
				t.Fatalf("unexpected new Identity account in %s", scope)
			}
		}
		for id := range current.credentials {
			if _, exists := original.credentials[id]; !exists && id != insertedID {
				t.Fatalf("unexpected new Identity credential in %s", scope)
			}
		}
		if insertedID == "" {
			continue
		}
		if insertedID == adminID || current.accounts[insertedID] == ([32]byte{}) ||
			current.credentials[insertedID] == ([32]byte{}) {
			t.Fatalf("browser acknowledgment/commit ID not present as exactly one account+credential in %s", scope)
		}
		conn, err := pgx.Connect(ctx, dsns[scope])
		if err != nil {
			t.Fatal(err)
		}
		var gotLogin string
		var enabled bool
		var revision int64
		err = conn.QueryRow(ctx, "SELECT login,enabled,revision FROM identity.accounts WHERE id=$1", insertedID).
			Scan(&gotLogin, &enabled, &revision)
		_ = conn.Close(ctx)
		if err != nil || gotLogin != newLogin || !enabled || revision != 1 {
			t.Fatalf("acknowledged Identity ID was not the expected enabled new account in %s: %v", scope, err)
		}
	}
}
