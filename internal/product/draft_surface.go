// SPDX-License-Identifier: MPL-2.0

package product

import (
	"bytes"
	"context"
	"html/template"
	"net/http"
	"net/url"
	"regexp"
	"strings"

	"github.com/AChWorks/achrix"
	shell "github.com/AChWorks/achrix/admin"
	"github.com/AChWorks/achrix/admin/localize"
	xhtml "golang.org/x/net/html"
	"golang.org/x/net/html/atom"
)

// draftService is the product's saved-source workflow. Publication is composed
// separately; a selected control workspace cannot accidentally expose it.
type draftService interface {
	ListPage(context.Context, achrix.Principal, string, int) (ContentListPage, error)
	Head(context.Context, achrix.Principal, string) (ContentRevision, error)
	Preview(context.Context, achrix.Principal, string, int64) (ContentRevision, error)
	RevisionsPage(context.Context, achrix.Principal, string, string, int) (RevisionPage, error)
	Create(context.Context, achrix.Principal, string, ContentKind, string, string) (ContentRevision, error)
	Save(context.Context, achrix.Principal, string, string, int64, string, string) (ContentRevision, error)
	Operation(context.Context, achrix.Principal, string) (EditorialOperation, error)
	Appearance(context.Context, achrix.Principal) (Appearance, error)
}

type controlDraftManager interface {
	ListSiteContentPage(context.Context, achrix.Principal, string, string, int) (ContentListPage, error)
	HeadSiteContent(context.Context, achrix.Principal, string, string) (ContentRevision, error)
	PreviewSiteContent(context.Context, achrix.Principal, string, string, int64) (ContentRevision, error)
	SiteContentRevisionsPage(context.Context, achrix.Principal, string, string, string, int) (RevisionPage, error)
	CreateSiteContent(context.Context, achrix.Principal, string, string, ContentKind, string, string) (ContentRevision, error)
	SaveSiteContent(context.Context, achrix.Principal, string, string, string, int64, string, string) (ContentRevision, error)
	SiteEditorialOperation(context.Context, achrix.Principal, string, string) (EditorialOperation, error)
	SiteAppearance(context.Context, achrix.Principal, string) (Appearance, error)
}

type selectedDraftService struct {
	manager controlDraftManager
	siteID  string
}

func (s selectedDraftService) ListPage(c context.Context, a achrix.Principal, cursor string, limit int) (ContentListPage, error) {
	return s.manager.ListSiteContentPage(c, a, s.siteID, cursor, limit)
}
func (s selectedDraftService) Head(c context.Context, a achrix.Principal, id string) (ContentRevision, error) {
	return s.manager.HeadSiteContent(c, a, s.siteID, id)
}
func (s selectedDraftService) Preview(c context.Context, a achrix.Principal, id string, n int64) (ContentRevision, error) {
	return s.manager.PreviewSiteContent(c, a, s.siteID, id, n)
}
func (s selectedDraftService) RevisionsPage(c context.Context, a achrix.Principal, id, cursor string, limit int) (RevisionPage, error) {
	return s.manager.SiteContentRevisionsPage(c, a, s.siteID, id, cursor, limit)
}
func (s selectedDraftService) Create(c context.Context, a achrix.Principal, op string, kind ContentKind, title, body string) (ContentRevision, error) {
	return s.manager.CreateSiteContent(c, a, s.siteID, op, kind, title, body)
}
func (s selectedDraftService) Save(c context.Context, a achrix.Principal, op, id string, n int64, title, body string) (ContentRevision, error) {
	return s.manager.SaveSiteContent(c, a, s.siteID, op, id, n, title, body)
}
func (s selectedDraftService) Operation(c context.Context, a achrix.Principal, op string) (EditorialOperation, error) {
	return s.manager.SiteEditorialOperation(c, a, s.siteID, op)
}
func (s selectedDraftService) Appearance(c context.Context, a achrix.Principal) (Appearance, error) {
	return s.manager.SiteAppearance(c, a, s.siteID)
}

