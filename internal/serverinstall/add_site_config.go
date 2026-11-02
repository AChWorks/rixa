// SPDX-License-Identifier: MPL-2.0
package serverinstall

import (
	"bytes"
	"encoding/json"
	"reflect"

	"github.com/AChWorks/rixa/internal/product"
)

// addSiteConfigPlan is private pre-activation material. In particular, its
// Environment contains all three database credentials and must never appear
// in an operator result, ordinary log or public planning response.
type addSiteConfigPlan struct {
	Configuration []byte
	Environment   []byte
}

// composeAddSiteConfig proves that the proposed two-site configuration can
// retain all current, operator-selected presentation/resource settings. The
// original single-site configuration is never reconstructed from the original
// installer defaults: updates may have retained legitimate changes to it.
// This is a pure source-level preflight, not activation or permission to Apply.
func composeAddSiteConfig(originalConfig, originalEnv []byte, parent *journal, proposed AddSiteInspectionDraft, siteBPrincipal string) (addSiteConfigPlan, error) {
	var result addSiteConfigPlan
	if parent == nil || parent.State != "installed" || parent.Frozen == "" ||
		siteBRequested(parent.Draft) || len(ValidateAddSiteInspection(parent.Draft, proposed)) != 0 ||
		siteBPrincipal != "" && (!principalSyntax.MatchString(siteBPrincipal) ||
			siteBPrincipal == parent.ControlPrincipal || siteBPrincipal == parent.SitePrincipal) ||
		!bytes.Equal(originalEnv, environmentFile(parent.Draft)) {
		return result, ErrState
	}
	original, err := product.ParseConfig(originalConfig)
	if err != nil {
		return result, ErrState
	}
	baseline, err := product.ParseConfig(configBytesWithSiteB(parent.Draft, parent.ControlPrincipal, parent.SitePrincipal, ""))
	if err != nil || original.HostingProfile != product.HostingProfileDirectTLS || len(original.Sites) != 1 ||
		original.Sites[0].ID != "site-a" || !supportedUpdateConfig(original, baseline) {
		return result, ErrState
	}
	// Strict product parsing above rejects unknown JSON fields. Keep the
	// existing root and site JSON values intact, so all supported current
	// settings and the original site administrator identity survive.
	var document map[string]json.RawMessage
	if err := json.Unmarshal(originalConfig, &document); err != nil {
		return result, ErrState
	}
	var sites []json.RawMessage
	if err := json.Unmarshal(document["sites"], &sites); err != nil || len(sites) != 1 {
		return result, ErrState
	}
	candidate := product.SiteConfig{
		ID: "site-b", Origin: proposed.SiteBOrigin, DatabaseEnv: "RIXA_SITE_B_DSN",
		MediaRoot: "/var/lib/rixa/site-b/media", PublicRoot: "/var/lib/rixa/site-b/public",
		PublicPolicy: product.PublicPolicyConfig{
			Indexing: "noindex", Snippet: "none",
			Crawlers: []product.PublicCrawlerPolicy{{UserAgent: "*", Access: "disallow", Purpose: "search"}},
		},
		AdminPrincipal: siteBPrincipal,
	}
	siteBytes, err := json.Marshal(candidate)
	if err != nil {
		return result, ErrState
	}
	sites = append(sites, siteBytes)
	document["sites"], err = json.Marshal(sites)
	if err != nil {
		return result, ErrState
	}
	result.Configuration, err = json.Marshal(document)
	if err != nil {
		return addSiteConfigPlan{}, ErrState
	}
	parsed, err := product.ParseConfig(result.Configuration)
	// ParseConfig checks structural constraints; the actual product runtime
	// also rejects catalog roots overlapping any site's Media/Public roots.
	// Use the same pure check as BuildRuntime's catalogSources admission.
	// Future site-b storage paths must not need to exist at Review time.
	if err != nil || parsed.ValidateCatalogStorageIsolation() != nil || len(parsed.Sites) != 2 ||
		!reflect.DeepEqual(parsed.Sites[0], original.Sites[0]) || !reflect.DeepEqual(parsed.Sites[1], candidate) {
		return addSiteConfigPlan{}, ErrState
	}
	// No other existing semantic field is permitted to change.
	parsed.Sites = parsed.Sites[:1]
	if !reflect.DeepEqual(parsed, original) {
		return addSiteConfigPlan{}, ErrState
	}
	result.Environment = environmentFile(proposed.withInstallation(parent.Draft))
	if !bytes.HasPrefix(result.Environment, originalEnv) ||
		bytes.Count(result.Environment, []byte("RIXA_SITE_B_DSN=")) != 1 {
		return addSiteConfigPlan{}, ErrState
	}
	return result, nil
}
