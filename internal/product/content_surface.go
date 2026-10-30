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
	"github.com/AChWorks/achrix/admin/localize"
)

type contentSurface struct {
	service        draftService
	intentService  *EditorialService
	routes         contentRoutes
	publication    *PublicationService
	contentLocales []localize.Locale
}

func newContentSurface(service *EditorialService, publication *PublicationService, languages ...localize.Locale) (shell.Surface, error) {
	if service == nil {
		return shell.Surface{}, shell.ErrConfiguration
	}
	h := &contentSurface{service: service, intentService: service, publication: publication, contentLocales: append([]localize.Locale(nil), languages...)}
	return shell.Surface{ID: "content", Title: shell.Text{Domain: "rixa", Key: "content"}, Capability: CapabilityContentList, Target: ContentCollectionTarget, Handler: h.serve}, nil
}

type contentIndexView struct {
	Routes      contentRoutes
	DraftOnly   bool
	NextCursor  string
	Locale      *localize.Localizer
	Items       []ContentSummary
	Count       int64
	OperationID string
}
type contentEditView struct {
	Routes      contentRoutes
	DraftOnly   bool
	NextCursor  string
	Locale      *localize.Localizer
	Revision    ContentRevision
	History     []RevisionSummary
	Body        template.HTML
	Publication PublicationState
	OperationID string
}
type contentPreviewView struct {
	Routes           contentRoutes
	Locale           *localize.Localizer
	Revision         ContentRevision
	Body             template.HTML
	Appearance       Appearance
	ContentDirection string
}

