// SPDX-License-Identifier: MPL-2.0

package product

import (
	"errors"
	"html/template"
	"io"
	"net/http"
	"strconv"

	shell "github.com/AChWorks/achrix/admin"
	"github.com/AChWorks/achrix/admin/localize"
)

type appearanceSurface struct {
	service        *EditorialService
	publication    *PublicationService
	contentLocales []localize.Locale
}

func newAppearanceSurface(service *EditorialService, publication *PublicationService, languages ...localize.Locale) (shell.Surface, error) {
	if service == nil {
		return shell.Surface{}, shell.ErrConfiguration
	}
	h := &appearanceSurface{service: service, publication: publication, contentLocales: append([]localize.Locale(nil), languages...)}
	return shell.Surface{
		ID:         "appearance",
		Title:      shell.Text{Domain: "rixa", Key: "appearance"},
		Capability: CapabilityAppearanceRead,
		Target:     AppearanceTarget,
		Handler:    h.serve,
	}, nil
}

type appearanceView struct {
	Locale         *localize.Localizer
	ContentLocales []localize.Locale
	Appearance     Appearance
	Publication    PublicationState
	OperationID    string
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
	case "/publish":
		h.publish(w, r, view)
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
	if err = renderProductPage(view, "appearance", shell.Page{
		Template: appearanceTemplate,
		Data: appearanceView{
			Locale: view.Localizer, ContentLocales: h.contentLocales, Appearance: current,
			Publication: h.publication.State("", ""), OperationID: newOperationID(),
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
	if err == nil {
		editorialJSON(w, map[string]string{
			"operation_id": op.ID, "kind": op.Kind, "resource_id": op.ResourceID,
			"result_revision": strconv.FormatInt(op.ResultRevision, 10),
			"created_at":      op.CreatedAt.Format("2006-01-02T15:04:05.999999Z07:00"),
		})
		return
	}
	if !errors.Is(err, ErrEditorialNotFound) || h.publication == nil {
		editorialHTTPFail(w, err, body.OperationID)
		return
	}
	publication, publicationErr := h.publication.Operation(r.Context(), view.Principal, body.OperationID)
	if publicationErr != nil {
		editorialHTTPFail(w, publicationErr, body.OperationID)
		return
	}
	editorialJSON(w, map[string]string{
		"operation_id": publication.ID, "kind": "publication.apply",
		"result_generation": publication.Generation, "route": publication.Route,
		"created_at": publication.CreatedAt.Format("2006-01-02T15:04:05.999999Z07:00"),
	})
}

func (h *appearanceSurface) publish(w http.ResponseWriter, r *http.Request, view shell.Request) {
	if h.publication == nil {
		editorialHTTPFail(w, ErrEditorialUnavailable, "")
		return
	}
	var body struct {
		OperationID        string `json:"operation_id"`
		ExpectedGeneration string `json:"expected_generation"`
	}
	if err := decodeEditorialJSON(w, r, &body); err != nil {
		editorialHTTPFail(w, err, body.OperationID)
		return
	}
	result, err := h.publication.Apply(r.Context(), view.Principal, PublicationRequest{
		OperationID: body.OperationID, ExpectedGeneration: body.ExpectedGeneration,
	})
	if err != nil {
		editorialHTTPFail(w, err, body.OperationID)
		return
	}
	editorialJSON(w, map[string]string{
		"operation_id": body.OperationID, "generation": result.Generation,
	})
}

var appearanceTemplate = template.Must(template.New("appearance").Parse(`{{define "content"}}
<script src="/admin/appearance/appearance.js" defer></script>
<p>{{.Locale.Text "rixa" "appearance_locale_notice" nil}}</p>
<p>{{.Locale.Text "rixa" "appearance_revision" nil}}: <output data-appearance-revision>{{.Appearance.Revision}}</output></p>
<form data-admin-form data-appearance-save action="/admin/appearance/save" method="post">
<input type="hidden" name="operation_id" value="{{.OperationID}}" data-operation-field>
<input type="hidden" name="expected_head" value="{{.Appearance.Revision}}" data-head-field>
<fieldset><legend>{{.Locale.Text "rixa" "site" nil}}</legend>
<label>{{.Locale.Text "rixa" "site_title" nil}} <input name="site_title" value="{{.Appearance.SiteTitle}}" maxlength="120" required dir="auto"></label>
<label>{{.Locale.Text "rixa" "site_description" nil}} <textarea name="site_description" maxlength="300" dir="auto">{{.Appearance.SiteDescription}}</textarea></label>
<label>{{.Locale.Text "rixa" "site_language" nil}} <select name="site_language" required>
{{range .ContentLocales}}<option value="{{.Tag}}" {{if eq $.Appearance.SiteLanguage .Tag}}selected{{end}}>{{.Name}}</option>{{end}}
</select></label>
</fieldset>
<fieldset><legend>{{.Locale.Text "rixa" "home" nil}}</legend>
<label>{{.Locale.Text "rixa" "home_mode" nil}} <select name="home_mode" data-home-mode required>
<option value="latest_posts" {{if eq .Appearance.HomeMode "latest_posts"}}selected{{end}}>{{.Locale.Text "rixa" "latest_posts" nil}}</option>
<option value="page" {{if eq .Appearance.HomeMode "page"}}selected{{end}}>{{.Locale.Text "rixa" "specific_page" nil}}</option>
</select></label>
<label>{{.Locale.Text "rixa" "home_page_id" nil}} <input name="home_page_id" data-home-page value="{{.Appearance.HomePageID}}" dir="ltr" maxlength="26" pattern="[A-Z2-7]{26}"></label>
<p>{{.Locale.Text "rixa" "home_page_notice" nil}}</p>
</fieldset>
<fieldset><legend>{{.Locale.Text "rixa" "header" nil}}</legend>
<label><input type="checkbox" name="header_show_title" {{if .Appearance.HeaderShowTitle}}checked{{end}}> {{.Locale.Text "rixa" "header_show_title" nil}}</label>
<label>{{.Locale.Text "rixa" "header_tagline" nil}} <input name="header_tagline" value="{{.Appearance.HeaderTagline}}" maxlength="160" dir="auto"></label>
</fieldset>
<fieldset><legend>{{.Locale.Text "rixa" "footer" nil}}</legend>
<label>{{.Locale.Text "rixa" "footer_text" nil}} <textarea name="footer_text" maxlength="500" dir="auto">{{.Appearance.FooterText}}</textarea></label>
</fieldset>
<fieldset><legend>{{.Locale.Text "rixa" "theme" nil}}</legend>
<label>{{.Locale.Text "rixa" "color_mode" nil}} <select name="theme" required>
<option value="light" {{if eq .Appearance.Theme "light"}}selected{{end}}>{{.Locale.Text "rixa" "light" nil}}</option>
<option value="dark" {{if eq .Appearance.Theme "dark"}}selected{{end}}>{{.Locale.Text "rixa" "dark" nil}}</option>
</select></label>
</fieldset>
<button type="submit">{{.Locale.Text "rixa" "save_appearance" nil}}</button>
</form>
{{if .Publication.Enabled}}<section aria-labelledby="appearance-publication-heading"><h2 id="appearance-publication-heading">{{.Locale.Text "rixa" "public_publication" nil}}</h2>
<p>{{.Locale.Text "rixa" "appearance_publication_notice" nil}}</p>
<p>{{.Locale.Text "rixa" "active_generation" nil}}: <output data-publication-output>{{if .Publication.Generation}}{{.Publication.Generation}}{{else}}—{{end}}</output></p>
<form data-admin-form data-publication-apply action="/admin/appearance/publish" method="post">
<input type="hidden" name="operation_id" value="{{.OperationID}}" data-operation-field>
<input type="hidden" name="expected_generation" value="{{.Publication.Generation}}" data-publication-generation>
<button type="submit">{{.Locale.Text "rixa" "apply_publication" nil}}</button>
</form></section>{{end}}
<section aria-labelledby="appearance-operation-heading"><h2 id="appearance-operation-heading">{{.Locale.Text "rixa" "inspect_mutation_outcome" nil}}</h2>
<p>{{.Locale.Text "rixa" "unknown_save_notice" nil}}</p>
<form data-admin-form data-read-only data-operation-lookup action="/admin/appearance/operation" method="post">
<label>{{.Locale.Text "rixa" "operation_id" nil}} <input name="operation_id" data-operation-lookup-input dir="ltr" maxlength="36" required></label>
<button type="submit">{{.Locale.Text "rixa" "inspect" nil}}</button>
</form></section>
{{end}}`))

const appearanceJS = `
"use strict";
(() => {
  const status=document.getElementById("status");
  const i18n=window.AChrixI18n;
  function isolated(value){const node=document.createElement("bdi");node.dir="ltr";node.textContent=value;return node}
  function uuid(){if(crypto.randomUUID)return crypto.randomUUID();const b=new Uint8Array(16);crypto.getRandomValues(b);b[6]=(b[6]&15)|64;b[8]=(b[8]&63)|128;const h=[...b].map(v=>v.toString(16).padStart(2,"0")).join("");return h.slice(0,8)+"-"+h.slice(8,12)+"-"+h.slice(12,16)+"-"+h.slice(16,20)+"-"+h.slice(20)}
  const form=document.querySelector("[data-appearance-save]"),mode=document.querySelector("[data-home-mode]"),pageID=document.querySelector("[data-home-page]");
  const publicationForm=document.querySelector("[data-publication-apply]");
  if(publicationForm){const operation=publicationForm.querySelector("[data-operation-field]");if(operation)operation.value=uuid()}
  function homeState(){if(!mode||!pageID)return;const page=mode.value==="page";pageID.disabled=!page;pageID.required=page;if(!page)pageID.value=""}
  mode?.addEventListener("change",homeState);homeState();
  form?.addEventListener("admin:success",event=>{const rev=event.detail?.revision;if(!rev)return;document.querySelector("[data-head-field]").value=rev;document.querySelector("[data-appearance-revision]").textContent=rev;form.querySelector("[data-operation-field]").value=uuid()});
  document.querySelector("[data-publication-apply]")?.addEventListener("admin:success",event=>{const generation=event.detail?.generation;if(!generation)return;document.querySelector("[data-publication-generation]").value=generation;document.querySelector("[data-publication-output]").textContent=generation;document.querySelector("[data-publication-apply] [data-operation-field]").value=uuid()});
  document.querySelectorAll("form[data-admin-form]").forEach(current=>current.addEventListener("admin:failure",event=>{if(!event.detail?.unknown)return;const op=current.querySelector("input[name=operation_id]"),lookup=document.querySelector("[data-operation-lookup-input]");if(op&&lookup)lookup.value=op.value;if(op&&status)status.replaceChildren(i18n.parts("rixa","outcome_unknown",{Operation:isolated(op.value)}))}));
  document.querySelector("[data-operation-lookup]")?.addEventListener("admin:success",event=>{const d=event.detail||{};if(status)status.replaceChildren(i18n.parts("rixa","appearance_operation_committed",{Operation:isolated(d.operation_id||""),Kind:isolated(d.kind||""),Revision:isolated(d.result_revision||"0")}))});
})();
`
