// SPDX-License-Identifier: MPL-2.0
package product

import (
	"context"
	"encoding/json"
	"io"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/AChWorks/achrix"
	shell "github.com/AChWorks/achrix/admin"
	"github.com/AChWorks/achrix/admin/localize"
	"github.com/AChWorks/achrix/identity"
)

const draftTestID = "AAAAAAAAAAAAAAAAAAAAAAAAAA"
const draftTestOp = "BBBBBBBBBBBBBBBBBBBBBBBBBB"

type draftControlCall struct {
	actor                achrix.Principal
	site, action, cursor string
}
type draftControlStub struct {
	calls    []draftControlCall
	err      error
	revision *ContentRevision
}

func (s *draftControlStub) record(a achrix.Principal, site, action, cursor string) error {
	s.calls = append(s.calls, draftControlCall{a, site, action, cursor})
	if a != "control-actor" || site != "site-a" {
		return achrix.ErrDenied
	}
	return s.err
}
func (s *draftControlStub) CreateSiteAccount(context.Context, achrix.Principal, string, string, string) (identity.Account, error) {
	return identity.Account{}, achrix.ErrDenied
}
func (s *draftControlStub) ListSiteContentPage(c context.Context, a achrix.Principal, site, cursor string, limit int) (ContentListPage, error) {
	e := s.record(a, site, "list", cursor)
	return ContentListPage{Items: []ContentSummary{{ID: draftTestID, Title: "<title> عنوان", Kind: ContentPost, HeadRevision: 2}}, NextCursor: "next_cursor"}, e
}
func (s *draftControlStub) HeadSiteContent(c context.Context, a achrix.Principal, site, id string) (ContentRevision, error) {
	e := s.record(a, site, "head", id)
	if s.revision != nil {
		return *s.revision, e
	}
	return draftTestRevision(), e
}
func (s *draftControlStub) PreviewSiteContent(c context.Context, a achrix.Principal, site, id string, n int64) (ContentRevision, error) {
	e := s.record(a, site, "preview", id)
	v := draftTestRevision()
	v.Revision = n
	return v, e
}
func (s *draftControlStub) SiteContentRevisionsPage(c context.Context, a achrix.Principal, site, id, cursor string, limit int) (RevisionPage, error) {
	e := s.record(a, site, "history", cursor)
	return RevisionPage{Items: []RevisionSummary{{Revision: 2, Title: "عنوان", Actor: "site-operator"}}, NextCursor: "older_cursor"}, e
}
func (s *draftControlStub) CreateSiteContent(c context.Context, a achrix.Principal, site, op string, k ContentKind, title, body string) (ContentRevision, error) {
	e := s.record(a, site, "create", op)
	v := draftTestRevision()
	v.Revision = 1
	return v, e
}
func (s *draftControlStub) SaveSiteContent(c context.Context, a achrix.Principal, site, op, id string, n int64, title, body string) (ContentRevision, error) {
	e := s.record(a, site, "save", op)
	return draftTestRevision(), e
}
func (s *draftControlStub) SiteEditorialOperation(c context.Context, a achrix.Principal, site, op string) (EditorialOperation, error) {
	e := s.record(a, site, "operation", op)
	return EditorialOperation{ID: op, Kind: "content.save", ResourceID: draftTestID, ResultRevision: 2}, e
}
func (s *draftControlStub) SiteAppearance(c context.Context, a achrix.Principal, site string) (Appearance, error) {
	e := s.record(a, site, "appearance", "")
	return Appearance{SiteTitle: "Mixed نمونه", SiteLanguage: "fa", Theme: "light", HeaderShowTitle: true}, e
}
func draftTestRevision() ContentRevision {
	return ContentRevision{ID: draftTestID, Kind: ContentPost, Revision: 2, Title: "<title> عنوان", BodyHTML: `<p dir="auto"><a href="/posts/example/?q=1&amp;x=2">پیوند</a></p>`}
}

func draftRequest(t *testing.T, s *draftControlStub, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	response := httptest.NewRecorder()
	request := httptest.NewRequest(method, "https://control.rixa.test"+path, strings.NewReader(body))
	if method == http.MethodPost {
		request.Header.Set("Content-Type", "application/json")
	}
	locale := testProductLocalizer(t, "en")
	view := shell.Request{Principal: "control-actor", Localizer: locale, Allowed: func(string, string) bool { return true }, Render: func(page shell.Page) error { return page.Template.ExecuteTemplate(response, "content", page.Data) }}
	newControlSurface([]SiteConfig{{ID: "site-a", Origin: "https://a.rixa.test"}}, s, localize.Locale{Tag: "en", Direction: "ltr"}, localize.Locale{Tag: "fa", Direction: "rtl"}).Handler(response, request, view)
	return response
}

