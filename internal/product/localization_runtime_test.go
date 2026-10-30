// SPDX-License-Identifier: MPL-2.0

package product

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/AChWorks/achrix/admin/localize"
	"github.com/AChWorks/achrix/installation"
)

func TestInterfaceLanguagePreferenceIsBoundedAndRetained(t *testing.T) {
	locales := []localize.Locale{{Tag: "en"}, {Tag: "fa"}, {Tag: "de"}}
	calls := 0
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.Header().Set("Selected", selectInterfaceLanguage(r))
		w.WriteHeader(http.StatusNoContent)
	})
	handler := interfaceLanguageHandler(next, locales)
	request := httptest.NewRequest(http.MethodGet, "https://a.rixa.test/admin/language/?ui_language=de", nil)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusNoContent || response.Header().Get("Selected") != "de" {
		t.Fatal("installed selection did not reach the shell")
	}
	cookies := response.Result().Cookies()
	if len(cookies) != 1 {
		t.Fatal("missing preference")
	}
	pref := cookies[0]
	if pref.Name != interfaceLanguageCookie || pref.Value != "de" || !pref.Secure || !pref.HttpOnly || pref.Domain != "" || pref.Path != "/admin" || pref.SameSite != http.SameSiteLaxMode {
		t.Fatal("preference widened its browser scope")
	}
	request = httptest.NewRequest(http.MethodPost, "https://a.rixa.test/admin/content/save", strings.NewReader("{}"))
	request.AddCookie(pref)
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Header().Get("Selected") != "de" || len(response.Result().Cookies()) != 0 {
		t.Fatal("browser POST lost the interface preference")
	}
	for _, raw := range []string{"ui_language=xx", "ui_language=", "ui_language=fa&ui_language=de", "ui_language=%zz"} {
		before := calls
		response = httptest.NewRecorder()
		handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "https://a.rixa.test/admin/content/?"+raw, nil))
		if response.Code != http.StatusBadRequest || calls != before || len(response.Result().Cookies()) != 0 {
			t.Fatalf("invalid selection reached dispatch: %s", raw)
		}
	}
	response = httptest.NewRecorder()
	request = httptest.NewRequest(http.MethodGet, "https://a.rixa.test/admin/language/?ui_language=fa", nil)
	request.Header.Set("Sec-Fetch-Site", "cross-site")
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusBadRequest {
		t.Fatal("cross-site preference accepted")
	}
	response = httptest.NewRecorder()
	request = httptest.NewRequest(http.MethodGet, "https://a.rixa.test/admin/content/", nil)
	request.AddCookie(&http.Cookie{Name: interfaceLanguageCookie, Value: "removed"})
	request.AddCookie(&http.Cookie{Name: "session-canary", Value: "keep"})
	handler.ServeHTTP(response, request)
	if response.Header().Get("Selected") != "" || response.Code != http.StatusNoContent || response.Result().Cookies()[0].MaxAge != -1 {
		t.Fatal("removed locale stranded the interface")
	}
}

func writeInstalledLocale(t *testing.T, root, domain, key, value string) {
	t.Helper()
	data, err := json.Marshal(map[string]any{"schema": 1, "locale": "de", "name": "Deutsch", "direction": "ltr", "domain": domain, "messages": map[string]any{key: map[string]string{"other": value}}})
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(root, domain+".de.json"), data, 0o600); err != nil {
		t.Fatal(err)
	}
}

func localeRuntimeConfig(t *testing.T, root, language string) RuntimeConfig {
	t.Helper()
	media := filepath.Join(t.TempDir(), "private-media")
	if err := os.Mkdir(media, 0o700); err != nil {
		t.Fatal(err)
	}
	c := Config{Listen: "127.0.0.1:18443", Language: language, CatalogRoot: root, StartupTimeout: 5 * time.Second, ShutdownTimeout: 5 * time.Second,
		Control: ControlConfig{Origin: "https://control.rixa.test", DatabaseEnv: "RIXA_CONTROL", AdminPrincipal: testControlAdmin},
		Sites:   []SiteConfig{{ID: "a", Origin: "https://a.rixa.test", DatabaseEnv: "RIXA_A", MediaRoot: media, AdminPrincipal: testSiteAAdmin}}}
	return RuntimeConfig{Config: c, Control: ResolvedControl{ControlConfig: c.Control, DSN: "postgres://fixture:fixture@127.0.0.1:5432/control?sslmode=disable"}, Sites: []ResolvedSite{{SiteConfig: c.Sites[0], DSN: "postgres://fixture:fixture@127.0.0.1:5432/site?sslmode=disable"}}}
}

