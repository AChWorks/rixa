// SPDX-License-Identifier: MPL-2.0

package product

import (
	"html/template"
	"io"
	"net/http"
	"strconv"

	shell "github.com/AChWorks/achrix/admin"
)

type appearanceSurface struct {
	service *EditorialService
}

func newAppearanceSurface(service *EditorialService) (shell.Surface, error) {
	if service == nil {
		return shell.Surface{}, shell.ErrConfiguration
	}
	h := &appearanceSurface{service: service}
	return shell.Surface{
		ID:         "appearance",
		Title:      shell.Text{English: "Appearance", Persian: "ظاهر سایت"},
		Capability: CapabilityAppearanceRead,
		Target:     AppearanceTarget,
		Handler:    h.serve,
	}, nil
}

type appearanceView struct {
	Language    string
	Appearance  Appearance
	OperationID string
}

func (h *appearanceSurface) serve(w http.ResponseWriter, r *http.Request, view shell.Request) {
	if r.Method == http.MethodGet {
		switch r.URL.Path {
		case "/":
			h.show(w, r, view)
		case "/appearance.js":
			w.Header().Set("Content-Type", "text/javascript; charset=utf-8")
			_, _ = io.WriteString(w, appearanceJS)
		default:
			editorialHTTPFail(w, ErrEditorialNotFound, "")
		}
		return
	}
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", "GET, POST")
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	switch r.URL.Path {
	case "/save":
		h.save(w, r, view)
	case "/operation":
		h.operation(w, r, view)
	default:
		editorialHTTPFail(w, ErrEditorialNotFound, "")
	}
}

func (h *appearanceSurface) show(w http.ResponseWriter, r *http.Request, view shell.Request) {
	current, err := h.service.Appearance(r.Context(), view.Principal)
	if err != nil {
		editorialHTTPFail(w, err, "")
		return
	}
	if err = view.Render(shell.Page{
		Title:    (shell.Text{English: "Appearance", Persian: "ظاهر سایت"}).In(view.Language),
		Template: appearanceTemplate,
		Data: appearanceView{
			Language: view.Language, Appearance: current, OperationID: newOperationID(),
		},
	}); err != nil {
		editorialHTTPFail(w, ErrEditorialUnavailable, "")
	}
}

func (h *appearanceSurface) save(w http.ResponseWriter, r *http.Request, view shell.Request) {
	var body struct {
		OperationID     string `json:"operation_id"`
		Expected        string `json:"expected_head"`
		SiteTitle       string `json:"site_title"`
		SiteDescription string `json:"site_description"`
		SiteLanguage    string `json:"site_language"`
		HomeMode        string `json:"home_mode"`
		HomePageID      string `json:"home_page_id"`
		HeaderShowTitle bool   `json:"header_show_title"`
		HeaderTagline   string `json:"header_tagline"`
		FooterText      string `json:"footer_text"`
		Theme           string `json:"theme"`
	}
	if err := decodeEditorialJSON(w, r, &body); err != nil {
		editorialHTTPFail(w, err, body.OperationID)
		return
	}
	expected, err := parseRevision(body.Expected)
	if err != nil {
		editorialHTTPFail(w, ErrEditorialInvalid, body.OperationID)
		return
	}
	result, err := h.service.SaveAppearance(r.Context(), view.Principal, body.OperationID, expected, AppearanceInput{
		SiteTitle: body.SiteTitle, SiteDescription: body.SiteDescription, SiteLanguage: body.SiteLanguage,
		HomeMode: body.HomeMode, HomePageID: body.HomePageID,
		HeaderShowTitle: body.HeaderShowTitle, HeaderTagline: body.HeaderTagline,
		FooterText: body.FooterText, Theme: body.Theme,
	})
	if err != nil {
		editorialHTTPFail(w, err, body.OperationID)
		return
	}
	editorialJSON(w, map[string]string{
		"revision": strconv.FormatInt(result.Revision, 10), "operation_id": body.OperationID,
	})
}