// All routes originate in validated server configuration/path segments. The
// zero value preserves direct site-origin presentation and its public workflow.
type contentRoutes struct{ SiteID, Origin string }

func (s contentRoutes) Base() string {
	if s.SiteID == "" {
		return "/admin/content"
	}
	return "/admin/sites/site/" + s.SiteID + "/content"
}
func (s contentRoutes) Assets() string {
	if s.SiteID == "" {
		return "/admin/content"
	}
	return "/admin/sites"
}

var contentCursorPath = regexp.MustCompile(`^[A-Za-z0-9_-]{1,256}$`)

func serveSelectedContent(manager controlDraftManager, sites []SiteConfig, languages []localize.Locale, w http.ResponseWriter, r *http.Request, view shell.Request) {
	// No caller-supplied target is accepted anywhere except this one path.
	parts := strings.SplitN(strings.TrimPrefix(r.URL.Path, "/site/"), "/", 3)
	if len(parts) != 3 || !siteIDSyntax.MatchString(parts[0]) || parts[1] != "content" {
		editorialHTTPFail(w, ErrEditorialNotFound, "")
		return
	}
	origin := ""
	for _, site := range sites {
		if site.ID == parts[0] {
			origin = site.Origin
			break
		}
	}
	h := &contentSurface{service: selectedDraftService{manager: manager, siteID: parts[0]}, routes: contentRoutes{SiteID: parts[0], Origin: origin}, contentLocales: languages}
	if publication, ok := manager.(controlPublicationManager); ok {
		selected := selectedPublication{manager: publication, siteID: parts[0]}
		h.intentService = selected
		h.publication = selected
	}
	copy := r.Clone(r.Context())
	copy.URL.Path = "/" + parts[2]
	h.serve(w, copy, view) // Principal remains the actual authenticated control actor.
}

type contentHistoryView struct {
	Locale     *localize.Localizer
	Routes     contentRoutes
	Revision   ContentRevision
	History    []RevisionSummary
	NextCursor string
}

func (h *contentSurface) history(w http.ResponseWriter, r *http.Request, view shell.Request, tail string) {
	parts := strings.Split(tail, "/")
	if len(parts) != 3 || !validEditorialID(parts[0]) || parts[1] != "before" || !contentCursorPath.MatchString(parts[2]) {
		editorialHTTPFail(w, ErrEditorialNotFound, "")
		return
	}
	head, err := h.service.Head(r.Context(), view.Principal, parts[0])
	if err != nil {
		editorialHTTPFail(w, err, "")
		return
	}
	page, err := h.service.RevisionsPage(r.Context(), view.Principal, parts[0], parts[2], 25)
	if err != nil {
		editorialHTTPFail(w, err, "")
		return
	}
	if err = renderProductPage(view, "saved_revisions", shell.Page{Template: contentHistoryTemplate, Data: contentHistoryView{Locale: view.Localizer, Routes: h.routes, Revision: head, History: page.Items, NextCursor: page.NextCursor}}); err != nil {
		editorialHTTPFail(w, ErrEditorialUnavailable, "")
	}
}

