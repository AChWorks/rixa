// SPDX-License-Identifier: MPL-2.0

package product

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"io"
	"io/fs"
	"mime/multipart"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/AChWorks/achrix"
	"github.com/AChWorks/achrix/identity"
	"github.com/jackc/pgx/v5"
)

// The old executable is taken from the immutable published v0.1.0 artifact,
// never rebuilt with current source or assigned an invented old version.
const retainedBaselineBinarySHA256 = "f43a446486b7803dc061e379d7f76bd2b172b5d01e06ca06799ee59498f93188"

type retainedSession struct {
	cookie    *http.Cookie
	csrf      string
	principal string
}

type retainedHTTP struct {
	t      *testing.T
	ctx    context.Context
	client *http.Client
	port   string
}

type retainedSite struct {
	host, contentID, assetID, assetPath, title string
	session                                    retainedSession
	original, public                           []byte
}

func TestRetainedReleaseUpgrade(t *testing.T) {
	if os.Getenv("RIXA_RETAINED_UPGRADE_OBSERVATION") != "1" {
		t.Skip("set RIXA_RETAINED_UPGRADE_OBSERVATION=1 for the published-release upgrade proof")
	}
	binary := os.Getenv("RIXA_UPGRADE_BASELINE_BINARY")
	dsn := os.Getenv("RIXA_TEST_POSTGRES_DSN")
	if binary == "" || dsn == "" {
		t.Fatal("retained upgrade requires RIXA_UPGRADE_BASELINE_BINARY and RIXA_TEST_POSTGRES_DSN")
	}
	verifyRetainedBaseline(t, binary)
	for module, expected := range map[string]string{
		"github.com/AChWorks/achrix":           "v0.4.0",
		"github.com/AChWorks/achrix/admin":     "v0.1.0",
		"github.com/AChWorks/achrix/audit":     "v0.1.0",
		"github.com/AChWorks/achrix/identity":  "v0.1.0",
		"github.com/AChWorks/achrix/media":     "v0.1.0",
		"github.com/AChWorks/achrix/multisite": "v0.1.0",
	} {
		if achrix.DependencyVersion(module) != expected {
			t.Fatalf("retained-upgrade candidate component identity mismatch: %s", module)
		}
	}

	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()
	dsns := createTestDatabases(t, ctx, dsn)
	dir := t.TempDir()
	port := strconv.Itoa(freePort(t))
	cert, key, roots := writeTestCertificate(t, dir, []string{
		"control.rixa.test", "a.rixa.test", "b.rixa.test", "disabled.rixa.test",
	})
	raw := fileConfig{
		Listen: net.JoinHostPort("127.0.0.1", port),
		TLS:    TLSConfig{CertFile: cert, KeyFile: key}, Language: "en",
		StartupTimeout: "15s", ShutdownTimeout: "10s",
		Control: ControlConfig{Origin: "https://control.rixa.test:" + port, DatabaseEnv: "RIXA_UPGRADE_CONTROL"},
	}
	for i, id := range []string{"site-a", "site-b"} {
		media, public := filepath.Join(dir, id+"-media"), filepath.Join(dir, id+"-public")
		for _, root := range []string{media, public} {
			if err := os.Mkdir(root, 0o700); err != nil {
				t.Fatal(err)
			}
		}
		host := []string{"a.rixa.test", "b.rixa.test"}[i]
		raw.Sites = append(raw.Sites, SiteConfig{
			ID: id, Origin: "https://" + host + ":" + port,
			DatabaseEnv: []string{"RIXA_UPGRADE_A", "RIXA_UPGRADE_B"}[i],
			MediaRoot:   media, PublicRoot: public, PublicPolicy: testPublicPolicy(),
		})
	}
	raw.Sites = append(raw.Sites, SiteConfig{
		ID: "disabled", Origin: "https://disabled.rixa.test:" + port,
		DatabaseEnv: "RIXA_UPGRADE_DISABLED", MediaRoot: filepath.Join(dir, "never-created"), Disabled: true,
	})
	secrets := map[string]string{
		"RIXA_UPGRADE_CONTROL": dsns["control"], "RIXA_UPGRADE_A": dsns["a"], "RIXA_UPGRADE_B": dsns["b"],
	}
	env := []string{"PATH=" + os.Getenv("PATH"), "LANG=C"}
	for _, name := range []string{"RIXA_UPGRADE_CONTROL", "RIXA_UPGRADE_A", "RIXA_UPGRADE_B"} {
		env = append(env, name+"="+secrets[name])
	}
	configPath := filepath.Join(dir, "rixa.json")
	writeRetainedConfig(t, configPath, raw)
	retainedCommand(t, ctx, binary, env, "migrate", "--config", configPath)
	passwords := []string{testControlPassword, testSiteAPassword, testSiteBPassword}
	scopes := []string{"control", "site:site-a", "site:site-b"}
	logins := []string{"control-admin", "site-a-admin", "site-b-admin"}
	for i, scope := range scopes {
		output := retainedCommand(t, ctx, binary, append(env, "RIXA_UPGRADE_PASSWORD="+passwords[i]),
			"bootstrap-admin", "--config", configPath, "--scope", scope, "--login", logins[i],
			"--password-env", "RIXA_UPGRADE_PASSWORD")
		var result struct {
			Principal string `json:"principal"`
			Created   bool   `json:"created"`
		}
		if json.Unmarshal(output, &result) != nil || !result.Created || !accountIDSyntax.MatchString(result.Principal) {
			t.Fatal("old release did not bootstrap the expected administrator")
		}
		if i == 0 {
			raw.Control.AdminPrincipal = result.Principal
		} else {
			raw.Sites[i-1].AdminPrincipal = result.Principal
		}
	}
	writeRetainedConfig(t, configPath, raw)
	retainedCommand(t, ctx, binary, env, "check", "--config", configPath)

	httpFixture := newRetainedHTTP(t, ctx, raw.Listen, port, roots)
	stopOld := startRetainedBaseline(t, ctx, binary, env, configPath, dir)
	httpFixture.waitReady(filepath.Join(dir, "baseline-runtime.log"), env)
	control := httpFixture.login("control.rixa.test", logins[0], passwords[0])
	var sites []retainedSite
	for i, host := range []string{"a.rixa.test", "b.rixa.test"} {
		session := httpFixture.login(host, logins[i+1], passwords[i+1])
		httpFixture.post("control.rixa.test", "/admin/sites/account-create", control, map[string]string{
			"site_id": raw.Sites[i].ID, "login": "retained-member", "password": "Retained-Member-Password-2026!",
		}, http.StatusOK)
		sites = append(sites, httpFixture.seedSite(host, raw.Sites[i].ID, session, uint8(40+i*80)))
	}
	revoked := httpFixture.login("a.rixa.test", logins[1], passwords[1])
	httpFixture.post("a.rixa.test", "/auth/logout", revoked, map[string]string{}, http.StatusNoContent)
	// Ensure the negative session control was actually revoked by the old release.
	httpFixture.post("a.rixa.test", "/admin/content/create", revoked, map[string]string{}, http.StatusUnauthorized)
	stopOld()

	config, err := LoadConfig(configPath)
	if err != nil {
		t.Fatal(err)
	}
	beforeConfig, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	beforeRows := snapshotRetainedRows(t, ctx, dsns)
	beforeFiles := snapshotRetainedFiles(t, raw.Sites[:2])
	assertRetainedFixturePopulated(t, ctx, dsns)

	// Apply only the new release's explicit migration/check lifecycle to the old
	// databases, roots, configured principals and untouched browser credentials.
	for range 2 {
		if err = Migrate(ctx, config, mapLookup(secrets)); err != nil {
			t.Fatalf("candidate retained migration: %v", err)
		}
	}
	resolved, err := config.ResolveRuntime(ctx, mapLookup(secrets))
	if err != nil {
		t.Fatal(err)
	}
	checkRuntime, err := BuildRuntime(resolved, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err = Check(ctx, checkRuntime); err != nil {
		t.Fatalf("candidate retained check: %v", err)
	}
	assertRetainedSnapshots(t, ctx, dsns, raw.Sites[:2], configPath, beforeRows, beforeFiles, beforeConfig)
	runtime, err := BuildRuntime(resolved, nil)
	if err != nil {
		t.Fatal(err)
	}
	stopNew := startRetainedCandidate(t, ctx, runtime)
	httpFixture.waitReady("", nil)
	assertRetainedSnapshots(t, ctx, dsns, raw.Sites[:2], configPath, beforeRows, beforeFiles, beforeConfig)
	if runtime.Sites["disabled"] != nil {
		t.Fatal("disabled site acquired a runtime during upgrade")
	}

	// No new login or fixture mutation occurs before the exact retention checks.
	httpFixture.get("control.rixa.test", "/admin/sites/", &control, http.StatusOK)
	for i, site := range sites {
		httpFixture.get(site.host, "/admin/content/", &site.session, http.StatusOK)
		httpFixture.post(site.host, "/admin/identity/lookup-login", site.session, map[string]string{"login": logins[i+1]}, http.StatusOK)
		page := httpFixture.get(site.host, "/retained/", nil, http.StatusOK)
		if !bytes.Equal(page, site.public) {
			t.Fatalf("site %d public page changed across the dependency update", i)
		}
		original := httpFixture.get(site.host, "/admin/media/read/"+site.assetID, &site.session, http.StatusOK)
		if !bytes.Equal(original, site.original) {
			t.Fatalf("site %d original Media changed", i)
		}
		httpFixture.get(site.host, site.assetPath, nil, http.StatusOK)
		other := sites[1-i]
		httpFixture.get(site.host, "/admin/content/edit/"+other.contentID, &site.session, http.StatusNotFound)
		httpFixture.post(site.host, "/admin/media/status", site.session, map[string]string{"id": other.assetID}, http.StatusNotFound)
		httpFixture.post(site.host, "/admin/content/create", other.session, map[string]string{}, http.StatusUnauthorized)
	}
	httpFixture.post("a.rixa.test", "/admin/content/create", revoked, map[string]string{}, http.StatusUnauthorized)
	wrongCSRF := sites[0].session
	wrongCSRF.csrf = strings.Repeat("x", 43)
	httpFixture.post("a.rixa.test", "/admin/content/create", wrongCSRF, map[string]string{}, http.StatusUnauthorized)
	httpFixture.post("control.rixa.test", "/admin/sites/account-create", sites[0].session, map[string]string{}, http.StatusUnauthorized)
	for _, id := range []string{"disabled", "unknown"} {
		httpFixture.post("control.rixa.test", "/admin/sites/account-create", control, map[string]string{
			"site_id": id, "login": "denied", "password": "Retained-Member-Password-2026!",
		}, http.StatusForbidden)
	}
	// The retained control cookie and its old CSRF token still dispatch through
	// the existing bounded site operator; it is not a universal site session.
	httpFixture.post("control.rixa.test", "/admin/sites/account-create", control, map[string]string{
		"site_id": "site-b", "login": "after-upgrade", "password": "Retained-Member-Password-2026!",
	}, http.StatusOK)
	for i, site := range sites {
		fresh := httpFixture.login(site.host, logins[i+1], passwords[i+1])
		httpFixture.get(site.host, "/admin/content/", &fresh, http.StatusOK)
		member := httpFixture.login(site.host, "retained-member", "Retained-Member-Password-2026!")
		httpFixture.post(site.host, "/admin/content/create", member, map[string]string{
			"operation_id": newOperationID(), "kind": "post", "title": "denied", "body_html": "<p>denied</p>",
		}, http.StatusForbidden)
	}
	// Direct global principal use stays denied; the dispatcher must authorize
	// and translate it into the site's bounded operator identity.
	if _, err = runtime.Sites["site-a"].Identity.LookupAccount(ctx, runtime.Control.AdminPrincipal, "retained-member"); !errors.Is(err, achrix.ErrDenied) {
		t.Fatal("control principal bypassed site authorization after upgrade")
	}
	stopNew()
	t.Log("RIXA_RETAINED_UPGRADE baseline=v0.1.0 foundation=v0.3.0 sites=2 exact_rows_files_ledgers=true valid_sessions_retained=true revoked_foreign_denied=true control_dispatch=true")
}

func verifyRetainedBaseline(t *testing.T, path string) {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatal("published baseline binary is unavailable")
	}
	h := sha256.New()
	n, err := io.Copy(h, io.LimitReader(f, 32<<20))
	_ = f.Close()
	if err != nil || n == 32<<20 || hex.EncodeToString(h.Sum(nil)) != retainedBaselineBinarySHA256 {
		t.Fatal("published baseline binary SHA-256 mismatch")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	output := retainedCommand(t, ctx, path, []string{"PATH=" + os.Getenv("PATH")}, "version")
	var version struct {
		Rixa   string `json:"rixa"`
		AChrix string `json:"achrix"`
	}
	if json.Unmarshal(output, &version) != nil || version.Rixa != "v0.1.0" || version.AChrix != "v0.3.0" {
		t.Fatal("published baseline runtime identity mismatch")
	}
}

func writeRetainedConfig(t *testing.T, path string, raw fileConfig) {
	t.Helper()
	data, err := json.Marshal(raw)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
}

func retainedCommand(t *testing.T, ctx context.Context, binary string, env []string, args ...string) []byte {
	t.Helper()
	command := exec.CommandContext(ctx, binary, args...)
	command.Env = env
	output, err := command.Output()
	if err != nil {
		// Provider errors, credentials and session state are never dumped by this proof.
		var detail string
		if exit, ok := err.(*exec.ExitError); ok {
			detail = string(exit.Stderr)
		}
		t.Fatalf("published baseline %s failed: %v; stderr=%s", args[0], err, retainedDiagnostic(detail, env))
	}
	return output
}

func startRetainedBaseline(t *testing.T, ctx context.Context, binary string, env []string, config, dir string) func() {
	t.Helper()
	log, err := os.OpenFile(filepath.Join(dir, "baseline-runtime.log"), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	command := exec.CommandContext(ctx, binary, "run", "--config", config)
	command.Env, command.Stdout, command.Stderr = env, log, log
	if err = command.Start(); err != nil {
		_ = log.Close()
		t.Fatal(err)
	}
	done := make(chan struct{})
	var result error
	go func() { result = command.Wait(); _ = log.Close(); close(done) }()
	var once sync.Once
	stop := func() {
		once.Do(func() {
			_ = command.Process.Signal(syscall.SIGTERM)
			select {
			case <-done:
				if result != nil {
					t.Errorf("published baseline runtime stopped unsuccessfully: %v; log=%s", result, retainedDiagnostic(retainedLogTail(filepath.Join(dir, "baseline-runtime.log")), env))
				}
			case <-time.After(12 * time.Second):
				_ = command.Process.Kill()
				<-done
				t.Errorf("published baseline runtime did not drain within its bound; log=%s", retainedDiagnostic(retainedLogTail(filepath.Join(dir, "baseline-runtime.log")), env))
			}
		})
	}
	t.Cleanup(stop)
	return stop
}

func startRetainedCandidate(t *testing.T, parent context.Context, runtime *Runtime) func() {
	t.Helper()
	ctx, cancel := context.WithCancel(parent)
	done := make(chan error, 1)
	go func() { done <- Serve(ctx, runtime, nil) }()
	var once sync.Once
	stop := func() {
		once.Do(func() {
			cancel()
			select {
			case err := <-done:
				if err != nil {
					t.Errorf("candidate runtime shutdown: %v", err)
				}
			case <-time.After(12 * time.Second):
				t.Error("candidate runtime did not drain within its bound")
			}
		})
	}
	t.Cleanup(stop)
	return stop
}

func newRetainedHTTP(t *testing.T, ctx context.Context, listen, port string, roots *x509.CertPool) retainedHTTP {
	t.Helper()
	transport := &http.Transport{
		TLSClientConfig: &tls.Config{RootCAs: roots, MinVersion: tls.VersionTLS12},
		DialContext: func(ctx context.Context, network, _ string) (net.Conn, error) {
			return (&net.Dialer{Timeout: time.Second}).DialContext(ctx, network, listen)
		},
	}
	t.Cleanup(transport.CloseIdleConnections)
	return retainedHTTP{t, ctx, &http.Client{Transport: transport, Timeout: 5 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}, port}
}

func (h retainedHTTP) request(host, method, path, contentType string, body []byte, session *retainedSession) (*http.Response, error) {
	request, err := http.NewRequestWithContext(h.ctx, method, "https://"+net.JoinHostPort(host, h.port)+path, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	if contentType != "" {
		request.Header.Set("Content-Type", contentType)
	}
	request.Header.Set("Origin", "https://"+net.JoinHostPort(host, h.port))
	request.Header.Set("X-Identity-Request", "1")
	if session != nil {
		request.AddCookie(session.cookie)
		request.Header.Set("X-CSRF-Token", session.csrf)
	}
	return h.client.Do(request)
}

func (h retainedHTTP) waitReady(baselineLog string, env []string) {
	h.t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	var lastError error
	for {
		response, err := h.request("a.rixa.test", http.MethodGet, "/admin/login", "", nil, nil)
		if err == nil {
			_ = response.Body.Close()
			if response.StatusCode != http.StatusOK {
				h.t.Fatalf("retained runtime readiness status=%d; log=%s", response.StatusCode, retainedDiagnostic(retainedLogTail(baselineLog), env))
			}
			return
		}
		lastError = err
		if time.Now().After(deadline) {
			h.t.Fatalf("retained HTTPS ingress did not become ready: %v; log=%s", lastError, retainedDiagnostic(retainedLogTail(baselineLog), env))
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func (h retainedHTTP) read(response *http.Response, method, path string, want int) []byte {
	h.t.Helper()
	defer response.Body.Close()
	data, err := io.ReadAll(io.LimitReader(response.Body, 2<<20))
	if err != nil || len(data) == 2<<20 {
		h.t.Fatal("retained HTTP response exceeded its bound")
	}
	if response.StatusCode != want {
		h.t.Fatalf("%s %s status=%d want=%d; %s", method, path, response.StatusCode, want, retainedResponseDiagnostic(data))
	}
	return data
}

func (h retainedHTTP) post(host, path string, session retainedSession, value any, want int) []byte {
	h.t.Helper()
	body, err := json.Marshal(value)
	if err != nil {
		h.t.Fatal(err)
	}
	response, err := h.request(host, http.MethodPost, path, "application/json", body, &session)
	if err != nil {
		h.t.Fatalf("POST %s on %s failed: %v", path, host, err)
	}
	return h.read(response, http.MethodPost, path, want)
}

func (h retainedHTTP) get(host, path string, session *retainedSession, want int) []byte {
	h.t.Helper()
	response, err := h.request(host, http.MethodGet, path, "", nil, session)
	if err != nil {
		h.t.Fatalf("GET %s on %s failed: %v", path, host, err)
	}
	return h.read(response, http.MethodGet, path, want)
}

func (h retainedHTTP) login(host, login, password string) retainedSession {
	h.t.Helper()
	body, _ := json.Marshal(map[string]string{"login": login, "password": password})
	response, err := h.request(host, http.MethodPost, "/auth/login", "application/json", body, nil)
	if err != nil {
		h.t.Fatalf("retained credential login on %s failed: %v", host, err)
	}
	var result struct {
		CSRF      string `json:"csrf"`
		Principal string `json:"principal"`
	}
	if json.Unmarshal(h.read(response, http.MethodPost, "/auth/login", http.StatusOK), &result) != nil {
		h.t.Fatal("invalid login result")
	}
	var cookie *http.Cookie
	for _, candidate := range response.Cookies() {
		if candidate.Name == identity.CookieName {
			cookie = candidate
		}
	}
	if cookie == nil || result.CSRF == "" || result.Principal == "" {
		h.t.Fatal("incomplete retained session")
	}
	return retainedSession{cookie, result.CSRF, result.Principal}
}

func (h retainedHTTP) seedSite(host, id string, session retainedSession, red uint8) retainedSite {
	h.t.Helper()
	pixel := image.NewNRGBA(image.Rect(0, 0, 2, 2))
	pixel.SetNRGBA(0, 0, color.NRGBA{R: red, G: 40, B: 180, A: 255})
	var original bytes.Buffer
	if err := png.Encode(&original, pixel); err != nil {
		h.t.Fatal(err)
	}
	var multipartBody bytes.Buffer
	writer := multipart.NewWriter(&multipartBody)
	part, err := writer.CreateFormFile("file", id+".png")
	if err != nil {
		h.t.Fatal(err)
	}
	if _, err = part.Write(original.Bytes()); err != nil {
		h.t.Fatal(err)
	}
	if err = writer.Close(); err != nil {
		h.t.Fatal(err)
	}
	response, err := h.request(host, http.MethodPost, "/admin/media/upload", writer.FormDataContentType(), multipartBody.Bytes(), &session)
	if err != nil {
		h.t.Fatalf("old release Media upload on %s failed: %v", host, err)
	}
	var upload struct {
		Asset struct {
			ID string `json:"id"`
		} `json:"asset"`
	}
	if json.Unmarshal(h.read(response, http.MethodPost, "/admin/media/upload", http.StatusOK), &upload) != nil || !validEditorialID(upload.Asset.ID) {
		h.t.Fatal("old release did not create Media")
	}
	title := "Retained " + id + " فارسی"
	body := "<p>Baseline فارسی English</p><figure data-media-id=\"" + upload.Asset.ID + "\"><figcaption>اصل تصویر</figcaption></figure>"
	var created struct {
		ID string `json:"id"`
	}
	if json.Unmarshal(h.post(host, "/admin/content/create", session, map[string]string{
		"operation_id": newOperationID(), "kind": "post", "title": title, "body_html": body,
	}, http.StatusOK), &created) != nil || !validEditorialID(created.ID) {
		h.t.Fatal("old release did not create content")
	}
	h.post(host, "/admin/content/save", session, map[string]string{
		"operation_id": newOperationID(), "id": created.ID, "expected_head": "1", "title": title,
		"body_html": body + "<p>Revision two</p>",
	}, http.StatusOK)
	h.post(host, "/admin/content/publication-intent", session, map[string]string{
		"operation_id": newOperationID(), "id": created.ID, "expected_head": "2", "expected_publication_version": "1", "revision": "2",
	}, http.StatusOK)
	h.post(host, "/admin/appearance/save", session, map[string]any{
		"operation_id": newOperationID(), "expected_head": "1", "site_title": "Retained " + id,
		"site_description": "Retained فارسی", "site_language": "fa", "home_mode": "latest_posts", "home_page_id": "",
		"header_show_title": true, "header_tagline": "Header " + id, "footer_text": "Footer " + id, "theme": "dark",
	}, http.StatusOK)
	h.post(host, "/admin/content/publish", session, map[string]string{
		"operation_id": newOperationID(), "id": created.ID, "expected_generation": "", "route": "/retained/",
	}, http.StatusOK)
	page := h.get(host, "/retained/", nil, http.StatusOK)
	if !bytes.Contains(page, []byte(title)) {
		h.t.Fatal("old release publication lacks retained title")
	}
	assetPath := extractPublishedAssetPath(h.t, string(page))
	h.get(host, assetPath, nil, http.StatusOK)
	return retainedSite{host, created.ID, upload.Asset.ID, assetPath, title, session, original.Bytes(), page}
}

func snapshotRetainedRows(t *testing.T, ctx context.Context, dsns map[string]string) map[string]string {
	t.Helper()
	result := map[string]string{}
	for _, scope := range []string{"control", "a", "b"} {
		conn, err := pgx.Connect(ctx, dsns[scope])
		if err != nil {
			t.Fatal("retained snapshot database unavailable")
		}
		rows, err := conn.Query(ctx, "SELECT schemaname,tablename FROM pg_tables WHERE schemaname IN ('identity','audit','media','rixa') ORDER BY schemaname,tablename")
		if err != nil {
			_ = conn.Close(ctx)
			t.Fatal("retained snapshot table inventory failed")
		}
		var tables [][2]string
		for rows.Next() {
			var schema, table string
			if err = rows.Scan(&schema, &table); err != nil {
				t.Fatal(err)
			}
			tables = append(tables, [2]string{schema, table})
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			t.Fatal(err)
		}
		for _, table := range tables {
			name := pgx.Identifier{table[0], table[1]}.Sanitize()
			rows, err = conn.Query(ctx, "SELECT row_to_json(t)::text FROM "+name+" t ORDER BY row_to_json(t)::text")
			if err != nil {
				t.Fatalf("retained snapshot query failed: %s/%s", scope, name)
			}
			hash := sha256.New()
			count := 0
			for rows.Next() {
				var row string
				if err = rows.Scan(&row); err != nil {
					t.Fatal(err)
				}
				_, _ = io.WriteString(hash, row+"\n")
				count++
			}
			err = rows.Err()
			rows.Close()
			if err != nil {
				t.Fatal(err)
			}
			result[scope+"/"+name] = fmt.Sprintf("%d:%x", count, hash.Sum(nil))
		}
		_ = conn.Close(ctx)
	}
	return result
}

func snapshotRetainedFiles(t *testing.T, sites []SiteConfig) map[string]string {
	t.Helper()
	result := map[string]string{}
	for _, site := range sites {
		for label, root := range map[string]string{"media": site.MediaRoot, "public": site.PublicRoot} {
			err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
				if err != nil {
					return err
				}
				info, err := entry.Info()
				if err != nil {
					return err
				}
				relative, err := filepath.Rel(root, path)
				if err != nil {
					return err
				}
				key := site.ID + "/" + label + "/" + filepath.ToSlash(relative)
				if entry.IsDir() {
					result[key] = info.Mode().String()
					return nil
				}
				if !info.Mode().IsRegular() {
					return fmt.Errorf("non-regular retained file")
				}
				file, err := os.Open(path)
				if err != nil {
					return err
				}
				hash := sha256.New()
				size, err := io.Copy(hash, io.LimitReader(file, 32<<20))
				_ = file.Close()
				if err != nil || size == 32<<20 {
					return fmt.Errorf("retained file read bound")
				}
				result[key] = fmt.Sprintf("%s:%d:%x", info.Mode(), size, hash.Sum(nil))
				return nil
			})
			if err != nil {
				t.Fatalf("retained file snapshot failed: %s/%s", site.ID, label)
			}
		}
	}
	return result
}

func assertRetainedSnapshots(t *testing.T, ctx context.Context, dsns map[string]string, sites []SiteConfig, configPath string, rows, files map[string]string, config []byte) {
	t.Helper()
	if after := snapshotRetainedRows(t, ctx, dsns); !reflect.DeepEqual(after, rows) {
		var changed []string
		for key, value := range rows {
			if after[key] != value {
				changed = append(changed, key)
			}
		}
		for key := range after {
			if _, exists := rows[key]; !exists {
				changed = append(changed, key)
			}
		}
		sort.Strings(changed)
		t.Fatalf("retained database state changed during migration/readiness: tables=%v", changed)
	}
	if !reflect.DeepEqual(snapshotRetainedFiles(t, sites), files) {
		t.Fatal("retained private/public file state changed during migration/readiness")
	}
	afterConfig, err := os.ReadFile(configPath)
	if err != nil || !bytes.Equal(afterConfig, config) {
		t.Fatal("retained configuration changed")
	}
}

func assertRetainedFixturePopulated(t *testing.T, ctx context.Context, dsns map[string]string) {
	t.Helper()
	for _, scope := range []string{"a", "b"} {
		conn, err := pgx.Connect(ctx, dsns[scope])
		if err != nil {
			t.Fatal(err)
		}
		for _, item := range []struct {
			table   string
			minimum int
		}{
			{"identity.accounts", 2}, {"identity.credentials", 2}, {"identity.sessions", 1},
			{"audit.records", 1}, {"media.assets", 1}, {"rixa.content_items", 1},
			{"rixa.content_revisions", 2}, {"rixa.content_media_refs", 2}, {"rixa.appearance_revisions", 2},
			{"rixa.operations", 1}, {"identity.schema_migrations", 1}, {"audit.schema_migrations", 1},
			{"media.schema_migrations", 3}, {"rixa.schema_migrations", 2},
		} {
			var count int
			if err = conn.QueryRow(ctx, "SELECT count(*) FROM "+item.table).Scan(&count); err != nil || count < item.minimum {
				_ = conn.Close(ctx)
				t.Fatalf("old release fixture missing required retained state: %s/%s", scope, item.table)
			}
		}
		_ = conn.Close(ctx)
	}
}

// Operational failures retain useful scope/category text. Only known fixture
// secrets are replaced; rows, credential hashes and tokens are never printed.
func retainedDiagnostic(text string, env []string) string {
	values := []string{testControlPassword, testSiteAPassword, testSiteBPassword, "Retained-Member-Password-2026!"}
	for _, binding := range env {
		name, value, ok := strings.Cut(binding, "=")
		if ok && strings.HasPrefix(name, "RIXA_UPGRADE_") && value != "" {
			values = append(values, value)
			if parsed, err := url.Parse(value); err == nil && parsed.User != nil {
				if password, exists := parsed.User.Password(); exists && password != "" {
					values = append(values, password)
				}
			}
		}
	}
	sort.Slice(values, func(i, j int) bool { return len(values[i]) > len(values[j]) })
	for _, value := range values {
		text = strings.ReplaceAll(text, value, "[redacted]")
	}
	if len(text) > 4096 {
		text = text[len(text)-4096:]
	}
	return strings.TrimSpace(text)
}

func retainedLogTail(path string) string {
	if path == "" {
		return "unavailable"
	}
	file, err := os.Open(path)
	if err != nil {
		return "unavailable"
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return "unavailable"
	}
	if info.Size() > 4096 {
		_, _ = file.Seek(info.Size()-4096, io.SeekStart)
	}
	data, err := io.ReadAll(io.LimitReader(file, 4096))
	if err != nil {
		return "unavailable"
	}
	return string(data)
}

func retainedResponseDiagnostic(data []byte) string {
	var response struct {
		Code string `json:"code"`
	}
	if json.Unmarshal(data, &response) == nil && response.Code != "" {
		return fmt.Sprintf("code=%q", retainedDiagnostic(response.Code, nil))
	}
	return fmt.Sprintf("no API error category; response_bytes=%d", len(data))
}

func TestRetainedUpgradeDiagnosticRedaction(t *testing.T) {
	dsn := "postgres://fixture:unique-fixture-password@127.0.0.1:19524/retained?sslmode=disable"
	message := "database target site-a: connection refused; endpoint=" + dsn + "; password=unique-fixture-password"
	got := retainedDiagnostic(message, []string{"RIXA_UPGRADE_A=" + dsn})
	if strings.Contains(got, dsn) || strings.Contains(got, "unique-fixture-password") {
		t.Fatal("retained proof diagnostic leaked a known credential")
	}
	if !strings.Contains(got, "database target site-a: connection refused") || !strings.Contains(got, "[redacted]") {
		t.Fatal("retained proof diagnostic lost useful failure scope/category")
	}
	if got = retainedResponseDiagnostic([]byte(`{"code":"permission_denied","token":"must-not-be-dumped"}`)); !strings.Contains(got, "permission_denied") || strings.Contains(got, "must-not-be-dumped") {
		t.Fatal("retained HTTP diagnostic did not preserve only the safe error category")
	}
}
