// SPDX-License-Identifier: MPL-2.0

package product

import (
	"archive/zip"
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"html/template"
	"image"
	"image/color"
	"image/png"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestThemeBundlePortableRoundTripAndAdmission(t *testing.T) {
	bundle := defaultThemeBundle()
	bundle.HeadHTML = `<meta name="theme-color" content="{{(index .Theme.Settings "brand_color").Value}}">`
	bundle.HeaderHTML = `<header><img src="{{asset "logo.png"}}" alt="{{.Site.Title}}"></header>`
	bundle.CSS = `@media(max-width:640px){header{display:block}}[dir="rtl"]{direction:rtl}`
	bundle.JavaScript = `document.documentElement.dataset.codeTheme="ready";`
	bundle.Settings = map[string]ThemeSetting{"brand_color": {Type: "color", Value: "#12ac45"}}
	picture := image.NewRGBA(image.Rect(0, 0, 2, 2))
	picture.Set(0, 0, color.RGBA{R: 100, G: 20, B: 80, A: 255})
	var content bytes.Buffer
	if err := png.Encode(&content, picture); err != nil {
		t.Fatal(err)
	}
	bundle.Assets = map[string]ThemeAsset{"logo.png": {MIME: "image/png", Data: content.Bytes()}}
	bundle.CSS += ".brand{background-image:url(" + `{{asset "logo.png"}}` + ")}"
	if err := bundle.validate(); err != nil {
		t.Fatalf("valid bundle rejected: %v", err)
	}
	archive, err := exportThemeBundle(bundle)
	if err != nil {
		t.Fatal(err)
	}
	imported, err := importThemeBundle(archive)
	if err != nil {
		t.Fatal(err)
	}
	if imported.sourceHash() != bundle.sourceHash() {
		t.Fatal("portable bundle lost source or asset bytes")
	}
	for _, term := range []string{"head.html", "header.html", "body.html", "footer.html", "site.css", "site.js"} {
		if !bytes.Contains(archive, []byte(term)) {
			t.Fatalf("portable file %s missing", term)
		}
	}
	data := themeRenderBinding{Site: themeSiteBinding{Title: "علامت", Origin: "https://site.test"}}
	html, err := executeThemeSlot(bundle.HeaderHTML, bundle, data)
	if err != nil || !strings.Contains(html, themeAssetPath("logo.png", bundle.Assets["logo.png"])) || !strings.Contains(html, "علامت") {
		t.Fatalf("authorized public asset did not resolve: %q %v", html, err)
	}
	if _, err := executeThemeSlot(`<img src="{{asset "private.png"}}">`, bundle, data); err == nil {
		t.Fatal("nondeclared image resolved")
	}
	if strings.Contains(string(archive), "/var/lib/") {
		t.Fatal("theme bundle references server paths")
	}
	root := t.TempDir()
	manifest := &publicationManifest{
		SiteID: "site-a", Origin: "https://example.test",
		Routes: map[string]publicationRoute{}, Files: map[string]publicationFile{},
	}
	cssURL, _, _, err := stageThemeAssets(root, manifest, imported)
	if err != nil {
		t.Fatal(err)
	}
	css, err := os.ReadFile(filepath.Join(root, strings.TrimPrefix(cssURL, "/")))
	if err != nil || !strings.Contains(string(css), themeAssetPath("logo.png", imported.Assets["logo.png"])) ||
		strings.Contains(string(css), "{{asset") {
		t.Fatalf("CSS public asset reference unresolved: %q / %v", css, err)
	}
}

func TestThemeBundleRejectsDangerousSources(t *testing.T) {
	tests := map[string]func(*ThemeBundle){
		"body omission":      func(b *ThemeBundle) { b.BodyHTML = `{{.Content.BodyHTML}}` },
		"reserved SEO title": func(b *ThemeBundle) { b.HeadHTML = `<title>override</title>` },
		"reserved canonical": func(b *ThemeBundle) { b.HeadHTML = `<link rel="canonical" href="https://evil.example/">` },
		"inline event":       func(b *ThemeBundle) { b.HeaderHTML = `<img src="/x" onerror="alert(1)">` },
		"external script":    func(b *ThemeBundle) { b.HeaderHTML = `<script src="//evil.test/x.js"></script>` },
		"unknown context":    func(b *ThemeBundle) { b.HeaderHTML = `{{.Site.SecretKey}}` },
		"unknown CSS asset":  func(b *ThemeBundle) { b.CSS = "body{background:url(" + `{{asset "inaccessible.jpg"}}` + ")}" },
		"unknown asset":      func(b *ThemeBundle) { b.HeaderHTML = `{{asset "private.jpg"}}` },
		"server privilege":   func(b *ThemeBundle) { b.HeaderHTML = `{{env "HOME"}}` },
		"oversize source":    func(b *ThemeBundle) { b.CSS = strings.Repeat("x", maxThemeSourceBytes+1) },
		"invalid color": func(b *ThemeBundle) {
			b.Settings = map[string]ThemeSetting{"color": {Type: "color", Value: "expression(alert(1))"}}
		},
		"arbitrary setting key": func(b *ThemeBundle) {
			b.Settings = map[string]ThemeSetting{"../../private": {Type: "text", Value: "bad"}}
		},
		"invalid unicode": func(b *ThemeBundle) { b.JavaScript = string([]byte{0xff, 0x00}) },
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			b := defaultThemeBundle()
			mutate(&b)
			if err := b.validate(); err == nil {
				t.Fatal("invalid Theme source accepted")
			}
		})
	}
}

