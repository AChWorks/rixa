// SPDX-License-Identifier: MPL-2.0
package serverinstall

import (
	"bytes"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/AChWorks/rixa/internal/product"
)

func TestComposeAddSiteConfigRetainsCurrentSettings(t *testing.T) {
	m := testManager(t)
	parent := installedParent(t, m)
	proposed := addSiteDraftFixture().AddSiteInspectionDraft
	proposed.InstallationOperation = parent.Draft.OperationID
	source := configBytesWithSiteB(parent.Draft, parent.ControlPrincipal, parent.SitePrincipal, "")
	var doc map[string]any
	if err := json.Unmarshal(source, &doc); err != nil {
		t.Fatal(err)
	}
	doc["language"] = "fa"
	doc["catalog_root"] = "/var/lib/rixa/catalog"
	doc["resources"] = map[string]any{"identity": map[string]any{"max_conns": 4}, "audit": map[string]any{"max_operations": 12}}
	doc["startup_timeout"] = "20s"
	doc["shutdown_timeout"] = "21s"
	first := doc["sites"].([]any)[0].(map[string]any)
	first["public_policy"] = map[string]any{"indexing": "allow", "snippet": "allow", "crawlers": []any{map[string]any{"user_agent": "*", "access": "allow", "purpose": "search"}}}
	source, _ = json.Marshal(doc)
	original, err := product.ParseConfig(source)
	if err != nil {
		t.Fatalf("invalid operator customization: %v", err)
	}
	env := environmentFile(parent.Draft)
	originalBytes := append([]byte(nil), source...)
	originalEnv := append([]byte(nil), env...)
	principal := strings.Repeat("C", 26)
	plan, err := composeAddSiteConfig(source, env, parent, proposed, principal)
	if err != nil {
		t.Fatalf("cannot compose two-site settings: %v", err)
	}
	if !bytes.Equal(source, originalBytes) || !bytes.Equal(env, originalEnv) {
		t.Fatal("composition altered original evidence")
	}
	got, err := product.ParseConfig(plan.Configuration)
	if err != nil || len(got.Sites) != 2 {
		t.Fatalf("invalid candidate: %v", err)
	}
	siteB := got.Sites[1]
	if siteB.ID != "site-b" || siteB.Origin != proposed.SiteBOrigin ||
		siteB.DatabaseEnv != "RIXA_SITE_B_DSN" || siteB.AdminPrincipal != principal ||
		siteB.MediaRoot != "/var/lib/rixa/site-b/media" ||
		siteB.PublicRoot != "/var/lib/rixa/site-b/public" ||
		siteB.PublicPolicy.Indexing != "noindex" || siteB.PublicPolicy.Snippet != "none" {
		t.Fatalf("candidate second site mismatch: %+v", siteB)
	}
	got.Sites = got.Sites[:1]
	if !reflect.DeepEqual(got, original) {
		t.Fatal("existing presentation or resource settings were changed")
	}
	if bytes.Contains(plan.Configuration, []byte(proposed.SiteBDSN)) ||
		bytes.Contains(plan.Configuration, []byte(proposed.SiteBPassword)) {
		t.Fatal("private credentials entered config")
	}
	if !bytes.Equal(plan.Environment, environmentFile(proposed.withInstallation(parent.Draft))) ||
		!bytes.HasPrefix(plan.Environment, env) ||
		bytes.Count(plan.Environment, []byte("RIXA_SITE_B_DSN=")) != 1 {
		t.Fatal("new env does not retain old values and append site-b")
	}
	pending, err := composeAddSiteConfig(source, env, parent, proposed, "")
	if err != nil {
		t.Fatal(err)
	}
	stillPending, err := product.ParseConfig(pending.Configuration)
	if err != nil || stillPending.Sites[1].AdminPrincipal != "" {
		t.Fatal("premature principal recorded")
	}
}

func TestComposeAddSiteConfigRejectsChangedSources(t *testing.T) {
	m := testManager(t)
	base := installedParent(t, m)
	proposed := addSiteDraftFixture().AddSiteInspectionDraft
	proposed.InstallationOperation = base.Draft.OperationID
	config := configBytesWithSiteB(base.Draft, base.ControlPrincipal, base.SitePrincipal, "")
	env := environmentFile(base.Draft)
	tests := []struct {
		name   string
		change func(*journal, *AddSiteInspectionDraft, *[]byte, *[]byte, *string)
	}{
		{"removed-parent", func(j *journal, _ *AddSiteInspectionDraft, _ *[]byte, _ *[]byte, _ *string) { j.State = "removed" }},
		{"site-b-already-owned", func(j *journal, _ *AddSiteInspectionDraft, _ *[]byte, _ *[]byte, _ *string) {
			j.Draft.SiteBOrigin = "https://existing.rixa.test:8443"
		}},
		{"changed-existing-env", func(_ *journal, _ *AddSiteInspectionDraft, _ *[]byte, e *[]byte, _ *string) {
			*e = append(*e, []byte("EXTRA=changed\n")...)
		}},
		{"changed-existing-site", func(_ *journal, _ *AddSiteInspectionDraft, c *[]byte, _ *[]byte, _ *string) {
			*c = bytes.Replace(*c, []byte(base.Draft.SiteOrigin), []byte("https://changed.rixa.test:8443"), 1)
		}},
		{"invalid-proposed-origin", func(_ *journal, d *AddSiteInspectionDraft, _ *[]byte, _ *[]byte, _ *string) {
			d.SiteBOrigin = "http://insecure.rixa.test"
		}},
		{"invalid-new-principal", func(_ *journal, _ *AddSiteInspectionDraft, _ *[]byte, _ *[]byte, p *string) { *p = "invalid" }},
		{"reused-existing-principal", func(j *journal, _ *AddSiteInspectionDraft, _ *[]byte, _ *[]byte, p *string) { *p = j.SitePrincipal }},
		{"unknown-config-field", func(_ *journal, _ *AddSiteInspectionDraft, c *[]byte, _ *[]byte, _ *string) {
			*c = append(bytes.TrimSuffix(*c, []byte("}")), []byte(",\"untrusted\":true}")...)
		}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			p := *base
			d := proposed
			c := append([]byte(nil), config...)
			e := append([]byte(nil), env...)
			principal := strings.Repeat("C", 26)
			tc.change(&p, &d, &c, &e, &principal)
			beforeConfig := append([]byte(nil), c...)
			beforeEnv := append([]byte(nil), e...)
			plan, err := composeAddSiteConfig(c, e, &p, d, principal)
			if !errors.Is(err, ErrState) || len(plan.Configuration) > 0 || len(plan.Environment) > 0 {
				t.Fatalf("invalid topology plan accepted: %v", err)
			}
			if !bytes.Equal(c, beforeConfig) || !bytes.Equal(e, beforeEnv) {
				t.Fatal("failed preflight mutated existing evidence")
			}
		})
	}
}
