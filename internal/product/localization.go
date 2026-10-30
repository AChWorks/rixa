// SPDX-License-Identifier: MPL-2.0

package product

import (
	"context"
	"fmt"
	"html/template"
	"io/fs"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	shell "github.com/AChWorks/achrix/admin"
	"github.com/AChWorks/achrix/admin/localize"
)

const interfaceLanguageCookie = "rixa-ui-language"

type interfaceLanguageContextKey struct{}

// CatalogRoot is operator-controlled private translation data, never code.
func (c Config) catalogSources() ([]fs.FS, error) {
	sources := []fs.FS{bundledProductCatalogs()}
	if c.CatalogRoot == "" {
		return sources, nil
	}
	root := c.CatalogRoot
	if !filepath.IsAbs(root) || filepath.Clean(root) != root || root == "/" {
		return nil, fmt.Errorf("%w: catalog_root must be a private absolute directory", ErrConfiguration)
	}
	for _, site := range c.Sites {
		for _, dataRoot := range []string{site.MediaRoot, site.PublicRoot} {
			if dataRoot != "" && overlappingLocaleRoots(root, dataRoot) {
				return nil, fmt.Errorf("%w: catalog_root overlaps site storage", ErrConfiguration)
			}
		}
	}
	info, err := os.Lstat(root)
	if err != nil {
		return nil, fmt.Errorf("%w: catalog_root: %w", ErrConfiguration, err)
	}
	actual, err := filepath.EvalSymlinks(root)
	if err != nil || actual != root || !info.IsDir() || info.Mode().Perm()&0o077 != 0 {
		return nil, fmt.Errorf("%w: catalog_root must be a private directory without symlink aliases", ErrConfiguration)
	}
	return append(sources, os.DirFS(root)), nil
}

func overlappingLocaleRoots(a, b string) bool {
	return a == b || strings.HasPrefix(a, b+string(filepath.Separator)) || strings.HasPrefix(b, a+string(filepath.Separator))
}

func presentationCatalog(sources []fs.FS, surfaces ...shell.Surface) (*localize.Catalog, error) {
	all := []fs.FS{shell.Catalogs()}
	for _, surface := range surfaces {
		if surface.Catalogs != nil {
			all = append(all, surface.Catalogs)
		}
	}
	all = append(all, sources...)
	catalog, err := localize.Load(all...)
	if err != nil {
		return nil, fmt.Errorf("%w: interface catalogs: %w", ErrConfiguration, err)
	}
	return catalog, nil
}

// Content languages remain a separate, intentionally bounded domain contract.
// Installing another UI language does not change the persisted content schema.
func contentLocales(catalog *localize.Catalog) []localize.Locale {
	var result []localize.Locale
	for _, metadata := range catalog.Locales() {
		if supportedContentLanguage(metadata.Tag) {
			result = append(result, metadata)
		}
	}
	return result
}
func supportedContentLanguage(tag string) bool { return tag == "en" || tag == "fa" }

func selectInterfaceLanguage(r *http.Request) string {
	if selected, ok := r.Context().Value(interfaceLanguageContextKey{}).(string); ok {
		return selected
	}
	if cookie, err := r.Cookie(interfaceLanguageCookie); err == nil {
		return cookie.Value
	}
	return ""
}

// Preferences are host-only presentation data, separate from auth cookies and
// stored content. Only an explicit GET choice writes a preference cookie.
func interfaceLanguageHandler(next http.Handler, locales []localize.Locale) http.Handler {
	supported := make(map[string]bool, len(locales))
	for _, metadata := range locales {
		supported[metadata.Tag] = true
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		values, err := url.ParseQuery(r.URL.RawQuery)
		if err != nil {
			localeSelectionFailure(w)
			return
		}
		choices, explicit := values["ui_language"]
		if explicit {
			if r.Method != http.MethodGet || len(choices) != 1 || !supported[choices[0]] || r.Header.Get("Sec-Fetch-Site") == "cross-site" {
				localeSelectionFailure(w)
				return
			}
			// Consume only this product-owned presentation parameter before
			// the shared shell's strict no-query admission. Clone preserves
			// the caller's request and all authentication/Origin headers.
			// Any unrelated parameter remains subject to the shell's guard.
			selected := choices[0]
			values.Del("ui_language")
			r = r.Clone(context.WithValue(r.Context(), interfaceLanguageContextKey{}, selected))
			r.URL.RawQuery = values.Encode()
			http.SetCookie(w, &http.Cookie{Name: interfaceLanguageCookie, Value: choices[0], Path: "/admin", Secure: true, HttpOnly: true, SameSite: http.SameSiteLaxMode, MaxAge: 365 * 24 * 60 * 60})
		} else if cookie, cookieErr := r.Cookie(interfaceLanguageCookie); cookieErr == nil && !supported[cookie.Value] {
			// Removing a pack must not strand an operator behind a stale preference.
			clone := r.Clone(r.Context())
			clone.Header = r.Header.Clone()
			clone.Header.Del("Cookie")
			for _, retained := range r.Cookies() {
				if retained.Name != interfaceLanguageCookie {
					clone.AddCookie(retained)
				}
			}
			r = clone
			http.SetCookie(w, &http.Cookie{Name: interfaceLanguageCookie, Path: "/admin", Secure: true, HttpOnly: true, SameSite: http.SameSiteLaxMode, MaxAge: -1})
		}
		next.ServeHTTP(w, r)
	})
}

func localeSelectionFailure(w http.ResponseWriter) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	http.Error(w, "Unsupported interface language. Choose an installed language at /admin/language/ or remove ui_language from the URL.", http.StatusBadRequest)
}

type interfaceLanguageView struct {
	Locale    *localize.Localizer
	Languages []localize.Locale
}

var interfaceLanguageTemplate = template.Must(template.New("interface-language").Parse(`{{define "content"}}<p>{{.Locale.Text "rixa" "language.help" nil}}</p><form method="get" action="/admin/language/"><label>{{.Locale.Text "rixa" "language.label" nil}} <select name="ui_language">{{$selected := .Locale.Locale.Tag}}{{range .Languages}}<option value="{{.Tag}}" {{if eq .Tag $selected}}selected{{end}} dir="{{.Direction}}">{{.Name}}</option>{{end}}</select></label><button type="submit">{{.Locale.Text "rixa" "language.select" nil}}</button></form>{{end}}`))

func interfaceLanguageSurface(capability, target string, inventory func() []localize.Locale) shell.Surface {
	return shell.Surface{ID: "language", Title: shell.Text{Domain: "rixa", Key: "language.title"}, Capability: capability, Target: target, Handler: func(w http.ResponseWriter, r *http.Request, view shell.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		title, err := view.Localizer.Text("rixa", "language.title", nil)
		if err == nil {
			err = view.Render(shell.Page{Title: title, Template: interfaceLanguageTemplate, Data: interfaceLanguageView{view.Localizer, inventory()}})
		}
		if err != nil {
			editorialHTTPFail(w, ErrEditorialUnavailable, "")
		}
	}}
}
