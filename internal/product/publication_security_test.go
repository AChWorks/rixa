// SPDX-License-Identifier: MPL-2.0

package product

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
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
