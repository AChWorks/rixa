// SPDX-License-Identifier: MPL-2.0

package product

import (
	"context"
	"encoding/json"
	"errors"
	"html/template"
	"io"
	"net/http"
	"strings"

	"github.com/AChWorks/achrix"
	shell "github.com/AChWorks/achrix/admin"
	"github.com/AChWorks/achrix/admin/localize"
	"github.com/AChWorks/achrix/identity"
)

type controlSiteManager interface {
	CreateSiteAccount(context.Context, achrix.Principal, string, string, string) (identity.Account, error)
}

type inventoryItem struct {
	ID        string
	Origin    string
	Disabled  bool
	CanManage bool
}

type inventoryView struct {
	Locale *localize.Localizer
	Sites  []inventoryItem
}

type accountCreateView struct {
	Locale *localize.Localizer
	Site   inventoryItem
}

func newControlSurface(sites []SiteConfig, service controlSiteManager, languages ...localize.Locale) shell.Surface {
	return shell.Surface{
		ID:         "sites",
		Title:      shell.Text{Domain: "rixa", Key: "sites"},
		Capability: CapabilityControlInventoryRead,
		Handler: func(w http.ResponseWriter, r *http.Request, view shell.Request) {
			if r.Method == http.MethodGet && (r.URL.Path == "/editor.js" || r.URL.Path == "/editor.css") {
				if r.URL.Path == "/editor.js" {
					w.Header().Set("Content-Type", "text/javascript; charset=utf-8")
					_, _ = io.WriteString(w, contentEditorJS)
				} else {
					w.Header().Set("Content-Type", "text/css; charset=utf-8")
					_, _ = io.WriteString(w, contentEditorCSS)
				}
				return
			}
			if strings.HasPrefix(r.URL.Path, "/site/") {
				manager, ok := service.(controlDraftManager)
				if !ok {
					editorialHTTPFail(w, ErrEditorialUnavailable, "")
					return
				}
				serveSelectedContent(manager, sites, languages, w, r, view)
				return
			}
			serveControlSurface(service, sites, w, r, view)
		},
	}
}

func serveControlSurface(service controlSiteManager, sites []SiteConfig, w http.ResponseWriter, r *http.Request, view shell.Request) {
	if r.Method == http.MethodGet {
		if r.URL.Path == "/account-create" {
			serveControlAccountCreate(service, sites, w, r, view)
			return
		}
		if r.URL.Path != "/" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		items := make([]inventoryItem, 0, len(sites))
		for _, site := range sites {
			items = append(items, inventoryItem{
				ID:        site.ID,
				Origin:    site.Origin,
				Disabled:  site.Disabled,
				CanManage: !site.Disabled && view.Allowed(CapabilityControlSiteManage, site.ID),
			})
		}
		if err := renderProductPage(view, "sites", shell.Page{
			Template: inventoryTemplate,
			Data:     inventoryView{Locale: view.Localizer, Sites: items},
		}); err != nil {
			controlSurfaceFail(w, ErrUnavailable)
		}
		return
	}
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", "GET, POST")
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	if r.URL.Path != "/account-create" {
		w.WriteHeader(http.StatusNotFound)
		return
	}
	if service == nil || len(r.Header.Values("Content-Type")) != 1 || r.Header.Get("Content-Type") != "application/json" {
		controlSurfaceFail(w, identity.ErrInvalid)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 4096)
	defer r.Body.Close()
	var body struct {
		SiteID   string `json:"site_id"`
		Login    string `json:"login"`
		Password string `json:"password"`
	}
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&body); err != nil {
		controlSurfaceFail(w, identity.ErrInvalid)
		return
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		controlSurfaceFail(w, identity.ErrInvalid)
		return
	}
	account, err := service.CreateSiteAccount(r.Context(), view.Principal, body.SiteID, body.Login, body.Password)
	body.Password = ""
	if err != nil {
		controlSurfaceFail(w, err)
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_ = json.NewEncoder(w).Encode(struct {
		ID       string `json:"id"`
		Login    string `json:"login"`
		Enabled  bool   `json:"enabled"`
		Revision int64  `json:"revision"`
	}{account.ID, account.Login, account.Enabled, account.Revision})
}

