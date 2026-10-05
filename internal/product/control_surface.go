// SPDX-License-Identifier: MPL-2.0

package product

import (
	"html/template"
	"net/http"

	shell "github.com/AChWorks/achrix/admin"
)

type inventoryItem struct {
	ID       string
	Origin   string
	Disabled bool
}

type inventoryView struct {
	Language string
	Sites    []inventoryItem
}

func newInventorySurface(sites []SiteConfig) shell.Surface {
	items := make([]inventoryItem, 0, len(sites))
	for _, site := range sites {
		items = append(items, inventoryItem{ID: site.ID, Origin: site.Origin, Disabled: site.Disabled})
	}
	return shell.Surface{
		ID:         "sites",
		Title:      shell.Text{English: "Sites", Persian: "سایت‌ها"},
		Capability: CapabilityControlInventoryRead,
		Handler: func(w http.ResponseWriter, r *http.Request, view shell.Request) {
			if r.Method != http.MethodGet || r.URL.Path != "/" {
				w.Header().Set("Content-Type", "application/json; charset=utf-8")
				w.WriteHeader(http.StatusMethodNotAllowed)
				_, _ = w.Write([]byte(`{"code":"invalid_request"}`))
				return
			}
			if err := view.Render(shell.Page{
				Title:    (shell.Text{English: "Sites", Persian: "سایت‌ها"}).In(view.Language),
				Template: inventoryTemplate,
				Data:     inventoryView{Language: view.Language, Sites: items},
			}); err != nil {
				w.Header().Set("Content-Type", "application/json; charset=utf-8")
				w.WriteHeader(http.StatusServiceUnavailable)
				_, _ = w.Write([]byte(`{"code":"unavailable"}`))
			}
		},
	}
}

var inventoryTemplate = template.Must(template.New("inventory").Parse(`{{define "content"}}
{{if eq .Language "fa"}}<p>فهرست سایت‌های تعریف‌شده در این نصب. نشست مدیریت زیرساخت به‌تنهایی مجوز سایت نیست.</p>{{else}}<p>Declared sites for this installation. A control session is not itself a site permission.</p>{{end}}
<table>
<thead><tr><th>{{if eq .Language "fa"}}شناسه{{else}}ID{{end}}</th><th>{{if eq .Language "fa"}}مبدأ{{else}}Origin{{end}}</th><th>{{if eq .Language "fa"}}وضعیت{{else}}State{{end}}</th></tr></thead>
<tbody>{{range .Sites}}<tr><td dir="ltr">{{.ID}}</td><td dir="ltr">{{.Origin}}</td><td>{{if .Disabled}}{{if eq $.Language "fa"}}غیرفعال{{else}}disabled{{end}}{{else}}{{if eq $.Language "fa"}}فعال{{else}}enabled{{end}}{{end}}</td></tr>{{end}}</tbody>
</table>
{{end}}`))
