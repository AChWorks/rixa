// SPDX-License-Identifier: MPL-2.0

package serverinstall

import (
	"errors"
	"os"
	"reflect"
	"testing"
)

func TestDraftFieldsAllowPendingCoupledEdits(t *testing.T) {
	cases := []struct {
		name, field, code string
		change            func(*Draft)
	}{
		{"later-listener", "control_origin", "port", func(d *Draft) {
			d.ControlOrigin = "https://control.rixa.test"
			d.SiteOrigin = "https://site.rixa.test"
		}},
		{"hostnames-during-swap", "site_origin", "isolation", func(d *Draft) { d.ControlOrigin = d.SiteOrigin }},
		{"databases-during-swap", "site_dsn", "isolation", func(d *Draft) { d.ControlDSN = d.SiteDSN }},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			d := validDraft()
			c.change(&d)
			if p := ValidateDraftFields(d); len(p) != 0 {
				t.Fatalf("individually valid pending edits rejected: %+v", p)
			}
			if !hasDraftProblem(ValidateDraft(d), c.field, c.code) {
				t.Fatalf("complete draft lost %s/%s validation", c.field, c.code)
			}
		})
	}
}

func TestDraftFieldsStillRejectMalformedValues(t *testing.T) {
	cases := []struct {
		name, field string
		change      func(*Draft)
	}{
		{"operation", "operation_id", func(d *Draft) { d.OperationID = "../other" }},
		{"archive", "archive", func(d *Draft) { d.ArchivePath = "release.tar.gz" }},
		{"bundle", "bundle", func(d *Draft) { d.BundlePath = "/private/../bundle" }},
		{"release", "release", func(d *Draft) { d.Release = "latest" }},
		{"commit", "source_commit", func(d *Draft) { d.SourceCommit = "main" }},
		{"control-https", "control_origin", func(d *Draft) { d.ControlOrigin = "http://control.rixa.test" }},
		{"site-path", "site_origin", func(d *Draft) { d.SiteOrigin = "https://site.rixa.test/hidden" }},
		{"listener", "listen", func(d *Draft) { d.Listen = "127.0.0.1:443" }},
		{"certificate", "tls_cert", func(d *Draft) { d.TLSCertPath = "/private/\x1b.pem" }},
		{"private-key", "tls_key", func(d *Draft) { d.TLSKeyPath = "/" }},
		{"control-database", "control_dsn", func(d *Draft) { d.ControlDSN = "postgres:///control" }},
		{"site-database", "site_dsn", func(d *Draft) { d.SiteDSN = "postgres://site:password@db.example:5432/site?sslmode=disable" }},
		{"login", "control_login", func(d *Draft) { d.ControlLogin = "Admin" }},
		{"password", "site_password", func(d *Draft) { d.SitePassword = "short" }},
		{"language", "language", func(d *Draft) { d.Language = "unknown" }},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			d := validDraft()
			c.change(&d)
			found := false
			for _, p := range ValidateDraftFields(d) {
				if p.Field == c.field {
					found = true
				}
			}
			if !found {
				t.Fatalf("malformed %s passed field validation", c.field)
			}
		})
	}
}

func TestSaveDraftPortMismatchCannotPersistOrReplaceValidWork(t *testing.T) {
	m := testManager(t)
	d := validDraft()
	d.ControlOrigin = "https://control.rixa.test"
	d.SiteOrigin = "https://site.rixa.test"
	assertRejected := func() {
		t.Helper()
		r, err := m.SaveDraft(t.Context(), d)
		if !errors.Is(err, ErrInput) || r.Code != "invalid-input" || !hasDraftProblem(r.Problems, "control_origin", "port") || !hasDraftProblem(r.Problems, "site_origin", "port") {
			t.Fatalf("complete port mismatch accepted: code=%s err=%v problems=%+v", r.Code, err, r.Problems)
		}
	}
	assertRejected()
	if _, err := os.Stat(m.statePath(d.OperationID)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("invalid new draft persisted: %v", err)
	}
	d.Listen = "0.0.0.0:443"
	if _, err := m.SaveDraft(t.Context(), d); err != nil {
		t.Fatal(err)
	}
	saved := d
	d.Listen = "0.0.0.0:9443"
	assertRejected()
	loaded, err := m.LoadDraft(t.Context(), d.OperationID)
	if err != nil || !reflect.DeepEqual(loaded, saved) {
		t.Fatal("rejected mismatch replaced previously valid work")
	}
}

func hasDraftProblem(problems []Problem, field, code string) bool {
	for _, p := range problems {
		if p.Field == field && p.Code == code {
			return true
		}
	}
	return false
}