func TestSelectedDraftRoutesBindOriginalActorAndOnePathScope(t *testing.T) {
	for _, path := range []string{"/site/site-a/content/", "/site/site-a/content/page/next_cursor", "/site/site-a/content/edit/" + draftTestID, "/site/site-a/content/history/" + draftTestID + "/before/older_cursor", "/site/site-a/content/preview/" + draftTestID + "/2"} {
		t.Run(path, func(t *testing.T) {
			s := &draftControlStub{}
			r := draftRequest(t, s, http.MethodGet, path, "")
			if r.Code != 200 {
				t.Fatalf("status %d: %s", r.Code, r.Body.String())
			}
			page := r.Body.String()
			for _, want := range []string{"Selected site", "site-a", "https://a.rixa.test"} {
				if !strings.Contains(page, want) {
					t.Fatalf("missing scope %q", want)
				}
			}
			for _, call := range s.calls {
				if call.actor != "control-actor" || call.site != "site-a" {
					t.Fatalf("forged principal/scope: %+v", call)
				}
			}
			if len(s.calls) == 0 {
				t.Fatal("selected action bypassed service")
			}
			for _, forbidden := range []string{"/admin/content/", "/publication-intent", "/publish", "<title>", "StorageRoot"} {
				if strings.Contains(page, forbidden) {
					t.Fatalf("selected draft leaked %q", forbidden)
				}
			}
		})
	}
}
func TestSelectedDraftRejectsMalformedOrSecondTargetsBeforeService(t *testing.T) {
	for _, path := range []string{"/site/../content/", "/site/site-a/content/edit/bad", "/site/site-a/content/page/a/b", "/site/site-a/content/history/" + draftTestID + "/before/bad.cursor", "/site/site-a/content/preview/" + draftTestID + "/+1", "/site/site-a/content/publication-intent", "/site/site-a/content/publish"} {
		s := &draftControlStub{}
		method := http.MethodGet
		if strings.HasSuffix(path, "publish") || strings.HasSuffix(path, "publication-intent") {
			method = http.MethodPost
		}
		r := draftRequest(t, s, method, path, "{}")
		if r.Code != 404 {
			t.Errorf("%s got %d", path, r.Code)
		}
		if len(s.calls) != 0 {
			t.Errorf("%s touched service", path)
		}
	}
	for _, body := range []string{`{"operation_id":"` + draftTestOp + `","kind":"post","title":"ok","body_html":"<p>ok</p>","site_id":"other"}`, `{"operation_id":"` + draftTestOp + `","kind":"post","title":"ok","principal":"other"}`, `{} {}`} {
		s := &draftControlStub{}
		r := draftRequest(t, s, http.MethodPost, "/site/site-a/content/create", body)
		if r.Code != 400 || len(s.calls) != 0 {
			t.Fatalf("second target admitted: %d %+v", r.Code, s.calls)
		}
	}
	s := &draftControlStub{}
	r := draftRequest(t, s, http.MethodGet, "/site/other/content/", "")
	if r.Code != 403 || len(s.calls) != 1 || s.calls[0].site != "other" {
		t.Fatal("valid unknown site did not use domain gate")
	}
}
func TestSelectedDraftMutationResponsesAndUnknownOutcome(t *testing.T) {
	for _, action := range []string{"create", "save", "operation"} {
		body := map[string]string{"operation_id": draftTestOp}
		if action == "create" {
			body["kind"] = "post"
			body["title"] = "عنوان"
			body["body_html"] = "<p>body</p>"
		}
		if action == "save" {
			body["id"] = draftTestID
			body["expected_head"] = "1"
			body["title"] = "عنوان"
			body["body_html"] = "<p>body</p>"
		}
		input, _ := json.Marshal(body)
		s := &draftControlStub{}
		r := draftRequest(t, s, http.MethodPost, "/site/site-a/content/"+action, string(input))
		var result map[string]string
		if r.Code != 200 || json.Unmarshal(r.Body.Bytes(), &result) != nil || result["site_id"] != "site-a" || result["origin"] != "https://a.rixa.test" || result["operation_id"] != draftTestOp {
			t.Fatalf("missing exact response scope: %d %s", r.Code, r.Body.String())
		}
		if len(s.calls) != 1 || s.calls[0].actor != "control-actor" || s.calls[0].site != "site-a" {
			t.Fatal(s.calls)
		}
	}
	s := &draftControlStub{err: ErrEditorialUnknownOutcome}
	r := draftRequest(t, s, http.MethodPost, "/site/site-a/content/save", `{"operation_id":"`+draftTestOp+`","id":"`+draftTestID+`","expected_head":"1","title":"ok","body_html":"<p>ok</p>"}`)
	if r.Code != 503 || !strings.Contains(r.Body.String(), `"code":"unknown_outcome"`) || !strings.Contains(r.Body.String(), draftTestOp) {
		t.Fatalf("unknown outcome lost identity: %d %s", r.Code, r.Body.String())
	}
}
func TestSelectedPrivatePreviewResolvesOnlyCanonicalRelativeLinks(t *testing.T) {
	source := draftTestRevision().BodyHTML
	rendered, e := trustedPreviewHTML(source, "https://site.example:9443")
	if e != nil || !strings.Contains(string(rendered), `href="https://site.example:9443/posts/example/?q=1&amp;x=2"`) {
		t.Fatalf("preview meaning: %s %v", rendered, e)
	}
	if draftTestRevision().BodyHTML != source {
		t.Fatal("source changed")
	}
	if _, e = trustedPreviewHTML(`<script>alert(1)</script>`, "https://site.example"); e == nil {
		t.Fatal("untrusted HTML admitted")
	}
	if _, e = trustedPreviewHTML(source, "http://site.example"); e == nil {
		t.Fatal("untrusted origin admitted")
	}
	original, e := trustedPreviewHTML(source, "")
	if e != nil || string(original) != source {
		t.Fatal("direct-site link meaning changed")
	}
}