// The account form is deliberately opened for one explicitly selected site.
// Authorization is checked again by CreateSiteAccount on POST; a hidden field
// is never a grant and does not authorize an alternate site.
func serveControlAccountCreate(service controlSiteManager, sites []SiteConfig, w http.ResponseWriter, r *http.Request, view shell.Request) {
	query := r.URL.Query()["site_id"]
	if len(query) != 1 || query[0] == "" || service == nil {
		controlSurfaceFail(w, achrix.ErrDenied)
		return
	}
	for _, site := range sites {
		if site.ID != query[0] {
			continue
		}
		if site.Disabled || !view.Allowed(CapabilityControlSiteManage, site.ID) {
			controlSurfaceFail(w, achrix.ErrDenied)
			return
		}
		if err := renderProductPage(view, "create_site_account", shell.Page{
			Template: accountCreateTemplate,
			Data: accountCreateView{
				Locale: view.Localizer,
				Site:   inventoryItem{ID: site.ID, Origin: site.Origin},
			},
		}); err != nil {
			controlSurfaceFail(w, ErrUnavailable)
		}
		return
	}
	controlSurfaceFail(w, achrix.ErrDenied)
}

func controlSurfaceFail(w http.ResponseWriter, err error) {
	status, code := http.StatusServiceUnavailable, "unavailable"
	switch {
	case errors.Is(err, achrix.ErrDenied):
		status, code = http.StatusForbidden, "permission_denied"
	case errors.Is(err, identity.ErrInvalid):
		status, code = http.StatusBadRequest, "invalid_input"
	case errors.Is(err, identity.ErrConflict):
		status, code = http.StatusConflict, "conflict"
	case errors.Is(err, identity.ErrLimited):
		status, code = http.StatusTooManyRequests, "limited"
	case errors.Is(err, identity.ErrNotFound):
		status, code = http.StatusNotFound, "not_found"
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(struct {
		Code string `json:"code"`
	}{code})
}

var inventoryTemplate = template.Must(template.New("inventory").Parse(`{{define "content"}}
<p>{{.Locale.Text "rixa" "sites_notice" nil}}</p>
<table>
<thead><tr><th scope="col">{{.Locale.Text "rixa" "id" nil}}</th><th scope="col">{{.Locale.Text "rixa" "origin" nil}}</th><th scope="col">{{.Locale.Text "rixa" "state" nil}}</th><th scope="col">{{.Locale.Text "rixa" "actions" nil}}</th></tr></thead>
<tbody>{{range .Sites}}<tr>
<th scope="row" dir="ltr"><bdi>{{.ID}}</bdi></th><td dir="ltr">{{.Origin}}</td>
<td>{{if .Disabled}}{{$.Locale.Text "rixa" "disabled" nil}}{{else}}{{$.Locale.Text "rixa" "enabled" nil}}{{end}}</td>
<td>{{if .CanManage}}<a href="/admin/sites/site/{{.ID}}/content/">{{$.Locale.Text "rixa" "manage_content" nil}}</a> · <a href="/admin/sites/account-create?site_id={{urlquery .ID}}">{{$.Locale.Text "rixa" "create_site_account" nil}}</a>{{else}}—{{end}}</td>
</tr>{{end}}</tbody>
</table>
{{end}}`))

var accountCreateTemplate = template.Must(template.New("account-create").Parse(`{{define "content"}}
<h2>{{.Locale.Text "rixa" "create_site_account" nil}}</h2>
<p>{{.Locale.Text "rixa" "site" nil}}: <bdi dir="ltr">{{.Site.ID}}</bdi> · <bdi dir="ltr">{{.Site.Origin}}</bdi></p>
<p>{{.Locale.Text "rixa" "site_account_scope_notice" nil}}</p>
<form data-admin-form data-control-account-create action="/admin/sites/account-create" method="post" autocomplete="off">
<input type="hidden" name="site_id" value="{{.Site.ID}}">
<label>{{.Locale.Text "rixa" "login" nil}} <input name="login" dir="ltr" maxlength="64" autocapitalize="none" autocomplete="off" spellcheck="false" required></label>
<label>{{.Locale.Text "rixa" "initial_password" nil}} <input name="password" type="password" autocomplete="new-password" required></label>
<button type="submit">{{.Locale.Text "rixa" "create_site_account" nil}}</button>
</form>
<p>{{.Locale.Text "rixa" "site_account_retry_notice" nil}}</p>
<p><a href="/admin/sites/">{{.Locale.Text "rixa" "sites" nil}}</a></p>
{{end}}`))