func TestThemeZIPRejectsTraversalSymlinksAndExtraFiles(t *testing.T) {
	fixture := defaultThemeBundle()
	original, err := exportThemeBundle(fixture)
	if err != nil {
		t.Fatal(err)
	}
	reader, err := zip.NewReader(bytes.NewReader(original), int64(len(original)))
	if err != nil {
		t.Fatal(err)
	}
	evil := func(name string, mode os.FileMode, extra string) []byte {
		t.Helper()
		var out bytes.Buffer
		writer := zip.NewWriter(&out)
		for _, entry := range reader.File {
			h := &zip.FileHeader{Name: entry.Name, Method: zip.Deflate}
			h.SetMode(0600)
			if entry.Name == "site.css" && extra == "" {
				h.Name = name
				h.SetMode(mode)
			}
			dst, e := writer.CreateHeader(h)
			if e != nil {
				t.Fatal(e)
			}
			src, e := entry.Open()
			if e != nil {
				t.Fatal(e)
			}
			_, e = io.Copy(dst, src)
			_ = src.Close()
			if e != nil {
				t.Fatal(e)
			}
		}
		if extra != "" {
			dst, e := writer.Create(extra)
			if e != nil {
				t.Fatal(e)
			}
			_, _ = dst.Write([]byte("not allowed"))
		}
		if err := writer.Close(); err != nil {
			t.Fatal(err)
		}
		return out.Bytes()
	}
	for name, archive := range map[string][]byte{
		"path traversal":  evil("../site.css", 0600, ""),
		"absolute":        evil("/site.css", 0600, ""),
		"symlink":         evil("site.css", os.ModeSymlink|0777, ""),
		"unexpected file": evil("site.css", 0600, ".env"),
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := importThemeBundle(archive); err == nil {
				t.Fatal("untrusted archive accepted")
			}
		})
	}
}

