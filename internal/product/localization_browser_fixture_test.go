// SPDX-License-Identifier: MPL-2.0

package product

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// TestLocalizationBrowserFixture is deliberately opt-in: it holds a real supplied
// product executable while an external browser driver exercises both phases.
// It never builds/replaces that executable or touches a declared installation.
func TestLocalizationBrowserFixture(t *testing.T) {
	if os.Getenv("RIXA_BROWSER_OBSERVATION") != "1" {
		t.Skip("set RIXA_BROWSER_OBSERVATION=1 for the external real-binary browser fixture")
	}
	binary, root, dsn := os.Getenv("RIXA_BROWSER_BINARY"), os.Getenv("RIXA_BROWSER_ROOT"), os.Getenv("RIXA_TEST_POSTGRES_DSN")
	if !filepath.IsAbs(binary) || !filepath.IsAbs(root) || root == "/" || dsn == "" {
		t.Fatal("browser fixture requires an absolute RIXA_BROWSER_BINARY, a new private RIXA_BROWSER_ROOT and RIXA_TEST_POSTGRES_DSN")
	}
	originalDigest := browserExecutableDigest(t, binary)
	if err := os.Mkdir(root, 0o700); err != nil {
		t.Fatalf("browser fixture refuses an existing or unavailable root: %v", err)
	}
	info, err := os.Lstat(root)
	if err != nil || !info.IsDir() || info.Mode().Perm() != 0o700 {
		t.Fatal("browser fixture root is not a private ordinary directory")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 16*time.Minute)
	defer cancel()
	dsns := createTestDatabases(t, ctx, dsn)
	port := strconv.Itoa(freePort(t))
	cert, key, roots := writeTestCertificate(t, root, []string{"control.localhost", "a.localhost", "b.localhost"})
	t.Cleanup(func() { _ = os.Remove(key) })
	catalogRoot := filepath.Join(root, "catalogs")
	if err = os.Mkdir(catalogRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	raw := fileConfig{
		Listen: net.JoinHostPort("127.0.0.1", port),
		TLS:    TLSConfig{CertFile: cert, KeyFile: key}, Language: "en",
		StartupTimeout: "15s", ShutdownTimeout: "10s",
		Control: ControlConfig{Origin: "https://control.localhost:" + port, DatabaseEnv: "RIXA_UPGRADE_CONTROL"},
	}
	for i, name := range []string{"a", "b"} {
		media, public := filepath.Join(root, name+"-media"), filepath.Join(root, name+"-public")
		for _, path := range []string{media, public} {
			if err = os.Mkdir(path, 0o700); err != nil {
				t.Fatal(err)
			}
		}
		raw.Sites = append(raw.Sites, SiteConfig{
			ID: "site-" + name, Origin: "https://" + name + ".localhost:" + port,
			DatabaseEnv: []string{"RIXA_UPGRADE_A", "RIXA_UPGRADE_B"}[i],
			MediaRoot:   media, PublicRoot: public, PublicPolicy: testPublicPolicy(),
		})
	}
	env := []string{"PATH=" + os.Getenv("PATH"), "LANG=C"}
	for name, value := range map[string]string{"RIXA_UPGRADE_CONTROL": dsns["control"], "RIXA_UPGRADE_A": dsns["a"], "RIXA_UPGRADE_B": dsns["b"]} {
		env = append(env, name+"="+value)
	}
	configPath := filepath.Join(root, "rixa.json")
	writeBrowserConfig(t, configPath, raw, catalogRoot)
	retainedCommand(t, ctx, binary, env, "migrate", "--config", configPath)
	for i, scope := range []string{"control", "site:site-a", "site:site-b"} {
		result := retainedCommand(t, ctx, binary, append(append([]string{}, env...), "RIXA_UPGRADE_PASSWORD="+[]string{testControlPassword, testSiteAPassword, testSiteBPassword}[i]),
			"bootstrap-admin", "--config", configPath, "--scope", scope, "--login", []string{"control-admin", "site-a-admin", "site-b-admin"}[i], "--password-env", "RIXA_UPGRADE_PASSWORD")
		var account struct {
			Principal string `json:"principal"`
			Created   bool   `json:"created"`
		}
		if json.Unmarshal(result, &account) != nil || !account.Created || !accountIDSyntax.MatchString(account.Principal) {
			t.Fatal("browser fixture did not create the expected initial administrator")
		}
		if i == 0 {
			raw.Control.AdminPrincipal = account.Principal
		} else {
			raw.Sites[i-1].AdminPrincipal = account.Principal
		}
	}
	writeBrowserConfig(t, configPath, raw, catalogRoot)
	retainedCommand(t, ctx, binary, env, "check", "--config", configPath)
	h := newRetainedHTTP(t, ctx, raw.Listen, port, roots)
	phaseDir := filepath.Join(root, "initial")
	if err = os.Mkdir(phaseDir, 0o700); err != nil {
		t.Fatal(err)
	}
	stop := startRetainedBaseline(t, ctx, binary, env, configPath, phaseDir)
	waitBrowserReady(t, h)
	var sites []retainedSite
	for i, host := range []string{"a.localhost", "b.localhost"} {
		session := h.login(host, []string{"site-a-admin", "site-b-admin"}[i], []string{testSiteAPassword, testSiteBPassword}[i])
		sites = append(sites, h.seedSite(host, raw.Sites[i].ID, session, uint8(40+i*80)))
	}
	// Reject bad replacement data through the actual executable while the old
	// constructed instance remains healthy. Diagnostics must retain file context.
	invalidPath := filepath.Join(catalogRoot, "rejected.json")
	if err = os.WriteFile(invalidPath, []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	invalidCheck := exec.CommandContext(ctx, binary, "check", "--config", configPath)
	invalidCheck.Env = env
	diagnostic, invalidErr := invalidCheck.CombinedOutput()
	if invalidErr == nil || !strings.Contains(string(diagnostic), "rejected.json") {
		t.Fatal("actual executable did not reject invalid catalog with source diagnostic")
	}
	h.get("a.localhost", "/admin/content/", &sites[0].session, http.StatusOK)
	if err = os.Remove(invalidPath); err != nil {
		t.Fatal(err)
	}
	t.Log("RIXA_BROWSER_INVALID_CATALOG rejected_with_file_context=true existing_instance_available=true")
	receipt := browserFixtureReceipt{
		Schema: "rixa.localization-browser.v1", Phase: "initial", BinarySHA256: originalDigest,
		Control: raw.Control.Origin, SiteA: raw.Sites[0].Origin, SiteB: raw.Sites[1].Origin,
	}
	for _, site := range sites {
		receipt.Sites = append(receipt.Sites, browserFixtureSite{
			ContentID: site.contentID, AssetID: site.assetID, PreviewPath: "/admin/content/preview/" + site.contentID + "/2",
			PublicPath: "/retained/", AssetPath: site.assetPath,
		})
	}
	writeBrowserReceipt(t, filepath.Join(root, "ready.json"), receipt)
	t.Logf("RIXA_BROWSER_READY phase=initial receipt=%s binary_sha256=%s", filepath.Join(root, "ready.json"), originalDigest)

	// The operator installs a data-only third locale in catalogRoot, then signals.
	// The current process remains immutable until its normal bounded restart.
	waitBrowserMarker(t, ctx, filepath.Join(root, "restart.request"))
	stop()
	beforeRows := snapshotRetainedRows(t, ctx, dsns)
	beforeFiles := snapshotRetainedFiles(t, raw.Sites)
	beforeConfig, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if browserExecutableDigest(t, binary) != originalDigest {
		t.Fatal("browser fixture executable changed before data-only catalog restart")
	}
	retainedCommand(t, ctx, binary, env, "check", "--config", configPath)
	phaseDir = filepath.Join(root, "restarted")
	if err = os.Mkdir(phaseDir, 0o700); err != nil {
		t.Fatal(err)
	}
	stop = startRetainedBaseline(t, ctx, binary, env, configPath, phaseDir)
	waitBrowserReady(t, h)
	assertRetainedSnapshots(t, ctx, dsns, raw.Sites, configPath, beforeRows, beforeFiles, beforeConfig)
	for _, site := range sites {
		if !bytes.Equal(h.get(site.host, "/retained/", nil, http.StatusOK), site.public) {
			t.Fatal("public bytes changed across data-only locale restart")
		}
		if !bytes.Equal(h.get(site.host, "/admin/media/read/"+site.assetID, &site.session, http.StatusOK), site.original) {
			t.Fatal("original Media/session changed across data-only locale restart")
		}
	}
	receipt.Phase = "restarted"
	writeBrowserReceipt(t, filepath.Join(root, "ready-after-restart.json"), receipt)
	t.Logf("RIXA_BROWSER_READY phase=restarted receipt=%s binary_sha256=%s", filepath.Join(root, "ready-after-restart.json"), originalDigest)
	waitBrowserMarker(t, ctx, filepath.Join(root, "finish.request"))
	stop()
	if browserExecutableDigest(t, binary) != originalDigest {
		t.Fatal("browser fixture executable changed during acceptance")
	}
	t.Log("RIXA_BROWSER_FIXTURE unchanged_executable=true scoped_restart=true exact_rows_files_ledgers=true original_media_sessions=true public_bytes_retained=true")
}

type browserFixtureReceipt struct {
	Schema       string               `json:"schema"`
	Phase        string               `json:"phase"`
	BinarySHA256 string               `json:"binary_sha256"`
	Control      string               `json:"control"`
	SiteA        string               `json:"site_a"`
	SiteB        string               `json:"site_b"`
	Sites        []browserFixtureSite `json:"sites"`
}

type browserFixtureSite struct {
	ContentID   string `json:"content_id"`
	AssetID     string `json:"asset_id"`
	PreviewPath string `json:"preview_path"`
	PublicPath  string `json:"public_path"`
	AssetPath   string `json:"asset_path"`
}

func writeBrowserConfig(t *testing.T, path string, raw fileConfig, catalogs string) {
	t.Helper()
	// The outer explicit property works with the source-version fileConfig and
	// passes catalog_root through the actual executable's strict JSON parser.
	value := struct {
		fileConfig
		CatalogRoot string `json:"catalog_root"`
	}{raw, catalogs}
	data, err := json.Marshal(value)
	if err != nil || os.WriteFile(path, data, 0o600) != nil {
		t.Fatal("browser fixture configuration write failed")
	}
}

func writeBrowserReceipt(t *testing.T, path string, value browserFixtureReceipt) {
	t.Helper()
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	_, writeErr := file.Write(append(data, '\n'))
	if err = file.Close(); writeErr != nil || err != nil {
		t.Fatal("browser fixture receipt write failed")
	}
}

func browserExecutableDigest(t *testing.T, path string) string {
	t.Helper()
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0o111 == 0 {
		t.Fatal("browser fixture requires one existing ordinary executable")
	}
	file, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	hash := sha256.New()
	n, err := io.Copy(hash, io.LimitReader(file, 64<<20))
	if err != nil || n == 64<<20 {
		t.Fatal("browser fixture executable read failed or exceeded its bound")
	}
	return hex.EncodeToString(hash.Sum(nil))
}

func waitBrowserReady(t *testing.T, h retainedHTTP) {
	t.Helper()
	deadline := time.Now().Add(12 * time.Second)
	for {
		response, err := h.request("a.localhost", http.MethodGet, "/admin/login", "", nil, nil)
		if err == nil {
			_ = response.Body.Close()
			if response.StatusCode != http.StatusOK {
				t.Fatalf("browser fixture readiness status=%d", response.StatusCode)
			}
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("browser fixture HTTPS readiness failed: %v", err)
		}
		time.Sleep(100 * time.Millisecond)
	}
}

func waitBrowserMarker(t *testing.T, ctx context.Context, path string) {
	t.Helper()
	ticker := time.NewTicker(200 * time.Millisecond)
	defer ticker.Stop()
	for {
		info, err := os.Lstat(path)
		if err == nil {
			if !info.Mode().IsRegular() || info.Size() != 0 {
				t.Fatalf("browser fixture marker must be an empty ordinary file: %s", filepath.Base(path))
			}
			return
		}
		if !os.IsNotExist(err) {
			t.Fatalf("browser fixture marker lookup failed: %v", err)
		}
		select {
		case <-ctx.Done():
			t.Fatal(fmt.Errorf("browser fixture timed out waiting for %s: %w", filepath.Base(path), ctx.Err()))
		case <-ticker.C:
		}
	}
}
