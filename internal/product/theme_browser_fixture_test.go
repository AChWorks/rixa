// SPDX-License-Identifier: MPL-2.0

package product

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/AChWorks/achrix"
)

type themeBrowserReceipt struct {
	Schema      string `json:"schema"`
	Control     string `json:"control"`
	ManagementA string `json:"management_a"`
	ManagementB string `json:"management_b"`
	PublicA     string `json:"public_a"`
	PublicB     string `json:"public_b"`
	PreviewA    string `json:"preview_a"`
	PreviewB    string `json:"preview_b"`
	ContentID   string `json:"content_id"`
	PostPath    string `json:"post_path"`
	PagePath    string `json:"page_path"`
	GenerationA string `json:"generation_a"`
	GenerationB string `json:"generation_b"`
}

// Owns disposable PostgreSQL DBs and local TLS sockets. Browser automation
// must present a finish marker before this test terminates. The fixture does
// not require production DNS, credentials, or external hosts.
func TestCodeFirstThemeChromiumFixture(t *testing.T) {
	if os.Getenv("RIXA_THEME_BROWSER_OBSERVATION") != "1" {
		t.Skip("set RIXA_THEME_BROWSER_OBSERVATION=1 with disposable PostgreSQL and isolated fixture root")
	}
	root, dsn := os.Getenv("RIXA_THEME_BROWSER_ROOT"), os.Getenv("RIXA_TEST_POSTGRES_DSN")
	if !filepath.IsAbs(root) || root == "/" || dsn == "" {
		t.Fatal("theme browser requires fresh absolute root and disposable PostgreSQL DSN")
	}
	if err := os.Mkdir(root, 0700); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 8*time.Minute)
	defer cancel()
	dsns := createTestDatabases(t, ctx, dsn)
	port := strconv.Itoa(freePort(t))
	cert, key, _ := writeTestCertificate(t, root, []string{
		"control.localhost", "a.localhost", "b.localhost",
		"manage-a.localhost", "manage-b.localhost",
		"preview-a.localhost", "preview-b.localhost",
	})
	sites := []struct{ id, host, admin, preview, dsn, media, published string }{
		{"site-a", "a.localhost", "manage-a.localhost", "preview-a.localhost", "RIXA_THEME_BROWSER_A", filepath.Join(root, "media-a"), filepath.Join(root, "public-a")},
		{"site-b", "b.localhost", "manage-b.localhost", "preview-b.localhost", "RIXA_THEME_BROWSER_B", filepath.Join(root, "media-b"), filepath.Join(root, "public-b")},
	}
	config := Config{
		Listen: net.JoinHostPort("127.0.0.1", port), TLS: TLSConfig{CertFile: cert, KeyFile: key},
		Language: "en", StartupTimeout: 15 * time.Second, ShutdownTimeout: 10 * time.Second,
		Control: ControlConfig{Origin: "https://control.localhost:" + port, DatabaseEnv: "RIXA_THEME_BROWSER_CONTROL"},
	}
	for _, site := range sites {
		for _, dir := range []string{site.media, site.published} {
			if err := os.Mkdir(dir, 0700); err != nil {
				t.Fatal(err)
			}
		}
		config.Sites = append(config.Sites, SiteConfig{
			ID: site.id, Origin: "https://" + site.host + ":" + port,
			ManagementOrigin: "https://" + site.admin + ":" + port,
			PreviewOrigin:    "https://" + site.preview + ":" + port,
			DatabaseEnv:      site.dsn, MediaRoot: site.media, PublicRoot: site.published,
			PublicPolicy: testPublicPolicy(),
		})
	}
	secrets := mapLookup(map[string]string{
		"RIXA_THEME_BROWSER_CONTROL": dsns["control"],
		"RIXA_THEME_BROWSER_A":       dsns["a"], "RIXA_THEME_BROWSER_B": dsns["b"],
	})
	if err := Migrate(ctx, config, secrets); err != nil {
		t.Fatal(err)
	}
	for i, item := range []struct{ scope, login, password string }{
		{"control", "control-admin", testControlPassword},
		{"site:site-a", "site-a-admin", testSiteAPassword},
		{"site:site-b", "site-b-admin", testSiteBPassword},
	} {
		admin, e := BootstrapAdmin(ctx, config, item.scope, item.login, item.password, secrets, nil)
		if e != nil {
			t.Fatal(e)
		}
		if i == 0 {
			config.Control.AdminPrincipal = admin.Account.ID
		} else {
			config.Sites[i-1].AdminPrincipal = admin.Account.ID
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
		stop, done := context.WithTimeout(context.Background(), 10*time.Second)
		defer done()
		_ = runtime.Shutdown(stop)
	})
	a, b := runtime.Sites["site-a"], runtime.Sites["site-b"]
	if a == nil || b == nil {
		t.Fatal("missing two site runtimes")
	}
	// A real content editor can work with content while lacking executable
	// Theme permissions; the browser proof must exercise that exact account.
	editor, err := a.Identity.CreateAccount(ctx, a.adminPrincipal, "site-a-editor", "Site-A-Editor-Password-2026!")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = a.Access.Set(ctx, a.adminPrincipal, editor.ID, CapabilityContentEdit, true, 0); err != nil {
		t.Fatal(err)
	}
	if _, err = a.Editorial.Create(ctx, achrix.Principal(editor.ID), newOperationID(), ContentPost, "Editor private draft", "<p>private</p>"); err != nil {
		t.Fatalf("content editor lost ordinary editing permission: %v", err)
	}
	if _, err = a.Themes.Head(ctx, achrix.Principal(editor.ID)); !errors.Is(err, achrix.ErrDenied) {
		t.Fatalf("ordinary content editor acquired Theme permission: %v", err)
	}
	if _, err = a.Themes.SaveDraft(ctx, achrix.Principal(editor.ID), newOperationID(), 0, defaultThemeBundle()); !errors.Is(err, achrix.ErrDenied) {
		t.Fatalf("ordinary content editor acquired Theme write permission: %v", err)
	}
	if _, err = a.Themes.SaveDraft(ctx, b.adminPrincipal, newOperationID(), 0, defaultThemeBundle()); !errors.Is(err, achrix.ErrDenied) {
		t.Fatalf("foreign-site administrator acquired Theme write permission: %v", err)
	}
	appearance, err := a.Editorial.Appearance(ctx, a.adminPrincipal)
	if err != nil {
		t.Fatal(err)
	}
	update := appearance.input()
	update.SiteLanguage = "fa"
	if _, err = a.Editorial.SaveAppearance(ctx, a.adminPrincipal, newOperationID(), appearance.Revision, update); err != nil {
		t.Fatal(err)
	}
	post, err := a.Editorial.Create(ctx, a.adminPrincipal, newOperationID(), ContentPost, "صفحه آزمایشی فارسی", "<p>Visible HTML body</p>")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = a.Editorial.SetPublicationIntent(ctx, a.adminPrincipal, newOperationID(), post.ID, 1, 1, 1); err != nil {
		t.Fatal(err)
	}
	page, err := a.Editorial.Create(ctx, a.adminPrincipal, newOperationID(), ContentPage, "English page proof", "<p>Page-specific published body</p>")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = a.Editorial.SetPublicationIntent(ctx, a.adminPrincipal, newOperationID(), page.ID, 1, 1, 1); err != nil {
		t.Fatal(err)
	}
	publicationA, err := a.Publication.Apply(ctx, a.adminPrincipal, PublicationRequest{OperationID: newOperationID()})
	if err != nil {
		t.Fatal(err)
	}
	publicationB, err := b.Publication.Apply(ctx, b.adminPrincipal, PublicationRequest{OperationID: newOperationID()})
	if err != nil {
		t.Fatal(err)
	}

	certificate, err := tls.LoadX509KeyPair(cert, key)
	if err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("tcp", config.Listen)
	if err != nil {
		t.Fatal(err)
	}
	server := &http.Server{Handler: runtime.Handler, TLSConfig: &tls.Config{MinVersion: tls.VersionTLS12, Certificates: []tls.Certificate{certificate}}}
	served := make(chan error, 1)
	go func() { served <- server.ServeTLS(listener, "", "") }()
	t.Cleanup(func() {
		stop, done := context.WithTimeout(context.Background(), 5*time.Second)
		defer done()
		_ = server.Shutdown(stop)
		<-served
	})
	receipt := themeBrowserReceipt{
		Schema:      "rixa.code-theme-browser.v1",
		Control:     config.Control.Origin,
		ManagementA: config.Sites[0].ManagementOrigin,
		ManagementB: config.Sites[1].ManagementOrigin,
		PublicA:     config.Sites[0].Origin, PublicB: config.Sites[1].Origin,
		PreviewA: config.Sites[0].PreviewOrigin, PreviewB: config.Sites[1].PreviewOrigin,
		ContentID: post.ID, PostPath: defaultPublicationRoute(ContentPost, post.ID),
		PagePath:    defaultPublicationRoute(ContentPage, page.ID),
		GenerationA: publicationA.Generation, GenerationB: publicationB.Generation,
	}
	serialized, err := json.MarshalIndent(receipt, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(root, "ready.json"), append(serialized, '\n'), 0600); err != nil {
		t.Fatal(err)
	}
	t.Logf("RIXA_THEME_BROWSER_READY root=%s siteA=%s generation=%s", root, receipt.PublicA, receipt.GenerationA)
	waitBrowserMarker(t, ctx, filepath.Join(root, "finish.request"))

	if _, err = a.Themes.Head(ctx, a.adminPrincipal); err != nil {
		t.Fatal(err)
	}
	history, err := a.Themes.History(ctx, a.adminPrincipal)
	if err != nil || len(history) < 2 {
		t.Fatalf("browser did not commit expected Theme revisions: %#v / %v", history, err)
	}
	other, err := b.Themes.Head(ctx, b.adminPrincipal)
	if err != nil || other.Revision != 0 {
		t.Fatalf("Theme browser altered site B: %#v %v", other, err)
	}
	if b.Publication.State("", "").Generation != publicationB.Generation {
		t.Fatal("site B generation changed in Theme browser")
	}
	if a.Publication.State("", "").Generation == publicationA.Generation {
		t.Fatal("site A code Theme was not published in browser")
	}
	t.Log(fmt.Sprintf("RIXA_THEME_BROWSER_COMPLETE siteA_theme_revisions=%d siteB_head=0 siteB_public_unchanged=true", len(history)))
}