func (h *appearanceSurface) operation(w http.ResponseWriter, r *http.Request, view shell.Request) {
	var body struct {
		OperationID string `json:"operation_id"`
	}
	if err := decodeEditorialJSON(w, r, &body); err != nil {
		editorialHTTPFail(w, err, body.OperationID)
		return
	}
	op, err := h.service.Operation(r.Context(), view.Principal, body.OperationID)
	if err != nil {
		editorialHTTPFail(w, err, body.OperationID)
		return
	}
	editorialJSON(w, map[string]string{
		"operation_id": op.ID, "kind": op.Kind, "resource_id": op.ResourceID,
		"result_revision": strconv.FormatInt(op.ResultRevision, 10),
		"created_at":      op.CreatedAt.Format("2006-01-02T15:04:05.999999Z07:00"),
	})
}

var appearanceTemplate = template.Must(template.New("appearance").Parse(`{{define "content"}}
<script src="/admin/appearance/appearance.js" defer></script>
<p>{{if eq .Language "fa"}}این زبان و ظاهر متعلق به خود سایت است و مستقل از زبان رابط مدیریت است.{{else}}These settings belong to the site and are independent from the administration interface language.{{end}}</p>
<p>{{if eq .Language "fa"}}نسخهٔ فعلی تنظیمات{{else}}Current appearance revision{{end}}: <output data-appearance-revision>{{.Appearance.Revision}}</output></p>
<form data-admin-form data-appearance-save action="/admin/appearance/save" method="post">
<input type="hidden" name="operation_id" value="{{.OperationID}}" data-operation-field>
<input type="hidden" name="expected_head" value="{{.Appearance.Revision}}" data-head-field>
<fieldset><legend>{{if eq .Language "fa"}}سایت{{else}}Site{{end}}</legend>
<label>{{if eq .Language "fa"}}عنوان سایت{{else}}Site title{{end}} <input name="site_title" value="{{.Appearance.SiteTitle}}" maxlength="120" required dir="auto"></label>
<label>{{if eq .Language "fa"}}توضیح سایت{{else}}Site description{{end}} <textarea name="site_description" maxlength="300" dir="auto">{{.Appearance.SiteDescription}}</textarea></label>
<label>{{if eq .Language "fa"}}زبان سایت{{else}}Site language{{end}} <select name="site_language" required>
<option value="en" {{if eq .Appearance.SiteLanguage "en"}}selected{{end}}>English</option>
<option value="fa" {{if eq .Appearance.SiteLanguage "fa"}}selected{{end}}>فارسی</option>
</select></label>
</fieldset>
<fieldset><legend>{{if eq .Language "fa"}}صفحهٔ خانه{{else}}Home{{end}}</legend>
<label>{{if eq .Language "fa"}}نوع صفحهٔ خانه{{else}}Home mode{{end}} <select name="home_mode" data-home-mode required>
<option value="latest_posts" {{if eq .Appearance.HomeMode "latest_posts"}}selected{{end}}>{{if eq .Language "fa"}}آخرین نوشته‌ها{{else}}Latest posts{{end}}</option>
<option value="page" {{if eq .Appearance.HomeMode "page"}}selected{{end}}>{{if eq .Language "fa"}}یک برگهٔ مشخص{{else}}A specific page{{end}}</option>
</select></label>
<label>{{if eq .Language "fa"}}شناسهٔ برگهٔ خانه{{else}}Home page ID{{end}} <input name="home_page_id" data-home-page value="{{.Appearance.HomePageID}}" dir="ltr" maxlength="26" pattern="[A-Z2-7]{26}"></label>
<p>{{if eq .Language "fa"}}در حالت «برگه»، شناسه باید به یک برگهٔ ذخیره‌شده در همین سایت اشاره کند.{{else}}In page mode, the ID must reference a saved page in this same site.{{end}}</p>
</fieldset>
<fieldset><legend>{{if eq .Language "fa"}}سربرگ{{else}}Header{{end}}</legend>
<label><input type="checkbox" name="header_show_title" {{if .Appearance.HeaderShowTitle}}checked{{end}}> {{if eq .Language "fa"}}نمایش عنوان سایت در سربرگ{{else}}Show site title in header{{end}}</label>
<label>{{if eq .Language "fa"}}زیرعنوان سربرگ{{else}}Header tagline{{end}} <input name="header_tagline" value="{{.Appearance.HeaderTagline}}" maxlength="160" dir="auto"></label>
</fieldset>
<fieldset><legend>{{if eq .Language "fa"}}پابرگ{{else}}Footer{{end}}</legend>
<label>{{if eq .Language "fa"}}متن پابرگ{{else}}Footer text{{end}} <textarea name="footer_text" maxlength="500" dir="auto">{{.Appearance.FooterText}}</textarea></label>
</fieldset>
<fieldset><legend>{{if eq .Language "fa"}}پوسته{{else}}Theme{{end}}</legend>
<label>{{if eq .Language "fa"}}حالت رنگ{{else}}Color mode{{end}} <select name="theme" required>
<option value="light" {{if eq .Appearance.Theme "light"}}selected{{end}}>{{if eq .Language "fa"}}روشن{{else}}Light{{end}}</option>
<option value="dark" {{if eq .Appearance.Theme "dark"}}selected{{end}}>{{if eq .Language "fa"}}تیره{{else}}Dark{{end}}</option>
</select></label>
</fieldset>
<button type="submit">{{if eq .Language "fa"}}ذخیرهٔ تنظیمات{{else}}Save appearance{{end}}</button>
</form>
<section aria-labelledby="appearance-operation-heading"><h2 id="appearance-operation-heading">{{if eq .Language "fa"}}بررسی نتیجهٔ عملیات{{else}}Inspect mutation outcome{{end}}</h2>
<p>{{if eq .Language "fa"}}اگر پاسخ ذخیره از دست رفت، همان شناسهٔ عملیات را بررسی کنید؛ ذخیره را خودکار تکرار نکنید.{{else}}If the save response was lost, inspect the same operation ID; do not automatically submit it again.{{end}}</p>
<form data-admin-form data-read-only data-operation-lookup action="/admin/appearance/operation" method="post">
<label>{{if eq .Language "fa"}}شناسهٔ عملیات{{else}}Operation ID{{end}} <input name="operation_id" data-operation-lookup-input dir="ltr" maxlength="36" required></label>
<button type="submit">{{if eq .Language "fa"}}بررسی{{else}}Inspect{{end}}</button>
</form></section>
{{end}}`))

