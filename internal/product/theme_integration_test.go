// SPDX-License-Identifier: MPL-2.0

package product

import (
	"context"
	"crypto/tls"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"time"

	"github.com/AChWorks/achrix"
)

func TestCodeFirstThemeSiteIsolationPersistencePreviewAndPublication(t *testing.T) {
	adminDSN := os.Getenv("RIXA_TEST_POSTGRES_DSN")
	if adminDSN == "" {
		if os.Getenv("CI") != "" {
			t.Fatal("RIXA_TEST_POSTGRES_DSN required in CI")
		}
		t.Skip("provide a disposable PostgreSQL instance for Theme integration")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Minute)
	defer cancel()
	dsns := createTestDatabases(t, ctx, adminDSN)
	root := t.TempDir()
	cert, key, _ := writeTestCertificate(t, root, []string{
		"control.rixa.test", "a.rixa.test", "b.rixa.test",
		"manage-a.rixa.test", "preview-a.rixa.test",
		"manage-b.rixa.test", "preview-b.rixa.test",
	})
	mediaA := filepath.Join(root, "media-a")
	mediaB := filepath.Join(root, "media-b")
	publicA := filepath.Join(root, "public-a")
	publicB := filepath.Join(root, "public-b")
	for _, d := range []string{mediaA, mediaB, publicA, publicB} {
		if err := os.Mkdir(d, 0700); err != nil {
			t.Fatal(err)
		}
	}
	config := Config{
		Listen:   "127.0.0.1:19443",
		TLS:      TLSConfig{CertFile: cert, KeyFile: key},
		Language: "en", StartupTimeout: 15 * time.Second, ShutdownTimeout: 10 * time.Second,
		Control: ControlConfig{Origin: "https://control.rixa.test:19443", DatabaseEnv: "RIXA_THEME_CONTROL"},
		Sites: []SiteConfig{
			{ID: "site-a", Origin: "https://a.rixa.test:19443",
				ManagementOrigin: "https://manage-a.rixa.test:19443",
				PreviewOrigin:    "https://preview-a.rixa.test:19443",
				DatabaseEnv:      "RIXA_THEME_A", MediaRoot: mediaA, PublicRoot: publicA, PublicPolicy: testPublicPolicy()},
			{ID: "site-b", Origin: "https://b.rixa.test:19443",
				ManagementOrigin: "https://manage-b.rixa.test:19443",
				PreviewOrigin:    "https://preview-b.rixa.test:19443",
				DatabaseEnv:      "RIXA_THEME_B", MediaRoot: mediaB, PublicRoot: publicB, PublicPolicy: testPublicPolicy()},
		},
	}
	secrets := mapLookup(map[string]string{
		"RIXA_THEME_CONTROL": dsns["control"], "RIXA_THEME_A": dsns["a"], "RIXA_THEME_B": dsns["b"],
	})
	if err := Migrate(ctx, config, secrets); err != nil {
		t.Fatal(err)
	}
	for i, item := range []struct{ scope, login, password string }{
		{"control", "control-admin", testControlPassword},
		{"site:site-a", "site-a-admin", testSiteAPassword},
		{"site:site-b", "site-b-admin", testSiteBPassword},
	} {
		account, err := BootstrapAdmin(ctx, config, item.scope, item.login, item.password, secrets, nil)
		if err != nil {
			t.Fatalf("bootstrap %s: %v", item.scope, err)
		}
		if i == 0 {
			config.Control.AdminPrincipal = account.Account.ID
		}
		if i > 0 {
			config.Sites[i-1].AdminPrincipal = account.Account.ID
		}
	}
	resolved, err := config.ResolveRuntime(ctx, secrets)
	if err != nil {
		t.Fatal(err)
	}
	runtime, err := BuildRuntime(resolved, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err = runtime.Start(ctx); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		stop, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = runtime.Shutdown(stop)
	})

	a := runtime.Sites["site-a"]
	b := runtime.Sites["site-b"]
	if a == nil || b == nil || a.Themes == nil || b.Themes == nil {
		t.Fatal("site-owned Theme services not composed")
	}
	aAdmin, bAdmin := a.adminPrincipal, b.adminPrincipal
	if _, err = a.Themes.Head(ctx, bAdmin); !errors.Is(err, achrix.ErrDenied) {
		t.Fatalf("site B read site A Theme: %v", err)
	}
	if _, err = a.Themes.Head(ctx, a.operator); !errors.Is(err, achrix.ErrDenied) {
		t.Fatalf("Control operator could author site code: %v", err)
	}

	request := func(host, path string, cookie *http.Cookie) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequest(http.MethodGet, "https://"+host+path, nil)
		req.Host = host
		req.TLS = &tls.ConnectionState{}
		if cookie != nil {
			req.AddCookie(cookie)
		}
		response := httptest.NewRecorder()
		runtime.Handler.ServeHTTP(response, req)
		return response
	}
	for _, host := range []string{"a.rixa.test:19443", "preview-a.rixa.test:19443", "b.rixa.test:19443"} {
		if r := request(host, "/admin", nil); r.Code != http.StatusNotFound {
			t.Fatalf("privileged Admin exposed at %s: %d", host, r.Code)
		}
		if r := request(host, "/auth/login", nil); r.Code != http.StatusNotFound {
			t.Fatalf("privileged identity exposed at %s: %d", host, r.Code)
		}
	}
	if r := request("manage-a.rixa.test:19443", "/", nil); r.Code != http.StatusNotFound {
		t.Fatalf("management host served public root: %d", r.Code)
	}
	// Browser shell mounted only on management; unauthenticated gets a login
	// redirect/denial, not a public static page.
	if r := request("manage-a.rixa.test:19443", "/admin", nil); r.Code == http.StatusNotFound {
		t.Fatalf("real management host did not admit Admin route: %d", r.Code)
	}

	source, err := a.Editorial.Create(ctx, aAdmin, newOperationID(), ContentPost, "سلام Rixa", "<p>Published text</p>")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = a.Editorial.SetPublicationIntent(ctx, aAdmin, newOperationID(), source.ID, 1, 1, 1); err != nil {
		t.Fatal(err)
	}
	base, err := a.Publication.Apply(ctx, aAdmin, PublicationRequest{OperationID: newOperationID()})
	if err != nil {
		t.Fatal(err)
	}
	if got := request("a.rixa.test:19443", "/", nil); got.Code != http.StatusOK ||
		!strings.Contains(got.Body.String(), "سلام Rixa") {
		t.Fatalf("baseline homepage failed: %d", got.Code)
	}
	defaultHead, err := a.Themes.Head(ctx, aAdmin)
	if err != nil || defaultHead.Revision != 0 {
		t.Fatalf("default Theme not available: %#v / %v", defaultHead, err)
	}
	badPageOnly := defaultHead.Bundle
	badPageOnly.BodyHTML = `{{if .Content}}{{if eq .Content.Kind "page"}}{{.Content.PageOnlySecret}}{{end}}{{.Content.BodyHTML}}{{else}}<main>Home</main>{{end}}`
	invalidSaveID := newOperationID()
	if _, err = a.Themes.SaveDraft(ctx, aAdmin, invalidSaveID, 0, badPageOnly); !errors.Is(err, ErrEditorialInvalid) {
		t.Fatalf("Page-only invalid binding accepted by SaveDraft: %v", err)
	}
	if head, headErr := a.Themes.Head(ctx, aAdmin); headErr != nil || head.Revision != 0 {
		t.Fatalf("rejected Page-only draft mutated source head: %#v / %v", head, headErr)
	}
	if _, lookupErr := a.Themes.Operation(ctx, aAdmin, invalidSaveID); !errors.Is(lookupErr, ErrEditorialNotFound) {
		t.Fatalf("invalid draft unexpectedly committed an operation: %v", lookupErr)
	}
	theme1 := defaultHead.Bundle
	theme1.HeadHTML = `<script>document.documentElement.dataset.themeHook="v1";</script>`
	theme1.JavaScript = `document.body.dataset.rixa="theme-v1";`
	theme1.CSS = `body{outline:1px solid #f00}`
	theme1.HeaderHTML = `<header><a href="{{.Site.HomeURL}}">{{.Site.Title}}</a><span>Theme v1</span></header>`
	op1 := newOperationID()
	saved, err := a.Themes.SaveDraft(ctx, aAdmin, op1, 0, theme1)
	if err != nil || saved.Revision != 1 {
		t.Fatalf("save Theme first revision: %#v / %v", saved, err)
	}
	replay, err := a.Themes.SaveDraft(ctx, aAdmin, op1, 0, theme1)
	if err != nil || replay.Revision != 1 {
		t.Fatalf("same operation lost response did not reconcile: %#v / %v", replay, err)
	}
	wrong := theme1
	wrong.HeaderHTML += "changed"
	if _, err = a.Themes.SaveDraft(ctx, aAdmin, op1, 0, wrong); !errors.Is(err, ErrEditorialConflict) {
		t.Fatalf("operation ID reused with changed Theme: %v", err)
	}
	receipt, err := a.Themes.Operation(ctx, aAdmin, op1)
	if err != nil || receipt.ResultRevision != 1 || receipt.Kind != "theme.save" {
		t.Fatalf("Theme draft receipt: %#v / %v", receipt, err)
	}
	if _, err = a.Themes.SaveDraft(ctx, aAdmin, newOperationID(), 0, theme1); !errors.Is(err, ErrEditorialConflict) {
		t.Fatalf("stale Theme head was accepted: %v", err)
	}
	if active := a.Themes.PublishedRevision(); active != 0 {
		t.Fatalf("private draft became public: %d", active)
	}

	preview, err := a.Themes.Preview(ctx, aAdmin, 1)
	if err != nil || !strings.HasPrefix(preview.URL, "https://preview-a.rixa.test:19443/entry/") {
		t.Fatalf("preview was not isolated: %#v / %v", preview, err)
	}
	token := strings.TrimPrefix(preview.URL, "https://preview-a.rixa.test:19443/entry/")
	entry := request("preview-a.rixa.test:19443", "/entry/"+token, nil)
	if entry.Code != http.StatusSeeOther || entry.Header().Get("Location") != "/" {
		t.Fatalf("ticket exchange: %d %#v", entry.Code, entry.Header())
	}
	cookies := entry.Result().Cookies()
	if len(cookies) != 1 || !cookies[0].Secure || !cookies[0].HttpOnly || cookies[0].Domain != "" ||
		cookies[0].SameSite != http.SameSiteStrictMode {
		t.Fatalf("preview ticket lacked host-only strict cookie: %#v", cookies)
	}
	previewPage := request("preview-a.rixa.test:19443", "/", cookies[0])
	if previewPage.Code != http.StatusOK || !strings.Contains(previewPage.Body.String(), "Theme v1") ||
		!strings.Contains(previewPage.Header().Get("Content-Security-Policy"), "'sha256-") ||
		!strings.Contains(previewPage.Header().Get("Cache-Control"), "no-store") {
		t.Fatalf("isolated preview failed: status=%d body=%s headers=%#v", previewPage.Code, previewPage.Body.String(), previewPage.Header())
	}
	if r := request("preview-b.rixa.test:19443", "/", cookies[0]); r.Code != http.StatusNotFound {
		t.Fatalf("cross-site preview cookie admitted: %d", r.Code)
	}
	if r := request("a.rixa.test:19443", "/", cookies[0]); strings.Contains(r.Body.String(), "Theme v1") {
		t.Fatal("preview modified public generation")
	}

	publish1Op := newOperationID()
	published1, err := a.Themes.PublishRevision(ctx, aAdmin, publish1Op, 1, base.Generation)
	if err != nil {
		t.Fatalf("Theme publish: %v", err)
	}
	if a.Themes.PublishedRevision() != 1 {
		t.Fatal("Theme published revision missing")
	}
	page := request("a.rixa.test:19443", "/", nil)
	if page.Code != http.StatusOK || !strings.Contains(page.Body.String(), "Theme v1") ||
		!strings.Contains(page.Body.String(), "/theme-assets/") ||
		!strings.Contains(page.Header().Get("Content-Security-Policy"), "script-src 'self'") ||
		strings.Contains(page.Header().Get("Content-Security-Policy"), "script-src 'unsafe-inline'") {
		t.Fatalf("public Theme render/CSP: status=%d body=%s CSP=%q", page.Code, page.Body.String(), page.Header().Get("Content-Security-Policy"))
	}
	if r := request("b.rixa.test:19443", "/", nil); strings.Contains(r.Body.String(), "Theme v1") {
		t.Fatal("site A public Theme leaked into B")
	}
	path := findThemeAssetURL(page.Body.String(), "site.js")
	if path == "" {
		t.Fatal("Theme JS URL not present")
	}
	if r := request("a.rixa.test:19443", path, nil); r.Code != http.StatusOK || !strings.Contains(r.Body.String(), "theme-v1") {
		t.Fatalf("public approved JavaScript not served: %d", r.Code)
	}
	if r := request("b.rixa.test:19443", path, nil); r.Code != http.StatusNotFound {
		t.Fatalf("site B fetched site A JS: %d", r.Code)
	}

	theme2 := theme1
	theme2.HeaderHTML = `<header>Theme v2</header>`
	saved2, err := a.Themes.SaveDraft(ctx, aAdmin, newOperationID(), 1, theme2)
	if err != nil || saved2.Revision != 2 {
		t.Fatalf("second source revision: %#v %v", saved2, err)
	}
	if r := request("a.rixa.test:19443", "/", nil); strings.Contains(r.Body.String(), "Theme v2") {
		t.Fatal("second draft applied without Publish")
	}
	history, err := a.Themes.History(ctx, aAdmin)
	if err != nil || len(history) != 2 || history[0].Revision != 2 || history[1].Revision != 1 {
		t.Fatalf("Theme history: %#v %v", history, err)
	}
	if _, err = a.Themes.PublishRevision(ctx, aAdmin, newOperationID(), 2, base.Generation); !errors.Is(err, ErrEditorialConflict) {
		t.Fatalf("stale generation succeeded: %v", err)
	}
	second, err := a.Themes.PublishRevision(ctx, aAdmin, newOperationID(), 2, published1.Generation)
	if err != nil {
		t.Fatal(err)
	}
	if r := request("a.rixa.test:19443", "/", nil); !strings.Contains(r.Body.String(), "Theme v2") {
		t.Fatal("second revision not active")
	}
	rollbackOp := newOperationID()
	rolled, err := a.Themes.PublishRevision(ctx, aAdmin, rollbackOp, 1, second.Generation)
	if err != nil || rolled.Generation == published1.Generation {
		t.Fatalf("previous revision republish did not make new generation: %#v / %v", rolled, err)
	}
	if after, err := a.Themes.PublishRevision(ctx, aAdmin, rollbackOp, 1, second.Generation); err != nil || after.Generation != rolled.Generation {
		t.Fatalf("republish lost acknowledgement not idempotent: %#v / %v", after, err)
	}
	operation, err := a.Publication.Operation(ctx, aAdmin, rollbackOp)
	if err != nil || operation.ThemeRevision != 1 || operation.Generation != rolled.Generation {
		t.Fatalf("republish receipt mismatch: %#v / %v", operation, err)
	}
	if current := a.Themes.PublishedRevision(); current != 1 {
		t.Fatalf("republished previous Theme revision not active: %d", current)
	}

	// A valid saved revision can become unreadable due to storage corruption.
	// A failed Theme-specific apply must not replace the active public
	// generation, install partial assets, or acknowledge the failed operation.
	conn, err := pgx.Connect(ctx, dsns["a"])
	if err != nil {
		t.Fatal(err)
	}
	if _, err = conn.Exec(ctx, "UPDATE rixa.theme_revisions SET source_hash=repeat('f',64) WHERE revision=2"); err != nil {
		_ = conn.Close(context.Background())
		t.Fatal(err)
	}
	failedPublishID := newOperationID()
	if _, err = a.Themes.PublishRevision(ctx, aAdmin, failedPublishID, 2, rolled.Generation); !errors.Is(err, ErrEditorialUnavailable) {
		t.Fatalf("corrupt Theme revision unexpectedly published: %v", err)
	}
	if active := a.Publication.State("", ""); active.Generation != rolled.Generation ||
		a.Themes.PublishedRevision() != 1 {
		t.Fatalf("failed Theme publication changed the active generation: %#v", active)
	}
	if observed := request("a.rixa.test:19443", "/", nil); observed.Code != http.StatusOK ||
		!strings.Contains(observed.Body.String(), "Theme v1") {
		t.Fatal("failed Theme publication changed public output")
	}
	if _, err = a.Publication.Operation(ctx, aAdmin, failedPublishID); !errors.Is(err, ErrEditorialNotFound) {
		t.Fatalf("failed Theme publication unexpectedly committed a receipt: %v", err)
	}
	if _, err = conn.Exec(ctx, "UPDATE rixa.theme_revisions SET source_hash=$1 WHERE revision=2", saved2.SourceHash); err != nil {
		_ = conn.Close(context.Background())
		t.Fatal(err)
	}
	if err = conn.Close(ctx); err != nil {
		t.Fatal(err)
	}

	newRuntime, err := BuildRuntime(resolved, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err = newRuntime.Start(ctx); err != nil {
		t.Fatal(err)
	}
	defer func() {
		stop, stopCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer stopCancel()
		_ = newRuntime.Shutdown(stop)
	}()
	if got := newRuntime.Sites["site-a"].Themes.PublishedRevision(); got != 1 {
		t.Fatalf("restart did not retain Theme generation: %d", got)
	}
	if h, err := newRuntime.Sites["site-a"].Themes.Head(ctx, aAdmin); err != nil || h.Revision != 2 {
		t.Fatalf("restart lost private draft head: %#v / %v", h, err)
	}
	if h, err := newRuntime.Sites["site-b"].Themes.Head(ctx, bAdmin); err != nil || h.Revision != 0 {
		t.Fatalf("site B inherited site A theme: %#v / %v", h, err)
	}
}

// Do not rely on generated hashes in tests; discover from actual trusted
// publication markup and verify the asset serves only at the correct host.
func findThemeAssetURL(page, name string) string {
	for _, token := range strings.FieldsFunc(page, func(r rune) bool {
		return r == '"' || r == '\'' || r == '<' || r == '>' || r == ' ' || r == '\n'
	}) {
		if strings.HasPrefix(token, "/theme-assets/") && strings.HasSuffix(token, "-"+name) {
			return token
		}
	}
	return ""
}
