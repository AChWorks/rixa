// SPDX-License-Identifier: MPL-2.0

package product

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/AChWorks/achrix"
	shell "github.com/AChWorks/achrix/admin"
	"github.com/AChWorks/achrix/admin/localize"
)

type selectedPublicationStub struct {
	*draftControlStub
	calls       []draftControlCall
	request     PublicationRequest
	failure     error
	unpublished bool
}

func publicationStub() *selectedPublicationStub {
	head := draftTestRevision()
	head.PublicationIntentRevision = 1
	head.PublicationIntentVersion = 1
	return &selectedPublicationStub{draftControlStub: &draftControlStub{revision: &head}}
}
func (s *selectedPublicationStub) mark(actor achrix.Principal, site, action string) error {
	s.calls = append(s.calls, draftControlCall{actor: actor, site: site, action: action})
	if actor != "control-actor" || site != "site-a" {
		return achrix.ErrDenied
	}
	return s.failure
}
func (s *selectedPublicationStub) SetSitePublicationIntent(_ context.Context, actor achrix.Principal, site, operation, id string, head, version, rev int64) (ContentRevision, error) {
	e := s.mark(actor, site, "intent-set")
	v := draftTestRevision()
	v.PublicationIntentRevision = rev
	v.PublicationIntentVersion = version + 1
	return v, e
}
func (s *selectedPublicationStub) ClearSitePublicationIntent(_ context.Context, actor achrix.Principal, site, operation, id string, head, version int64) (ContentRevision, error) {
	e := s.mark(actor, site, "intent-clear")
	v := draftTestRevision()
	v.PublicationIntentVersion = version + 1
	return v, e
}
func (s *selectedPublicationStub) SitePublicationState(_ context.Context, actor achrix.Principal, site, id string, kind ContentKind) (PublicationState, error) {
	e := s.mark(actor, site, "state")
	state := PublicationState{Enabled: true, Generation: "CCCCCCCCCCCCCCCCCCCCCCCCCC", DesiredRoute: "/posts/review/", ActiveRevision: 1}
	if s.unpublished {
		state.ActiveRevision = 0
	}
	return state, e
}
func (s *selectedPublicationStub) ApplySitePublication(_ context.Context, actor achrix.Principal, site string, request PublicationRequest) (PublicationResult, error) {
	s.request = request
	e := s.mark(actor, site, "apply")
	return PublicationResult{Generation: "DDDDDDDDDDDDDDDDDDDDDDDDDD", ContentID: request.ContentID, Route: request.Route}, e
}
func (s *selectedPublicationStub) SitePublicationOperation(_ context.Context, actor achrix.Principal, site, operation string) (PublicationOperation, error) {
	e := s.mark(actor, site, "inspect")
	return PublicationOperation{ID: operation, ContentID: draftTestID, Generation: "DDDDDDDDDDDDDDDDDDDDDDDDDD", Route: "/posts/review/"}, e
}
func (s *selectedPublicationStub) SiteEditorialOperation(_ context.Context, _ achrix.Principal, _, _ string) (EditorialOperation, error) {
	return EditorialOperation{}, ErrEditorialNotFound
}
func selectedPublicationResponse(t *testing.T, s *selectedPublicationStub, locale, method, path string, body map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	payload, _ := json.Marshal(body)
	request := httptest.NewRequest(method, "https://control.rixa.test"+path, strings.NewReader(string(payload)))
	if method == http.MethodPost {
		request.Header.Set("Content-Type", "application/json")
	}
	rec := httptest.NewRecorder()
	view := shell.Request{Principal: "control-actor", Localizer: testProductLocalizer(t, locale), Allowed: func(string, string) bool { return true },
		Render: func(page shell.Page) error { return page.Template.ExecuteTemplate(rec, "content", page.Data) }}
	newControlSurface([]SiteConfig{{ID: "site-a", Origin: "https://a.rixa.test"}}, s,
		localize.Locale{Tag: "en", Direction: "ltr"}, localize.Locale{Tag: "fa", Direction: "rtl"}).Handler(rec, request, view)
	return rec
}
func selectedPublishBody() map[string]string {
	return map[string]string{
		"operation_id": draftTestOp, "id": draftTestID, "expected_generation": "CCCCCCCCCCCCCCCCCCCCCCCCCC",
		"expected_head": "2", "expected_publication_version": "1", "expected_intent_revision": "1",
		"route": "/posts/review/",
	}
}
func TestSelectedPublicationReviewInEnglishAndPersian(t *testing.T) {
	for _, tt := range []struct{ locale, visible string }{{"en", "entire selected site"}, {"fa", "کل سایت انتخاب‌شده"}} {
		t.Run(tt.locale, func(t *testing.T) {
			s := publicationStub()
			response := selectedPublicationResponse(t, s, tt.locale, http.MethodGet, "/site/site-a/content/edit/"+draftTestID, nil)
			if response.Code != 200 {
				t.Fatalf("page %d: %s", response.Code, response.Body.String())
			}
			for _, part := range []string{tt.visible, "data-confirm required", "data-publication-apply",
				"name=\"expected_head\" value=\"2\"", "name=\"expected_publication_version\" value=\"1\"",
				"name=\"expected_intent_revision\" value=\"1\"", "https://a.rixa.test", "data-review-generation",
				"publication-intent-clear", "/publish"} {
				if !strings.Contains(response.Body.String(), part) {
					t.Errorf("missing %q", part)
				}
			}
			if len(s.calls) != 1 || s.calls[0].action != "state" || s.calls[0].site != "site-a" {
				t.Fatal(s.calls)
			}
		})
	}
}
func TestSelectedPublicationMarkFirstDoesNotInviteInvalidApply(t *testing.T) {
	stub := publicationStub()
	stub.unpublished = true
	stub.draftControlStub.revision.PublicationIntentRevision = 0
	page := selectedPublicationResponse(t, stub, "en", http.MethodGet, "/site/site-a/content/edit/"+draftTestID, nil)
	if page.Code != 200 {
		t.Fatal(page.Code, page.Body.String())
	}
	for _, piece := range []string{"data-publication-mark-first", "Select a saved revision for publication first",
		"data-publication-apply hidden", "data-active-published=\"false\""} {
		if !strings.Contains(page.Body.String(), piece) {
			t.Errorf("unpublished content did not require intent: missing %q", piece)
		}
	}
}

