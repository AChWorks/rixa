// SPDX-License-Identifier: MPL-2.0

package manageweb

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/AChWorks/rixa/internal/serverinstall"
)

func TestConsoleOptionalSiteBPrivateRetentionAndExplicitRemoval(t *testing.T) {
	backend := &fakeNative{draft: serverinstall.Draft{
		OperationID: "first-install", Release: "v0.8.0",
		SiteBOrigin: "https://b.rixa.test:8443",
		SiteBDSN:    "postgres://private:secret@127.0.0.1:5432/b?sslmode=disable",
		SiteBLogin:  "b-admin", SiteBPassword: strings.Repeat("secret", 4),
	}, calls: map[string]int{}}
	c := &Console{backend: backend, options: Options{Scope: Scope{Kind: "install", Operation: "first-install"}}}
	in := configureInput{ControlOrigin: "https://control.rixa.test:8443", SiteOrigin: "https://a.rixa.test:8443", SiteBOrigin: "https://b.rixa.test:8443",
		SiteBLogin: "b-admin", SiteBAction: "keep"}
	if _, err := c.configure(context.Background(), in); err != nil {
		t.Fatal(err)
	}
	if backend.draft.SiteBDSN == "" || backend.draft.SiteBPassword == "" {
		t.Fatal("blank second-site secret inputs unexpectedly cleared protected values")
	}
	summary, err := c.reviewSummary(context.Background())
	if err != nil || strings.Contains(summary["site_b_dsn"], "secret@") || summary["site_b_origin"] != in.SiteBOrigin {
		t.Fatal("optional second-site review lost origin or exposed DSN password")
	}
	in.SiteBAction = "remove"
	if _, err := c.configure(context.Background(), in); err != nil {
		t.Fatal(err)
	}
	if backend.draft.SiteBDSN != "" || backend.draft.SiteBPassword != "" || backend.draft.SiteBOrigin != "" || backend.draft.SiteBLogin != "" {
		t.Fatal("explicit removal did not clear all optional-site draft fields")
	}
	in.SiteBAction = "unrecognized"
	if _, err := c.configure(context.Background(), in); err == nil {
		t.Fatal("unknown second-site draft action accepted")
	}
}

func TestConsoleOptionalSiteBHTTPSFormNeverEchoesCredentials(t *testing.T) {
	h := fixture(t, func(_ *Options, _ *dependencies, backend *fakeNative) {
		backend.draft.Release = "v0.8.0"
	})
	h.login(t)
	siteBSecret := strings.Repeat("private-site-b", 2)
	siteBDSN := "postgres://siteb:private-dsn-secret@127.0.0.1:5432/site_b?sslmode=disable"
	body := map[string]string{
		"control_origin": "https://control.rixa.test:8443", "site_origin": "https://a.rixa.test:8443",
		"site_b_origin": "https://b.rixa.test:8443", "site_b_login": "b-admin",
		"site_b_dsn": siteBDSN, "site_b_password": siteBSecret,
		"site_b_action": "keep",
	}
	value, _ := json.Marshal(body)
	code, received, _ := h.call(t, "POST", "/admin/operation/configure", string(value), true)
	if code != 200 {
		t.Fatalf("scoped optional-site Save returned %d", code)
	}
	if strings.Contains(string(received), siteBSecret) || strings.Contains(string(received), siteBDSN) || strings.Contains(string(received), "private-dsn-secret") {
		t.Fatal("two-site JSON response disclosed private credentials")
	}
	if h.backend.draft.SiteBDSN != siteBDSN || h.backend.draft.SiteBPassword != siteBSecret {
		t.Fatal("protected second-site configuration was not persisted by shared backend")
	}
	code, page, _ := h.call(t, "GET", "/admin/operation/", "", false)
	if code != 200 || !strings.Contains(string(page), "Optional second site") ||
		!strings.Contains(string(page), "https://b.rixa.test:8443") {
		t.Fatalf("optional-site web journey not visible, status %d", code)
	}
	if strings.Contains(string(page), siteBSecret) || strings.Contains(string(page), siteBDSN) || strings.Contains(string(page), "private-dsn-secret") {
		t.Fatal("optional-site web page exposed private draft credentials")
	}
}
