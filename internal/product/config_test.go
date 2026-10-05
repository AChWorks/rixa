// SPDX-License-Identifier: MPL-2.0

package product

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

const (
	testControlAdmin = "AAAAAAAAAAAAAAAAAAAAAAAAAA"
	testSiteAAdmin   = "BBBBBBBBBBBBBBBBBBBBBBBBBB"
	testSiteBAdmin   = "CCCCCCCCCCCCCCCCCCCCCCCCCC"
)

func TestResolveRuntimeSkipsDisabledSiteResources(t *testing.T) {
	dir := t.TempDir()
	rootA := filepath.Join(dir, "media-a")
	if err := os.Mkdir(rootA, 0o700); err != nil {
		t.Fatal(err)
	}
	cert, key, _ := writeTestCertificate(t, dir, []string{"control.rixa.test", "a.rixa.test", "disabled.rixa.test"})
	config := Config{
		Listen:          "127.0.0.1:18443",
		TLS:             TLSConfig{CertFile: cert, KeyFile: key},
		Language:        "en",
		StartupTimeout:  5 * time.Second,
		ShutdownTimeout: 5 * time.Second,
		Control: ControlConfig{
			Origin:         "https://control.rixa.test:18443",
			DatabaseEnv:    "RIXA_TEST_CONTROL",
			AdminPrincipal: testControlAdmin,
		},
		Sites: []SiteConfig{
			{
				ID:             "site-a",
				Origin:         "https://a.rixa.test:18443",
				DatabaseEnv:    "RIXA_TEST_A",
				MediaRoot:      rootA,
				AdminPrincipal: testSiteAAdmin,
			},
			{
				ID:          "disabled",
				Origin:      "https://disabled.rixa.test:18443",
				DatabaseEnv: "RIXA_TEST_DISABLED",
				MediaRoot:   filepath.Join(dir, "does-not-exist"),
				Disabled:    true,
			},
		},
	}
	if err := config.validateStructure(); err != nil {
		t.Fatal(err)
	}
	values := map[string]string{
		"RIXA_TEST_CONTROL": "postgres://control:secret@127.0.0.1:5432/control?sslmode=disable",
		"RIXA_TEST_A":       "postgres://a:secret@127.0.0.1:5432/site_a?sslmode=disable",
	}
	resolved, err := config.resolveRuntime(context.Background(), mapLookup(values), nil)
	if err != nil {
		t.Fatal(err)
	}
	if resolved.Sites[1].DSN != "" {
		t.Fatal("disabled site resolved a database secret")
	}
	runtime, err := BuildRuntime(resolved, nil)
	if err != nil {
		t.Fatal(err)
	}
	if runtime.Sites["disabled"] != nil {
		t.Fatal("disabled site acquired a runtime")
	}
	if !runtime.UsesMultiSite() {
		t.Fatal("declared two-site inventory should use the resolver")
	}
}

func TestSingleSiteCompositionOmitsMultiSite(t *testing.T) {
	dir := t.TempDir()
	root := filepath.Join(dir, "media")
	if err := os.Mkdir(root, 0o700); err != nil {
		t.Fatal(err)
	}
	cert, key, _ := writeTestCertificate(t, dir, []string{"control.rixa.test", "site.rixa.test"})
	config := Config{
		Listen:          "127.0.0.1:18444",
		TLS:             TLSConfig{CertFile: cert, KeyFile: key},
		Language:        "fa",
		StartupTimeout:  5 * time.Second,
		ShutdownTimeout: 5 * time.Second,
		Control: ControlConfig{
			Origin:         "https://control.rixa.test:18444",
			DatabaseEnv:    "RIXA_TEST_CONTROL",
			AdminPrincipal: testControlAdmin,
		},
		Sites: []SiteConfig{{
			ID:             "site",
			Origin:         "https://site.rixa.test:18444",
			DatabaseEnv:    "RIXA_TEST_SITE",
			MediaRoot:      root,
			AdminPrincipal: testSiteAAdmin,
		}},
	}
	if err := config.validateStructure(); err != nil {
		t.Fatal(err)
	}
	resolved, err := config.resolveRuntime(context.Background(), mapLookup(map[string]string{
		"RIXA_TEST_CONTROL": "postgres://control:secret@127.0.0.1:5432/control?sslmode=disable",
		"RIXA_TEST_SITE":    "postgres://site:secret@127.0.0.1:5432/site?sslmode=disable",
	}), nil)
	if err != nil {
		t.Fatal(err)
	}
	runtime, err := BuildRuntime(resolved, nil)
	if err != nil {
		t.Fatal(err)
	}
	if runtime.UsesMultiSite() {
		t.Fatal("single-site profile composed Multi-Site")
	}
}

func TestLoadConfigRejectsUnknownFields(t *testing.T) {
	path := filepath.Join(t.TempDir(), "rixa.json")
	data := []byte(`{"listen":"127.0.0.1:8443","tls":{},"control":{"origin":"https://control.test:8443","database_env":"RIXA_CONTROL"},"sites":[{"id":"a","origin":"https://a.test:8443","database_env":"RIXA_A","media_root":"/tmp/a"}],"unknown":true}`)
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadConfig(path); !errors.Is(err, ErrConfiguration) {
		t.Fatalf("expected strict decode failure, got %v", err)
	}
}

