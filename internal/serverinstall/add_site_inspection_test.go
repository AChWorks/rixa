// SPDX-License-Identifier: MPL-2.0
package serverinstall

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"math/big"
	"reflect"
	"strings"
	"testing"
	"time"
)

func candidateInspection() (Draft, AddSiteInspectionDraft) {
	base := validDraft()
	base.ControlPassword = ""
	base.SitePassword = ""
	site := validTwoSiteDraft()
	return base, AddSiteInspectionDraft{
		InstallationOperation: base.OperationID, SiteBOrigin: site.SiteBOrigin,
		SiteBDSN: site.SiteBDSN, SiteBLogin: site.SiteBLogin, SiteBPassword: site.SiteBPassword,
	}
}

func TestAddSiteInspectionValidatesCandidateWithoutRestoringOldSecrets(t *testing.T) {
	base, proposed := candidateInspection()
	before := base
	if p := ValidateAddSiteInspection(base, proposed); len(p) != 0 {
		t.Fatalf("valid appended site rejected: %+v", p)
	}
	if !reflect.DeepEqual(base, before) || base.ControlPassword != "" || base.SitePassword != "" {
		t.Fatal("inspection changed parent or reintroduced prior bootstrap credentials")
	}
	extended := proposed.withInstallation(base)
	if !siteBRequested(extended) || databaseTargetCount(extended) != 3 ||
		extended.ControlDSN != base.ControlDSN || extended.SiteDSN != base.SiteDSN ||
		extended.ControlPassword != "" || extended.SitePassword != "" {
		t.Fatal("new candidate fails to retain original site/data inputs unchanged")
	}
	if strings.Contains(addSiteInspectionResult(proposed.InstallationOperation).Message, proposed.SiteBDSN) {
		t.Fatal("private candidate appeared in public operation result")
	}
	cases := []struct {
		name, field string
		change      func(*Draft, *AddSiteInspectionDraft)
	}{
		{"wrong-owner", "installation_operation", func(_ *Draft, d *AddSiteInspectionDraft) { d.InstallationOperation = "different-install" }},
		{"already-enabled", "site_b_origin", func(b *Draft, d *AddSiteInspectionDraft) { b.SiteBOrigin = d.SiteBOrigin }},
		{"empty", "site_b_origin", func(_ *Draft, d *AddSiteInspectionDraft) {
			*d = AddSiteInspectionDraft{InstallationOperation: d.InstallationOperation}
		}},
		{"missing-origin", "site_b_origin", func(_ *Draft, d *AddSiteInspectionDraft) { d.SiteBOrigin = "" }},
		{"missing-dsn", "site_b_dsn", func(_ *Draft, d *AddSiteInspectionDraft) { d.SiteBDSN = "" }},
		{"missing-login", "site_b_login", func(_ *Draft, d *AddSiteInspectionDraft) { d.SiteBLogin = "" }},
		{"missing-password", "site_b_password", func(_ *Draft, d *AddSiteInspectionDraft) { d.SiteBPassword = "" }},
		{"duplicate-host-a", "site_b_origin", func(b *Draft, d *AddSiteInspectionDraft) { d.SiteBOrigin = b.SiteOrigin }},
		{"duplicate-host-control", "site_b_origin", func(b *Draft, d *AddSiteInspectionDraft) { d.SiteBOrigin = b.ControlOrigin }},
		{"wrong-port", "site_b_origin", func(_ *Draft, d *AddSiteInspectionDraft) { d.SiteBOrigin = "https://site-b.rixa.test:443" }},
		{"shared-dsn", "site_b_dsn", func(b *Draft, d *AddSiteInspectionDraft) { d.SiteBDSN = b.SiteDSN }},
		{"shared-password", "site_b_dsn", func(b *Draft, d *AddSiteInspectionDraft) {
			d.SiteBDSN = strings.Replace(d.SiteBDSN, "second-db-secret", "site-db-secret", 1)
		}},
		{"bad-bootstrap-password", "site_b_password", func(_ *Draft, d *AddSiteInspectionDraft) { d.SiteBPassword = "tiny" }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			b, d := candidateInspection()
			tc.change(&b, &d)
			problems := ValidateAddSiteInspection(b, d)
			found := false
			for _, p := range problems {
				if p.Field == tc.field {
					found = true
				}
			}
			if !found {
				t.Fatalf("unsafe or incomplete candidate admitted: %+v", problems)
			}
		})
	}
}

func TestAddSiteTLSCertificateCoversOnlyActualHostnames(t *testing.T) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	serial, _ := rand.Int(rand.Reader, big.NewInt(1<<60))
	template := &x509.Certificate{SerialNumber: serial, Subject: pkix.Name{CommonName: "site-b.rixa.test"},
		DNSNames: []string{"site-b.rixa.test"}, NotBefore: now.Add(-time.Hour),
		NotAfter: now.Add(time.Hour), ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	cert := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	if !tlsCoversAddSite(cert, "https://site-b.rixa.test:8443", now) {
		t.Fatal("matching TLS SAN rejected")
	}
	for _, origin := range []string{"https://site-a.rixa.test:8443", "http://site-b.rixa.test:8443", "https://site-b.rixa.test.bad:8443"} {
		if tlsCoversAddSite(cert, origin, now) {
			t.Fatalf("unexpected TLS acceptance: %s", origin)
		}
	}
	if tlsCoversAddSite(cert, "https://site-b.rixa.test:8443", now.Add(2*time.Hour)) ||
		tlsCoversAddSite([]byte("not a certificate"), "https://site-b.rixa.test:8443", now) {
		t.Fatal("invalid/expired TLS certificate admitted")
	}
	template.ExtKeyUsage = []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}
	der, err = x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	if tlsCoversAddSite(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), "https://site-b.rixa.test:8443", now) {
		t.Fatal("client-only TLS certificate admitted")
	}
}

func TestInspectAddSiteRefusesMissingInstallationBeforeDatabaseAccess(t *testing.T) {
	m := testManager(t)
	_, proposed := candidateInspection()
	res, err := m.InspectAddSite(context.Background(), proposed)
	if !errors.Is(err, ErrState) || res.Code != "installation-ownership" ||
		res.State != "blocked" || res.ReviewToken != "" {
		t.Fatalf("absent installed owner admitted: %+v %v", res, err)
	}
}
