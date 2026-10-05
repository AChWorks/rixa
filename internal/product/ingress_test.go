// SPDX-License-Identifier: MPL-2.0

package product

import (
	"context"
	"crypto/tls"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestMultiSiteIngressIgnoresForwardedAuthorityAndHidesDisabledSites(t *testing.T) {
	base := Config{
		StartupTimeout:  time.Second,
		ShutdownTimeout: time.Second,
		Control:         ControlConfig{Origin: "https://control.rixa.test:8443"},
	}
	sites := []ResolvedSite{
		{SiteConfig: SiteConfig{ID: "a", Origin: "https://a.rixa.test:8443"}},
		{SiteConfig: SiteConfig{ID: "disabled", Origin: "https://disabled.rixa.test:8443", Disabled: true}},
	}
	router, err := buildRouter(base, sites, nil)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err = router.app.Start(ctx); err != nil {
		t.Fatal(err)
	}
	if err = router.app.Ready(ctx); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		stop, stopCancel := context.WithTimeout(context.Background(), time.Second)
		defer stopCancel()
		_ = router.app.Shutdown(stop)
	})

	control := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusAccepted) })
	active := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) })
	runtime := &Runtime{
		Config: RuntimeConfig{
			Config:  base,
			Control: ResolvedControl{ControlConfig: base.Control},
			Sites:   sites,
		},
		Control: &ControlRuntime{Handler: control},
		Sites:   map[string]*SiteRuntime{"a": {ID: "a", Handler: active}},
		router:  router,
	}
	handler := newIngressHandler(runtime)

	request := func(host string, forwarded string) int {
		req := httptest.NewRequest(http.MethodGet, "https://"+host+"/admin", nil)
		req.Host = host
		req.TLS = &tls.ConnectionState{}
		if forwarded != "" {
			req.Header.Set("X-Forwarded-Host", forwarded)
		}
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		return rec.Code
	}

	if code := request("a.rixa.test:8443", "evil.example"); code != http.StatusNoContent {
		t.Fatalf("active site status = %d", code)
	}
	if code := request("unknown.rixa.test:8443", "a.rixa.test:8443"); code != http.StatusNotFound {
		t.Fatalf("forwarded authority changed routing: %d", code)
	}
	if code := request("disabled.rixa.test:8443", ""); code != http.StatusNotFound {
		t.Fatalf("disabled site status = %d", code)
	}
	if code := request("control.rixa.test:8443", "a.rixa.test:8443"); code != http.StatusAccepted {
		t.Fatalf("control route status = %d", code)
	}
}
