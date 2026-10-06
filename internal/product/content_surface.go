// SPDX-License-Identifier: MPL-2.0

package product

import (
	"encoding/json"
	"errors"
	"html/template"
	"io"
	"net/http"
	"strconv"
	"strings"

	"github.com/AChWorks/achrix"
	shell "github.com/AChWorks/achrix/admin"
)

type contentSurface struct {
	service     *EditorialService
	publication *PublicationService
}

func newContentSurface(service *EditorialService, publication *PublicationService) (shell.Surface, error) {
	if service == nil {
		return shell.Surface{}, shell.ErrConfiguration
	}
	h := &contentSurface{service: service, publication: publication}
	return shell.Surface{ID: "content", Title: shell.Text{English: "Content", Persian: "محتوا"}, Capability: CapabilityContentList, Target: ContentCollectionTarget, Handler: h.serve}, nil
}

type contentIndexView struct {
	Language    string
	Items       []ContentSummary
	OperationID string
}
type contentEditView struct {
	Language    string
	Revision    ContentRevision
	History     []RevisionSummary
	Body        template.HTML
	Publication PublicationState
	OperationID string
}
type contentPreviewView struct {
	Language   string
	Revision   ContentRevision
	Body       template.HTML
	Appearance Appearance
}

func (h *contentSurface) serve(w http.ResponseWriter, r *http.Request, view shell.Request) {
	if r.Method == http.MethodGet {
		switch {
		case r.URL.Path == "/":
			h.index(w, r, view)
		case r.URL.Path == "/editor.js":
			w.Header().Set("Content-Type", "text/javascript; charset=utf-8")
			_, _ = io.WriteString(w, contentEditorJS)
		case r.URL.Path == "/editor.css":
			w.Header().Set("Content-Type", "text/css; charset=utf-8")
			_, _ = io.WriteString(w, contentEditorCSS)
		case strings.HasPrefix(r.URL.Path, "/edit/"):
			h.edit(w, r, view, strings.TrimPrefix(r.URL.Path, "/edit/"))
		case strings.HasPrefix(r.URL.Path, "/preview/"):
			h.preview(w, r, view, strings.TrimPrefix(r.URL.Path, "/preview/"))
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
	case "/create":
		h.create(w, r, view)
	case "/save":
		h.save(w, r, view)
	case "/publication-intent":
		h.publicationIntent(w, r, view, false)
	case "/publication-intent-clear":
		h.publicationIntent(w, r, view, true)
	case "/operation":
		h.operation(w, r, view)
	case "/publish":
		h.publish(w, r, view)
	default:
		editorialHTTPFail(w, ErrEditorialNotFound, "")
	}
}

func (h *contentSurface) index(w http.ResponseWriter, r *http.Request, view shell.Request) {
	items, err := h.service.List(r.Context(), view.Principal)
	if err != nil {
		editorialHTTPFail(w, err, "")
		return
	}
	if err = view.Render(shell.Page{Title: (shell.Text{English: "Content", Persian: "محتوا"}).In(view.Language), Template: contentIndexTemplate, Data: contentIndexView{Language: view.Language, Items: items, OperationID: newOperationID()}}); err != nil {
		editorialHTTPFail(w, ErrEditorialUnavailable, "")
	}
}

func (h *contentSurface) edit(w http.ResponseWriter, r *http.Request, view shell.Request, id string) {
	if !validEditorialID(id) {
		editorialHTTPFail(w, ErrEditorialNotFound, "")
		return
	}
	revision, err := h.service.Head(r.Context(), view.Principal, id)
	if err != nil {
		editorialHTTPFail(w, err, "")
		return
	}
	history, err := h.service.Revisions(r.Context(), view.Principal, id)
	if err != nil {
		editorialHTTPFail(w, err, "")
		return
	}
	body, err := trustedEditorialHTML(revision.BodyHTML)
	if err != nil {
		editorialHTTPFail(w, ErrEditorialUnavailable, "")
		return
	}
	publication := PublicationState{}
	if h.publication != nil {
		publication = h.publication.State(revision.ID, revision.Kind)
	}
	if err = view.Render(shell.Page{Title: (shell.Text{English: "Edit content", Persian: "ویرایش محتوا"}).In(view.Language), Template: contentEditTemplate, Data: contentEditView{Language: view.Language, Revision: revision, History: history, Body: body, Publication: publication, OperationID: newOperationID()}}); err != nil {
		editorialHTTPFail(w, ErrEditorialUnavailable, "")
	}
}

func (h *contentSurface) preview(w http.ResponseWriter, r *http.Request, view shell.Request, tail string) {
	parts := strings.Split(tail, "/")
	if len(parts) != 2 || !validEditorialID(parts[0]) {
		editorialHTTPFail(w, ErrEditorialNotFound, "")
		return
	}
	revisionNumber, err := parseRevision(parts[1])
	if err != nil {
		editorialHTTPFail(w, ErrEditorialNotFound, "")
		return
	}
	revision, err := h.service.Preview(r.Context(), view.Principal, parts[0], revisionNumber)
	if err != nil {
		editorialHTTPFail(w, err, "")
		return
	}
	appearance, err := h.service.Appearance(r.Context(), view.Principal)
	if err != nil {
		editorialHTTPFail(w, err, "")
		return
	}
	body, err := trustedEditorialHTML(revision.BodyHTML)
	if err != nil {
		editorialHTTPFail(w, ErrEditorialUnavailable, "")
		return
	}
	if err = view.Render(shell.Page{Title: (shell.Text{English: "Private preview", Persian: "پیش‌نمایش خصوصی"}).In(view.Language), Template: contentPreviewTemplate, Data: contentPreviewView{Language: view.Language, Revision: revision, Body: body, Appearance: appearance}}); err != nil {
		editorialHTTPFail(w, ErrEditorialUnavailable, "")
	}
}

func trustedEditorialHTML(body string) (template.HTML, error) {
	canonical, _, err := canonicalBody(body)
	if err != nil || canonical != body {
		return "", ErrEditorialUnavailable
	}
	return template.HTML(canonical), nil
}

func (h *contentSurface) create(w http.ResponseWriter, r *http.Request, view shell.Request) {
	var body struct {
		OperationID string `json:"operation_id"`
		Kind        string `json:"kind"`
		Title       string `json:"title"`
		BodyHTML    string `json:"body_html"`
	}
	if err := decodeEditorialJSON(w, r, &body); err != nil {
		editorialHTTPFail(w, err, body.OperationID)
		return
	}
	revision, err := h.service.Create(r.Context(), view.Principal, body.OperationID, ContentKind(body.Kind), body.Title, body.BodyHTML)
	if err != nil {
		editorialHTTPFail(w, err, body.OperationID)
		return
	}
	editorialJSON(w, map[string]string{"id": revision.ID, "revision": strconv.FormatInt(revision.Revision, 10), "operation_id": body.OperationID})
}

func (h *contentSurface) save(w http.ResponseWriter, r *http.Request, view shell.Request) {
	var body struct {
		OperationID string `json:"operation_id"`
		ID          string `json:"id"`
		Expected    string `json:"expected_head"`
		Title       string `json:"title"`
		BodyHTML    string `json:"body_html"`
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
	revision, err := h.service.Save(r.Context(), view.Principal, body.OperationID, body.ID, expected, body.Title, body.BodyHTML)
	if err != nil {
		editorialHTTPFail(w, err, body.OperationID)
		return
	}
	editorialJSON(w, map[string]string{"id": revision.ID, "revision": strconv.FormatInt(revision.Revision, 10), "operation_id": body.OperationID})
}

func (h *contentSurface) publicationIntent(w http.ResponseWriter, r *http.Request, view shell.Request, clear bool) {
	var body struct {
		OperationID         string `json:"operation_id"`
		ID                  string `json:"id"`
		Expected            string `json:"expected_head"`
		ExpectedPublication string `json:"expected_publication_version"`
		Revision            string `json:"revision"`
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
	expectedPublication, err := parseRevision(body.ExpectedPublication)
	if err != nil {
		editorialHTTPFail(w, ErrEditorialInvalid, body.OperationID)
		return
	}
	var revision ContentRevision
	if clear {
		revision, err = h.service.ClearPublicationIntent(r.Context(), view.Principal, body.OperationID, body.ID, expected, expectedPublication)
	} else {
		var selected int64
		selected, err = parseRevision(body.Revision)
		if err == nil {
			revision, err = h.service.SetPublicationIntent(r.Context(), view.Principal, body.OperationID, body.ID, expected, expectedPublication, selected)
		}
	}
	if err != nil {
		editorialHTTPFail(w, err, body.OperationID)
		return
	}
	editorialJSON(w, map[string]string{
		"id": revision.ID, "revision": strconv.FormatInt(revision.Revision, 10),
		"publication_intent_revision": strconv.FormatInt(revision.PublicationIntentRevision, 10),
		"publication_intent_version":  strconv.FormatInt(revision.PublicationIntentVersion, 10),
		"operation_id":                body.OperationID,
	})
}

func (h *contentSurface) operation(w http.ResponseWriter, r *http.Request, view shell.Request) {
	var body struct {
		OperationID string `json:"operation_id"`
	}
	if err := decodeEditorialJSON(w, r, &body); err != nil {
		editorialHTTPFail(w, err, body.OperationID)
		return
	}
	op, err := h.service.Operation(r.Context(), view.Principal, body.OperationID)
	if err == nil {
		editorialJSON(w, map[string]string{"operation_id": op.ID, "kind": op.Kind, "resource_id": op.ResourceID, "result_revision": strconv.FormatInt(op.ResultRevision, 10), "created_at": op.CreatedAt.Format("2006-01-02T15:04:05.999999Z07:00")})
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
		"operation_id": publication.ID, "kind": "publication.apply", "resource_id": publication.ContentID,
		"result_generation": publication.Generation, "route": publication.Route,
		"created_at": publication.CreatedAt.Format("2006-01-02T15:04:05.999999Z07:00"),
	})
}

func (h *contentSurface) publish(w http.ResponseWriter, r *http.Request, view shell.Request) {
	if h.publication == nil {
		editorialHTTPFail(w, ErrEditorialUnavailable, "")
		return
	}
	var body struct {
		OperationID        string `json:"operation_id"`
		ID                 string `json:"id"`
		ExpectedGeneration string `json:"expected_generation"`
		Route              string `json:"route"`
	}
	if err := decodeEditorialJSON(w, r, &body); err != nil {
		editorialHTTPFail(w, err, body.OperationID)
		return
	}
	result, err := h.publication.Apply(r.Context(), view.Principal, PublicationRequest{
		OperationID: body.OperationID, ExpectedGeneration: body.ExpectedGeneration,
		ContentID: body.ID, Route: body.Route,
	})
	if err != nil {
		editorialHTTPFail(w, err, body.OperationID)
		return
	}
	editorialJSON(w, map[string]string{
		"operation_id": body.OperationID, "generation": result.Generation,
		"id": result.ContentID, "route": result.Route,
	})
}


func decodeEditorialJSON(w http.ResponseWriter, r *http.Request, value any) error {
	if len(r.Header.Values("Content-Type")) != 1 || r.Header.Get("Content-Type") != "application/json" {
		return ErrEditorialInvalid
	}
	r.Body = http.MaxBytesReader(w, r.Body, 96<<10)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(value); err != nil {
		return ErrEditorialInvalid
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return ErrEditorialInvalid
	}
	return nil
}
func parseRevision(value string) (int64, error) {
	if value == "" || len(value) > 20 || value[0] == '+' || strings.TrimSpace(value) != value {
		return 0, ErrEditorialInvalid
	}
	v, err := strconv.ParseInt(value, 10, 64)
	if err != nil || v < 1 {
		return 0, ErrEditorialInvalid
	}
	return v, nil
}
func editorialJSON(w http.ResponseWriter, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_ = json.NewEncoder(w).Encode(value)
}
func editorialHTTPFail(w http.ResponseWriter, err error, operationID string) {
	status, code := http.StatusServiceUnavailable, "unavailable"
	switch {
	case errors.Is(err, achrix.ErrDenied):
		status, code = http.StatusForbidden, "permission_denied"
	case errors.Is(err, ErrEditorialInvalid):
		status, code = http.StatusBadRequest, "invalid_input"
	case errors.Is(err, ErrEditorialNotFound):
		status, code = http.StatusNotFound, "not_found"
	case errors.Is(err, ErrEditorialConflict):
		status, code = http.StatusConflict, "conflict"
	case errors.Is(err, ErrEditorialLimited):
		status, code = http.StatusTooManyRequests, "limited"
	case errors.Is(err, ErrEditorialUnknownOutcome):
		status, code = http.StatusServiceUnavailable, "unknown_outcome"
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(struct {
		Code        string `json:"code"`
		OperationID string `json:"operation_id,omitempty"`
	}{code, operationID})
}

var contentIndexTemplate = template.Must(template.New("content-index").Parse(`{{define "content"}}
<script src="/admin/content/editor.js" defer></script>
<section><h2>{{if eq .Language "fa"}}محتوای تازه{{else}}New content{{end}}</h2><p>{{if eq .Language "fa"}}ایجاد فقط یک پیش‌نویس خصوصی می‌سازد و چیزی را عمومی نمی‌کند.{{else}}Creation makes only a private draft and publishes nothing.{{end}}</p>
<form data-admin-form data-content-create action="/admin/content/create" method="post"><input type="hidden" name="operation_id" value="{{.OperationID}}"><input type="hidden" name="body_html" value="&lt;p&gt;&lt;br&gt;&lt;/p&gt;"><label>{{if eq .Language "fa"}}نوع{{else}}Type{{end}} <select name="kind" required><option value="post">{{if eq .Language "fa"}}نوشته{{else}}Post{{end}}</option><option value="page">{{if eq .Language "fa"}}برگه{{else}}Page{{end}}</option></select></label><label>{{if eq .Language "fa"}}عنوان{{else}}Title{{end}} <input name="title" maxlength="200" required dir="auto"></label><button type="submit">{{if eq .Language "fa"}}ایجاد پیش‌نویس{{else}}Create draft{{end}}</button></form></section>
<section><h2>{{if eq .Language "fa"}}محتوای ذخیره‌شده{{else}}Saved content{{end}}</h2>{{if .Items}}<table><thead><tr><th>{{if eq .Language "fa"}}عنوان{{else}}Title{{end}}</th><th>{{if eq .Language "fa"}}نوع{{else}}Type{{end}}</th><th>{{if eq .Language "fa"}}نسخه{{else}}Revision{{end}}</th><th>{{if eq .Language "fa"}}قصد انتشار{{else}}Publication intent{{end}}</th></tr></thead><tbody>{{range .Items}}<tr><td><a href="/admin/content/edit/{{.ID}}"><bdi dir="auto">{{.Title}}</bdi></a><br><code>{{.ID}}</code></td><td>{{.Kind}}</td><td>{{.HeadRevision}}</td><td>{{if .PublicationIntentRevision}}{{.PublicationIntentRevision}}{{else}}—{{end}}</td></tr>{{end}}</tbody></table>{{else}}<p>{{if eq .Language "fa"}}هنوز محتوایی ذخیره نشده است.{{else}}No content has been saved yet.{{end}}</p>{{end}}</section>
{{template "operation-lookup" .}}{{end}}
{{define "operation-lookup"}}<section><h2>{{if eq .Language "fa"}}بررسی نتیجهٔ عملیات{{else}}Inspect mutation outcome{{end}}</h2><p>{{if eq .Language "fa"}}اگر پاسخ تغییر از دست رفت، همان شناسه را بررسی کنید و تغییر را دوباره نفرستید.{{else}}If a mutation response was lost, inspect the same ID and do not submit the change again.{{end}}</p><form data-admin-form data-read-only data-operation-lookup action="/admin/content/operation" method="post"><label>{{if eq .Language "fa"}}شناسهٔ عملیات{{else}}Operation ID{{end}} <input name="operation_id" data-operation-lookup-input dir="ltr" maxlength="36" required></label><button type="submit">{{if eq .Language "fa"}}بررسی{{else}}Inspect{{end}}</button></form></section>{{end}}`))

var contentEditTemplate = template.Must(template.New("content-edit").Parse(`{{define "content"}}
<link rel="stylesheet" href="/admin/content/editor.css"><script src="/admin/content/editor.js" defer></script><p><a href="/admin/content">← {{if eq .Language "fa"}}فهرست محتوا{{else}}Content list{{end}}</a></p><p>ID: <code>{{.Revision.ID}}</code> · {{if eq .Language "fa"}}نسخه{{else}}revision{{end}} <output data-current-revision>{{.Revision.Revision}}</output></p>
<form data-admin-form data-content-save action="/admin/content/save" method="post"><input type="hidden" name="operation_id" value="{{.OperationID}}" data-operation-field><input type="hidden" name="id" value="{{.Revision.ID}}"><input type="hidden" name="expected_head" value="{{.Revision.Revision}}" data-head-field><textarea name="body_html" data-editor-source hidden>{{.Revision.BodyHTML}}</textarea><label>{{if eq .Language "fa"}}عنوان{{else}}Title{{end}} <input name="title" value="{{.Revision.Title}}" maxlength="200" required dir="auto"></label>
<fieldset><legend>{{if eq .Language "fa"}}قالب‌بندی{{else}}Formatting{{end}}</legend><div class="editor-toolbar" role="toolbar"><button type="button" data-wrap="strong"><strong>B</strong></button><button type="button" data-wrap="em"><em>I</em></button><button type="button" data-block="p">P</button><button type="button" data-block="h2">H2</button><button type="button" data-block="h3">H3</button></div><label>{{if eq .Language "fa"}}پیوند HTTPS یا داخلی{{else}}HTTPS or site-relative link{{end}} <input data-link-url dir="ltr"></label><button type="button" data-add-link>{{if eq .Language "fa"}}پیوند دادن انتخاب{{else}}Link selection{{end}}</button><label>Media ID <input data-media-id dir="ltr" maxlength="26" pattern="[A-Z2-7]{26}"></label><label>{{if eq .Language "fa"}}توضیح Media{{else}}Media description{{end}} <input data-media-caption maxlength="300" dir="auto"></label><button type="button" data-add-media>{{if eq .Language "fa"}}افزودن ارجاع Media{{else}}Insert Media reference{{end}}</button></fieldset>
<div data-editor contenteditable="true" role="textbox" aria-multiline="true" aria-label="{{if eq .Language "fa"}}متن محتوا{{else}}Content body{{end}}" dir="auto">{{.Body}}</div><button type="submit">{{if eq .Language "fa"}}ذخیرهٔ نسخهٔ تازه{{else}}Save new revision{{end}}</button></form>
<p><a data-preview-link href="/admin/content/preview/{{.Revision.ID}}/{{.Revision.Revision}}">{{if eq .Language "fa"}}پیش‌نمایش خصوصی همین نسخه{{else}}Private preview of this revision{{end}}</a></p>
<section><h2>{{if eq .Language "fa"}}قصد انتشار{{else}}Publication intent{{end}}</h2><p>{{if eq .Language "fa"}}این عمل چیزی را عمومی نمی‌کند.{{else}}This action does not make anything public.{{end}}</p><form data-admin-form data-publication-form action="/admin/content/publication-intent" method="post"><input type="hidden" name="operation_id" value="{{.OperationID}}" data-operation-field><input type="hidden" name="id" value="{{.Revision.ID}}"><input type="hidden" name="expected_head" value="{{.Revision.Revision}}" data-head-field><input type="hidden" name="expected_publication_version" value="{{.Revision.PublicationIntentVersion}}" data-publication-version-field><label>{{if eq .Language "fa"}}نسخه{{else}}Revision{{end}} <input name="revision" value="{{.Revision.Revision}}" data-revision-field inputmode="numeric" pattern="[0-9]+" required></label><button type="submit">{{if eq .Language "fa"}}ثبت قصد انتشار{{else}}Set publication intent{{end}}</button></form>{{if .Revision.PublicationIntentRevision}}<p>{{if eq .Language "fa"}}نسخهٔ علامت‌خورده{{else}}Marked revision{{end}}: <output data-publication-intent-output>{{.Revision.PublicationIntentRevision}}</output></p><form data-admin-form data-publication-clear action="/admin/content/publication-intent-clear" method="post"><input type="hidden" name="operation_id" value="{{.OperationID}}" data-operation-field><input type="hidden" name="id" value="{{.Revision.ID}}"><input type="hidden" name="expected_head" value="{{.Revision.Revision}}" data-head-field><input type="hidden" name="expected_publication_version" value="{{.Revision.PublicationIntentVersion}}" data-publication-version-field><input type="hidden" name="revision" value="{{.Revision.Revision}}"><button type="submit">{{if eq .Language "fa"}}پاک کردن قصد انتشار{{else}}Clear publication intent{{end}}</button></form>{{end}}</section>
{{if .Publication.Enabled}}<section><h2>{{if eq .Language "fa"}}انتشار عمومی{{else}}Public publication{{end}}</h2>
<p>{{if eq .Language "fa"}}«قصد انتشار» فقط نسخه را انتخاب می‌کند. این عمل یک نسل ایستای یکپارچه می‌سازد و پس از بررسی freshness آن را فعال می‌کند.{{else}}Publication intent only selects a revision. This action builds one coherent static generation and activates it only after freshness checks.{{end}}</p>
<p>{{if eq .Language "fa"}}نسل فعال{{else}}Active generation{{end}}: <output data-publication-generation-output>{{if .Publication.Generation}}{{.Publication.Generation}}{{else}}—{{end}}</output>{{if .Publication.ActiveRevision}} · {{if eq .Language "fa"}}نسخه عمومی{{else}}public revision{{end}} {{.Publication.ActiveRevision}}{{end}}</p>
<form data-admin-form data-publication-apply action="/admin/content/publish" method="post">
<input type="hidden" name="operation_id" value="{{.OperationID}}" data-operation-field>
<input type="hidden" name="id" value="{{.Revision.ID}}">
<input type="hidden" name="expected_generation" value="{{.Publication.Generation}}" data-publication-generation>
<label>{{if eq .Language "fa"}}مسیر عمومی{{else}}Public route{{end}} <input name="route" value="{{.Publication.DesiredRoute}}" maxlength="512" dir="ltr" required></label>
<button type="submit">{{if eq .Language "fa"}}اعمال انتشار یکپارچه{{else}}Apply coherent publication{{end}}</button>
</form></section>{{end}}
<section><h2>{{if eq .Language "fa"}}نسخه‌های ذخیره‌شده{{else}}Saved revisions{{end}}</h2><ol>{{range .History}}<li><a href="/admin/content/preview/{{$.Revision.ID}}/{{.Revision}}">{{if eq $.Language "fa"}}نسخه{{else}}Revision{{end}} {{.Revision}}</a> — <bdi dir="auto">{{.Title}}</bdi> · <code>{{.Actor}}</code></li>{{end}}</ol></section>
<section><h2>{{if eq .Language "fa"}}بررسی نتیجهٔ عملیات{{else}}Inspect mutation outcome{{end}}</h2><form data-admin-form data-read-only data-operation-lookup action="/admin/content/operation" method="post"><label>Operation ID <input name="operation_id" data-operation-lookup-input dir="ltr" maxlength="36" required></label><button type="submit">{{if eq .Language "fa"}}بررسی{{else}}Inspect{{end}}</button></form></section>{{end}}`))

var contentPreviewTemplate = template.Must(template.New("content-preview").Parse(`{{define "content"}}<link rel="stylesheet" href="/admin/content/editor.css"><p><a href="/admin/content/edit/{{.Revision.ID}}">← {{if eq .Language "fa"}}بازگشت به ویرایش{{else}}Back to edit{{end}}</a></p><p><strong>{{if eq .Language "fa"}}پیش‌نمایش خصوصی{{else}}Private preview{{end}}</strong> · {{if eq .Language "fa"}}نسخه{{else}}revision{{end}} {{.Revision.Revision}}</p><article class="site-preview" data-theme="{{.Appearance.Theme}}" lang="{{.Appearance.SiteLanguage}}" dir="{{if eq .Appearance.SiteLanguage "fa"}}rtl{{else}}ltr{{end}}"><header>{{if .Appearance.HeaderShowTitle}}<h1><bdi dir="auto">{{.Appearance.SiteTitle}}</bdi></h1>{{end}}{{if .Appearance.HeaderTagline}}<p dir="auto">{{.Appearance.HeaderTagline}}</p>{{end}}</header><h2 dir="auto">{{.Revision.Title}}</h2><div class="preview-body">{{.Body}}</div>{{if .Appearance.FooterText}}<footer dir="auto">{{.Appearance.FooterText}}</footer>{{end}}</article><p>{{if eq .Language "fa"}}ارجاع‌های Media عمداً فایل خصوصی را inline نمی‌کنند.{{else}}Media references intentionally do not inline private originals.{{end}}</p>{{end}}`))

const contentEditorCSS = `[data-editor]{min-height:16rem;border:1px solid GrayText;padding:1rem;max-width:52rem;overflow-wrap:anywhere}[data-editor]:focus{outline:3px solid #3478db;outline-offset:3px}.editor-toolbar{display:flex;flex-wrap:wrap;gap:.4rem}.site-preview{max-width:48rem;border:1px solid GrayText;padding:1.5rem}.site-preview[data-theme="dark"]{color-scheme:dark;background:#171717;color:#f2f2f2}.site-preview[data-theme="light"]{color-scheme:light;background:#fff;color:#111}.preview-body{overflow-wrap:anywhere}[data-editor] figure{border:1px dashed GrayText;padding:.7rem}`

const contentEditorJS = `
"use strict";
(() => {
  const fa = document.documentElement.lang === "fa";
  const status = document.getElementById("status");
  const text = (en, faText) => fa ? faText : en;

  function uuid() {
    if (crypto.randomUUID) return crypto.randomUUID();
    const bytes = new Uint8Array(16);
    crypto.getRandomValues(bytes);
    bytes[6] = (bytes[6] & 15) | 64;
    bytes[8] = (bytes[8] & 63) | 128;
    const hex = [...bytes].map(value => value.toString(16).padStart(2, "0")).join("");
    return hex.slice(0, 8) + "-" + hex.slice(8, 12) + "-" + hex.slice(12, 16) + "-" + hex.slice(16, 20) + "-" + hex.slice(20);
  }

  function rotate(form) {
    const field = form.querySelector("[data-operation-field],input[name=operation_id]");
    if (field) field.value = uuid();
  }

  function safeLink(value) {
    if (!value || /[\\\u0000-\u001f\u007f-\u009f\u061c\u200e\u200f\u202a-\u202e\u2066-\u2069]/u.test(value)) return false;
    if (value.startsWith("/")) return !value.startsWith("//");
    try {
      const parsed = new URL(value);
      return parsed.protocol === "https:" && !parsed.username && !parsed.password;
    } catch (_) {
      return false;
    }
  }

  function insertPlainText(editor, value) {
    const selection = getSelection();
    if (!selection || selection.rangeCount !== 1) return;
    const range = selection.getRangeAt(0);
    if (!editor.contains(range.commonAncestorContainer)) return;
    range.deleteContents();
    const fragment = document.createDocumentFragment();
    let last = null;
    value.replace(/\r\n?/g, "\n").split("\n").forEach((line, index) => {
      if (index > 0) {
        last = document.createElement("br");
        fragment.append(last);
      }
      const node = document.createTextNode(line);
      fragment.append(node);
      last = node;
    });
    range.insertNode(fragment);
    if (last) {
      range.setStartAfter(last);
      range.collapse(true);
      selection.removeAllRanges();
      selection.addRange(range);
    }
  }

  document.querySelectorAll("[data-content-save],[data-publication-form],[data-publication-clear],[data-publication-apply]").forEach((form, index) => {
    if (index > 0) rotate(form);
  });

  document.querySelectorAll("form[data-admin-form]").forEach(form => form.addEventListener("admin:failure", event => {
    if (!event.detail || !event.detail.unknown) return;
    const operation = form.querySelector("input[name=operation_id]");
    const lookup = document.querySelector("[data-operation-lookup-input]");
    if (operation && lookup) lookup.value = operation.value;
    if (operation && status) {
      status.textContent = text(
        "Outcome unknown. Do not submit again. Inspect operation " + operation.value + ".",
        "نتیجه نامعلوم است. دوباره ارسال نکنید. عملیات " + operation.value + " را بررسی کنید."
      );
    }
  }));

  const create = document.querySelector("[data-content-create]");
  if (create) create.addEventListener("admin:success", event => {
    if (event.detail && event.detail.id) location.assign("/admin/content/edit/" + event.detail.id);
  });

  const editor = document.querySelector("[data-editor]");
  const source = document.querySelector("[data-editor-source]");
  const save = document.querySelector("[data-content-save]");
  function sync() {
    if (editor && source) source.value = editor.innerHTML;
  }

  if (editor) {
    editor.addEventListener("input", sync);
    editor.addEventListener("paste", event => {
      event.preventDefault();
      insertPlainText(editor, event.clipboardData ? event.clipboardData.getData("text/plain") : "");
      sync();
    });
    editor.addEventListener("drop", event => {
      event.preventDefault();
      if (status) status.textContent = text(
        "Drop is disabled. Upload files in File library and insert the stable Media ID here.",
        "رها کردن فایل در ویرایشگر غیرفعال است. فایل را در کتابخانهٔ فایل‌ها بارگذاری کنید و شناسهٔ پایدار Media را اینجا درج کنید."
      );
    });
  }

  document.querySelectorAll("[data-wrap]").forEach(button => button.addEventListener("click", () => {
    if (!editor) return;
    const selection = getSelection();
    if (!selection || selection.rangeCount !== 1 || selection.isCollapsed) return;
    const range = selection.getRangeAt(0);
    if (!editor.contains(range.commonAncestorContainer)) return;
    const element = document.createElement(button.dataset.wrap);
    element.append(range.extractContents());
    range.insertNode(element);
    sync();
    editor.focus();
  }));

  document.querySelectorAll("[data-block]").forEach(button => button.addEventListener("click", () => {
    if (!editor) return;
    const selection = getSelection();
    if (!selection || !selection.anchorNode || !editor.contains(selection.anchorNode)) return;
    let node = selection.anchorNode.nodeType === 1 ? selection.anchorNode : selection.anchorNode.parentElement;
    const block = node.closest("p,h2,h3,blockquote");
    if (!block || !editor.contains(block)) return;
    const replacement = document.createElement(button.dataset.block);
    replacement.setAttribute("dir", "auto");
    while (block.firstChild) replacement.append(block.firstChild);
    block.replaceWith(replacement);
    sync();
    editor.focus();
  }));

  const linkButton = document.querySelector("[data-add-link]");
  if (linkButton) linkButton.addEventListener("click", () => {
    if (!editor) return;
    const href = document.querySelector("[data-link-url]")?.value || "";
    if (!safeLink(href)) {
      if (status) status.textContent = text("Use an HTTPS URL or a site-relative path.", "از نشانی HTTPS یا مسیر داخلی سایت استفاده کنید.");
      return;
    }
    const selection = getSelection();
    if (!selection || selection.rangeCount !== 1 || selection.isCollapsed) return;
    const range = selection.getRangeAt(0);
    if (!editor.contains(range.commonAncestorContainer)) return;
    const link = document.createElement("a");
    link.setAttribute("href", href);
    link.append(range.extractContents());
    range.insertNode(link);
    sync();
    editor.focus();
  });

  const mediaButton = document.querySelector("[data-add-media]");
  if (mediaButton) mediaButton.addEventListener("click", () => {
    if (!editor) return;
    const id = document.querySelector("[data-media-id]")?.value || "";
    const caption = document.querySelector("[data-media-caption]")?.value || "";
    if (!/^[A-Z2-7]{26}$/.test(id)) {
      if (status) status.textContent = text("Enter a valid Media asset ID.", "شناسهٔ معتبر Media را وارد کنید.");
      return;
    }
    const figure = document.createElement("figure");
    const figcaption = document.createElement("figcaption");
    figure.dataset.mediaId = id;
    figcaption.setAttribute("dir", "auto");
    figcaption.textContent = caption;
    figure.append(figcaption);
    editor.append(figure);
    sync();
    editor.focus();
  });

  if (save) save.addEventListener("admin:success", event => {
    const revision = event.detail?.revision;
    if (!revision) return;
    document.querySelectorAll("[data-head-field]").forEach(value => value.value = revision);
    document.querySelectorAll("[data-revision-field]").forEach(value => value.value = revision);
    document.querySelectorAll("[data-current-revision]").forEach(value => value.textContent = revision);
    const preview = document.querySelector("[data-preview-link]");
    const id = save.querySelector("input[name=id]")?.value;
    if (preview && id) preview.href = "/admin/content/preview/" + id + "/" + revision;
    rotate(save);
    sync();
  });

  document.querySelectorAll("[data-publication-form],[data-publication-clear]").forEach(form => form.addEventListener("admin:success", event => {
    const value = event.detail?.publication_intent_revision;
    const version = event.detail?.publication_intent_version;
    const output = document.querySelector("[data-publication-intent-output]");
    if (output && value !== undefined) output.textContent = value === "0" ? "—" : value;
    if (version) document.querySelectorAll("[data-publication-version-field]").forEach(field => field.value = version);
    rotate(form);
  }));

  document.querySelectorAll("[data-publication-apply]").forEach(form => form.addEventListener("admin:success", event => {
    const generation = event.detail?.generation;
    if (!generation) return;
    document.querySelectorAll("[data-publication-generation]").forEach(field => field.value = generation);
    const output = document.querySelector("[data-publication-generation-output]");
    if (output) output.textContent = generation;
    rotate(form);
  }));

  document.querySelectorAll("[data-operation-lookup]").forEach(form => form.addEventListener("admin:success", event => {
    const detail = event.detail || {};
    if (status) status.textContent = text(
      "Operation " + (detail.operation_id || "") + " committed as " + (detail.kind || "") + " on " + (detail.resource_id || "") + " revision " + (detail.result_revision || "0") + ".",
      "عملیات " + (detail.operation_id || "") + " ثبت شده است."
    );
  }));

  sync();
})();
`
