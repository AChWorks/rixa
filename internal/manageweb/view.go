// SPDX-License-Identifier: MPL-2.0
package manageweb

import (
	"embed"
	"encoding/json"
	"html/template"
	"io/fs"
	"net/http"
	"time"

	shell "github.com/AChWorks/achrix/admin"
	"github.com/AChWorks/achrix/admin/localize"
)

//go:embed locales/*.json
var localeFiles embed.FS

//go:embed assets/console.js assets/console.css
var assets embed.FS

func catalogs() fs.FS { f, _ := fs.Sub(localeFiles, "locales"); return f }

var client, _ = assets.ReadFile("assets/console.js")
var style, _ = assets.ReadFile("assets/console.css")

type pageView struct {
	Locale      *localize.Localizer
	Languages   []localize.Locale
	Scope       Scope
	Expires     string
	Manager     string
	Initial     response
	Draft       configureInput
	SecretSaved map[string]bool
	AccessOpen  bool
	Messages    string
}

func assetResponse(w http.ResponseWriter, kind string, b []byte) {
	w.Header().Set("Content-Type", kind)
	_, _ = w.Write(b)
}
func (c *Console) access(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		c.fail(w, r, 405, "invalid_request")
		return
	}
	if _, ok := c.current(r); ok {
		http.Redirect(w, r, "/admin/operation/", http.StatusSeeOther)
		return
	}
	l := c.localizer(r)
	domains := map[string]map[string]localize.Message{}
	for _, d := range []string{"admin", domain} {
		messages, e := l.Messages(d)
		if e != nil {
			c.fail(w, r, 503, "unavailable")
			return
		}
		domains[d] = messages
	}
	snapshot, e := json.Marshal(map[string]any{"locale": l.Locale().Tag, "domains": domains})
	if e != nil {
		c.fail(w, r, 503, "unavailable")
		return
	}
	c.mutex.Lock()
	open := c.liveLocked() && !c.used && c.attempts < 5
	c.mutex.Unlock()
	v := pageView{Locale: l, Languages: c.catalog.Locales(), Scope: c.options.Scope, Expires: c.expires.UTC().Format(time.RFC3339), AccessOpen: open, Messages: string(snapshot)}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if e = accessTemplate.Execute(w, v); e != nil {
		return
	}
}
func (c *Console) page(w http.ResponseWriter, r *http.Request, view shell.Request) {
	result, e := c.status(r.Context())
	if e != nil && result.OperationID == "" {
		c.fail(w, r, 503, "unavailable")
		return
	}
	v := pageView{Locale: view.Localizer, Languages: c.catalog.Locales(), Scope: c.options.Scope, Expires: c.expires.UTC().Format(time.RFC3339), Manager: c.options.ManagerVersion, Initial: c.present(r, view.Localizer, result, "page", e), SecretSaved: map[string]bool{}}
	if c.options.Scope.Kind == "install" {
		d, load := c.backend.LoadDraft(r.Context(), c.options.Scope.Operation)
		if load == nil {
			v.Draft = configureInput{ControlOrigin: d.ControlOrigin, SiteOrigin: d.SiteOrigin, Listen: d.Listen, ControlLogin: d.ControlLogin, SiteLogin: d.SiteLogin, Language: d.Language}
			v.SecretSaved = map[string]bool{"control_dsn": d.ControlDSN != "", "site_dsn": d.SiteDSN != "", "control_password": d.ControlPassword != "", "site_password": d.SitePassword != ""}
		} else {
			v.Draft.Language = "en"
		}
	}
	title := text(view.Localizer, "title")
	if view.Render(shell.Page{Title: title, Template: operationTemplate, Data: v}) != nil {
		c.fail(w, r, 503, "unavailable")
	}
}