func TestSelectedPublicationHTTPEnforcesReviewedInputsAndSite(t *testing.T) {
	path := "/site/site-a/content/publish"
	good := selectedPublishBody()
	s := publicationStub()
	response := selectedPublicationResponse(t, s, "en", http.MethodPost, path, good)
	var result map[string]string
	if response.Code != 200 || json.Unmarshal(response.Body.Bytes(), &result) != nil ||
		result["site_id"] != "site-a" || result["origin"] != "https://a.rixa.test" ||
		result["operation_id"] != draftTestOp || result["generation"] != "DDDDDDDDDDDDDDDDDDDDDDDDDD" {
		t.Fatalf("response %d: %s", response.Code, response.Body.String())
	}
	if len(s.calls) != 1 || s.calls[0].action != "apply" || s.request.ExpectedHead != 2 ||
		s.request.ExpectedIntentVersion != 1 || s.request.ExpectedIntentRevision != 1 {
		t.Fatalf("CAS lost: %#v %+v", s.request, s.calls)
	}
	for _, mutation := range []func(map[string]string){
		func(b map[string]string) { delete(b, "expected_head") },
		func(b map[string]string) { b["expected_intent_revision"] = "-1" },
		func(b map[string]string) { b["expected_head"] = "0" },
		func(b map[string]string) { b["site_id"] = "site-b" },
	} {
		b := selectedPublishBody()
		mutation(b)
		stub := publicationStub()
		res := selectedPublicationResponse(t, stub, "en", http.MethodPost, path, b)
		if res.Code != 400 || len(stub.calls) != 0 {
			t.Fatalf("unsafe public post: %d %s %+v", res.Code, res.Body.String(), stub.calls)
		}
	}
	other := publicationStub()
	res := selectedPublicationResponse(t, other, "en", http.MethodPost, "/site/site-b/content/publish", good)
	if res.Code != 403 || len(other.calls) != 1 || other.calls[0].site != "site-b" {
		t.Fatalf("site bypass %d %s %+v", res.Code, res.Body.String(), other.calls)
	}
	uncertain := publicationStub()
	uncertain.failure = ErrEditorialUnknownOutcome
	res = selectedPublicationResponse(t, uncertain, "en", http.MethodPost, path, good)
	if res.Code != 503 || !strings.Contains(res.Body.String(), draftTestOp) {
		t.Fatalf("lost public receipt %d %s", res.Code, res.Body.String())
	}
}
func TestSelectedPublicationIntentAndRecovery(t *testing.T) {
	s := publicationStub()
	path := "/site/site-a/content/"
	for _, tt := range []struct{ action, revision string }{{"publication-intent", "1"}, {"publication-intent-clear", "0"}} {
		b := map[string]string{"operation_id": draftTestOp, "id": draftTestID, "expected_head": "2", "expected_publication_version": "1", "revision": "1"}
		response := selectedPublicationResponse(t, s, "en", http.MethodPost, path+tt.action, b)
		var result map[string]string
		if response.Code != 200 || json.Unmarshal(response.Body.Bytes(), &result) != nil ||
			result["site_id"] != "site-a" || result["publication_intent_revision"] != tt.revision ||
			result["publication_intent_version"] != "2" {
			t.Fatalf("intent %s: %d %s", tt.action, response.Code, response.Body.String())
		}
	}
	response := selectedPublicationResponse(t, s, "en", http.MethodPost, path+"operation", map[string]string{"operation_id": draftTestOp})
	var outcome map[string]string
	if response.Code != 200 || json.Unmarshal(response.Body.Bytes(), &outcome) != nil ||
		outcome["kind"] != "publication.apply" || outcome["site_id"] != "site-a" ||
		outcome["result_generation"] != "DDDDDDDDDDDDDDDDDDDDDDDDDD" {
		t.Fatalf("operation reconciliation %d %s", response.Code, response.Body.String())
	}
	if len(s.calls) != 3 || s.calls[2].action != "inspect" {
		t.Fatal(s.calls)
	}
}
func TestPublicationOperationInspectionDistinguishesGenerationFromRevision(t *testing.T) {
	// These helpers must be defined in the editor primitive script itself.
	// A separate draft-recovery IIFE does not export them; the live browser
	// previously raised ReferenceError despite static tests finding callers.
	for _, function := range []string{"function matchesScope(result) {", "function validGeneration(value) {"} {
		if !strings.Contains(contentEditorPrimitivesJS, function) {
			t.Fatalf("editor primitive is missing its own helper %q", function)
		}
	}
	for _, key := range []string{"publication_operation_committed", "publication_outcome_unverified",
		"detail.kind === \"publication.apply\"", "detail.result_generation", "matchesScope(detail)", "validGeneration(detail.result_generation)"} {
		if !strings.Contains(contentEditorJS, key) {
			t.Fatalf("publication inspection lost %s", key)
		}
	}
	for _, localeName := range []string{"en", "fa"} {
		language := testProductLocalizer(t, localeName)
		message, err := language.Text("rixa", "publication_operation_committed", map[string]string{
			"Operation": "fixture-op", "Generation": "fixture-generation",
		})
		if err != nil || !strings.Contains(message, "fixture-generation") || !strings.Contains(message, "fixture-op") {
			t.Fatalf("%s committed generation message: %q %v", localeName, message, err)
		}
	}
}

func TestSelectedPublicationClientPreservesReviewAndRecoveryControls(t *testing.T) {
	for _, part := range []string{"[data-publication-apply] [data-confirm]", "[data-intent-revision-field]",
		"[data-review-generation]", "[data-publication-result]", "[data-operation-lookup-input]"} {
		if !strings.Contains(contentEditorJS, part) {
			t.Errorf("missing %s", part)
		}
	}
}
