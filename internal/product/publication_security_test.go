// SPDX-License-Identifier: MPL-2.0

package product

import (
	"crypto/sha256"
	"crypto/tls"
	"encoding/hex"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestNormalizePublicationRouteUsesBoundedURLSafeSegments(t *testing.T) {
	for input, want := range map[string]string{
		"/news/launch/":   "/news/launch/",
		"/page-1":         "/page-1/",
		"/nested/path_2/": "/nested/path_2/",
		"/A.B~c/":         "/A.B~c/",
	} {
		got, err := normalizePublicationRoute(input)
		if err != nil || got != want {
			t.Fatalf("normalize %q = %q err=%v want=%q", input, got, err, want)
		}
	}
	for _, input := range []string{
		"/", "/admin", "/auth/x/", "/assets/a/", "/sitemap.xml", "/robots.txt",
		"/space here/", "/فارسی/", "/a%2Fb/", "/a//b/", "/../", "/./", "/-leading/",
		strings.Repeat("/a", 300),
	} {
		if got, err := normalizePublicationRoute(input); !errors.Is(err, ErrEditorialInvalid) {
			t.Fatalf("unsafe route %q accepted as %q err=%v", input, got, err)
		}
	}
}

func TestSafePublicRequestRejectsEveryEncodedPath(t *testing.T) {
	canonical := httptest.NewRequest(http.MethodGet, "https://site.example/news/launch/", nil)
	if canonical.URL.RawPath != "" || !safePublicRequest(canonical) {
		t.Fatal("canonical unencoded public path was rejected")
	}
	for _, raw := range []string{
		"/%6Eews/launch/",
		"/news%2Flaunch/",
		"/%2E%2E/news/",
		"/%5Cnews/",
		"/%00news/",
	} {
		req := httptest.NewRequest(http.MethodGet, "https://site.example"+raw, nil)
		if safePublicRequest(req) {
			t.Fatalf("encoded public path %q was accepted as path=%q raw=%q", raw, req.URL.Path, req.URL.RawPath)
		}
	}
}

func TestPublicationReadyRejectsSameSizeTamperedArtifact(t *testing.T) {
	root := t.TempDir()
	if err := os.Chmod(root, 0o700); err != nil {
		t.Fatal(err)
	}
	generationID := "AAAAAAAAAAAAAAAAAAAAAAAAAA"
	generationRoot := filepath.Join(root, "generations", generationID)
	publicRoot := filepath.Join(generationRoot, "public")
	if err := os.MkdirAll(publicRoot, 0o700); err != nil {
		t.Fatal(err)
	}

	policy := testPublicPolicy()
	manifest := &publicationManifest{
		Version:            publicationManifestVersion,
		SiteID:             "site",
		Origin:             "https://site.example",
		Generation:         generationID,
		CreatedAt:          time.Unix(10, 0).UTC(),
		SourceFingerprint:  strings.Repeat("a", 64),
		AppearanceRevision: 1,
		Policy:             policy,
		Operation: PublicationOperation{
			ID:          "BBBBBBBBBBBBBBBBBBBBBBBBBB",
			RequestHash: strings.Repeat("b", 64),
			Generation:  generationID,
			CreatedAt:   time.Unix(10, 0).UTC(),
		},
		Entries: map[string]publicationEntry{},
		Routes: map[string]publicationRoute{
			"/":            {Kind: "file", File: "home.html"},
			"/sitemap.xml": {Kind: "file", File: "sitemap.xml"},
			"/robots.txt":  {Kind: "file", File: "robots.txt"},
		},
		Assets: map[string]publicationAsset{},
		Files:  map[string]publicationFile{},
	}
	writeArtifact := func(name, contentType string, body []byte) {
		t.Helper()
		path := filepath.Join(publicRoot, name)
		if err := os.WriteFile(path, body, 0o600); err != nil {
			t.Fatal(err)
		}
		sum := sha256.Sum256(body)
		manifest.Files[name] = publicationFile{
			Path: name, ContentType: contentType, Cache: generatedFileCache,
			Size: int64(len(body)), SHA256: hex.EncodeToString(sum[:]),
		}
	}
	writeArtifact("home.html", "text/html; charset=utf-8", []byte("GOOD"))
	writeArtifact("sitemap.xml", "application/xml; charset=utf-8", []byte("<urlset/>"))
	writeArtifact("robots.txt", "text/plain; charset=utf-8", []byte("User-agent: *\n"))
	if err := writePublicationManifest(generationRoot, manifest); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "current"), []byte(generationID+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	service := &PublicationService{
		siteID: "site", origin: "https://site.example", root: root, policy: policy,
		slots: make(chan struct{}, publicReadConcurrency),
	}
	if err := service.Ready(); err != nil {
		t.Fatalf("valid generation failed readiness: %v", err)
	}

	if err := os.WriteFile(filepath.Join(publicRoot, "home.html"), []byte("EVIL"), 0o600); err != nil {
		t.Fatal(err)
	}
	reloaded := &PublicationService{
		siteID: "site", origin: "https://site.example", root: root, policy: policy,
		slots: make(chan struct{}, publicReadConcurrency),
	}
	if err := reloaded.Ready(); !errors.Is(err, ErrEditorialUnavailable) {
		t.Fatalf("same-size artifact tampering was not rejected: %v", err)
	}
}


func TestStaticPublicationReadNeedsNoDynamicServices(t *testing.T) {
	root := t.TempDir()
	generationRoot := filepath.Join(root, "generations", "CCCCCCCCCCCCCCCCCCCCCCCCCC")
	publicRoot := filepath.Join(generationRoot, "public")
	if err := os.MkdirAll(publicRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	body := []byte("<!doctype html><html><body>static-only</body></html>")
	if err := os.WriteFile(filepath.Join(publicRoot, "home.html"), body, 0o600); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(body)
	manifest := &publicationManifest{
		Generation: "CCCCCCCCCCCCCCCCCCCCCCCCCC",
		CreatedAt:  time.Unix(20, 0).UTC(),
		Routes: map[string]publicationRoute{
			"/":             {Kind: "file", File: "home.html"},
			"/news/launch/": {Kind: "file", File: "home.html"},
		},
		Files: map[string]publicationFile{
			"home.html": {
				Path: "home.html", ContentType: "text/html; charset=utf-8",
				Cache: generatedFileCache, Size: int64(len(body)), SHA256: hex.EncodeToString(sum[:]),
			},
		},
		Assets: map[string]publicationAsset{},
		Entries: map[string]publicationEntry{},
	}
	generation := &publishedGeneration{manifest: manifest, root: generationRoot}
	service := &PublicationService{
		authority: "site.example", slots: make(chan struct{}, publicReadConcurrency),
	}
	service.state.Store(newPublicationReadState([]*publishedGeneration{generation}))
	service.ready.Store(true)

	req := httptest.NewRequest(http.MethodGet, "https://site.example/", nil)
	req.Host = "site.example"
	req.TLS = &tls.ConnectionState{}
	rec := httptest.NewRecorder()
	service.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "static-only") {
		t.Fatalf("static read unexpectedly needed dynamic services: status=%d body=%q", rec.Code, rec.Body.String())
	}

	canonicalRoute := httptest.NewRequest(http.MethodGet, "https://site.example/news/launch/", nil)
	canonicalRoute.Host = "site.example"
	canonicalRoute.TLS = &tls.ConnectionState{}
	canonicalRecorder := httptest.NewRecorder()
	service.ServeHTTP(canonicalRecorder, canonicalRoute)
	if canonicalRecorder.Code != http.StatusOK {
		t.Fatalf("canonical ASCII route status=%d", canonicalRecorder.Code)
	}

	encodedRoute := httptest.NewRequest(http.MethodGet, "https://site.example/%6Eews/launch/", nil)
	encodedRoute.Host = "site.example"
	encodedRoute.TLS = &tls.ConnectionState{}
	if encodedRoute.URL.RawPath == "" || encodedRoute.URL.Path != "/news/launch/" {
		t.Fatalf("encoded route fixture path=%q raw=%q", encodedRoute.URL.Path, encodedRoute.URL.RawPath)
	}
	encodedRecorder := httptest.NewRecorder()
	service.ServeHTTP(encodedRecorder, encodedRoute)
	if encodedRecorder.Code != http.StatusNotFound {
		t.Fatalf("encoded spelling reached canonical route: status=%d", encodedRecorder.Code)
	}
}
