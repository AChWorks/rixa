// SPDX-License-Identifier: MPL-2.0

package product

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func testPublicPolicy() PublicPolicyConfig {
	return PublicPolicyConfig{
		Indexing: "allow",
		Snippet:  "allow",
		Crawlers: []PublicCrawlerPolicy{
			{UserAgent: "*", Access: "allow", Purpose: "search"},
			{UserAgent: "TrainingBot", Access: "disallow", Purpose: "ai-training"},
		},
	}
}

func TestPublicPolicyRequiresExplicitDefaultCrawlerRule(t *testing.T) {
	policy := testPublicPolicy()
	policy.Crawlers = policy.Crawlers[1:]
	if err := policy.validate(); !errors.Is(err, ErrConfiguration) {
		t.Fatalf("policy without wildcard accepted: %v", err)
	}
	policy = testPublicPolicy()
	policy.Crawlers[1].Purpose = "magic"
	if err := policy.validate(); !errors.Is(err, ErrConfiguration) {
		t.Fatalf("unknown crawler purpose accepted: %v", err)
	}
}

func TestPublicRootMustBePrivateAndDisjointFromMedia(t *testing.T) {
	dir := t.TempDir()
	mediaRoot := filepath.Join(dir, "media")
	publicRoot := filepath.Join(dir, "public")
	for _, root := range []string{mediaRoot, publicRoot} {
		if err := os.Mkdir(root, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	site := ResolvedSite{SiteConfig: SiteConfig{
		ID: "site", MediaRoot: mediaRoot, PublicRoot: publicRoot, PublicPolicy: testPublicPolicy(),
	}}
	if err := validateMediaRoots([]ResolvedSite{site}); err != nil {
		t.Fatalf("separate roots rejected: %v", err)
	}

	nested := filepath.Join(mediaRoot, "public")
	if err := os.Mkdir(nested, 0o700); err != nil {
		t.Fatal(err)
	}
	site.PublicRoot = nested
	if err := validateMediaRoots([]ResolvedSite{site}); !errors.Is(err, ErrConfiguration) {
		t.Fatalf("nested public root accepted: %v", err)
	}

	site.PublicRoot = publicRoot
	if err := os.Chmod(publicRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := validateMediaRoots([]ResolvedSite{site}); !errors.Is(err, ErrConfiguration) {
		t.Fatalf("world-readable public control root accepted: %v", err)
	}
}

func TestPublicPolicyCannotExistWithoutPublicRoot(t *testing.T) {
	config := Config{
		Language: "en",
		Control: ControlConfig{Origin: "https://control.rixa.test:8443", DatabaseEnv: "RIXA_CONTROL"},
		Sites: []SiteConfig{{
			ID: "site", Origin: "https://site.rixa.test:8443", DatabaseEnv: "RIXA_SITE",
			MediaRoot: "/var/lib/rixa/site/media", PublicPolicy: testPublicPolicy(),
		}},
	}
	if err := config.validateStructure(); !errors.Is(err, ErrConfiguration) {
		t.Fatalf("public policy without root accepted: %v", err)
	}
}