const appearanceJS = `
"use strict";
(() => {
  const fa=document.documentElement.lang==="fa",status=document.getElementById("status"),text=(en,faText)=>fa?faText:en;
  function uuid(){if(crypto.randomUUID)return crypto.randomUUID();const b=new Uint8Array(16);crypto.getRandomValues(b);b[6]=(b[6]&15)|64;b[8]=(b[8]&63)|128;const h=[...b].map(v=>v.toString(16).padStart(2,"0")).join("");return h.slice(0,8)+"-"+h.slice(8,12)+"-"+h.slice(12,16)+"-"+h.slice(16,20)+"-"+h.slice(20)}
  const form=document.querySelector("[data-appearance-save]"),mode=document.querySelector("[data-home-mode]"),pageID=document.querySelector("[data-home-page]");
  function homeState(){if(!mode||!pageID)return;const page=mode.value==="page";pageID.disabled=!page;pageID.required=page;if(!page)pageID.value=""}
  mode?.addEventListener("change",homeState);homeState();
  form?.addEventListener("admin:success",event=>{const rev=event.detail?.revision;if(!rev)return;document.querySelector("[data-head-field]").value=rev;document.querySelector("[data-appearance-revision]").textContent=rev;form.querySelector("[data-operation-field]").value=uuid()});
  document.querySelectorAll("form[data-admin-form]").forEach(current=>current.addEventListener("admin:failure",event=>{if(!event.detail?.unknown)return;const op=current.querySelector("input[name=operation_id]"),lookup=document.querySelector("[data-operation-lookup-input]");if(op&&lookup)lookup.value=op.value;if(op&&status)status.textContent=text("Outcome unknown. Do not submit again. Inspect operation "+op.value+".","نتیجه نامعلوم است. دوباره ارسال نکنید. عملیات "+op.value+" را بررسی کنید.")}));
  document.querySelector("[data-operation-lookup]")?.addEventListener("admin:success",event=>{const d=event.detail||{};if(status)status.textContent=text("Operation "+(d.operation_id||"")+" committed as "+(d.kind||"")+" revision "+(d.result_revision||"0")+".","عملیات "+(d.operation_id||"")+" ثبت شده است.")});
})();
`
