// SPDX-License-Identifier: MPL-2.0

package product

import (
	"bytes"
	"context"
	"encoding/json"
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
		Schema       string   `json:"schema"`
		BinarySHA256 string   `json:"binary_sha256"`
		Checks       []string `json:"checks"`
	}
	if json.Unmarshal(resultBytes, &result) != nil ||
		result.Schema != receipt["schema"] || result.BinarySHA256 != digest || len(result.Checks) < 8 {
		t.Fatal("browser result missing actual acceptance checks")
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
	verifyControlBrowserAccounts(t, ctx, dsns)
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
		if scope == "a" || scope == "b" {
			allow = allow || strings.Contains(table, "\"identity\".\"accounts\"") ||
				strings.Contains(table, "\"identity\".\"credentials\"")
		}
		if !allow && afterRows[key] != before {
			t.Fatalf("browser modified out-of-scope DB table %s", key)
		}
	}
	t.Log("RIXA_CONTROL_ACCOUNT_BROWSER real_https_postgres=true scope_isolated=true result_success_conflict_unknown=true unchanged_media_public_config=true")
}

func verifyControlBrowserAccounts(t *testing.T, ctx context.Context, dsns map[string]string) {
	t.Helper()
	for _, scope := range []string{"control", "a", "b"} {
		conn, err := pgx.Connect(ctx, dsns[scope])
		if err != nil {
			t.Fatal(err)
		}
		for _, test := range []struct {
			login string
			count int
		}{
			{"browser-account-a", map[string]int{"control": 0, "a": 1, "b": 0}[scope]},
			{"browser-account-b", map[string]int{"control": 0, "a": 0, "b": 1}[scope]},
		} {
			var count int
			err = conn.QueryRow(ctx, "SELECT count(*) FROM identity.accounts WHERE login=$1", test.login).Scan(&count)
			if err != nil || count != test.count {
				t.Errorf("account-browser cross-site/duplicate login %s/%s: count=%d err=%v", scope, test.login, count, err)
			}
		}
		_ = conn.Close(ctx)
	}
}