func TestInstalledLocaleReconstructionUsesWholeProductComposition(t *testing.T) {
	root := t.TempDir()
	if err := os.Chmod(root, 0o700); err != nil {
		t.Fatal(err)
	}
	config := localeRuntimeConfig(t, root, "en")
	before, err := BuildRuntime(config, nil)
	if err != nil {
		t.Fatal(err)
	}
	// The new data arrives after the first runtime has been constructed.
	for _, message := range []struct{ domain, key, value string }{
		{"rixa", "language.title", "Sprache <b>safe</b>"},
		{"admin", "sign_in", "Anmelden"},
		{"identity", "accounts.title", "Konten"},
		{"media", "library.title", "Medien"},
	} {
		writeInstalledLocale(t, root, message.domain, message.key, message.value)
	}
	config.Language = "de"
	after, err := BuildRuntime(config, nil)
	if err != nil {
		t.Fatalf("complete pack must be usable at control and site origins: %v", err)
	}
	for _, runtime := range []*Runtime{before, after} {
		server := httptest.NewTLSServer(runtime.Handler)
		client := server.Client()
		client.Timeout = 5 * time.Second
		for _, host := range []string{"control.rixa.test", "a.rixa.test"} {
			request, err := http.NewRequest(http.MethodGet, server.URL+"/admin/login", nil)
			if err != nil {
				t.Fatal(err)
			}
			request.Host = host
			response, err := client.Do(request)
			if err != nil {
				t.Fatal(err)
			}
			body, readErr := io.ReadAll(io.LimitReader(response.Body, 1<<20))
			_ = response.Body.Close()
			expected, label := "en", "Sign in"
			if runtime == after {
				expected, label = "de", "Anmelden"
			}
			if readErr != nil || response.StatusCode != http.StatusOK || !strings.Contains(string(body), `lang="`+expected+`"`) || !strings.Contains(string(body), label) {
				t.Fatalf("runtime snapshot mismatch at %s: status%d", host, response.StatusCode)
			}
		}
		server.Close()
	}
	sources, err := config.Config.catalogSources()
	if err != nil {
		t.Fatal(err)
	}
	// Loading an installed pack with Module domains requires those baselines.
	// Runtime composition supplies the actual public constructors' resources.
	if _, err = localize.Load(sources...); err == nil {
		t.Fatal("partial domain baselines were silently invented")
	}
	if supportedContentLanguage("de") {
		t.Fatal("UI installation changed the content schema contract")
	}
	if err = (AppearanceInput{SiteTitle: "site", SiteLanguage: "de", HomeMode: "latest_posts", Theme: "light"}).validateShape(); !errors.Is(err, ErrEditorialInvalid) {
		t.Fatal("UI locale expanded persisted content languages")
	}
}

func TestCatalogRootValidationAndDiagnostics(t *testing.T) {
	root := t.TempDir()
	if err := os.Chmod(root, 0o700); err != nil {
		t.Fatal(err)
	}
	config := localeRuntimeConfig(t, root, "en")
	if _, err := BuildRuntime(config, nil); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "broken.json"), []byte("{broken"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := BuildRuntime(config, nil); err == nil || !strings.Contains(err.Error(), "broken.json") {
		t.Fatal("invalid data lost actionable source context")
	}
	if err := os.Remove(filepath.Join(root, "broken.json")); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(root, 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := config.Config.catalogSources(); err == nil {
		t.Fatal("public catalog root accepted")
	}
	if err := os.Chmod(root, 0o700); err != nil {
		t.Fatal(err)
	}
	config.Config.Sites[0].MediaRoot = root
	if _, err := config.Config.catalogSources(); err == nil {
		t.Fatal("Media catalog root overlap accepted")
	}
	config.Config.Sites[0].MediaRoot = filepath.Join(root, "uploads")
	if _, err := config.Config.catalogSources(); err == nil {
		t.Fatal("source tree containing uploads accepted")
	}
}

func TestCatalogRootConfigurationRoundTrip(t *testing.T) {
	root := t.TempDir()
	raw := fileConfig{Listen: "127.0.0.1:18443", CatalogRoot: root, Language: "de",
		Control: ControlConfig{Origin: "https://control.rixa.test", DatabaseEnv: "RIXA_CONTROL"},
		Sites:   []SiteConfig{{ID: "a", Origin: "https://a.rixa.test", DatabaseEnv: "RIXA_A", MediaRoot: filepath.Join(t.TempDir(), "media")}}}
	data, err := json.Marshal(raw)
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := ParseConfig(data)
	if err != nil {
		t.Fatal(err)
	}
	if parsed.CatalogRoot != root || parsed.Language != "de" {
		t.Fatal("configuration dropped installed catalog identity")
	}
	data, err = json.Marshal(parsed)
	if err != nil {
		t.Fatal(err)
	}
	var persisted map[string]any
	if json.Unmarshal(data, &persisted) != nil || persisted["catalog_root"] != root {
		t.Fatal("configuration persistence dropped catalog_root")
	}
}

func TestContentLocaleMetadataComesFromCatalog(t *testing.T) {
	catalog, err := localize.Load(bundledProductCatalogs())
	if err != nil {
		t.Fatal(err)
	}
	locales := contentLocales(catalog)
	if len(locales) != 2 {
		t.Fatal("content locale policy changed")
	}
	for _, metadata := range locales {
		selected, err := catalog.For(metadata.Tag)
		if err != nil || selected.Locale() != metadata || metadata.Name == "" || metadata.Direction == "" {
			t.Fatal("content metadata was invented separately")
		}
	}
}

func TestSetupPreservesInstalledCatalogConfiguration(t *testing.T) {
	request := setupFixture(t)
	var raw map[string]any
	if err := json.Unmarshal(request.Configuration, &raw); err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	raw["catalog_root"], raw["language"] = root, "de"
	data, err := json.Marshal(raw)
	if err != nil {
		t.Fatal(err)
	}
	request.Configuration = data
	applied, err := applySetup(context.Background(), request, nil, satisfiedSetupProbe, nil)
	if err != nil || applied.State != SetupVerified || applied.Plan.Status() != installation.Ready {
		t.Fatalf("catalog setup persistence: %v", err)
	}
	persisted, err := os.ReadFile(request.TargetPath)
	if err != nil || !bytes.Equal(persisted, request.Configuration) {
		t.Fatal("setup changed configured catalog source bytes")
	}
	c, err := LoadConfig(request.TargetPath)
	if err != nil || c.CatalogRoot != root || c.Language != "de" {
		t.Fatal("setup persistence dropped locale configuration")
	}
	again, err := applySetup(context.Background(), request, nil, satisfiedSetupProbe, nil)
	if err != nil || again.State != SetupVerified {
		t.Fatal("locale configuration did not survive setup reentry")
	}
}
