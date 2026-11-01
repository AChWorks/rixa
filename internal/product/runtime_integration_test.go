// SPDX-License-Identifier: MPL-2.0

package product

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/AChWorks/achrix"
	"github.com/AChWorks/achrix/identity"
	"github.com/jackc/pgx/v5"
)

const (
	testControlPassword = "A-Strong-Control-Password-2026!"
	testSiteAPassword   = "A-Strong-Site-A-Password-2026!"
	testSiteBPassword   = "A-Strong-Site-B-Password-2026!"
)

func TestRuntimeIsolationLifecycleAndTLSIngress(t *testing.T) {
	adminDSN := os.Getenv("RIXA_TEST_POSTGRES_DSN")
	if adminDSN == "" {
		if os.Getenv("CI") != "" {
			t.Fatal("RIXA_TEST_POSTGRES_DSN is required in CI")
		}
		t.Skip("set RIXA_TEST_POSTGRES_DSN to run PostgreSQL integration proof")
	}
	if !strings.HasPrefix(adminDSN, "postgres://") && !strings.HasPrefix(adminDSN, "postgresql://") {
		t.Fatal("RIXA_TEST_POSTGRES_DSN must be a PostgreSQL URL")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	dsns := createTestDatabases(t, ctx, adminDSN)
	dir := t.TempDir()
	rootA := filepath.Join(dir, "media-a")
	rootB := filepath.Join(dir, "media-b")
	publicA := filepath.Join(dir, "public-a")
	publicB := filepath.Join(dir, "public-b")
	for _, root := range []string{rootA, rootB, publicA, publicB} {
		if err := os.Mkdir(root, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	cert, key, _ := writeTestCertificate(t, dir, []string{
		"control.rixa.test", "a.rixa.test", "b.rixa.test", "disabled.rixa.test", "unknown.rixa.test",
	})

	config := Config{
		Listen:          "127.0.0.1:19443",
		TLS:             TLSConfig{CertFile: cert, KeyFile: key},
		Language:        "en",
		StartupTimeout:  15 * time.Second,
		ShutdownTimeout: 10 * time.Second,
		Control: ControlConfig{
			Origin:      "https://control.rixa.test:19443",
			DatabaseEnv: "RIXA_TEST_CONTROL",
		},
		Sites: []SiteConfig{
			{ID: "site-a", Origin: "https://a.rixa.test:19443", DatabaseEnv: "RIXA_TEST_A", MediaRoot: rootA, PublicRoot: publicA, PublicPolicy: testPublicPolicy()},
			{ID: "site-b", Origin: "https://b.rixa.test:19443", DatabaseEnv: "RIXA_TEST_B", MediaRoot: rootB, PublicRoot: publicB, PublicPolicy: PublicPolicyConfig{Indexing: "noindex", Snippet: "none", Crawlers: []PublicCrawlerPolicy{{UserAgent: "*", Access: "disallow", Purpose: "search"}}}},
			{ID: "disabled", Origin: "https://disabled.rixa.test:19443", DatabaseEnv: "RIXA_TEST_DISABLED", MediaRoot: filepath.Join(dir, "disabled-root"), Disabled: true},
		},
	}
	if err := config.validateStructure(); err != nil {
		t.Fatal(err)
	}
	secrets := map[string]string{
		"RIXA_TEST_CONTROL": dsns["control"],
		"RIXA_TEST_A":       dsns["a"],
		"RIXA_TEST_B":       dsns["b"],
	}
	getenv := mapLookup(secrets)

	aliasSecrets := map[string]string{
		"RIXA_TEST_CONTROL": dsns["control"],
		"RIXA_TEST_A":       dsnHostAlias(t, dsns["control"], "localhost"),
		"RIXA_TEST_B":       dsns["b"],
	}
	if err := Migrate(ctx, config, mapLookup(aliasSecrets)); !errors.Is(err, ErrConfiguration) {
		t.Fatalf("equivalent PostgreSQL endpoint aliases were not rejected before migration effects: %v", err)
	}

	if err := Migrate(ctx, config, getenv); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	controlBootstrap, err := BootstrapAdmin(ctx, config, "control", "control-admin", testControlPassword, getenv, nil)
	if err != nil {
		t.Fatalf("bootstrap control: %v", err)
	}
	aBootstrap, err := BootstrapAdmin(ctx, config, "site:site-a", "site-a-admin", testSiteAPassword, getenv, nil)
	if err != nil {
		t.Fatalf("bootstrap site A: %v", err)
	}
	bBootstrap, err := BootstrapAdmin(ctx, config, "site:site-b", "site-b-admin", testSiteBPassword, getenv, nil)
	if err != nil {
		t.Fatalf("bootstrap site B: %v", err)
	}
	if !controlBootstrap.Created || !aBootstrap.Created || !bBootstrap.Created {
		t.Fatal("fresh bootstrap did not create all administrators")
	}

	config.Control.AdminPrincipal = controlBootstrap.Account.ID
	config.Sites[0].AdminPrincipal = aBootstrap.Account.ID
	config.Sites[1].AdminPrincipal = bBootstrap.Account.ID

	resolved, err := config.ResolveRuntime(ctx, getenv)
	if err != nil {
		t.Fatalf("resolve runtime: %v", err)
	}
	if resolved.Sites[2].DSN != "" {
		t.Fatal("disabled site secret was resolved")
	}
	runtime, err := BuildRuntime(resolved, nil)
	if err != nil {
		t.Fatalf("build runtime: %v", err)
	}
	if !runtime.UsesMultiSite() {
		t.Fatal("two active sites plus a disabled binding must use Multi-Site")
	}
	if runtime.Sites["disabled"] != nil {
		t.Fatal("disabled site acquired resources")
	}
	if achrix.Version() != "v0.5.0" {
		t.Fatalf("unexpected Foundation identity %q", achrix.Version())
	}
	if err = runtime.Start(ctx); err != nil {
		t.Fatalf("start: %v", err)
	}

	aAdmin := runtime.Sites["site-a"].adminPrincipal
	bAdmin := runtime.Sites["site-b"].adminPrincipal
	accountA, err := runtime.Sites["site-a"].Identity.CreateAccount(ctx, aAdmin, "member-a", "Member-A-Password-2026!")
	if err != nil {
		t.Fatalf("site A account: %v", err)
	}
	if _, err = runtime.Sites["site-b"].Identity.Account(ctx, aAdmin, accountA.ID); !errors.Is(err, achrix.ErrDenied) {
		t.Fatalf("site A principal crossed into site B: %v", err)
	}
	accountB, err := runtime.Sites["site-b"].Identity.CreateAccount(ctx, bAdmin, "member-b", "Member-B-Password-2026!")
	if err != nil {
		t.Fatalf("site B account: %v", err)
	}

	control := runtime.ControlService()
	controlActor := runtime.Control.AdminPrincipal
	if _, err = control.CreateSiteAccount(ctx, controlActor, "site-a", "control-created-a", "Control-Created-A-2026!"); err != nil {
		t.Fatalf("control site A account: %v", err)
	}
	if _, err = control.CreateSiteAccount(ctx, controlActor, "site-b", "control-created-b", "Control-Created-B-2026!"); err != nil {
		t.Fatalf("control site B account: %v", err)
	}
	if _, err = control.CreateSiteAccount(ctx, controlActor, "disabled", "nope", "Never-Used-Password-2026!"); !errors.Is(err, achrix.ErrDenied) {
		t.Fatalf("control managed disabled site: %v", err)
	}
	if _, err = control.CreateSiteAccount(ctx, controlActor, "unknown", "nope", "Never-Used-Password-2026!"); !errors.Is(err, achrix.ErrDenied) {
		t.Fatalf("control managed unknown site: %v", err)
	}

	imageBytes := testPNG(t)
	assetA, err := runtime.Sites["site-a"].Media.Create(ctx, aAdmin, "pixel.png", bytes.NewReader(imageBytes))
	if err != nil {
		t.Fatalf("site A media: %v", err)
	}
	var readA bytes.Buffer
	if _, err = runtime.Sites["site-a"].Media.Read(ctx, aAdmin, assetA.ID, &readA); err != nil {
		t.Fatalf("site A read: %v", err)
	}
	if !bytes.Equal(readA.Bytes(), imageBytes) {
		t.Fatal("site A media bytes changed")
	}
	if _, err = runtime.Sites["site-b"].Media.Read(ctx, aAdmin, assetA.ID, &bytes.Buffer{}); !errors.Is(err, achrix.ErrDenied) {
		t.Fatalf("site A media principal crossed into site B: %v", err)
	}
	pageB, err := runtime.Sites["site-b"].Media.List(ctx, bAdmin, "", 10)
	if err != nil {
		t.Fatalf("site B media list: %v", err)
	}
	if len(pageB.Assets) != 0 {
		t.Fatal("site B observed site A media metadata")
	}
	_, controlledBytes, err := control.ReadSiteMedia(ctx, controlActor, "site-a", assetA.ID)
	if err != nil {
		t.Fatalf("control media read: %v", err)
	}
	if !bytes.Equal(controlledBytes, imageBytes) {
		t.Fatal("control path returned wrong site bytes")
	}

	publicationFixture := testEditorialRuntime(t, ctx, runtime, aAdmin, bAdmin, assetA.ID, assetA.Revision, dsns["a"], dsns["b"])
	testStaticPublicationRuntime(t, ctx, runtime, aAdmin, assetA.ID, publicationFixture)

	assertDatabaseIsolation(t, ctx, dsns["a"], dsns["b"], accountA.ID, accountB.ID)

	disabledReq := httptest.NewRequest(http.MethodGet, "https://disabled.rixa.test:19443/admin", nil)
	disabledReq.Host = "disabled.rixa.test:19443"
	disabledReq.TLS = &tls.ConnectionState{}
	disabledRec := httptest.NewRecorder()
	runtime.Handler.ServeHTTP(disabledRec, disabledReq)
	if disabledRec.Code != http.StatusNotFound {
		t.Fatalf("disabled site ingress = %d", disabledRec.Code)
	}

	stopCtx, stopCancel := context.WithTimeout(context.Background(), 10*time.Second)
	if err = runtime.Shutdown(stopCtx); err != nil {
		stopCancel()
		t.Fatalf("shutdown: %v", err)
	}
	stopCancel()

	singleConfig := config
	singleConfig.Sites = append([]SiteConfig(nil), config.Sites[:1]...)
	singleResolved, err := singleConfig.ResolveRuntime(ctx, getenv)
	if err != nil {
		t.Fatalf("single resolve: %v", err)
	}
	single, err := BuildRuntime(singleResolved, nil)
	if err != nil {
		t.Fatalf("single build: %v", err)
	}
	if single.UsesMultiSite() {
		t.Fatal("single-site runtime composed Multi-Site")
	}
	if err = single.Start(ctx); err != nil {
		t.Fatalf("single start: %v", err)
	}
	singleStop, singleCancel := context.WithTimeout(context.Background(), 10*time.Second)
	if err = single.Shutdown(singleStop); err != nil {
		singleCancel()
		t.Fatalf("single shutdown: %v", err)
	}
	singleCancel()

	testTrustedTLSIngress(t, ctx, config, getenv)
}

func assertDatabaseIsolation(t *testing.T, ctx context.Context, dsnA, dsnB, accountA, accountB string) {
	t.Helper()
	a, err := pgx.Connect(ctx, dsnA)
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close(context.Background())
	b, err := pgx.Connect(ctx, dsnB)
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close(context.Background())

	var count int
	if err = a.QueryRow(ctx, "SELECT count(*) FROM identity.accounts WHERE id=$1", accountA).Scan(&count); err != nil || count != 1 {
		t.Fatalf("site A account persistence: count=%d err=%v", count, err)
	}
	if err = b.QueryRow(ctx, "SELECT count(*) FROM identity.accounts WHERE id=$1", accountA).Scan(&count); err != nil || count != 0 {
		t.Fatalf("site A account leaked to site B: count=%d err=%v", count, err)
	}
	if err = b.QueryRow(ctx, "SELECT count(*) FROM identity.accounts WHERE id=$1", accountB).Scan(&count); err != nil || count != 1 {
		t.Fatalf("site B account persistence: count=%d err=%v", count, err)
	}
	if err = a.QueryRow(ctx, "SELECT count(*) FROM audit.records WHERE target=$1", accountA).Scan(&count); err != nil || count < 1 {
		t.Fatalf("site A identity/audit transaction evidence: count=%d err=%v", count, err)
	}
	if err = b.QueryRow(ctx, "SELECT count(*) FROM audit.records WHERE target=$1", accountA).Scan(&count); err != nil || count != 0 {
		t.Fatalf("site A audit leaked to site B: count=%d err=%v", count, err)
	}
}

func testTrustedTLSIngress(t *testing.T, parent context.Context, config Config, getenv func(string) (string, bool)) {
	t.Helper()
	port := freePort(t)
	config.HostingProfile = HostingProfileDirectTLS
	config.Listen = net.JoinHostPort("0.0.0.0", strconv.Itoa(port))
	for i := range config.Sites {
		host, _ := url.Parse(config.Sites[i].Origin)
		config.Sites[i].Origin = "https://" + net.JoinHostPort(host.Hostname(), strconv.Itoa(port))
		config.Sites[i].PublicRoot = ""
		config.Sites[i].PublicPolicy = PublicPolicyConfig{}
	}
	controlURL, _ := url.Parse(config.Control.Origin)
	config.Control.Origin = "https://" + net.JoinHostPort(controlURL.Hostname(), strconv.Itoa(port))
	cert, key, roots := writeTestCertificate(t, filepath.Dir(config.TLS.CertFile), []string{
		"control.rixa.test", "a.rixa.test", "b.rixa.test", "disabled.rixa.test", "unknown.rixa.test",
	})
	config.TLS = TLSConfig{CertFile: cert, KeyFile: key}

	resolved, err := config.ResolveRuntime(parent, getenv)
	if err != nil {
		t.Fatal(err)
	}
	runtime, err := BuildRuntime(resolved, nil)
	if err != nil {
		t.Fatal(err)
	}
	serveCtx, cancel := context.WithCancel(parent)
	result := make(chan error, 1)
	go func() { result <- Serve(serveCtx, runtime, nil) }()

	transport := &http.Transport{
		TLSClientConfig: &tls.Config{RootCAs: roots, MinVersion: tls.VersionTLS12},
		DialContext: func(ctx context.Context, network, _ string) (net.Conn, error) {
			return (&net.Dialer{Timeout: time.Second}).DialContext(ctx, network, net.JoinHostPort("127.0.0.1", strconv.Itoa(port)))
		},
	}
	client := &http.Client{
		Transport: transport,
		Timeout:   2 * time.Second,
		CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	request := func(host, method, path string, body []byte, headers map[string]string, cookie *http.Cookie) (*http.Response, error) {
		var input *bytes.Reader
		if body == nil {
			input = bytes.NewReader(nil)
		} else {
			input = bytes.NewReader(body)
		}
		req, e := http.NewRequestWithContext(parent, method, "https://"+net.JoinHostPort(host, strconv.Itoa(port))+path, input)
		if e != nil {
			return nil, e
		}
		for key, value := range headers {
			req.Header.Set(key, value)
		}
		if cookie != nil {
			req.AddCookie(cookie)
		}
		return client.Do(req)
	}

	deadline := time.Now().Add(8 * time.Second)
	for {
		response, requestErr := request("a.rixa.test", http.MethodGet, "/admin/login", nil, nil, nil)
		if requestErr == nil {
			_ = response.Body.Close()
			if response.StatusCode != http.StatusOK {
				cancel()
				t.Fatalf("site TLS login status = %d", response.StatusCode)
			}
			break
		}
		if time.Now().After(deadline) {
			cancel()
			t.Fatalf("TLS ingress did not become ready: %v", requestErr)
		}
		time.Sleep(50 * time.Millisecond)
	}

	for _, test := range []struct {
		host      string
		forwarded bool
		status    int
	}{
		{host: "control.rixa.test", status: http.StatusOK},
		{host: "disabled.rixa.test", status: http.StatusNotFound},
		{host: "unknown.rixa.test", status: http.StatusNotFound},
		{host: "unknown.rixa.test", forwarded: true, status: http.StatusNotFound},
	} {
		headers := map[string]string{}
		if test.forwarded {
			headers["X-Forwarded-Host"] = net.JoinHostPort("a.rixa.test", strconv.Itoa(port))
		}
		response, requestErr := request(test.host, http.MethodGet, "/admin/login", nil, headers, nil)
		if requestErr != nil {
			cancel()
			t.Fatalf("%s request: %v", test.host, requestErr)
		}
		_ = response.Body.Close()
		if response.StatusCode != test.status {
			cancel()
			t.Fatalf("%s status=%d want=%d", test.host, response.StatusCode, test.status)
		}
	}

	type authenticatedSession struct {
		cookie    *http.Cookie
		csrf      string
		principal achrix.Principal
	}
	login := func(host, origin, loginValue, password string) authenticatedSession {
		t.Helper()
		payload, marshalErr := json.Marshal(map[string]string{"login": loginValue, "password": password})
		if marshalErr != nil {
			t.Fatal(marshalErr)
		}
		response, requestErr := request(host, http.MethodPost, "/auth/login", payload, map[string]string{
			"Content-Type":       "application/json",
			"Origin":             origin,
			"X-Identity-Request": "1",
		}, nil)
		if requestErr != nil {
			t.Fatal(requestErr)
		}
		defer response.Body.Close()
		if response.StatusCode != http.StatusOK {
			t.Fatalf("%s login status=%d", host, response.StatusCode)
		}
		var session struct {
			Principal achrix.Principal `json:"principal"`
			CSRF      string           `json:"csrf"`
		}
		if decodeErr := json.NewDecoder(response.Body).Decode(&session); decodeErr != nil {
			t.Fatal(decodeErr)
		}
		var sessionCookie *http.Cookie
		for _, cookie := range response.Cookies() {
			if cookie.Name == identity.CookieName {
				sessionCookie = cookie
				break
			}
		}
		if sessionCookie == nil || session.CSRF == "" || session.Principal == "" {
			t.Fatal("login did not return a complete authenticated session")
		}
		return authenticatedSession{sessionCookie, session.CSRF, session.Principal}
	}

	controlSession := login("control.rixa.test", config.Control.Origin, "control-admin", testControlPassword)
	siteSession := login("a.rixa.test", config.Sites[0].Origin, "site-a-admin", testSiteAPassword)
	if controlSession.principal != runtime.Control.AdminPrincipal {
		t.Fatalf("control login principal=%s want=%s", controlSession.principal, runtime.Control.AdminPrincipal)
	}
	if siteSession.principal != runtime.Sites["site-a"].adminPrincipal {
		t.Fatalf("site login principal=%s want=%s", siteSession.principal, runtime.Sites["site-a"].adminPrincipal)
	}

	// Explicit negative HTTPS matrix: real site-specific session cookies,
	// CSRF and Origin values are never portable to the other enabled site.
	siteBSession := login("b.rixa.test", config.Sites[1].Origin, "site-b-admin", testSiteBPassword)
	for _, pair := range []struct {
		name, ownHost, foreignHost, ownOrigin, foreignOrigin string
		own, foreign                                         authenticatedSession
	}{
		{"a-to-b", "a.rixa.test", "b.rixa.test", config.Sites[0].Origin, config.Sites[1].Origin, siteSession, siteBSession},
		{"b-to-a", "b.rixa.test", "a.rixa.test", config.Sites[1].Origin, config.Sites[0].Origin, siteBSession, siteSession},
	} {
		t.Run("cross-site-session-"+pair.name, func(t *testing.T) {
			local, err := request(pair.ownHost, http.MethodGet, "/auth/csrf", nil,
				map[string]string{"X-Identity-Request": "1", "Referer": pair.ownOrigin + "/"}, pair.own.cookie)
			if err != nil {
				t.Fatal(err)
			}
			local.Body.Close()
			if local.StatusCode != http.StatusOK {
				t.Fatalf("valid same-site CSRF rejected: %d", local.StatusCode)
			}

			foreign, err := request(pair.foreignHost, http.MethodGet, "/auth/csrf", nil,
				map[string]string{"X-Identity-Request": "1", "Referer": pair.foreignOrigin + "/"}, pair.own.cookie)
			if err != nil {
				t.Fatal(err)
			}
			foreign.Body.Close()
			if foreign.StatusCode != http.StatusUnauthorized {
				t.Fatalf("foreign-site cookie authorized CSRF: %d", foreign.StatusCode)
			}

			post := func(origin, csrf string, cookie *http.Cookie) int {
				payload, err := json.Marshal(map[string]string{"operation_id": newOperationID()})
				if err != nil {
					t.Fatal(err)
				}
				res, err := request(pair.foreignHost, http.MethodPost, "/admin/content/operation", payload,
					map[string]string{"Content-Type": "application/json", "Origin": origin,
						"X-CSRF-Token": csrf, "X-Identity-Request": "1"}, cookie)
				if err != nil {
					t.Fatal(err)
				}
				res.Body.Close()
				return res.StatusCode
			}
			for _, negative := range []struct {
				name   string
				status int
			}{
				{"foreign-cookie-and-csrf", post(pair.foreignOrigin, pair.own.csrf, pair.own.cookie)},
				{"wrong-origin", post(pair.ownOrigin, pair.foreign.csrf, pair.foreign.cookie)},
				{"wrong-csrf", post(pair.foreignOrigin, pair.own.csrf, pair.foreign.cookie)},
			} {
				if negative.status != http.StatusUnauthorized && negative.status != http.StatusForbidden {
					t.Fatalf("%s accepted cross-site authentication: HTTP %d", negative.name, negative.status)
				}
			}
		})
	}

	controlPage, requestErr := request("control.rixa.test", http.MethodGet, "/admin/sites", nil, nil, controlSession.cookie)
	if requestErr != nil {
		t.Fatal(requestErr)
	}
	pageBody := new(bytes.Buffer)
	_, _ = pageBody.ReadFrom(controlPage.Body)
	_ = controlPage.Body.Close()
	if controlPage.StatusCode != http.StatusOK {
		t.Fatalf("control Sites page status=%d body=%s", controlPage.StatusCode, pageBody.Bytes())
	}
	for _, expected := range []string{
		`/admin/assets/admin.js`,
		`data-admin-form`,
		`action="/admin/sites/account-create"`,
		`name="site_id" value="site-a"`,
	} {
		if !strings.Contains(pageBody.String(), expected) {
			t.Fatalf("control Sites page missing %q", expected)
		}
	}

	adminScript, scriptErr := request("control.rixa.test", http.MethodGet, "/admin/assets/admin.js", nil, nil, nil)
	if scriptErr != nil {
		t.Fatal(scriptErr)
	}
	scriptBody := new(bytes.Buffer)
	_, _ = scriptBody.ReadFrom(adminScript.Body)
	_ = adminScript.Body.Close()
	if adminScript.StatusCode != http.StatusOK {
		t.Fatalf("AChrix Admin script status=%d", adminScript.StatusCode)
	}
	for _, expected := range []string{
		`referrer:location.origin+"/"`,
		`referrerPolicy:"same-origin"`,
		`form[data-admin-form]`,
	} {
		if !strings.Contains(scriptBody.String(), expected) {
			t.Fatalf("served AChrix Admin script missing %q", expected)
		}
	}

	refreshControlCSRF := func() string {
		t.Helper()
		response, refreshErr := request("control.rixa.test", http.MethodGet, "/auth/csrf", nil, map[string]string{
			"Referer":            config.Control.Origin + "/",
			"X-Identity-Request": "1",
		}, controlSession.cookie)
		if refreshErr != nil {
			t.Fatal(refreshErr)
		}
		defer response.Body.Close()
		if response.StatusCode != http.StatusOK {
			t.Fatalf("control CSRF refresh status=%d", response.StatusCode)
		}
		var payload struct {
			CSRF string `json:"csrf"`
		}
		if decodeErr := json.NewDecoder(response.Body).Decode(&payload); decodeErr != nil {
			t.Fatal(decodeErr)
		}
		if payload.CSRF == "" {
			t.Fatal("control CSRF refresh returned an empty token")
		}
		return payload.CSRF
	}
	controlSession.csrf = refreshControlCSRF()

	// This TLS runtime intentionally has NO public roots (configured above).
	// A real authenticated control session must still be able to create,
	// edit, inspect and preview private drafts on such an enabled site.
	if runtime.Sites["site-a"].Publication != nil {
		t.Fatal("no-root runtime unexpectedly composed a publication service")
	}
	selectedPath := "/admin/sites/site/site-a/content"
	selectedRead := func(path string, want int) string {
		t.Helper()
		response, requestErr := request("control.rixa.test", http.MethodGet, path, nil, nil, controlSession.cookie)
		if requestErr != nil {
			t.Fatal(requestErr)
		}
		defer response.Body.Close()
		data, readErr := io.ReadAll(response.Body)
		if readErr != nil {
			t.Fatal(readErr)
		}
		if response.StatusCode != want {
			t.Fatalf("selected read %s status=%d want=%d body=%s", path, response.StatusCode, want, data)
		}
		return string(data)
	}
	if page := selectedRead(selectedPath+"/", http.StatusOK); !strings.Contains(page, "Saved content") {
		t.Fatal("no-root selected content inventory is missing")
	}
	saved, readErr := runtime.ControlService().ListSiteContent(parent, controlSession.principal, "site-a")
	if readErr != nil || len(saved) == 0 {
		t.Fatalf("no-root selected site lost saved entries: %#v %v", saved, readErr)
	}
	itemID := saved[0].ID
	for _, path := range []string{selectedPath + "/edit/" + itemID,
		selectedPath + "/history/" + itemID + "/before/" + encodeRevisionPageCursor(itemID, 1),
		selectedPath + "/preview/" + itemID + "/1"} {
		page := selectedRead(path, http.StatusOK)
		if strings.Contains(page, "data-publication-apply") || strings.Contains(page, "publication-intent-clear") {
			t.Fatalf("no-root draft presented public actions at %s", path)
		}
	}
	selectedPost := func(path string, body map[string]string, want int) map[string]string {
		t.Helper()
		payload, e := json.Marshal(body)
		if e != nil {
			t.Fatal(e)
		}
		response, e := request("control.rixa.test", http.MethodPost, path, payload, map[string]string{
			"Content-Type": "application/json",
			"Origin":       config.Control.Origin,
			"X-CSRF-Token": controlSession.csrf,
		}, controlSession.cookie)
		if e != nil {
			t.Fatal(e)
		}
		defer response.Body.Close()
		raw, e := io.ReadAll(response.Body)
		if e != nil {
			t.Fatal(e)
		}
		if response.StatusCode != want {
			t.Fatalf("selected mutation %s: %d want=%d body=%s", path, response.StatusCode, want, raw)
		}
		data := map[string]string{}
		if e = json.Unmarshal(raw, &data); e != nil {
			t.Fatalf("selected mutation %s response: %v", path, e)
		}
		return data
	}
	draftCreated := selectedPost(selectedPath+"/create", map[string]string{
		"operation_id": newOperationID(), "kind": "post", "title": "No-root private draft", "body_html": "<p>Private draft</p>",
	}, http.StatusOK)
	if !validEditorialID(draftCreated["id"]) || draftCreated["revision"] != "1" {
		t.Fatalf("no-root create: %#v", draftCreated)
	}
	draftUpdated := selectedPost(selectedPath+"/save", map[string]string{
		"operation_id": newOperationID(), "id": draftCreated["id"], "expected_head": "1",
		"title": "No-root saved draft", "body_html": "<p>Only private source</p>",
	}, http.StatusOK)
	if draftUpdated["revision"] != "2" || draftUpdated["id"] != draftCreated["id"] {
		t.Fatalf("no-root save: %#v", draftUpdated)
	}
	for _, path := range []string{selectedPath + "/edit/" + draftCreated["id"],
		selectedPath + "/history/" + draftCreated["id"] + "/before/" + encodeRevisionPageCursor(draftCreated["id"], 2),
		selectedPath + "/preview/" + draftCreated["id"] + "/1",
		selectedPath + "/preview/" + draftCreated["id"] + "/2"} {
		selectedRead(path, http.StatusOK)
	}
	for _, path := range []string{selectedPath + "/publication-intent", selectedPath + "/publication-intent-clear", selectedPath + "/publish"} {
		selectedPost(path, map[string]string{"operation_id": newOperationID()}, http.StatusNotFound)
	}
	if _, e := runtime.ControlService().ApplySitePublication(parent, controlSession.principal, "site-a", PublicationRequest{OperationID: newOperationID()}); !errors.Is(e, ErrEditorialUnavailable) {
		t.Fatalf("unconfigured publication applied: %v", e)
	}
	for _, siteID := range []string{"disabled", "unknown"} {
		selectedRead("/admin/sites/site/"+siteID+"/content/", http.StatusForbidden)
	}
	noSession, e := request("control.rixa.test", http.MethodGet, selectedPath+"/", nil, nil, nil)
	if e != nil {
		t.Fatal(e)
	}
	_ = noSession.Body.Close()
	if noSession.StatusCode == http.StatusOK {
		t.Fatal("unauthenticated control reached selected draft")
	}

	if _, directErr := runtime.Sites["site-a"].Identity.CreateAccount(parent, runtime.Control.AdminPrincipal, "direct-control-denied", "Direct-Control-Denied-2026!"); !errors.Is(directErr, achrix.ErrDenied) {
		t.Fatalf("control principal bypassed target site policy: %v", directErr)
	}

	postControl := func(session authenticatedSession, body map[string]string, extraHeaders map[string]string) (*http.Response, []byte) {
		t.Helper()
		payload, marshalErr := json.Marshal(body)
		if marshalErr != nil {
			t.Fatal(marshalErr)
		}
		headers := map[string]string{
			"Content-Type": "application/json",
			"Origin":       config.Control.Origin,
			"X-CSRF-Token": session.csrf,
		}
		for key, value := range extraHeaders {
			headers[key] = value
		}
		response, requestErr := request("control.rixa.test", http.MethodPost, "/admin/sites/account-create", payload, headers, session.cookie)
		if requestErr != nil {
			t.Fatal(requestErr)
		}
		data := new(bytes.Buffer)
		_, _ = data.ReadFrom(response.Body)
		_ = response.Body.Close()
		return response, data.Bytes()
	}

	response, data := postControl(controlSession, map[string]string{
		"site_id": "site-a", "login": "https-control-created", "password": "HTTPS-Control-Created-2026!",
	}, map[string]string{"X-Principal": string(runtime.Sites["site-a"].adminPrincipal)})
	if response.StatusCode != http.StatusOK {
		t.Fatalf("authenticated control management status=%d body=%s", response.StatusCode, data)
	}
	created, lookupErr := runtime.Sites["site-a"].Identity.LookupAccount(parent, runtime.Sites["site-a"].adminPrincipal, "https-control-created")
	if lookupErr != nil || created.ID == "" {
		t.Fatalf("authenticated control management did not reach target site: account=%#v err=%v", created, lookupErr)
	}

	for _, siteID := range []string{"disabled", "unknown"} {
		response, _ = postControl(controlSession, map[string]string{
			"site_id": siteID, "login": "forbidden-" + siteID, "password": "Forbidden-Site-Password-2026!",
		}, nil)
		if response.StatusCode != http.StatusForbidden {
			t.Fatalf("control management of %s status=%d want=403", siteID, response.StatusCode)
		}
	}

	response, _ = postControl(siteSession, map[string]string{
		"site_id": "site-a", "login": "site-session-forbidden", "password": "Site-Session-Forbidden-2026!",
	}, map[string]string{"X-Principal": string(runtime.Control.AdminPrincipal)})
	if response.StatusCode != http.StatusUnauthorized {
		t.Fatalf("site session invoked control management: status=%d want=401", response.StatusCode)
	}

	forgedPayload, _ := json.Marshal(map[string]string{
		"site_id": "site-a", "login": "forged-body", "password": "Forged-Body-Password-2026!", "principal": string(runtime.Control.AdminPrincipal),
	})
	response, requestErr = request("control.rixa.test", http.MethodPost, "/admin/sites/account-create", forgedPayload, map[string]string{
		"Content-Type": "application/json",
		"Origin":       config.Control.Origin,
		"X-CSRF-Token": controlSession.csrf,
	}, controlSession.cookie)
	if requestErr != nil {
		t.Fatal(requestErr)
	}
	_ = response.Body.Close()
	if response.StatusCode != http.StatusBadRequest {
		t.Fatalf("caller-supplied principal body accepted: status=%d", response.StatusCode)
	}

	cancel()
	select {
	case err = <-result:
		if err != nil {
			t.Fatalf("serve shutdown: %v", err)
		}
	case <-time.After(12 * time.Second):
		t.Fatal("serve did not drain within bound")
	}
}

func createTestDatabases(t *testing.T, ctx context.Context, adminDSN string) map[string]string {
	t.Helper()
	admin, err := pgx.Connect(ctx, adminDSN)
	if err != nil {
		t.Fatalf("connect PostgreSQL: %v", err)
	}
	defer admin.Close(context.Background())

	suffix := strconv.FormatInt(time.Now().UnixNano(), 36)
	names := map[string]string{
		"control": "rixa_control_" + suffix,
		"a":       "rixa_a_" + suffix,
		"b":       "rixa_b_" + suffix,
	}
	for _, name := range names {
		if _, err = admin.Exec(ctx, "CREATE DATABASE "+name+" TEMPLATE template0 ENCODING 'UTF8'"); err != nil {
			t.Fatalf("create database: %v", err)
		}
	}
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cleanupCancel()
		conn, connectErr := pgx.Connect(cleanupCtx, adminDSN)
		if connectErr != nil {
			t.Logf("cleanup PostgreSQL connect: %v", connectErr)
			return
		}
		defer conn.Close(context.Background())
		for _, name := range names {
			if _, dropErr := conn.Exec(cleanupCtx, "DROP DATABASE "+name+" WITH (FORCE)"); dropErr != nil {
				t.Logf("drop %s: %v", name, dropErr)
			}
		}
	})
	result := make(map[string]string, len(names))
	for key, name := range names {
		result[key] = dsnDatabase(t, adminDSN, name)
	}
	return result
}

func dsnDatabase(t *testing.T, dsn, database string) string {
	t.Helper()
	u, err := url.Parse(dsn)
	if err != nil {
		t.Fatal(err)
	}
	u.Path = "/" + database
	return u.String()
}

func dsnHostAlias(t *testing.T, dsn, host string) string {
	t.Helper()
	u, err := url.Parse(dsn)
	if err != nil {
		t.Fatal(err)
	}
	port := u.Port()
	if port == "" {
		port = "5432"
	}
	u.Host = net.JoinHostPort(host, port)
	return u.String()
}

func testPNG(t *testing.T) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, 2, 2))
	img.Set(0, 0, color.RGBA{R: 20, G: 40, B: 60, A: 255})
	var output bytes.Buffer
	if err := png.Encode(&output, img); err != nil {
		t.Fatal(err)
	}
	return output.Bytes()
}

func freePort(t *testing.T) int {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	return listener.Addr().(*net.TCPAddr).Port
}

func Example_runtimeProfile() {
	fmt.Println("explicit migrations -> build -> readiness -> TLS ingress -> drain -> shutdown")
	// Output: explicit migrations -> build -> readiness -> TLS ingress -> drain -> shutdown
}
