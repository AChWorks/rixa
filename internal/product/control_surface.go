// SPDX-License-Identifier: MPL-2.0

package product

import (
	"context"
	"encoding/json"
	"errors"
	"html/template"
	"io"
	"net/http"

	"github.com/AChWorks/achrix"
	shell "github.com/AChWorks/achrix/admin"
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
	Language string
	Sites    []inventoryItem
}

func newControlSurface(sites []SiteConfig, service controlSiteManager) shell.Surface {
	return shell.Surface{
		ID:         "sites",
		Title:      shell.Text{English: "Sites", Persian: "سایت‌ها"},
		Capability: CapabilityControlInventoryRead,
		Handler: func(w http.ResponseWriter, r *http.Request, view shell.Request) {
			serveControlSurface(service, sites, w, r, view)
		},
	}
}

func serveControlSurface(service controlSiteManager, sites []SiteConfig, w http.ResponseWriter, r *http.Request, view shell.Request) {
	if r.Method == http.MethodGet {
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
		if err := view.Render(shell.Page{
			Title:    (shell.Text{English: "Sites", Persian: "سایت‌ها"}).In(view.Language),
			Template: inventoryTemplate,
			Data:     inventoryView{Language: view.Language, Sites: items},
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
{{if eq .Language "fa"}}<p>فهرست سایت‌های تعریف‌شده در این نصب. نشست مدیریت زیرساخت به‌تنهایی مجوز سایت نیست.</p>{{else}}<p>Declared sites for this installation. A control session is not itself a site permission.</p>{{end}}
<table>
<thead><tr><th>{{if eq .Language "fa"}}شناسه{{else}}ID{{end}}</th><th>{{if eq .Language "fa"}}مبدأ{{else}}Origin{{end}}</th><th>{{if eq .Language "fa"}}وضعیت{{else}}State{{end}}</th></tr></thead>
<tbody>{{range .Sites}}<tr><td dir="ltr">{{.ID}}</td><td dir="ltr">{{.Origin}}</td><td>{{if .Disabled}}{{if eq $.Language "fa"}}غیرفعال{{else}}disabled{{end}}{{else}}{{if eq $.Language "fa"}}فعال{{else}}enabled{{end}}{{end}}</td></tr>
{{if .CanManage}}<tr><td colspan="3"><form data-admin-form action="/admin/sites/account-create" method="post">
<input type="hidden" name="site_id" value="{{.ID}}">
<label>{{if eq $.Language "fa"}}شناسه ورود{{else}}Login{{end}} <input name="login" dir="ltr" maxlength="64" autocomplete="off" required></label>
<label>{{if eq $.Language "fa"}}رمز اولیه{{else}}Initial password{{end}} <input name="password" type="password" autocomplete="new-password" required></label>
<button type="submit">{{if eq $.Language "fa"}}ایجاد حساب در این سایت{{else}}Create site account{{end}}</button>
</form></td></tr>{{end}}{{end}}</tbody>
</table>
{{end}}`))
