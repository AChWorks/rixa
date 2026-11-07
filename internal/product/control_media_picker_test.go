// SPDX-License-Identifier: MPL-2.0

package product

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"testing/fstest"

	"github.com/AChWorks/achrix"
	shell "github.com/AChWorks/achrix/admin"
	"github.com/AChWorks/achrix/admin/localize"
	"github.com/AChWorks/achrix/identity"
	"github.com/AChWorks/achrix/media"
	mediaadmin "github.com/AChWorks/achrix/media/admin"
)

const pickerAssetA = "AAAAAAAAAAAAAAAAAAAAAAAAAA"
const pickerAssetB = "BBBBBBBBBBBBBBBBBBBBBBBBBB"
const pickerAssetC = "CCCCCCCCCCCCCCCCCCCCCCCCCC"

type pickerTestManager struct {
	mu                sync.Mutex
	calls             []string
	selectionError    error
	revokeAfterSelect bool
	revoked           atomic.Bool
}

func (m *pickerTestManager) record(action, site string, actor achrix.Principal) {
	m.mu.Lock()
	m.calls = append(m.calls, fmt.Sprintf("%s:%s:%s", action, site, actor))
	m.mu.Unlock()
}
func (m *pickerTestManager) CreateSiteAccount(context.Context, achrix.Principal, string, string, string) (identity.Account, error) {
	return identity.Account{}, achrix.ErrDenied
}
func (m *pickerTestManager) AuthorizeSiteMedia(_ context.Context, actor achrix.Principal, site string) error {
	m.record("authorize", site, actor)
	if m.revoked.Load() || actor != "control-actor" || site != "site-a" && site != "site-b" {
		return achrix.ErrDenied
	}
	return nil
}
func (m *pickerTestManager) ListSiteMedia(_ context.Context, actor achrix.Principal, site, cursor string, limit int) (media.Page, error) {
	m.record("list", site, actor)
	if actor != "control-actor" || limit != 25 || cursor != "" && cursor != strings.Repeat("C", 36) {
		return media.Page{}, achrix.ErrDenied
	}
	if site == "site-a" {
		return media.Page{Assets: []media.Asset{
			{ID: pickerAssetA, Filename: "a.png", MIME: "image/png", State: "ready", SHA256: "private-fingerprint"},
			{ID: pickerAssetC, Filename: "not-ready.png", MIME: "image/png", State: "pending"},
			{ID: pickerAssetB, Filename: "foreign.png", MIME: "image/png", State: "ready"},
		}, NextCursor: strings.Repeat("C", 36)}, nil
	}
	if site == "site-b" {
		return media.Page{Assets: []media.Asset{{ID: pickerAssetB, Filename: "b.png", MIME: "image/png", State: "ready"}}}, nil
	}
	return media.Page{}, achrix.ErrDenied
}
func (m *pickerTestManager) SelectSiteMedia(_ context.Context, actor achrix.Principal, site, id string) (media.Selection, error) {
	m.record("select:"+id, site, actor)
	if m.revokeAfterSelect {
		m.revoked.Store(true)
	}
	if m.selectionError != nil {
		return media.Selection{}, m.selectionError
	}
	if actor != "control-actor" {
		return media.Selection{}, achrix.ErrDenied
	}
	if site == "site-a" && id == pickerAssetA {
		return media.Selection{ID: id, Filename: "a.png", MIME: "image/png", Size: 7, Width: 1, Height: 1, Revision: math.MaxInt64, State: "ready"}, nil
	}
	if site == "site-b" && id == pickerAssetB {
		return media.Selection{ID: id, Filename: "b.png", MIME: "image/png", Size: 8, Width: 1, Height: 1, Revision: 2, State: "ready"}, nil
	}
	return media.Selection{}, media.ErrNotFound
}

