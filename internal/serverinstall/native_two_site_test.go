// SPDX-License-Identifier: MPL-2.0

package serverinstall

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/AChWorks/rixa/internal/product"
)

func validTwoSiteDraft() Draft {
	d := validDraft()
	d.SiteBOrigin = "https://site-b.rixa.test:8443"
	d.SiteBDSN = "postgres://siteb:second-db-secret@127.0.0.1:5432/site_b?sslmode=disable"
	d.SiteBLogin = "site-b-admin"
	d.SiteBPassword = "second-site-password-strong"
	return d
}

func TestNativeOptionalTwoSiteConfigurationAndReviewInputs(t *testing.T) {
	single := validDraft()
	if p := ValidateDraft(single); len(p) != 0 {
		t.Fatalf("existing one-site input rejected: %+v", p)
	}
	if got := databaseTargetCount(single); got != 2 {
		t.Fatalf("single-site target count %d", got)
	}
	configSingle, err := product.ParseConfig(configBytes(single, "", ""))
	if err != nil || len(configSingle.Sites) != 1 || configSingle.Sites[0].ID != "site-a" {
		t.Fatalf("legacy one-site configuration changed: %v", err)
	}
	if bytes.Contains(environmentFile(single), []byte("RIXA_SITE_B_DSN")) || len(nativeInstallStages(single)) != 11 {
		t.Fatal("default native installation unexpectedly enables a second site")
	}

	d := validTwoSiteDraft()
	if p := ValidateDraft(d); len(p) != 0 {
		t.Fatalf("optional two-site draft rejected: %+v", p)
	}
	if got := databaseTargetCount(d); got != 3 {
		t.Fatalf("two-site database target count %d", got)
	}
	cfg, err := product.ParseConfig(configBytesWithSiteB(d, "AAAAAAAAAAAAAAAAAAAAAAAAAA", "BBBBBBBBBBBBBBBBBBBBBBBBBB", "CCCCCCCCCCCCCCCCCCCCCCCCCC"))
	if err != nil {
		t.Fatalf("native two-site config fails product parser: %v", err)
	}
	if len(cfg.Sites) != 2 || cfg.Sites[0].ID != "site-a" || cfg.Sites[1].ID != "site-b" ||
		cfg.Sites[0].DatabaseEnv != "RIXA_SITE_DSN" || cfg.Sites[1].DatabaseEnv != "RIXA_SITE_B_DSN" ||
		cfg.Sites[0].MediaRoot == cfg.Sites[1].MediaRoot || cfg.Sites[0].PublicRoot == cfg.Sites[1].PublicRoot ||
		cfg.Sites[1].AdminPrincipal != "CCCCCCCCCCCCCCCCCCCCCCCCCC" {
		t.Fatal("two-site config lacks independent identity, origin, roots or database")
	}
	if stages := nativeInstallStages(d); len(stages) != 12 || stages[7] != "bootstrap-site-b" {
		t.Fatalf("missing ordered dedicated second-site bootstrap: %v", stages)
	}
	if lookup, ok := databaseLookup(d)("RIXA_SITE_B_DSN"); !ok || lookup != d.SiteBDSN {
		t.Fatal("second-site credentials not bound under private database env")
	}
	if !bytes.Contains(environmentFile(d), []byte("RIXA_SITE_B_DSN=")) {
		t.Fatal("second database missing from private service environment")
	}
	if bytes.Contains([]byte(draftDigest(d)), []byte(d.SiteBPassword)) {
		t.Fatal("private password leaked into digest")
	}
	persisted := d
	persisted.SiteBPassword = ""
	if draftDigest(persisted) != draftDigest(d) {
		t.Fatal("password stripping broke frozen installed journal digest")
	}
	for _, test := range []struct {
		name, field string
		change      func(*Draft)
	}{
		{"missing-origin", "site_b_origin", func(d *Draft) { d.SiteBOrigin = "" }},
		{"missing-database", "site_b_dsn", func(d *Draft) { d.SiteBDSN = "" }},
		{"missing-login", "site_b_login", func(d *Draft) { d.SiteBLogin = "" }},
		{"missing-password", "site_b_password", func(d *Draft) { d.SiteBPassword = "" }},
		{"duplicate-control-host", "site_b_origin", func(d *Draft) { d.SiteBOrigin = d.ControlOrigin }},
		{"duplicate-site-host", "site_b_origin", func(d *Draft) { d.SiteBOrigin = d.SiteOrigin }},
		{"other-port", "site_b_origin", func(d *Draft) { d.SiteBOrigin = "https://site-b.rixa.test:9443" }},
		{"duplicate-control-dsn", "site_b_dsn", func(d *Draft) { d.SiteBDSN = d.ControlDSN }},
		{"duplicate-site-dsn", "site_b_dsn", func(d *Draft) { d.SiteBDSN = d.SiteDSN }},
		{"bad-secret", "site_b_password", func(d *Draft) { d.SiteBPassword = "short" }},
	} {
		t.Run(test.name, func(t *testing.T) {
			d := validTwoSiteDraft()
			test.change(&d)
			found := false
			for _, p := range ValidateDraft(d) {
				if p.Field == test.field {
					found = true
				}
			}
			if !found {
				t.Fatalf("second-site bad input admitted: %s", test.field)
			}
		})
	}
}