const languageTemplate = `{{define "language"}}<label class="language">{{.Locale.Text "rixa-manager" "language" nil}} <select data-language>{{$tag := .Locale.Locale.Tag}}{{range .Languages}}<option value="{{.Tag}}" {{if eq $tag .Tag}}selected{{end}} dir="{{.Direction}}">{{.Name}}</option>{{end}}</select></label>{{end}}`

var accessTemplate = template.Must(template.New("access").Parse(`<!doctype html><html lang="{{.Locale.Locale.Tag}}" dir="{{.Locale.Locale.Direction}}"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1"><title>{{.Locale.Text "rixa-manager" "access_title" nil}}</title><link rel="stylesheet" href="/console-assets/console.css"><script src="/admin/assets/i18n.js" defer></script><script src="/console-assets/console.js" defer></script></head><body><div id="admin-messages" hidden>{{.Messages}}</div><main class="access"><header><strong>Rixa</strong>{{template "language" .}}</header><h1>{{.Locale.Text "rixa-manager" "access_title" nil}}</h1><p>{{.Locale.Text "rixa-manager" "access_help" nil}}</p><dl class="scope"><dt>{{.Locale.Text "rixa-manager" "operation" nil}}</dt><dd><bdi dir="ltr">{{.Scope.Operation}}</bdi></dd><dt>{{.Locale.Text "rixa-manager" "expires" nil}}</dt><dd><bdi dir="ltr">{{.Expires}}</bdi></dd></dl><p id="status" role="status" aria-live="polite" tabindex="-1"></p>{{if .AccessOpen}}<form data-permit action="/console-auth/login" method="post"><label for="permit">{{.Locale.Text "rixa-manager" "permit" nil}}</label><input id="permit" name="permit" type="password" required autocomplete="off" maxlength="43" minlength="43" dir="ltr"><button type="submit">{{.Locale.Text "rixa-manager" "open_console" nil}}</button></form>{{else}}<p role="alert">{{.Locale.Text "rixa-manager" "access_expired" nil}}</p>{{end}}</main></body></html>` + languageTemplate))
var operationTemplate = template.Must(template.New("operation").Parse(`{{define "content"}}<link rel="stylesheet" href="/console-assets/console.css"><script src="/console-assets/console.js" defer></script><section data-console data-operation="{{.Scope.Operation}}" data-kind="{{.Scope.Kind}}">
<div class="context"><p>{{.Locale.Text "rixa-manager" "scope_help" nil}}</p>{{template "language" .}}</div><dl class="scope"><dt>{{.Locale.Text "rixa-manager" "operation" nil}}</dt><dd><bdi dir="ltr">{{.Scope.Operation}}</bdi></dd><dt>{{.Locale.Text "rixa-manager" "kind" nil}}</dt><dd><bdi dir="ltr">{{.Scope.Kind}}</bdi></dd>{{if .Scope.Installation}}<dt>{{.Locale.Text "rixa-manager" "original_installation" nil}}</dt><dd><bdi dir="ltr">{{.Scope.Installation}}</bdi></dd>{{end}}<dt>{{.Locale.Text "rixa-manager" "manager" nil}}</dt><dd><bdi dir="ltr">{{.Manager}}</bdi></dd><dt>{{.Locale.Text "rixa-manager" "expires" nil}}</dt><dd><bdi dir="ltr">{{.Expires}}</bdi></dd></dl>
<section class="progress" aria-labelledby="progress-title"><h2 id="progress-title">{{.Locale.Text "rixa-manager" "progress" nil}}</h2><p data-state>{{.Initial.Message}}</p><p><bdi dir="ltr" data-code>{{.Initial.State}} · {{.Initial.Stage}} · {{.Initial.Code}}</bdi></p><ul data-problems>{{range .Initial.Problems}}<li><bdi dir="ltr">{{.Field}} · {{.Code}}</bdi> — {{.Message}}{{if .Detail}}<p lang="en"><bdi dir="ltr">{{.Detail}}</bdi></p>{{end}}</li>{{end}}</ul><details data-diagnostics {{if not .Initial.TechnicalDetail}}hidden{{end}}><summary>{{.Locale.Text "rixa-manager" "diagnostic_title" nil}}</summary><p lang="{{.Initial.TechnicalLanguage}}"><bdi dir="{{if eq .Initial.TechnicalLanguage "en"}}ltr{{else}}auto{{end}}" data-technical-detail>{{.Initial.TechnicalDetail}}</bdi></p></details><button type="button" data-inspect>{{.Locale.Text "rixa-manager" "inspect" nil}}</button><a data-admin-url hidden target="_blank" rel="noopener noreferrer">{{.Locale.Text "rixa-manager" "open_admin" nil}}</a></section>
<section data-pending hidden role="alert"><h2>{{.Locale.Text "rixa-manager" "uncertain_title" nil}}</h2><p>{{.Locale.Text "rixa-manager" "uncertain_help" nil}}</p><button type="button" data-retry hidden>{{.Locale.Text "rixa-manager" "retry_exact" nil}}</button></section>
<section data-config><h2>{{.Locale.Text "rixa-manager" "configuration" nil}}</h2>
<details open class="artifact"><summary>{{.Locale.Text "rixa-manager" "release" nil}}</summary><p data-artifact>{{.Initial.Artifact.Release}} <bdi dir="ltr">{{.Initial.Artifact.Source}}</bdi></p>
<form data-fetch action="/artifact/fetch" method="post"><label>{{.Locale.Text "rixa-manager" "channel" nil}}<select name="channel"><option value="stable">{{.Locale.Text "rixa-manager" "stable" nil}}</option><option value="development">{{.Locale.Text "rixa-manager" "development" nil}}</option></select></label><label>{{.Locale.Text "rixa-manager" "exact_release" nil}}<input name="release" dir="ltr" maxlength="128" placeholder="v0.0.0"></label><button type="submit">{{.Locale.Text "rixa-manager" "fetch" nil}}</button></form>
<details><summary>{{.Locale.Text "rixa-manager" "upload" nil}}</summary><form data-upload action="/artifact/upload" method="post" enctype="multipart/form-data"><label>{{.Locale.Text "rixa-manager" "release" nil}}<input name="release" dir="ltr" required maxlength="128"></label><label>{{.Locale.Text "rixa-manager" "source" nil}}<input name="source_commit" dir="ltr" required maxlength="40" minlength="40"></label><label>{{.Locale.Text "rixa-manager" "archive" nil}}<input name="archive" type="file" required></label><label>{{.Locale.Text "rixa-manager" "bundle" nil}}<input name="bundle" type="file" required></label><button type="submit">{{.Locale.Text "rixa-manager" "verify_upload" nil}}</button></form></details></details>
<form data-configure action="/admin/operation/configure" method="post">
{{if eq .Scope.Kind "install"}}<fieldset><legend>{{.Locale.Text "rixa-manager" "runtime" nil}}</legend><p>{{.Locale.Text "rixa-manager" "tls_bound" nil}}</p><div class="fields"><label>{{.Locale.Text "rixa-manager" "control_origin" nil}}<input name="control_origin" value="{{.Draft.ControlOrigin}}" required dir="ltr" maxlength="2048" placeholder="https://control.example.com:9443"></label><label>{{.Locale.Text "rixa-manager" "site_origin" nil}}<input name="site_origin" value="{{.Draft.SiteOrigin}}" required dir="ltr" maxlength="2048" placeholder="https://site.example.com:9443"></label><label>{{.Locale.Text "rixa-manager" "listen" nil}}<input name="listen" value="{{.Draft.Listen}}" required dir="ltr" maxlength="128" placeholder="0.0.0.0:9443"></label><label>{{.Locale.Text "rixa-manager" "runtime_language" nil}}<select name="language"><option value="en" {{if eq .Draft.Language "en"}}selected{{end}}>{{.Locale.Text "rixa-manager" "language_en" nil}}</option><option value="fa" {{if eq .Draft.Language "fa"}}selected{{end}}>{{.Locale.Text "rixa-manager" "language_fa" nil}}</option></select></label></div></fieldset>
<fieldset><legend>{{.Locale.Text "rixa-manager" "databases" nil}}</legend><p>{{.Locale.Text "rixa-manager" "secret_help" nil}}</p><div class="fields"><label>{{.Locale.Text "rixa-manager" "control_dsn" nil}}<input name="control_dsn" type="password" dir="ltr" autocomplete="off" maxlength="4096" {{if index .SecretSaved "control_dsn"}}placeholder="{{.Locale.Text "rixa-manager" "saved_secret" nil}}"{{else}}required{{end}}></label><label>{{.Locale.Text "rixa-manager" "site_dsn" nil}}<input name="site_dsn" type="password" dir="ltr" autocomplete="off" maxlength="4096" {{if index .SecretSaved "site_dsn"}}placeholder="{{.Locale.Text "rixa-manager" "saved_secret" nil}}"{{else}}required{{end}}></label></div></fieldset>
<fieldset><legend>{{.Locale.Text "rixa-manager" "administrators" nil}}</legend><div class="fields"><label>{{.Locale.Text "rixa-manager" "control_login" nil}}<input name="control_login" value="{{.Draft.ControlLogin}}" required dir="ltr" autocomplete="off" maxlength="64"></label><label>{{.Locale.Text "rixa-manager" "control_password" nil}}<input name="control_password" type="password" autocomplete="new-password" maxlength="256" {{if index .SecretSaved "control_password"}}placeholder="{{.Locale.Text "rixa-manager" "saved_secret" nil}}"{{else}}required{{end}}></label><label>{{.Locale.Text "rixa-manager" "site_login" nil}}<input name="site_login" value="{{.Draft.SiteLogin}}" required dir="ltr" autocomplete="off" maxlength="64"></label><label>{{.Locale.Text "rixa-manager" "site_password" nil}}<input name="site_password" type="password" autocomplete="new-password" maxlength="256" {{if index .SecretSaved "site_password"}}placeholder="{{.Locale.Text "rixa-manager" "saved_secret" nil}}"{{else}}required{{end}}></label></div></fieldset>{{else}}<p>{{.Locale.Text "rixa-manager" "update_retains" nil}}</p>{{end}}
<button type="submit">{{.Locale.Text "rixa-manager" "save" nil}}</button></form></section>
<section class="actions"><h2>{{.Locale.Text "rixa-manager" "review_heading" nil}}</h2><p>{{.Locale.Text "rixa-manager" "review_help" nil}}</p><button type="button" data-action="review">{{.Locale.Text "rixa-manager" "review" nil}}</button><dl class="scope" data-review hidden></dl><form data-action-form="apply" action="/admin/operation/apply" method="post"><label class="check"><input type="checkbox" name="confirm_apply" required>{{.Locale.Text "rixa-manager" "apply_confirmation" nil}}</label><button type="submit">{{.Locale.Text "rixa-manager" "apply" nil}}</button></form><button type="button" data-action="resume">{{.Locale.Text "rixa-manager" "resume" nil}}</button></section>
<details data-remove><summary>{{.Locale.Text "rixa-manager" "remove" nil}}</summary><p>{{.Locale.Text "rixa-manager" "remove_help" nil}}</p><form data-action-form="remove" action="/admin/operation/remove" method="post"><label>{{.Locale.Text "rixa-manager" "remove_confirmation" nil}} <bdi dir="ltr">REMOVE</bdi><input name="confirmation" required pattern="REMOVE" autocomplete="off" dir="ltr"></label><button type="submit" class="danger">{{.Locale.Text "rixa-manager" "remove" nil}}</button></form></details>
</section>{{end}}` + languageTemplate))