// Use the real released shell buffer/admission and TLS transport, rather than
// claiming a standalone template proves a complete administration response.
type draftRenderAuth struct{}

func (draftRenderAuth) AuthenticateRequest(*http.Request, bool) (achrix.Principal, error) {
	return "control-actor", nil
}

type draftRenderPolicy struct{}

func (draftRenderPolicy) Authorize(context.Context, achrix.Principal, string, string) error {
	return nil
}
func TestSelectedDraftLargestSourceFitsReleasedShell(t *testing.T) {
	rich := `<h2 dir="auto">عنوان English</h2><p dir="auto"><strong>متن</strong> <em>mixed</em> <a href="/posts/example/?q=1&amp;x=2">پیوند</a></p><figure data-media-id="AAAAAAAAAAAAAAAAAAAAAAAAAA"><figcaption dir="auto">توضیح Media</figcaption></figure>`
	body := rich + `<p dir="auto">` + strings.Repeat("x", maxEditorialBodyBytes-len(rich)-18) + `</p>`
	canonical, _, err := canonicalBody(body)
	if err != nil {
		t.Fatal(err)
	}
	revision := draftTestRevision()
	revision.BodyHTML = canonical
	service := &draftControlStub{revision: &revision}
	server := httptest.NewUnstartedServer(nil)
	origin := "https://" + server.Listener.Addr().String()
	admin, err := shell.New(shell.Config{Origin: origin, AuthPath: "/auth", Language: "en", Catalogs: []fs.FS{bundledProductCatalogs()}}, draftRenderAuth{}, draftRenderPolicy{}, newControlSurface([]SiteConfig{{ID: "site-a", Origin: "https://a.rixa.test"}}, service, localize.Locale{Tag: "en", Direction: "ltr"}, localize.Locale{Tag: "fa", Direction: "rtl"}))
	if err != nil {
		server.Close()
		t.Fatal(err)
	}
	server.Config.Handler = admin.Handler()
	server.StartTLS()
	defer server.Close()
	response, err := server.Client().Get(origin + "/admin/sites/site/site-a/content/edit/" + draftTestID)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	output, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != 200 || !strings.Contains(string(output), canonical) || strings.Count(string(output), canonical) != 1 {
		t.Fatalf("accepted source could not be edited through released shell: status=%d bytes=%d", response.StatusCode, len(output))
	}
}