func (h *contentSurface) serve(w http.ResponseWriter, r *http.Request, view shell.Request) {
	if r.Method == http.MethodGet {
		switch {
		case r.URL.Path == "/":
			h.index(w, r, view, "")
		case strings.HasPrefix(r.URL.Path, "/page/"):
			cursor := strings.TrimPrefix(r.URL.Path, "/page/")
			if !contentCursorPath.MatchString(cursor) {
				editorialHTTPFail(w, ErrEditorialNotFound, "")
				return
			}
			h.index(w, r, view, cursor)
		case strings.HasPrefix(r.URL.Path, "/history/"):
			h.history(w, r, view, strings.TrimPrefix(r.URL.Path, "/history/"))
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
		if h.intentService == nil {
			editorialHTTPFail(w, ErrEditorialNotFound, "")
			return
		}
		h.publicationIntent(w, r, view, false)
	case "/publication-intent-clear":
		if h.intentService == nil {
			editorialHTTPFail(w, ErrEditorialNotFound, "")
			return
		}
		h.publicationIntent(w, r, view, true)
	case "/operation":
		h.operation(w, r, view)
	case "/publish":
		if h.publication == nil {
			editorialHTTPFail(w, ErrEditorialNotFound, "")
			return
		}
		h.publish(w, r, view)
	default:
		editorialHTTPFail(w, ErrEditorialNotFound, "")
	}
}

func (h *contentSurface) index(w http.ResponseWriter, r *http.Request, view shell.Request, cursor string) {
	page, err := h.service.ListPage(r.Context(), view.Principal, cursor, 50)
	if err != nil {
		editorialHTTPFail(w, err, "")
		return
	}
	if err = renderProductPage(view, "content", shell.Page{Template: contentIndexTemplate, Data: contentIndexView{Locale: view.Localizer, Routes: h.routes, DraftOnly: h.intentService == nil, Items: page.Items, NextCursor: page.NextCursor, Count: int64(len(page.Items)), OperationID: newOperationID()}}); err != nil {
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
	history, err := h.service.RevisionsPage(r.Context(), view.Principal, id, "", 25)
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
	if err = renderProductPage(view, "edit_content", shell.Page{Template: contentEditTemplate, Data: contentEditView{Locale: view.Localizer, Revision: revision, Routes: h.routes, DraftOnly: h.intentService == nil, History: history.Items, NextCursor: history.NextCursor, Body: body, Publication: publication, OperationID: newOperationID()}}); err != nil {
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
	body, err := trustedPreviewHTML(revision.BodyHTML, h.routes.Origin)
	if err != nil {
		editorialHTTPFail(w, ErrEditorialUnavailable, "")
		return
	}
	direction := ""
	for _, language := range h.contentLocales {
		if language.Tag == appearance.SiteLanguage {
			direction = language.Direction
			break
		}
	}
	if direction == "" {
		editorialHTTPFail(w, ErrEditorialUnavailable, "")
		return
	}
	if err = renderProductPage(view, "private_preview", shell.Page{Template: contentPreviewTemplate, Data: contentPreviewView{Routes: h.routes, Locale: view.Localizer, Revision: revision, Body: body, Appearance: appearance, ContentDirection: direction}}); err != nil {
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
	editorialJSON(w, map[string]string{"site_id": h.routes.SiteID, "origin": h.routes.Origin, "id": revision.ID, "revision": strconv.FormatInt(revision.Revision, 10), "operation_id": body.OperationID})
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
	editorialJSON(w, map[string]string{"site_id": h.routes.SiteID, "origin": h.routes.Origin, "id": revision.ID, "revision": strconv.FormatInt(revision.Revision, 10), "operation_id": body.OperationID})
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
		revision, err = h.intentService.ClearPublicationIntent(r.Context(), view.Principal, body.OperationID, body.ID, expected, expectedPublication)
	} else {
		var selected int64
		selected, err = parseRevision(body.Revision)
		if err == nil {
			revision, err = h.intentService.SetPublicationIntent(r.Context(), view.Principal, body.OperationID, body.ID, expected, expectedPublication, selected)
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
		editorialJSON(w, map[string]string{"site_id": h.routes.SiteID, "origin": h.routes.Origin, "operation_id": op.ID, "kind": op.Kind, "resource_id": op.ResourceID, "result_revision": strconv.FormatInt(op.ResultRevision, 10), "created_at": op.CreatedAt.Format("2006-01-02T15:04:05.999999Z07:00")})
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
<div data-content-workspace data-base="{{.Routes.Base}}" data-site-id="{{.Routes.SiteID}}" data-origin="{{.Routes.Origin}}">{{template "content-scope" .}}<link rel="stylesheet" href="{{.Routes.Assets}}/editor.css"><script src="{{.Routes.Assets}}/editor.js" defer></script>
<section><h2>{{.Locale.Text "rixa" "new_content" nil}}</h2><p>{{.Locale.Text "rixa" "draft_creation_notice" nil}}</p>
<form data-draft-form data-content-create action="{{.Routes.Base}}/create" method="post"><input type="hidden" name="operation_id" value="{{.OperationID}}"><input type="hidden" name="body_html" value="&lt;p&gt;&lt;br&gt;&lt;/p&gt;"><label>{{.Locale.Text "rixa" "type" nil}} <select name="kind" required><option value="post">{{.Locale.Text "rixa" "post" nil}}</option><option value="page">{{.Locale.Text "rixa" "page" nil}}</option></select></label><label>{{.Locale.Text "rixa" "title" nil}} <input name="title" maxlength="200" required dir="auto"></label><button type="submit">{{.Locale.Text "rixa" "create_draft" nil}}</button></form></section>
<section><h2>{{.Locale.Text "rixa" "saved_content" nil}}</h2><p>{{.Locale.Plural "rixa" "content_page_count" .Count nil}}</p>{{if .Items}}<table><thead><tr><th>{{.Locale.Text "rixa" "title" nil}}</th><th>{{.Locale.Text "rixa" "type" nil}}</th><th>{{.Locale.Text "rixa" "revision" nil}}</th>{{if not .DraftOnly}}<th>{{.Locale.Text "rixa" "publication_intent" nil}}</th>{{end}}</tr></thead><tbody>{{range .Items}}<tr><td><a href="{{$.Routes.Base}}/edit/{{.ID}}"><bdi dir="auto">{{.Title}}</bdi></a><br><code>{{.ID}}</code></td><td>{{if eq .Kind "post"}}{{$.Locale.Text "rixa" "post" nil}}{{else}}{{$.Locale.Text "rixa" "page" nil}}{{end}}</td><td>{{.HeadRevision}}</td>{{if not $.DraftOnly}}<td>{{if .PublicationIntentRevision}}{{.PublicationIntentRevision}}{{else}}—{{end}}</td>{{end}}</tr>{{end}}</tbody></table>{{else}}<p>{{.Locale.Text "rixa" "no_content" nil}}</p>{{end}}</section>
{{if .NextCursor}}<a href="{{.Routes.Base}}/page/{{.NextCursor}}">{{.Locale.Text "rixa" "next_content_page" nil}}</a>{{end}} <a href="{{.Routes.Base}}/">{{.Locale.Text "rixa" "first_content_page" nil}}</a>{{template "draft-recovery" .}}{{template "operation-lookup" .}}</div>{{end}}
{{define "operation-lookup"}}<section><h2>{{.Locale.Text "rixa" "inspect_mutation_outcome" nil}}</h2><p>{{.Locale.Text "rixa" "unknown_mutation_notice" nil}}</p><form data-admin-form data-read-only data-operation-lookup action="{{.Routes.Base}}/operation" method="post"><label>{{.Locale.Text "rixa" "operation_id" nil}} <input name="operation_id" data-operation-lookup-input dir="ltr" maxlength="36" required></label><button type="submit">{{.Locale.Text "rixa" "inspect" nil}}</button></form></section>{{end}}` + contentScopeTemplates))

var contentEditTemplate = template.Must(template.New("content-edit").Parse(`{{define "content"}}
<div data-content-workspace data-base="{{.Routes.Base}}" data-site-id="{{.Routes.SiteID}}" data-origin="{{.Routes.Origin}}">{{template "content-scope" .}}<link rel="stylesheet" href="{{.Routes.Assets}}/editor.css"><script src="{{.Routes.Assets}}/editor.js" defer></script><p><a href="{{.Routes.Base}}">{{.Locale.Text "rixa" "content_list" nil}}</a></p><p>{{.Locale.Text "rixa" "id" nil}}: <code dir="ltr">{{.Revision.ID}}</code> · {{.Locale.Text "rixa" "revision" nil}} <output data-current-revision>{{.Revision.Revision}}</output></p>
<p>{{.Locale.Text "rixa" "save_source_notice" nil}}</p><form data-draft-form data-content-save action="{{.Routes.Base}}/save" method="post"><input type="hidden" name="operation_id" value="{{.OperationID}}" data-operation-field><input type="hidden" name="id" value="{{.Revision.ID}}"><input type="hidden" name="expected_head" value="{{.Revision.Revision}}" data-head-field><textarea name="body_html" data-editor-source hidden>{{.Revision.BodyHTML}}</textarea><label>{{.Locale.Text "rixa" "title" nil}} <input name="title" value="{{.Revision.Title}}" maxlength="200" required dir="auto"></label>
<fieldset><legend>{{.Locale.Text "rixa" "formatting" nil}}</legend><div class="editor-toolbar" role="toolbar"><button type="button" data-wrap="strong" aria-label="{{.Locale.Text "rixa" "bold" nil}}"><strong>B</strong></button><button type="button" data-wrap="em" aria-label="{{.Locale.Text "rixa" "italic" nil}}"><em>I</em></button><button type="button" data-block="p" aria-label="{{.Locale.Text "rixa" "paragraph" nil}}">P</button><button type="button" data-block="h2" aria-label="{{.Locale.Text "rixa" "heading_2" nil}}">H2</button><button type="button" data-block="h3" aria-label="{{.Locale.Text "rixa" "heading_3" nil}}">H3</button></div><label>{{.Locale.Text "rixa" "link_url" nil}} <input data-link-url dir="ltr"></label><button type="button" data-add-link>{{.Locale.Text "rixa" "link_selection" nil}}</button><label>{{.Locale.Text "rixa" "media_id" nil}} <input data-media-id dir="ltr" maxlength="26" pattern="[A-Z2-7]{26}"></label><label>{{.Locale.Text "rixa" "media_description" nil}} <input data-media-caption maxlength="300" dir="auto"></label><button type="button" data-add-media>{{.Locale.Text "rixa" "insert_media_reference" nil}}</button></fieldset>
<div data-editor contenteditable="true" role="textbox" aria-multiline="true" aria-label="{{.Locale.Text "rixa" "content_body" nil}}" dir="auto">{{.Body}}</div><button type="submit">{{.Locale.Text "rixa" "save_revision" nil}}</button></form>
<p><a data-preview-link href="{{.Routes.Base}}/preview/{{.Revision.ID}}/{{.Revision.Revision}}">{{.Locale.Text "rixa" "preview_revision" nil}}</a></p>
{{if not .DraftOnly}}<section><h2>{{.Locale.Text "rixa" "publication_intent" nil}}</h2><p>{{.Locale.Text "rixa" "publication_intent_notice" nil}}</p><form data-admin-form data-publication-form action="{{.Routes.Base}}/publication-intent" method="post"><input type="hidden" name="operation_id" value="{{.OperationID}}" data-operation-field><input type="hidden" name="id" value="{{.Revision.ID}}"><input type="hidden" name="expected_head" value="{{.Revision.Revision}}" data-head-field><input type="hidden" name="expected_publication_version" value="{{.Revision.PublicationIntentVersion}}" data-publication-version-field><label>{{.Locale.Text "rixa" "revision" nil}} <input name="revision" value="{{.Revision.Revision}}" data-revision-field inputmode="numeric" pattern="[0-9]+" required></label><button type="submit">{{.Locale.Text "rixa" "set_publication_intent" nil}}</button></form>{{if .Revision.PublicationIntentRevision}}<p>{{.Locale.Text "rixa" "marked_revision" nil}}: <output data-publication-intent-output>{{.Revision.PublicationIntentRevision}}</output></p><form data-admin-form data-publication-clear action="{{.Routes.Base}}/publication-intent-clear" method="post"><input type="hidden" name="operation_id" value="{{.OperationID}}" data-operation-field><input type="hidden" name="id" value="{{.Revision.ID}}"><input type="hidden" name="expected_head" value="{{.Revision.Revision}}" data-head-field><input type="hidden" name="expected_publication_version" value="{{.Revision.PublicationIntentVersion}}" data-publication-version-field><input type="hidden" name="revision" value="{{.Revision.Revision}}"><button type="submit">{{.Locale.Text "rixa" "clear_publication_intent" nil}}</button></form>{{end}}</section>
{{end}}{{if .Publication.Enabled}}<section><h2>{{.Locale.Text "rixa" "public_publication" nil}}</h2>
<p>{{.Locale.Text "rixa" "publication_apply_notice" nil}}</p>
<p>{{.Locale.Text "rixa" "active_generation" nil}}: <output data-publication-generation-output>{{if .Publication.Generation}}{{.Publication.Generation}}{{else}}—{{end}}</output>{{if .Publication.ActiveRevision}} · {{.Locale.Text "rixa" "public_revision" nil}} {{.Publication.ActiveRevision}}{{end}}</p>
<form data-admin-form data-publication-apply action="{{.Routes.Base}}/publish" method="post">
<input type="hidden" name="operation_id" value="{{.OperationID}}" data-operation-field>
<input type="hidden" name="id" value="{{.Revision.ID}}">
<input type="hidden" name="expected_generation" value="{{.Publication.Generation}}" data-publication-generation>
<label>{{.Locale.Text "rixa" "public_route" nil}} <input name="route" value="{{.Publication.DesiredRoute}}" maxlength="512" dir="ltr" required></label>
<button type="submit">{{.Locale.Text "rixa" "apply_publication" nil}}</button>
</form></section>{{end}}
<section><h2>{{.Locale.Text "rixa" "saved_revisions" nil}}</h2><ol>{{range .History}}<li><a href="{{$.Routes.Base}}/preview/{{$.Revision.ID}}/{{.Revision}}">{{$.Locale.Text "rixa" "revision" nil}} {{.Revision}}</a> — <bdi dir="auto">{{.Title}}</bdi> · <code>{{.Actor}}</code></li>{{end}}</ol>{{if .NextCursor}}<a href="{{.Routes.Base}}/history/{{.Revision.ID}}/before/{{.NextCursor}}">{{.Locale.Text "rixa" "older_revisions" nil}}</a>{{end}}</section>
{{template "draft-recovery" .}}<section><h2>{{.Locale.Text "rixa" "inspect_mutation_outcome" nil}}</h2><form data-admin-form data-read-only data-operation-lookup action="{{.Routes.Base}}/operation" method="post"><label>{{.Locale.Text "rixa" "operation_id" nil}} <input name="operation_id" data-operation-lookup-input dir="ltr" maxlength="36" required></label><button type="submit">{{.Locale.Text "rixa" "inspect" nil}}</button></form></section></div>{{end}}` + contentScopeTemplates))

var contentPreviewTemplate = template.Must(template.New("content-preview").Parse(`{{define "content"}}{{template "content-scope" .}}<link rel="stylesheet" href="{{.Routes.Assets}}/editor.css"><p><a href="{{.Routes.Base}}/edit/{{.Revision.ID}}">{{.Locale.Text "rixa" "back_to_edit" nil}}</a></p><p><strong>{{.Locale.Text "rixa" "private_preview" nil}}</strong> · {{.Locale.Text "rixa" "revision" nil}} {{.Revision.Revision}}</p><article class="site-preview" data-theme="{{.Appearance.Theme}}" lang="{{.Appearance.SiteLanguage}}" dir="{{.ContentDirection}}"><header>{{if .Appearance.HeaderShowTitle}}<h1><bdi dir="auto">{{.Appearance.SiteTitle}}</bdi></h1>{{end}}{{if .Appearance.HeaderTagline}}<p dir="auto">{{.Appearance.HeaderTagline}}</p>{{end}}</header><h2 dir="auto">{{.Revision.Title}}</h2><div class="preview-body">{{.Body}}</div>{{if .Appearance.FooterText}}<footer dir="auto">{{.Appearance.FooterText}}</footer>{{end}}</article><p>{{.Locale.Text "rixa" "private_media_notice" nil}}</p>{{end}}` + contentScopeTemplates))

const contentEditorCSS = `.selected-site{border-inline-start:3px solid GrayText;padding:.6rem 1rem;margin-block:1rem;overflow-wrap:anywhere}.workspace-breadcrumb{display:flex;flex-wrap:wrap;gap:.5rem}[data-content-workspace]{min-width:0}[data-content-workspace] table{width:100%;table-layout:fixed}[data-content-workspace] td,[data-content-workspace] th{overflow-wrap:anywhere}[data-content-workspace] input,[data-content-workspace] textarea{max-width:100%;box-sizing:border-box}[data-content-workspace] code{overflow-wrap:anywhere}[data-draft-recovery]{border:2px solid GrayText;padding:1rem;margin-block:1rem}[data-draft-recovery] button{margin-block:.25rem}@media(max-width:40rem){[data-content-workspace] fieldset{min-width:0}[data-content-workspace] .editor-toolbar{gap:.6rem}[data-content-workspace] button{min-height:2.5rem}}[data-editor]{min-height:16rem;border:1px solid GrayText;padding:1rem;max-width:52rem;overflow-wrap:anywhere}[data-editor]:focus{outline:3px solid #3478db;outline-offset:3px}.editor-toolbar{display:flex;flex-wrap:wrap;gap:.4rem}.site-preview{max-width:48rem;border:1px solid GrayText;padding:1.5rem}.site-preview[data-theme="dark"]{color-scheme:dark;background:#171717;color:#f2f2f2}.site-preview[data-theme="light"]{color-scheme:light;background:#fff;color:#111}.preview-body{overflow-wrap:anywhere}[data-editor] figure{border:1px dashed GrayText;padding:.7rem}`

const contentEditorPrimitivesJS = `
"use strict";
(() => {
  const base = document.querySelector("[data-content-workspace]")?.dataset.base || "/admin/content";
  const status = document.getElementById("status");
  const i18n = window.AChrixI18n;
  function isolated(value) {
    const node = document.createElement("bdi");
    node.dir = "ltr";
    node.textContent = value;
    return node;
  }

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
      status.replaceChildren(i18n.parts("rixa", "outcome_unknown", {Operation: isolated(operation.value)}));
    }
  }));

  const create = document.querySelector("[data-content-create]");
  if (create) create.addEventListener("admin:success", event => {
    if (event.detail && event.detail.id) location.assign(base + "/edit/" + event.detail.id);
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
      if (status) status.textContent = i18n.text("rixa", "editor_drop_disabled");
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
      if (status) status.textContent = i18n.text("rixa", "editor_invalid_link");
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
      if (status) status.textContent = i18n.text("rixa", "editor_invalid_media");
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
    if (preview && id) preview.href = base + "/preview/" + id + "/" + revision;
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
    if (status) status.replaceChildren(i18n.parts("rixa", "operation_committed", {
      Operation: isolated(detail.operation_id || ""), Kind: isolated(detail.kind || ""),
      Resource: isolated(detail.resource_id || ""), Revision: isolated(detail.result_revision || "0")
    }));
  }));

  sync();
})();
`

const contentEditorJS = contentEditorPrimitivesJS + draftRecoveryJS
