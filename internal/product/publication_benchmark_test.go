// SPDX-License-Identifier: MPL-2.0

package product

import (
	"crypto/sha256"
	"encoding/hex"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func BenchmarkStaticPublicationHTTPSRead(b *testing.B) {
	root := b.TempDir()
	generationRoot := filepath.Join(root, "generations", "AAAAAAAAAAAAAAAAAAAAAAAAAA")
	publicRoot := filepath.Join(generationRoot, "public")
	if err := os.MkdirAll(publicRoot, 0o700); err != nil {
		b.Fatal(err)
	}
	body := []byte("<!doctype html><html><body><main><h1>Rixa static path</h1></main></body></html>")
	path := filepath.Join(publicRoot, "home.html")
	if err := os.WriteFile(path, body, 0o600); err != nil {
		b.Fatal(err)
	}
	sum := sha256.Sum256(body)
	manifest := &publicationManifest{
		Generation: "AAAAAAAAAAAAAAAAAAAAAAAAAA",
		CreatedAt:  time.Unix(1, 0).UTC(),
		Files: map[string]publicationFile{
			"home.html": {
				Path: "home.html", ContentType: "text/html; charset=utf-8",
				Cache: generatedFileCache, Size: int64(len(body)), SHA256: hex.EncodeToString(sum[:]),
			},
		},
		Routes: map[string]publicationRoute{
			"/": {Kind: "file", File: "home.html"},
		},
		Assets:  map[string]publicationAsset{},
		Entries: map[string]publicationEntry{},
	}
	generation := &publishedGeneration{manifest: manifest, root: generationRoot}
	service := &PublicationService{slots: make(chan struct{}, publicReadConcurrency)}
	service.state.Store(newPublicationReadState([]*publishedGeneration{generation}))
	service.ready.Store(true)

	var ingress http.Handler
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ingress.ServeHTTP(w, r)
	}))
	server.StartTLS()
	defer server.Close()
	parsed, err := url.Parse(server.URL)
	if err != nil {
		b.Fatal(err)
	}
	service.authority = parsed.Host
	siteOrigin := "https://" + parsed.Host
	runtime := &Runtime{
		Config: RuntimeConfig{
			Config: Config{
				Control: ControlConfig{Origin: "https://control.invalid"},
				Sites:   []SiteConfig{{ID: "site", Origin: siteOrigin}},
			},
			Sites: []ResolvedSite{{SiteConfig: SiteConfig{ID: "site", Origin: siteOrigin}}},
		},
		Control: &ControlRuntime{Handler: http.NotFoundHandler()},
		Sites: map[string]*SiteRuntime{
			"site": {ID: "site", Origin: siteOrigin, Handler: siteRequestHandler(http.NotFoundHandler(), service)},
		},
	}
	ingress = newIngressHandler(runtime)

	client := server.Client()
	client.Timeout = 5 * time.Second

	request, err := http.NewRequest(http.MethodGet, server.URL+"/", nil)
	if err != nil {
		b.Fatal(err)
	}
	response, err := client.Do(request)
	if err != nil {
		b.Fatal(err)
	}
	_, _ = io.Copy(io.Discard, response.Body)
	_ = response.Body.Close()
	if response.StatusCode != http.StatusOK {
		b.Fatalf("warmup status=%d", response.StatusCode)
	}

	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		response, err = client.Get(server.URL + "/")
		if err != nil {
			b.Fatal(err)
		}
		if response.StatusCode != http.StatusOK {
			_ = response.Body.Close()
			b.Fatalf("status=%d", response.StatusCode)
		}
		if _, err = io.Copy(io.Discard, response.Body); err != nil {
			_ = response.Body.Close()
			b.Fatal(err)
		}
		if err = response.Body.Close(); err != nil {
			b.Fatal(err)
		}
	}
}
