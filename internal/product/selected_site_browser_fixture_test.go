// SPDX-License-Identifier: MPL-2.0

package product

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

// This explicit opt-in fixture serves only an already built supplied executable.
// Its test databases/files are unique; the caller owns the PostgreSQL service.
func TestSelectedSiteBrowserFixture(t *testing.T) {
	if os.Getenv("RIXA_SELECTED_SITE_OBSERVATION") != "1" {
		t.Skip("set RIXA_SELECTED_SITE_OBSERVATION=1 for the real selected-site browser fixture")
	}
	runSelectedSiteBrowserFixture(t, false)
}

// The publication variant keeps the same real login, PostgreSQL and TLS
// environment, but authorizes exactly one selected site's public generation
// to change. It is separately opt-in from the established draft-only proof.
func TestSelectedSitePublicationBrowserFixture(t *testing.T) {
	if os.Getenv("RIXA_SELECTED_SITE_PUBLICATION_OBSERVATION") != "1" {
		t.Skip("set RIXA_SELECTED_SITE_PUBLICATION_OBSERVATION=1 for the real publication browser proof")
	}
	runSelectedSiteBrowserFixture(t, true)
}

func runSelectedSiteBrowserFixture(t *testing.T, publication bool) {
	t.Helper()
	binary, root, dsn := os.Getenv("RIXA_SELECTED_SITE_BINARY"), os.Getenv("RIXA_SELECTED_SITE_ROOT"), os.Getenv("RIXA_TEST_POSTGRES_DSN")
	if !filepath.IsAbs(binary) || !filepath.IsAbs(root) || root == "/" || dsn == "" {
		t.Fatal("selected-site fixture requires an absolute executable, new private root and disposable PostgreSQL DSN")
	}
	digest := browserExecutableDigest(t, binary)
	if err := os.Mkdir(root, 0o700); err != nil {
		t.Fatalf("selected-site fixture refuses existing/unavailable root: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Minute)
	defer cancel()
	dsns := createTestDatabases(t, ctx, dsn)
	port := strconv.Itoa(freePort(t))
	cert, key, roots := writeTestCertificate(t, root, []string{"control.localhost", "a.localhost", "b.localhost"})
	t.Cleanup(func() { _ = os.Remove(key) })
	catalogRoot := filepath.Join(root, "catalogs")
	if err := os.Mkdir(catalogRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	// The executable already exists and is hashed before this data-only pack is
	// installed. This fixture does not embed a third product default language.
	pack, err := os.ReadFile(filepath.Join("..", "..", "scripts", "lab", "localization-third.json"))
	if err != nil || os.WriteFile(filepath.Join(catalogRoot, "fixture.ar.json"), pack, 0o600) != nil {
		t.Fatal("selected-site fixture could not install the test-only catalog")
	}
	raw := fileConfig{
		Listen: net.JoinHostPort("127.0.0.1", port), TLS: TLSConfig{CertFile: cert, KeyFile: key},
		Language: "en", StartupTimeout: "15s", ShutdownTimeout: "10s",
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
		output := retainedCommand(t, ctx, binary, append(append([]string{}, env...), "RIXA_UPGRADE_PASSWORD="+[]string{testControlPassword, testSiteAPassword, testSiteBPassword}[i]),
			"bootstrap-admin", "--config", configPath, "--scope", scope, "--login", []string{"control-admin", "site-a-admin", "site-b-admin"}[i], "--password-env", "RIXA_UPGRADE_PASSWORD")
		var account struct {
			Principal string `json:"principal"`
			Created   bool   `json:"created"`
		}
		if json.Unmarshal(output, &account) != nil || !account.Created || !accountIDSyntax.MatchString(account.Principal) {
			t.Fatal("selected-site fixture bootstrap identity mismatch")
		}
		if i == 0 {
			raw.Control.AdminPrincipal = account.Principal
		} else {
			raw.Sites[i-1].AdminPrincipal = account.Principal
		}
	}
	writeBrowserConfig(t, configPath, raw, catalogRoot)
	phaseDir := filepath.Join(root, "runtime")
	if err = os.Mkdir(phaseDir, 0o700); err != nil {
		t.Fatal(err)
	}
	stop := startRetainedBaseline(t, ctx, binary, env, configPath, phaseDir)
	h := newRetainedHTTP(t, ctx, raw.Listen, port, roots)
	waitBrowserReady(t, h)
	seedJournal, err := os.OpenFile(filepath.Join(root, "seed-attempts.jsonl"), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = seedJournal.Close() })
	h.client.Transport = selectedSiteSeedTransport{next: h.client.Transport, journal: seedJournal}
	post := func(host, path string, session retainedSession, body map[string]string) []byte {
		t.Helper()
		return h.post(host, path, session, body, http.StatusOK)
	}
	receipt := selectedSiteFixtureReceipt{
		Schema: "rixa.selected-site-browser.v1", BinarySHA256: digest, Control: raw.Control.Origin,
		SiteA: raw.Sites[0].Origin, SiteB: raw.Sites[1].Origin,
	}
	if publication {
		receipt.Schema = "rixa.selected-site-publication-browser.v1"
	}
	for i, host := range []string{"a.localhost", "b.localhost"} {
		session := h.login(host, []string{"site-a-admin", "site-b-admin"}[i], []string{testSiteAPassword, testSiteBPassword}[i])
		site := h.seedSite(host, raw.Sites[i].ID, session, uint8(40+i*80))
		for n := 0; n < 101; n++ {
			post(host, "/admin/content/create", session, map[string]string{
				"operation_id": newOperationID(), "kind": "post", "title": fmt.Sprintf("Pagination %s %03d فارسی", raw.Sites[i].ID, n),
				"body_html": "<p>Private pagination fixture.</p>",
			})
		}
		for revision := 3; revision <= 56; revision++ {
			post(host, "/admin/content/save", session, map[string]string{
				"operation_id": newOperationID(), "id": site.contentID, "expected_head": strconv.Itoa(revision - 1),
				"title":     "Selected " + raw.Sites[i].ID + " فارسی English",
				"body_html": fmt.Sprintf("<p>Private selected revision %d فارسی English.</p><p><a href=\"/relative-proof/\">Scoped relative link</a></p>", revision),
			})
		}
		receipt.Sites = append(receipt.Sites, selectedSiteFixtureSite{
			ID: raw.Sites[i].ID, Origin: raw.Sites[i].Origin, ContentID: site.contentID, AssetID: site.assetID,
			Head: "56", ContentCount: 102, HistoryCount: 56,
		})
	}
	if err = seedJournal.Close(); err != nil {
		t.Fatal(err)
	}
	beforeRows := snapshotRetainedRows(t, ctx, dsns)
	beforeFiles := snapshotRetainedFiles(t, raw.Sites)
	beforeConfig, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	data, err := json.MarshalIndent(receipt, "", "  ")
	if err != nil || os.WriteFile(filepath.Join(root, "ready.json"), append(data, '\n'), 0o600) != nil {
		t.Fatal("selected-site readiness receipt failed")
	}
	t.Logf("RIXA_SELECTED_SITE_READY receipt=%s binary_sha256=%s contents_per_site=102 selected_revisions=56", filepath.Join(root, "ready.json"), digest)
	waitBrowserMarker(t, ctx, filepath.Join(root, "finish.request"))
	stop()
	if browserExecutableDigest(t, binary) != digest {
		t.Fatal("selected-site browser acceptance changed its executable")
	}
	afterFiles := snapshotRetainedFiles(t, raw.Sites)
	afterRows := snapshotRetainedRows(t, ctx, dsns)
	if publication {
		assertSelectedPublicationBrowserIsolation(t, ctx, dsns, root, beforeFiles, afterFiles, beforeRows, afterRows)
	} else {
		if !equalSelectedSiteFiles(beforeFiles, afterFiles) {
			t.Fatal("draft browser journey changed Media originals or static publication files")
		}
		for table, before := range beforeRows {
			// Only session/audit and source-editorial effects are accepted here.
			if selectedSiteMutableTable(table) {
				continue
			}
			if afterRows[table] != before {
				t.Fatalf("draft browser journey changed an out-of-scope table: %s", table)
			}
		}
	}
	afterConfig, err := os.ReadFile(configPath)
	if err != nil || !bytes.Equal(beforeConfig, afterConfig) {
		t.Fatal("draft browser journey changed product configuration")
	}
	if publication {
		t.Log("RIXA_SELECTED_PUBLICATION_BROWSER unchanged_binary_config=true site_b_public_private_and_data_unchanged=true site_a_media_unchanged=true site_a_public_generation_changed=true")
	} else {
		assertSelectedSiteAttemptIsolation(t, ctx, dsns, filepath.Join(root, "selected-site-attempts.jsonl"))
		t.Log("RIXA_SELECTED_SITE_BROWSER unchanged_binary=true private_originals_static_config_ledgers_accounts=true attempted_operations_scoped_deduplicated=true")
	}
}

func assertSelectedPublicationBrowserIsolation(t *testing.T, ctx context.Context, dsns map[string]string, root string, beforeFiles, afterFiles, beforeRows, afterRows map[string]string) {
	t.Helper()
	// All site-B and all private originals must remain exactly unchanged,
	// including absence of new files. Site-A alone may activate new generations.
	for _, fileSet := range []map[string]string{beforeFiles, afterFiles} {
		for key := range fileSet {
			if !strings.HasPrefix(key, "site-b/") && !strings.HasPrefix(key, "site-a/media/") {
				continue
			}
			if beforeFiles[key] != afterFiles[key] {
				t.Fatalf("publication browser changed a protected file: %s", key)
			}
		}
	}
	changedPublic := false
	for key, before := range beforeFiles {
		if strings.HasPrefix(key, "site-a/public/") && afterFiles[key] != before {
			changedPublic = true
		}
	}
	for key := range afterFiles {
		if strings.HasPrefix(key, "site-a/public/") && beforeFiles[key] != afterFiles[key] {
			changedPublic = true
		}
	}
	if !changedPublic {
		t.Fatal("publication browser did not activate a new site-A public generation")
	}
	for table, before := range beforeRows {
		// All site-B tables, including identity sessions and audit, must remain
		// untouched. Only the active control session/audit and site-A
		// publication/editorial/session/audit state may legitimately change.
		if !strings.HasPrefix(table, "b/") &&
			(strings.HasSuffix(table, "/\"identity\".\"sessions\"") ||
				strings.HasSuffix(table, "/\"audit\".\"records\"") ||
				strings.HasPrefix(table, "a/\"rixa\".")) {
			continue
		}
		if afterRows[table] != before {
			t.Fatalf("publication browser changed a protected table: %s", table)
		}
	}
	operations, err := readSelectedPublicationJournal(filepath.Join(root, "publication-attempts.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	for _, operationID := range operations {
		conn, e := pgx.Connect(ctx, dsns["b"])
		if e != nil {
			t.Fatal(e)
		}
		var count int
		e = conn.QueryRow(ctx, "SELECT count(*) FROM rixa.operations WHERE operation_id=$1", operationID).Scan(&count)
		_ = conn.Close(ctx)
		if e != nil || count != 0 {
			t.Fatal("site-A browser mutation escaped to site-B editorial operations")
		}
	}
}

type selectedSiteFixtureReceipt struct {
	Schema       string                    `json:"schema"`
	BinarySHA256 string                    `json:"binary_sha256"`
	Control      string                    `json:"control"`
	SiteA        string                    `json:"site_a"`
	SiteB        string                    `json:"site_b"`
	Sites        []selectedSiteFixtureSite `json:"sites"`
}
type selectedSiteFixtureSite struct {
	ID           string `json:"id"`
	Origin       string `json:"origin"`
	ContentID    string `json:"content_id"`
	AssetID      string `json:"asset_id"`
	Head         string `json:"head"`
	ContentCount int    `json:"content_count"`
	HistoryCount int    `json:"history_count"`
}

func equalSelectedSiteFiles(a, b map[string]string) bool {
	if len(a) != len(b) {
		return false
	}
	for key, value := range a {
		if b[key] != value {
			return false
		}
	}
	return true
}
func selectedSiteMutableTable(table string) bool {
	for _, suffix := range []string{"/\"identity\".\"sessions\"", "/\"audit\".\"records\"", "/\"rixa\".\"content_items\"", "/\"rixa\".\"content_revisions\"", "/\"rixa\".\"content_media_refs\"", "/\"rixa\".\"operations\""} {
		if strings.HasSuffix(table, suffix) {
			return true
		}
	}
	return false
}

func assertSelectedSiteAttemptIsolation(t *testing.T, ctx context.Context, dsns map[string]string, path string) {
	t.Helper()
	file, err := os.Open(path)
	if err != nil {
		t.Fatal("browser attempt journal missing; browser execution is required")
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || info.Size() >= 2<<20 {
		t.Fatal("browser attempt journal exceeded its bound")
	}
	scanner := bufio.NewScanner(io.LimitReader(file, 2<<20))
	scanner.Buffer(make([]byte, 4096), 128<<10)
	type attempt struct {
		Type    string `json:"type"`
		SiteID  string `json:"site_id"`
		Payload string `json:"payload"`
	}
	seen := map[string]string{}
	complete := false
	for scanner.Scan() {
		var record attempt
		if json.Unmarshal(scanner.Bytes(), &record) != nil {
			t.Fatal("invalid private attempt journal")
		}
		if record.Type == "complete" {
			complete = true
		}
		if record.Type != "mutation" {
			continue
		}
		var payload struct {
			OperationID string `json:"operation_id"`
		}
		if json.Unmarshal([]byte(record.Payload), &payload) != nil || !validOperationID(payload.OperationID) {
			t.Fatal("browser journal mutation identity invalid")
		}
		key := record.SiteID + "/" + payload.OperationID
		if previous, exists := seen[key]; exists && previous != record.Payload {
			t.Fatal("pending browser mutation payload changed under the same operation ID")
		}
		seen[key] = record.Payload
	}
	if scanner.Err() != nil || len(seen) < 4 || !complete {
		t.Fatal("browser attempt journal incomplete or exceeded its bound")
	}
	for key := range seen {
		site, operation, ok := strings.Cut(key, "/")
		if !ok || (site != "site-a" && site != "site-b") {
			t.Fatal("browser attempt journal site invalid")
		}
		for name, dsn := range map[string]string{"site-a": dsns["a"], "site-b": dsns["b"]} {
			conn, err := pgx.Connect(ctx, dsn)
			if err != nil {
				t.Fatal(err)
			}
			var count int
			err = conn.QueryRow(ctx, "SELECT count(*) FROM rixa.operations WHERE operation_id=$1", operation).Scan(&count)
			_ = conn.Close(ctx)
			if err != nil || count > 1 || (name != site && count != 0) {
				t.Fatal("browser operation duplicated or escaped its bound site")
			}
		}
	}
}

// Capture the existing seeding helper's generated operation identities as well
// as the additional pagination/history writes before their real HTTP effect.
// Authentication endpoints/headers are deliberately outside this journal.
type selectedSiteSeedTransport struct {
	next    http.RoundTripper
	journal *os.File
}

func (s selectedSiteSeedTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	path := request.URL.Path
	if request.Method != http.MethodPost || !(strings.HasPrefix(path, "/admin/content/") || path == "/admin/appearance/save" || path == "/admin/media/upload") {
		return s.next.RoundTrip(request)
	}
	if request.GetBody == nil {
		return nil, fmt.Errorf("seed request cannot be journaled before effect")
	}
	body, err := request.GetBody()
	if err != nil {
		return nil, err
	}
	data, err := io.ReadAll(io.LimitReader(body, 128<<10))
	_ = body.Close()
	if err != nil || len(data) == 128<<10 {
		return nil, fmt.Errorf("seed request exceeded journal bound")
	}
	record := map[string]any{"host": request.URL.Host, "path": path}
	if request.Header.Get("Content-Type") == "application/json" {
		record["payload"] = string(data)
	} else {
		// Media admission has no caller operation ID. Bind its one non-replayed
		// upload to exact bytes without copying private binary data into JSON.
		record["body_sha256"] = fmt.Sprintf("%x", sha256.Sum256(data))
		record["body_bytes"] = len(data)
	}
	encoded, err := json.Marshal(record)
	if err != nil {
		return nil, err
	}
	if _, err = s.journal.Write(append(encoded, '\n')); err != nil {
		return nil, err
	}
	if err = s.journal.Sync(); err != nil {
		return nil, err
	}
	return s.next.RoundTrip(request)
}