// Canonical source is independently validated before rendering. Only the
// product-owned preview's root-relative links acquire the selected site's
// declared origin; Admin/Module HTML and stored source are never rewritten.
func trustedPreviewHTML(body, origin string) (template.HTML, error) {
	trusted, err := trustedEditorialHTML(body)
	if err != nil || origin == "" {
		return trusted, err
	}
	base, err := url.Parse(origin)
	if err != nil || base.Scheme != "https" || base.Host == "" || base.User != nil || base.Path != "" || base.RawQuery != "" || base.Fragment != "" {
		return "", ErrEditorialUnavailable
	}
	nodes, err := xhtml.ParseFragment(strings.NewReader(string(trusted)), &xhtml.Node{Type: xhtml.ElementNode, DataAtom: atom.Div, Data: "div"})
	if err != nil {
		return "", ErrEditorialUnavailable
	}
	var visit func(*xhtml.Node) error
	visit = func(n *xhtml.Node) error {
		if n.Type == xhtml.ElementNode && n.Data == "a" {
			for i, a := range n.Attr {
				if a.Key == "href" && strings.HasPrefix(a.Val, "/") {
					relative, e := url.Parse(a.Val)
					if e != nil {
						return ErrEditorialUnavailable
					}
					n.Attr[i].Val = base.ResolveReference(relative).String()
				}
			}
		}
		for child := n.FirstChild; child != nil; child = child.NextSibling {
			if e := visit(child); e != nil {
				return e
			}
		}
		return nil
	}
	var result bytes.Buffer
	for _, n := range nodes {
		if err = visit(n); err != nil {
			return "", err
		}
		if err = xhtml.Render(&result, n); err != nil || result.Len() > 96<<10 {
			return "", ErrEditorialUnavailable
		}
	}
	return template.HTML(result.String()), nil
}

const contentScopeTemplates = `
{{define "content-scope"}}{{if .Routes.SiteID}}<nav class="workspace-breadcrumb" aria-label="{{.Locale.Text "rixa" "workspace_breadcrumb" nil}}"><a href="/admin/sites">{{.Locale.Text "rixa" "sites" nil}}</a> / <a href="{{.Routes.Base}}/">{{.Locale.Text "rixa" "saved_content" nil}}</a></nav><aside class="selected-site"><strong>{{.Locale.Text "rixa" "selected_site" nil}}</strong> <bdi dir="ltr">{{.Routes.SiteID}}</bdi><br><bdi dir="ltr">{{.Routes.Origin}}</bdi></aside>{{end}}{{end}}
{{define "draft-recovery"}}<section data-draft-recovery hidden aria-labelledby="draft-recovery-title"><h2 id="draft-recovery-title">{{.Locale.Text "rixa" "save_recovery" nil}}</h2><p data-recovery-message></p><p>{{.Locale.Text "rixa" "operation_id" nil}}: <bdi dir="ltr" data-pending-operation></bdi></p><button type="button" data-inspect-pending>{{.Locale.Text "rixa" "inspect" nil}}</button> <button type="button" data-retry-pending>{{.Locale.Text "rixa" "retry_exact_save" nil}}</button></section><p data-session-recovery hidden><a href="/admin/login" target="_blank" rel="noopener">{{.Locale.Text "rixa" "sign_in_new_tab" nil}}</a> {{.Locale.Text "rixa" "return_and_retry" nil}}</p><p data-conflict-recovery hidden><a href="{{.Routes.Base}}/" data-latest-link target="_blank" rel="noopener">{{.Locale.Text "rixa" "view_latest_new_tab" nil}}</a> {{.Locale.Text "rixa" "conflict_keep_input" nil}}</p>{{end}}
`

var contentHistoryTemplate = template.Must(template.New("content-history").Parse(`{{define "content"}}{{template "content-scope" .}}<link rel="stylesheet" href="{{.Routes.Assets}}/editor.css"><p><a href="{{.Routes.Base}}/edit/{{.Revision.ID}}">{{.Locale.Text "rixa" "back_to_edit" nil}}</a></p><h2>{{.Locale.Text "rixa" "saved_revisions" nil}} — <bdi dir="auto">{{.Revision.Title}}</bdi></h2><ol>{{range .History}}<li><a href="{{$.Routes.Base}}/preview/{{$.Revision.ID}}/{{.Revision}}">{{$.Locale.Text "rixa" "revision" nil}} {{.Revision}}</a> — <bdi dir="auto">{{.Title}}</bdi> · <bdi dir="ltr">{{.Actor}}</bdi></li>{{end}}</ol>{{if .NextCursor}}<a href="{{.Routes.Base}}/history/{{.Revision.ID}}/before/{{.NextCursor}}">{{.Locale.Text "rixa" "older_revisions" nil}}</a>{{end}}{{end}}` + contentScopeTemplates))