func TestThemeRenderUsesPublicTransformedHTMLAndCSP(t *testing.T) {
	id := strings.Repeat("A", 26)
	asset := publicationAsset{SourceAssetID: id, PublicPath: "/assets/safe-public-photo.png", Width: 2, Height: 2}
	canonical, _, err := canonicalBody(`<p>سلام public</p><figure data-media-id="` + id + `"><figcaption>توضیح</figcaption></figure>`)
	if err != nil {
		t.Fatal(err)
	}
	renderedBody, err := publicEditorialHTML(canonical, map[string]publicationAsset{id: asset})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(renderedBody), id) || !strings.Contains(string(renderedBody), "safe-public-photo.png") {
		t.Fatal("canonical private Media ID leaked into publication-transformed body")
	}
	appearance := Appearance{SiteTitle: "سایت", SiteDescription: "توضیح", SiteLanguage: "fa"}
	b := defaultThemeBundle()
	b.HeadHTML = `<script>window.customTheme=true;</script>`
	b.JavaScript = `console.log("visitor JS")`
	page := publicPageView{
		Kind: ContentPage, Title: "صفحه", Description: "شرح صفحه", Body: renderedBody,
		Canonical: "https://site.test/pages/test/", Robots: "index, follow",
		Structured:  template.JS(`{"@context":"https://schema.org","@type":"WebPage"}`),
		PublishedAt: time.Now(), ModifiedAt: time.Now(),
	}
	out, err := renderThemeHTML(b, appearance, "https://site.test", page, nil, "/theme-assets/test-site.css", "/theme-assets/test-site.js")
	if err != nil {
		t.Fatal(err)
	}
	html := string(out)
	for _, value := range []string{`lang="fa" dir="rtl"`, `<title>صفحه · سایت</title>`, `rel="canonical"`, `type="application/ld+json"`, `safe-public-photo.png`, `/theme-assets/test-site.js`, `window.customTheme=true`} {
		if !strings.Contains(html, value) {
			t.Fatalf("published page lacks %q", value)
		}
	}
	if strings.Contains(html, id) || strings.Contains(html, "data-media-id") {
		t.Fatal("private source survived HTML transformation")
	}
	csp := themeCSP(out)
	if strings.Contains(csp, "script-src 'unsafe-inline'") || !strings.Contains(csp, "script-src 'self'") || !strings.Contains(csp, "'sha256-") {
		t.Fatalf("CSP incorrectly enabled inline code: %q", csp)
	}
	digest := sha256.Sum256([]byte("window.customTheme=true;"))
	want := "'sha256-" + base64.StdEncoding.EncodeToString(digest[:]) + "'"
	if !strings.Contains(csp, want) {
		t.Fatalf("inline script hash missing: %s", want)
	}
	before := themeCSP(out)
	if before != csp {
		t.Fatal("nondeterministic CSP hashes")
	}
	home := publicHomeView{Items: []publicHomeItem{{Title: "نوشته", URL: "/posts/a/"}}}
	home.Canonical = "https://site.test/"
	home.Structured = template.JS(`{"@type":"WebSite"}`)
	if _, err := renderThemeHTML(b, appearance, "https://site.test", home, &home, "", ""); err != nil {
		t.Fatalf("home render: %v", err)
	}
	bad := b
	bad.BodyHTML = `{{if .Content}}<p>hide</p>{{else}}{{range .Home.Items}}<a href="{{.URL}}">{{.Title}}</a>{{end}}{{end}}{{.Content.BodyHTML}}`
	if err := bad.validate(); err == nil {
		// The fallback must not make an invalid post layout look successful.
		t.Fatal("unconditional Content dereference accepted on home")
	}
}

func TestThemeAssetsRemainSiteScopedAndImmutable(t *testing.T) {
	root := t.TempDir()
	manifest := &publicationManifest{
		SiteID: "site-a", Origin: "https://a.site.test", Routes: map[string]publicationRoute{},
		Files: map[string]publicationFile{},
	}
	b := defaultThemeBundle()
	b.CSS = `body{background:#fff}`
	b.JavaScript = `document.body.dataset.ready="yes"`
	css, js, _, err := stageThemeAssets(root, manifest, b)
	if err != nil {
		t.Fatal(err)
	}
	for _, url := range []string{css, js} {
		route := manifest.Routes[url]
		if route.Kind != "file" || !strings.HasPrefix(route.File, "theme-assets/") {
			t.Fatalf("unsafe asset route %s: %#v", url, route)
		}
		body, err := os.ReadFile(filepath.Join(root, route.File))
		if err != nil || len(body) == 0 {
			t.Fatalf("asset missing %s: %v", url, err)
		}
		if _, err := os.Stat(filepath.Join(root, "assets", route.File)); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("theme asset unexpectedly accessed private Media lane: %v", err)
		}
	}
}

func TestThemeAssetManifestRouteAdmission(t *testing.T) {
	hash := strings.Repeat("a", 64)
	for _, path := range []string{
		"/theme-assets/" + hash + "-site.css",
		"/theme-assets/" + hash + "-site.js",
		"/theme-assets/" + hash + "-logo.png",
		"/theme-assets/" + hash + "-font.woff2",
	} {
		if !safeManifestRoute(path) {
			t.Fatalf("valid hashed Theme route rejected: %s", path)
		}
	}
	for _, path := range []string{
		"/theme-assets/../../private",
		"/theme-assets/" + hash + "-evil.exe",
		"/theme-assets/" + hash + "-bad/name.png",
		"/theme-assets/" + hash + "-bad.png/",
		"/theme-assets/garbage-site.js",
		"/assets/private",
	} {
		if safeManifestRoute(path) {
			t.Fatalf("unsafe Theme route admitted: %s", path)
		}
	}
}