func mapLookup(values map[string]string) func(string) (string, bool) {
	return func(key string) (string, bool) {
		value, ok := values[key]
		return value, ok
	}
}

func TestRuntimeValidationRejectsLooseTLSKeyAndSymlinkMediaRoot(t *testing.T) {
	dir := t.TempDir()
	realRoot := filepath.Join(dir, "real-media")
	if err := os.Mkdir(realRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	linkRoot := filepath.Join(dir, "media-link")
	if err := os.Symlink(realRoot, linkRoot); err != nil {
		t.Fatal(err)
	}
	cert, key, _ := writeTestCertificate(t, dir, []string{"control.rixa.test", "site.rixa.test"})
	config := Config{
		Listen: "127.0.0.1:18446", TLS: TLSConfig{CertFile: cert, KeyFile: key}, Language: "en",
		StartupTimeout: time.Second, ShutdownTimeout: time.Second,
		Control: ControlConfig{Origin: "https://control.rixa.test:18446", DatabaseEnv: "RIXA_CONTROL", AdminPrincipal: testControlAdmin},
		Sites:   []SiteConfig{{ID: "site", Origin: "https://site.rixa.test:18446", DatabaseEnv: "RIXA_SITE", MediaRoot: linkRoot, AdminPrincipal: testSiteAAdmin}},
	}
	if err := os.Chmod(key, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := config.ValidateRuntimeFiles(); !errors.Is(err, ErrConfiguration) {
		t.Fatalf("loose TLS key permissions accepted: %v", err)
	}
	if err := os.Chmod(key, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := validateMediaRoots([]ResolvedSite{{SiteConfig: config.Sites[0]}}); !errors.Is(err, ErrConfiguration) {
		t.Fatalf("symlink media root accepted: %v", err)
	}
}

func TestConfigRejectsSharedAuthenticationHostnameAcrossPorts(t *testing.T) {
	base := Config{
		Language: "en",
		Control:  ControlConfig{Origin: "https://shared.rixa.test:8443", DatabaseEnv: "RIXA_CONTROL"},
		Sites:    []SiteConfig{{ID: "a", Origin: "https://site-a.rixa.test:8443", DatabaseEnv: "RIXA_A", MediaRoot: "/var/lib/rixa/a"}},
	}

	controlCollision := base
	controlCollision.Sites = []SiteConfig{{ID: "a", Origin: "https://shared.rixa.test:9443", DatabaseEnv: "RIXA_A", MediaRoot: "/var/lib/rixa/a"}}
	if err := controlCollision.validateStructure(); !errors.Is(err, ErrConfiguration) {
		t.Fatalf("control/site hostname collision accepted: %v", err)
	}

	siteCollision := base
	siteCollision.Sites = []SiteConfig{
		{ID: "a", Origin: "https://sites.rixa.test:8443", DatabaseEnv: "RIXA_A", MediaRoot: "/var/lib/rixa/a"},
		{ID: "b", Origin: "https://sites.rixa.test:9443", DatabaseEnv: "RIXA_B", MediaRoot: "/var/lib/rixa/b"},
	}
	if err := siteCollision.validateStructure(); !errors.Is(err, ErrConfiguration) {
		t.Fatalf("site/site hostname collision accepted: %v", err)
	}
}

func TestMediaRootsRejectParentSymlinkAliasesAndNestedTrees(t *testing.T) {
	dir := t.TempDir()
	realParent := filepath.Join(dir, "real")
	rootA := filepath.Join(realParent, "a")
	nested := filepath.Join(rootA, "nested")
	for _, path := range []string{realParent, rootA, nested} {
		if err := os.Mkdir(path, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	aliasParent := filepath.Join(dir, "alias")
	if err := os.Symlink(realParent, aliasParent); err != nil {
		t.Fatal(err)
	}
	aliasA := filepath.Join(aliasParent, "a")
	if err := validateMediaRoots([]ResolvedSite{{SiteConfig: SiteConfig{ID: "alias", MediaRoot: aliasA}}}); !errors.Is(err, ErrConfiguration) {
		t.Fatalf("parent symlink alias accepted: %v", err)
	}
	if err := validateMediaRoots([]ResolvedSite{
		{SiteConfig: SiteConfig{ID: "a", MediaRoot: rootA}},
		{SiteConfig: SiteConfig{ID: "nested", MediaRoot: nested}},
	}); !errors.Is(err, ErrConfiguration) {
		t.Fatalf("nested media roots accepted: %v", err)
	}
	rootB := filepath.Join(realParent, "b")
	if err := os.Mkdir(rootB, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := validateMediaRoots([]ResolvedSite{
		{SiteConfig: SiteConfig{ID: "a", MediaRoot: rootA}},
		{SiteConfig: SiteConfig{ID: "b", MediaRoot: rootB}},
	}); err != nil {
		t.Fatalf("separate private roots rejected: %v", err)
	}
}