func TestNativeOptionalTwoSiteOwnedJournalAndSecretRedaction(t *testing.T) {
	m := testManager(t)
	d := validTwoSiteDraft()
	r, err := m.SaveDraft(context.Background(), d)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(r)
	for _, secret := range []string{d.SiteBPassword, d.SiteBDSN, "second-db-secret"} {
		if bytes.Contains(b, []byte(secret)) {
			t.Fatal("public result disclosed site-b secret")
		}
	}
	got, err := m.LoadDraft(context.Background(), d.OperationID)
	if err != nil || got != d {
		t.Fatalf("private second-site draft not recovered: %v", err)
	}
	if !strings.Contains(redactDiagnostic("site-b: "+d.SiteBPassword+" "+d.SiteBDSN, d), "[redacted]") ||
		strings.Contains(redactDiagnostic("site-b: "+d.SiteBPassword+" "+d.SiteBDSN, d), d.SiteBPassword) {
		t.Fatal("second-site diagnostics leaked secret")
	}
	parent := installedParent(t, m)
	parent.Draft = validTwoSiteDraft()
	parent.Draft.ControlPassword, parent.Draft.SitePassword, parent.Draft.SiteBPassword = "", "", ""
	parent.Frozen = draftDigest(parent.Draft)
	parent.DatabaseTargets["site-b"] = "1:102"
	parent.SiteBPrincipal = strings.Repeat("C", 26)
	parent.Attempts["bootstrap-site-b"] = "verified"
	if err := m.save(parent); err != nil {
		t.Fatal(err)
	}
	if _, err := m.load(parent.Draft.OperationID); err != nil {
		t.Fatalf("valid two-site installed journal rejected: %v", err)
	}
	parent.Attempts["bootstrap-site-b"] = "attempted"
	if err := m.save(parent); err != nil {
		t.Fatal(err)
	}
	if _, err := m.load(parent.Draft.OperationID); !errors.Is(err, ErrState) {
		t.Fatalf("incomplete second-site bootstrap accepted: %v", err)
	}
	parent.Attempts["bootstrap-site-b"] = "verified"
	delete(parent.DatabaseTargets, "site-b")
	if err := m.save(parent); err != nil {
		t.Fatal(err)
	}
	if _, err := m.load(parent.Draft.OperationID); !errors.Is(err, ErrState) {
		t.Fatalf("missing physical second-site target accepted: %v", err)
	}
}

func TestNativeTwoSiteConservativePreStopCompatibility(t *testing.T) {
	current := product.EditorialMigrationInspection{
		Scope: "rixa-editorial-only", Admission: "not-authorized",
		Sites: []product.EditorialMigrationSite{
			{ID: "site-a", State: "current", Current: 3, Target: 3},
			{ID: "site-b", State: "current", Current: 3, Target: 3},
		},
	}
	if err := preStopEditorialDecision(current); err != nil {
		t.Fatalf("two-site current ledger must reach candidate check: %v", err)
	}
	current.Sites[1].Current = 2
	current.Sites[1].State = "pending"
	current.Sites[1].Pending = []string{"rixa-editorial-v3-20261031-site-grants"}
	if err := preStopEditorialDecision(current); !errors.Is(err, errPreStopEditorialPending) {
		t.Fatalf("second-site pending ledger not rejected: %v", err)
	}
	current.Sites[0].State = "unknown"
	if err := preStopEditorialDecision(current); !errors.Is(err, ErrState) {
		t.Fatalf("pending site-b masked corrupted site-a ledger: %v", err)
	}
	current.Sites[0].State = "current"
	current.Sites[0], current.Sites[1] = current.Sites[1], current.Sites[0]
	if err := preStopEditorialDecision(current); !errors.Is(err, ErrState) {
		t.Fatalf("reordered site ledgers admitted: %v", err)
	}
}