func pickerTestLocalizer(t *testing.T, language string) *localize.Localizer {
	t.Helper()
	makeDocument := func(lang, dir string) []byte {
		messages := map[string]any{}
		for _, key := range pickerMessageKeys {
			messages["picker."+key] = map[string]string{"other": lang + " " + key}
		}
		messages["picker.selected"] = map[string]string{"other": "Selected: {Filename}"}
		document, err := json.Marshal(map[string]any{
			"schema": 1, "locale": lang, "name": lang, "direction": dir, "domain": "media", "messages": messages,
		})
		if err != nil {
			t.Fatal(err)
		}
		return document
	}
	source := fstest.MapFS{
		"media.en.json": &fstest.MapFile{Data: makeDocument("en", "ltr"), Mode: fs.FileMode(0o644)},
		"media.fa.json": &fstest.MapFile{Data: makeDocument("fa", "rtl"), Mode: fs.FileMode(0o644)},
	}
	catalog, err := localize.Load(source)
	if err != nil {
		t.Fatal(err)
	}
	l, err := catalog.For(language)
	if err != nil {
		t.Fatal(err)
	}
	return l
}
func pickerTestRequest(t *testing.T, m *pickerTestManager, principal achrix.Principal, language, method, path string) *httptest.ResponseRecorder {
	t.Helper()
	w := httptest.NewRecorder()
	r := httptest.NewRequest(method, "https://control.test"+path, nil)
	lang := pickerTestLocalizer(t, language)
	view := shell.Request{Principal: principal, Localizer: lang, Language: language}
	newControlSurface(nil, m).Handler(w, r, view)
	return w
}
func pickerTestJSON(t *testing.T, r *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var result map[string]any
	if err := json.Unmarshal(r.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	return result
}

func TestControlPickerExplicitSiteAndPrivateMetadata(t *testing.T) {
	m := &pickerTestManager{}
	path := "/site/site-a/media/picker/page"
	a := pickerTestRequest(t, m, "control-actor", "en", "GET", path)
	if a.Code != http.StatusOK {
		t.Fatalf("A page %d: %s", a.Code, a.Body.String())
	}
	data := pickerTestJSON(t, a)
	assets, ok := data["assets"].([]any)
	if !ok || len(assets) != 1 {
		t.Fatalf("A page includes foreign/pending media: %v", data)
	}
	asset, ok := assets[0].(map[string]any)
	if !ok || asset["id"] != pickerAssetA || asset["revision"] != "9223372036854775807" {
		t.Fatalf("selection metadata/revision lost: %v", assets[0])
	}
	if data["next_cursor"] != strings.Repeat("C", 36) {
		t.Fatalf("missing cursor: %v", data)
	}
	if strings.Contains(a.Body.String(), "private-fingerprint") || strings.Contains(a.Body.String(), "StorageRoot") || strings.Contains(a.Body.String(), "foreign") {
		t.Fatalf("private/foreign media exposed: %s", a.Body.String())
	}
	b := pickerTestRequest(t, m, "control-actor", "fa", "GET", "/site/site-b/media/picker/page")
	bAssets := pickerTestJSON(t, b)["assets"].([]any)
	if b.Code != http.StatusOK || len(bAssets) != 1 || bAssets[0].(map[string]any)["id"] != pickerAssetB {
		t.Fatalf("site B page not isolated: %s", b.Body.String())
	}
	// A tab with another site may not reuse an opaque ID from the first tab.
	foreign := pickerTestRequest(t, m, "control-actor", "en", "GET", "/site/site-b/media/picker/asset/"+pickerAssetA)
	if foreign.Code != http.StatusNotFound || pickerTestJSON(t, foreign)["code"] != "not_found" {
		t.Fatalf("foreign exact ID was visible: %d %s", foreign.Code, foreign.Body.String())
	}
	selected := pickerTestRequest(t, m, "control-actor", "en", "GET", "/site/site-a/media/picker/asset/"+pickerAssetA)
	if selected.Code != http.StatusOK || pickerTestJSON(t, selected)["asset"].(map[string]any)["revision"] != "9223372036854775807" || !strings.Contains(selected.Body.String(), "Selected: a.png") {
		t.Fatalf("exact selected metadata failed: %d %s", selected.Code, selected.Body.String())
	}
	for _, language := range []string{"en", "fa"} {
		messages := pickerTestRequest(t, m, "control-actor", language, "GET", "/site/site-a/media/picker/messages")
		value := pickerTestJSON(t, messages)
		if messages.Code != http.StatusOK || value["locale"] != language || len(value["messages"].(map[string]any)) != len(pickerMessageKeys) {
			t.Fatalf("localized catalog %s: %d %s", language, messages.Code, messages.Body.String())
		}
	}
}

func TestControlPickerRechecksCurrentPrincipalAndRejectsAlternateTargets(t *testing.T) {
	m := &pickerTestManager{}
	for _, site := range []string{"site-a", "site-b", "disabled", "unknown"} {
		for _, principal := range []achrix.Principal{"someone-else", "control-actor"} {
			if principal == "control-actor" && site != "disabled" && site != "unknown" {
				continue
			}
			r := pickerTestRequest(t, m, principal, "en", "GET", "/site/"+site+"/media/picker/messages")
			if r.Code != http.StatusForbidden || pickerTestJSON(t, r)["code"] != "permission_denied" {
				t.Fatalf("site %s principal %s: %d %s", site, principal, r.Code, r.Body.String())
			}
		}
	}
	baseline := len(m.calls)
	for _, path := range []string{
		"/site/site-a/media/picker/", "/site/site-a/media/picker/page/too-short",
		"/site/site-a/media/picker/page/" + strings.Repeat("X", 37),
		"/site/site-a/media/picker/asset/nope",
		"/site/site-a/media/picker/asset/" + pickerAssetA + "/more",
		"/site/../media/picker/messages", "/site/site-a/media/picker/messages?site=site-b",
		"/site/site-a/media/picker/messages/", "/site/site-a/media/picker/page/../messages",
		"/site/site-a/media/picker/page/%2f",
	} {
		r := pickerTestRequest(t, m, "control-actor", "en", "GET", path)
		if r.Code != http.StatusNotFound {
			t.Fatalf("malformed path %q: %d %s", path, r.Code, r.Body.String())
		}
	}
	if len(m.calls) != baseline {
		t.Fatalf("malformed paths invoked service: %v", m.calls[baseline:])
	}
	r := pickerTestRequest(t, m, "control-actor", "en", "POST", "/site/site-a/media/picker/page")
	if r.Code != http.StatusMethodNotAllowed || r.Header().Get("Allow") != "GET" {
		t.Fatalf("mutation method admitted: %d", r.Code)
	}
}

func TestControlPickerExactSelectFailureFailsClosed(t *testing.T) {
	for _, failure := range []error{media.ErrUnavailable, errors.New("temporary database failure")} {
		m := &pickerTestManager{selectionError: failure}
		r := pickerTestRequest(t, m, "control-actor", "en", "GET", "/site/site-a/media/picker/page")
		if r.Code != http.StatusServiceUnavailable || pickerTestJSON(t, r)["code"] != "unavailable" {
			t.Fatalf("partial page exposed on authorization failure: %d %s", r.Code, r.Body.String())
		}
	}
	m := &pickerTestManager{selectionError: achrix.ErrDenied}
	r := pickerTestRequest(t, m, "control-actor", "en", "GET", "/site/site-a/media/picker/page")
	if r.Code != http.StatusOK || len(pickerTestJSON(t, r)["assets"].([]any)) != 0 {
		t.Fatalf("denied choice not filtered: %d %s", r.Code, r.Body.String())
	}
}

func TestControlPickerRevokedGrantDuringPageFailsClosed(t *testing.T) {
	m := &pickerTestManager{revokeAfterSelect: true}
	result := pickerTestRequest(t, m, "control-actor", "en", "GET", "/site/site-a/media/picker/page")
	if result.Code != http.StatusForbidden || pickerTestJSON(t, result)["code"] != "permission_denied" {
		t.Fatalf("revoked grant still disclosed a collected page: %d %s", result.Code, result.Body.String())
	}
}

// Concurrent Control tabs have immutable site arguments; the manager never
// consumes an ambient selected-site variable or a process-global cursor.
func TestControlPickerConcurrentSiteTabs(t *testing.T) {
	m := &pickerTestManager{}
	const requests = 20
	var wg sync.WaitGroup
	errs := make(chan error, requests)
	for i := 0; i < requests; i++ {
		site, id := "site-a", pickerAssetA
		if i%2 == 1 {
			site, id = "site-b", pickerAssetB
		}
		wg.Add(1)
		go func(site, id string) {
			defer wg.Done()
			w := httptest.NewRecorder()
			r := httptest.NewRequest("GET", "https://control.test/site/"+site+"/media/picker/page", nil)
			view := shell.Request{Principal: "control-actor"}
			newControlSurface(nil, m).Handler(w, r, view)
			var body controlPickerPage
			if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
				errs <- err
				return
			}
			if w.Code != 200 || len(body.Assets) != 1 || body.Assets[0].ID != id {
				errs <- fmt.Errorf("%s got %d %s", site, w.Code, w.Body.String())
			}
		}(site, id)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Error(err)
	}
}

