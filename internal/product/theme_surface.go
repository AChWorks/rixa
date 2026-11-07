// SPDX-License-Identifier: MPL-2.0

package product

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"html/template"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	shell "github.com/AChWorks/achrix/admin"
	"github.com/AChWorks/achrix/admin/localize"
)

type themeSurface struct{ service *ThemeService }
type themeSurfaceView struct {
	Locale             *localize.Localizer
	Current            ThemeRevision
	History            []ThemeHistoryItem
	Generation         string
	PublishedRevision  int64
	SaveOperationID    string
	ImportOperationID  string
	PublishOperationID string
	SettingsJSON       string
}

func newThemeSurface(service *ThemeService) (shell.Surface, error) {
	if service == nil || !service.enabled() {
		return shell.Surface{}, shell.ErrConfiguration
	}
	h := &themeSurface{service: service}
	return shell.Surface{
		ID: "site-theme", Title: shell.Text{Domain: "rixa", Key: "site_theme"},
		Capability: CapabilityThemeRead, Target: ThemeTarget, Handler: h.serve,
	}, nil
}

func (h *themeSurface) serve(w http.ResponseWriter, r *http.Request, view shell.Request) {
	if r.Method == http.MethodGet {
		switch {
		case r.URL.Path == "/":
			h.show(w, r, view)
		case strings.HasPrefix(r.URL.Path, "/bundle/"):
			h.bundle(w, r, view, strings.TrimPrefix(r.URL.Path, "/bundle/"))
		case r.URL.Path == "/theme.js":
			w.Header().Set("Content-Type", "text/javascript; charset=utf-8")
			w.Header().Set("Cache-Control", "private, no-store")
			_, _ = io.WriteString(w, themeEditorJS)
		case strings.HasPrefix(r.URL.Path, "/export/"):
			h.export(w, r, view, strings.TrimPrefix(r.URL.Path, "/export/"))
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
	case "/import":
		h.importBundle(w, r, view)
	case "/preview":
		h.preview(w, r, view)
	case "/publish":
		h.publish(w, r, view)
	case "/operation":
		h.operation(w, r, view)
	default:
		editorialHTTPFail(w, ErrEditorialNotFound, "")
	}
}

func (h *themeSurface) show(w http.ResponseWriter, r *http.Request, view shell.Request) {
	head, err := h.service.Head(r.Context(), view.Principal)
	if err != nil {
		editorialHTTPFail(w, err, "")
		return
	}
	history, err := h.service.History(r.Context(), view.Principal)
	if err != nil {
		editorialHTTPFail(w, err, "")
		return
	}
	rawSettings, err := json.MarshalIndent(head.Bundle.Settings, "", "  ")
	if err != nil {
		editorialHTTPFail(w, ErrEditorialUnavailable, "")
		return
	}
	generation := ""
	if h.service.publication != nil {
		generation = h.service.publication.State("", "").Generation
	}
	if err = renderProductPage(view, "site_theme", shell.Page{
		Template: themeEditorTemplate,
		Data: themeSurfaceView{
			Locale: view.Localizer, Current: head, History: history,
			PublishedRevision: h.service.PublishedRevision(), Generation: generation,
			SaveOperationID: newOperationID(), ImportOperationID: newOperationID(), PublishOperationID: newOperationID(), SettingsJSON: string(rawSettings),
		},
	}); err != nil {
		editorialHTTPFail(w, ErrEditorialUnavailable, "")
	}
}

func decodeThemeJSON(w http.ResponseWriter, r *http.Request, value any) error {
	if len(r.Header.Values("Content-Type")) != 1 || r.Header.Get("Content-Type") != "application/json" {
		return ErrEditorialInvalid
	}
	r.Body = http.MaxBytesReader(w, r.Body, 8<<20)
	defer r.Body.Close()
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(value); err != nil {
		return ErrEditorialInvalid
	}
	var extra any
	if decoder.Decode(&extra) != io.EOF {
		return ErrEditorialInvalid
	}
	return nil
}
func themeRevisionInput(value string) (int64, error) {
	if value == "0" {
		return 0, nil
	}
	return parseRevision(value)
}
func (h *themeSurface) bundle(w http.ResponseWriter, r *http.Request, view shell.Request, revisionText string) {
	revision, err := themeRevisionInput(revisionText)
	if err != nil {
		editorialHTTPFail(w, err, "")
		return
	}
	var source ThemeRevision
	if revision == 0 {
		source, err = h.service.Head(r.Context(), view.Principal)
		if err == nil && source.Revision != 0 {
			err = ErrEditorialConflict
		}
	} else {
		source, err = h.service.Revision(r.Context(), view.Principal, revision)
	}
	if err != nil {
		editorialHTTPFail(w, err, "")
		return
	}
	w.Header().Set("Cache-Control", "private, no-store")
	editorialJSON(w, source.Bundle)
}

func (h *themeSurface) save(w http.ResponseWriter, r *http.Request, view shell.Request) {
	var body struct {
		OperationID  string `json:"operation_id"`
		ExpectedHead string `json:"expected_head"`
		HeadHTML     string `json:"head_html"`
		HeaderHTML   string `json:"header_html"`
		BodyHTML     string `json:"body_html"`
		FooterHTML   string `json:"footer_html"`
		CSS          string `json:"site_css"`
		JavaScript   string `json:"site_js"`
		SettingsJSON string `json:"settings_json"`
	}
	if err := decodeThemeJSON(w, r, &body); err != nil {
		editorialHTTPFail(w, err, body.OperationID)
		return
	}
	expected, err := themeRevisionInput(body.ExpectedHead)
	if err != nil {
		editorialHTTPFail(w, err, body.OperationID)
		return
	}
	// Preserve previously imported assets while editing source slots; source
	// revision CAS below detects any intervening import or change.
	var source ThemeRevision
	if expected == 0 {
		source = ThemeRevision{Bundle: defaultThemeBundle()}
	} else {
		source, err = h.service.Revision(r.Context(), view.Principal, expected)
		if err != nil {
			editorialHTTPFail(w, err, body.OperationID)
			return
		}
	}
	bundle := ThemeBundle{
		HeadHTML: body.HeadHTML, HeaderHTML: body.HeaderHTML, BodyHTML: body.BodyHTML,
		FooterHTML: body.FooterHTML, CSS: body.CSS, JavaScript: body.JavaScript, Assets: source.Bundle.Assets,
	}
	if len(body.SettingsJSON) > maxThemeSourceBytes {
		editorialHTTPFail(w, ErrEditorialLimited, body.OperationID)
		return
	}
	decoder := json.NewDecoder(strings.NewReader(body.SettingsJSON))
	decoder.DisallowUnknownFields()
	if err = decoder.Decode(&bundle.Settings); err != nil {
		editorialHTTPFail(w, ErrEditorialInvalid, body.OperationID)
		return
	}
	if decoder.Decode(new(any)) != io.EOF {
		editorialHTTPFail(w, ErrEditorialInvalid, body.OperationID)
		return
	}
	saved, err := h.service.SaveDraft(r.Context(), view.Principal, body.OperationID, expected, bundle)
	if err != nil {
		editorialHTTPFail(w, err, body.OperationID)
		return
	}
	editorialJSON(w, map[string]string{
		"operation_id": body.OperationID, "revision": strconv.FormatInt(saved.Revision, 10), "source_hash": saved.SourceHash,
	})
}
func (h *themeSurface) importBundle(w http.ResponseWriter, r *http.Request, view shell.Request) {
	if err := h.service.authorize(r.Context(), view.Principal, CapabilityThemeEdit); err != nil {
		editorialHTTPFail(w, err, "")
		return
	}
	var body struct {
		OperationID  string `json:"operation_id"`
		ExpectedHead string `json:"expected_head"`
		Archive      string `json:"archive_base64"`
	}
	if err := decodeThemeJSON(w, r, &body); err != nil {
		editorialHTTPFail(w, err, body.OperationID)
		return
	}
	expected, err := themeRevisionInput(body.ExpectedHead)
	if err != nil {
		editorialHTTPFail(w, err, body.OperationID)
		return
	}
	if len(body.Archive) > ((maxThemeBundleBytes+2)/3)*4+4 {
		editorialHTTPFail(w, ErrEditorialLimited, body.OperationID)
		return
	}
	archive, err := base64.StdEncoding.DecodeString(body.Archive)
	if err != nil {
		editorialHTTPFail(w, ErrEditorialInvalid, body.OperationID)
		return
	}
	bundle, err := importThemeBundle(archive)
	if err != nil {
		editorialHTTPFail(w, err, body.OperationID)
		return
	}
	saved, err := h.service.SaveDraft(r.Context(), view.Principal, body.OperationID, expected, bundle)
	if err != nil {
		editorialHTTPFail(w, err, body.OperationID)
		return
	}
	editorialJSON(w, map[string]string{
		"operation_id": body.OperationID, "revision": strconv.FormatInt(saved.Revision, 10), "source_hash": saved.SourceHash,
	})
}
func (h *themeSurface) export(w http.ResponseWriter, r *http.Request, view shell.Request, revisionText string) {
	revision, err := themeRevisionInput(revisionText)
	if err != nil {
		editorialHTTPFail(w, err, "")
		return
	}
	var source ThemeRevision
	if revision == 0 {
		source, err = h.service.Head(r.Context(), view.Principal)
		if source.Revision != 0 {
			err = ErrEditorialConflict
		}
	} else {
		source, err = h.service.Revision(r.Context(), view.Principal, revision)
	}
	if err != nil {
		editorialHTTPFail(w, err, "")
		return
	}
	archive, err := exportThemeBundle(source.Bundle)
	if err != nil {
		editorialHTTPFail(w, err, "")
		return
	}
	w.Header().Set("Cache-Control", "private, no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Content-Type", "application/zip")
	w.Header().Set("Content-Disposition", `attachment; filename="rixa-code-theme-v1.zip"`)
	w.Header().Set("Content-Length", strconv.Itoa(len(archive)))
	_, _ = io.Copy(w, bytes.NewReader(archive))
}
func (h *themeSurface) preview(w http.ResponseWriter, r *http.Request, view shell.Request) {
	var body struct {
		ThemeRevision string `json:"theme_revision"`
	}
	if err := decodeEditorialJSON(w, r, &body); err != nil {
		editorialHTTPFail(w, err, "")
		return
	}
	revision, err := themeRevisionInput(body.ThemeRevision)
	if err != nil {
		editorialHTTPFail(w, err, "")
		return
	}
	result, err := h.service.Preview(r.Context(), view.Principal, revision)
	if err != nil {
		editorialHTTPFail(w, err, "")
		return
	}
	editorialJSON(w, result)
}
func (h *themeSurface) publish(w http.ResponseWriter, r *http.Request, view shell.Request) {
	var body struct {
		OperationID        string `json:"operation_id"`
		ThemeRevision      string `json:"theme_revision"`
		ExpectedGeneration string `json:"expected_generation"`
		Confirm            bool   `json:"confirm"`
	}
	if err := decodeEditorialJSON(w, r, &body); err != nil {
		editorialHTTPFail(w, err, body.OperationID)
		return
	}
	revision, err := themeRevisionInput(body.ThemeRevision)
	if err != nil || !body.Confirm {
		editorialHTTPFail(w, ErrEditorialInvalid, body.OperationID)
		return
	}
	var result PublicationResult
	if revision > 0 {
		result, err = h.service.PublishRevision(r.Context(), view.Principal, body.OperationID, revision, body.ExpectedGeneration)
	} else {
		if err = h.service.authorize(r.Context(), view.Principal, CapabilityThemePublish); err == nil {
			result, err = h.service.publication.Apply(r.Context(), view.Principal, PublicationRequest{
				OperationID: body.OperationID, ExpectedGeneration: body.ExpectedGeneration, UseDefaultTheme: true,
			})
		}
	}
	if err != nil {
		editorialHTTPFail(w, err, body.OperationID)
		return
	}
	editorialJSON(w, map[string]string{
		"operation_id": body.OperationID, "generation": result.Generation, "theme_revision": body.ThemeRevision,
	})
}
func (h *themeSurface) operation(w http.ResponseWriter, r *http.Request, view shell.Request) {
	var body struct {
		OperationID string `json:"operation_id"`
	}
	if err := decodeEditorialJSON(w, r, &body); err != nil {
		editorialHTTPFail(w, err, body.OperationID)
		return
	}
	operation, err := h.service.Operation(r.Context(), view.Principal, body.OperationID)
	if err == nil {
		editorialJSON(w, map[string]string{
			"operation_id": operation.ID, "kind": operation.Kind,
			"result_revision": strconv.FormatInt(operation.ResultRevision, 10),
			"created_at":      operation.CreatedAt.Format(time.RFC3339Nano),
		})
		return
	}
	if !errors.Is(err, ErrEditorialNotFound) || h.service.publication == nil {
		editorialHTTPFail(w, err, body.OperationID)
		return
	}
	if err = h.service.authorize(r.Context(), view.Principal, CapabilityThemeRead); err != nil {
		editorialHTTPFail(w, err, body.OperationID)
		return
	}
	receipt, err := h.service.publication.Operation(r.Context(), view.Principal, body.OperationID)
	if err != nil {
		editorialHTTPFail(w, err, body.OperationID)
		return
	}
	editorialJSON(w, map[string]string{
		"operation_id": receipt.ID, "kind": "publication.apply", "generation": receipt.Generation,
		"theme_revision": strconv.FormatInt(receipt.ThemeRevision, 10),
		"created_at":     receipt.CreatedAt.Format(time.RFC3339Nano),
	})
}

var themeEditorTemplate = template.Must(template.New("theme-editor").Parse(`{{define "content"}}
<script src="/admin/site-theme/theme.js" defer></script>
<section data-theme-workspace>
<h2>{{.Locale.Text "rixa" "site_theme" nil}}</h2>
<p>{{.Locale.Text "rixa" "theme_security_notice" nil}}</p>
<p>Source revision: <output data-theme-current>{{.Current.Revision}}</output> · Active public revision: <output data-theme-active>{{.PublishedRevision}}</output></p>
<p>Generation: <output data-theme-generation>{{if .Generation}}{{.Generation}}{{else}}—{{end}}</output></p>
<form data-admin-form data-theme-save action="/admin/site-theme/save" method="post">
<p role="status" data-theme-loading>Loading source bundle from the authorized site...</p>
<input type="hidden" name="operation_id" value="{{.SaveOperationID}}" data-operation-field>
<input type="hidden" name="expected_head" value="{{.Current.Revision}}" data-theme-expected>
<fieldset><legend>HTML</legend>
<p>Author trusted templates using documented Site / Content / Home / Theme.Settings bindings. Rixa owns canonical SEO data.</p>
<details open><summary>head.html</summary><label for="theme-head">head.html</label><textarea id="theme-head" name="head_html" dir="ltr" rows="6" maxlength="1048576"></textarea></details>
<details><summary>header.html</summary><label for="theme-header">header.html</label><textarea id="theme-header" name="header_html" dir="ltr" rows="8" maxlength="1048576"></textarea></details>
<details open><summary>body.html</summary><label for="theme-body">body.html</label><textarea id="theme-body" name="body_html" dir="ltr" rows="14" maxlength="1048576" required></textarea></details>
<details><summary>footer.html</summary><label for="theme-footer">footer.html</label><textarea id="theme-footer" name="footer_html" dir="ltr" rows="8" maxlength="1048576"></textarea></details>
</fieldset>
<fieldset><legend>Public assets</legend>
<details><summary>site.css</summary><label for="theme-css">site.css</label><textarea id="theme-css" name="site_css" dir="ltr" rows="12" maxlength="1048576"></textarea></details>
<details><summary>site.js</summary><label for="theme-js">site.js</label><textarea id="theme-js" name="site_js" dir="ltr" rows="12" maxlength="1048576"></textarea></details>
<details><summary>Public settings (JSON)</summary><label for="theme-settings">Typed public-only Theme.Settings</label><textarea id="theme-settings" name="settings_json" dir="ltr" rows="6" maxlength="1048576"></textarea></details>
</fieldset>
<button type="submit" disabled data-theme-save-button>Save Theme draft</button>
</form>
<section><h3>Preview</h3><p>A preview is read-only, time-limited and runs on an isolated origin. Saved draft changes do not change public output.</p>
<form data-admin-form data-read-only data-theme-preview action="/admin/site-theme/preview" method="post">
<label>Revision <input name="theme_revision" data-theme-revision value="{{.Current.Revision}}" pattern="[0-9]+" required></label>
<button type="submit">Generate preview</button></form>
<p><a data-theme-preview-link href="" hidden rel="noreferrer noopener" target="_blank">Open isolated preview</a></p></section>
<section><h3>Publish or restore an older revision</h3><p>Publication changes the whole public generation atomically. Review and confirm the exact revision and generation.</p>
<form data-admin-form data-theme-publish action="/admin/site-theme/publish" method="post">
<input type="hidden" name="operation_id" value="{{.PublishOperationID}}" data-operation-field>
<input type="hidden" name="expected_generation" value="{{.Generation}}" data-theme-generation-field>
<label>Theme revision (0 = built-in)<input name="theme_revision" data-theme-revision value="{{.Current.Revision}}" inputmode="numeric" pattern="[0-9]+" required></label>
<label><input type="checkbox" name="confirm" data-publish-confirm required> I reviewed and approve this public JavaScript and HTML</label>
<button type="submit">Publish this revision</button>
</form></section>
<section><h3>Portable source bundle</h3>
<p><a href="/admin/site-theme/export/{{.Current.Revision}}" data-theme-current-export download>Export current Theme ZIP</a></p>
<form data-admin-form data-theme-import action="/admin/site-theme/import" method="post">
<input type="hidden" name="operation_id" value="{{.ImportOperationID}}" data-operation-field>
<input type="hidden" name="expected_head" value="{{.Current.Revision}}" data-theme-expected>
<input type="hidden" name="archive_base64">
<label>Import v1 Theme ZIP <input type="file" data-theme-archive accept=".zip,application/zip" required></label>
<button type="submit">Import as draft</button>
</form></section>
<section><h3>Recent revisions</h3>
{{if .History}}<table><thead><tr><th>Revision</th><th>Author</th><th>Saved</th><th>Source export</th></tr></thead><tbody>{{range .History}}<tr><td>{{.Revision}}</td><td><bdi dir="auto">{{.Actor}}</bdi></td><td>{{.CreatedAt.Format "2006-01-02 15:04 UTC"}}</td><td><a href="/admin/site-theme/export/{{.Revision}}">Download ZIP</a></td></tr>{{end}}</tbody></table>{{else}}<p>No saved Theme revision yet; the built-in design is available.</p>{{end}}
</section>
<section><h3>Inspect operation</h3>
<form data-admin-form data-read-only data-theme-operation action="/admin/site-theme/operation" method="post">
<label>Original operation ID <input name="operation_id" data-theme-operation-input required maxlength="36" dir="ltr"></label>
<button type="submit">Inspect original result</button>
</form></section>
</section>
{{end}}`))