func TestControlPickerServesExactOfficialScriptWithoutMediaService(t *testing.T) {
	manager := &pickerTestManager{}
	view := shell.Request{Principal: "control-actor", Language: "en"}
	req := httptest.NewRequest(http.MethodGet, "https://control.test/picker.js", nil)
	actual := httptest.NewRecorder()
	newControlSurface(nil, manager).Handler(actual, req, view)
	if actual.Code != http.StatusOK || !strings.HasPrefix(actual.Header().Get("Content-Type"), "text/javascript") ||
		actual.Header().Get("Cache-Control") != "private, no-store" || len(actual.Body.Bytes()) < 1000 {
		t.Fatalf("Control script: %d %v", actual.Code, actual.Header())
	}
	official, err := mediaadmin.New(&media.Service{})
	if err != nil {
		t.Fatal(err)
	}
	expected := httptest.NewRecorder()
	official.Handler(expected, req, view)
	if expected.Code != http.StatusOK || !bytes.Equal(actual.Body.Bytes(), expected.Body.Bytes()) {
		t.Fatal("Control served modified or copied Picker instead of the official embedded Media asset")
	}
	if len(manager.calls) != 0 {
		t.Fatal("serving static Picker script reached private Media service")
	}
	for _, target := range []string{"/picker/page", "/read/" + pickerAssetA, "/media.js"} {
		response := pickerTestRequest(t, manager, "control-actor", "en", http.MethodGet, target)
		if response.Code != http.StatusNotFound {
			t.Fatalf("non-allowlisted Media route %q: %d", target, response.Code)
		}
	}
}
