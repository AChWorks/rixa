// SPDX-License-Identifier: MPL-2.0

package product

import (
	"context"
	"crypto/tls"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/AChWorks/achrix"
	"github.com/jackc/pgx/v5"
)

func testStaticPublicationRuntime(t *testing.T, ctx context.Context, runtime *Runtime, actor achrix.Principal, sourceAssetID string, fixture publicationFixture) {
	t.Helper()
	site := runtime.Sites["site-a"]
	if site == nil || site.Publication == nil {
		t.Fatal("site A publication service was not composed")
	}
	if runtime.Sites["site-b"] == nil || runtime.Sites["site-b"].Publication == nil {
		t.Fatal("site B publication service was not composed")
	}

	releaseSource, err := site.Editorial.acquirePublicationSource(ctx)
	if err != nil {
		t.Fatalf("acquire publication source gate: %v", err)
	}
	blockedApplyCtx, blockedApplyCancel := context.WithTimeout(ctx, 250*time.Millisecond)
	_, blockedApplyErr := site.Publication.Apply(blockedApplyCtx, actor, PublicationRequest{OperationID: newOperationID()})
	blockedApplyCancel()
	if !errors.Is(blockedApplyErr, context.DeadlineExceeded) {
		releaseSource()
		t.Fatalf("publication apply bypassed source gate: %v", blockedApplyErr)
	}
	blockedIntentCtx, blockedIntentCancel := context.WithTimeout(ctx, 250*time.Millisecond)
	_, blockedIntentErr := site.Editorial.SetPublicationIntent(blockedIntentCtx, actor, newOperationID(), fixture.PostID, 2, 4, 1)
	blockedIntentCancel()
	releaseSource()
	if !errors.Is(blockedIntentErr, context.DeadlineExceeded) {
		t.Fatalf("publication intent bypassed source gate: %v", blockedIntentErr)
	}

	request := func(host, path string) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequest(http.MethodGet, "https://"+host+path, nil)
		req.Host = host
		req.TLS = &tls.ConnectionState{}
		rec := httptest.NewRecorder()
		runtime.Handler.ServeHTTP(rec, req)
		return rec
	}

	if response := request("b.rixa.test:19443", "/"); response.Code != http.StatusNotFound {
		t.Fatalf("unpublished site B root status=%d want=404", response.Code)
	}

	controlPublicationOp := newOperationID()
	controlPublication, err := runtime.ControlService().ApplySitePublication(ctx, runtime.Control.AdminPrincipal, "site-b", PublicationRequest{
		OperationID: controlPublicationOp,
	})
	if err != nil || !validEditorialID(controlPublication.Generation) {
		t.Fatalf("control-origin publication for empty site B=%#v err=%v", controlPublication, err)
	}
	if response := request("b.rixa.test:19443", "/"); response.Code != http.StatusOK ||
		!strings.Contains(response.Body.String(), "No published posts.") {
		t.Fatalf("control-origin empty-site generation not served: status=%d body=%s", response.Code, response.Body.String())
	}
	controlOutcome, err := runtime.ControlService().SitePublicationOperation(ctx, runtime.Control.AdminPrincipal, "site-b", controlPublicationOp)
	if err != nil || controlOutcome.Generation != controlPublication.Generation {
		t.Fatalf("control-origin publication reconciliation=%#v err=%v", controlOutcome, err)
	}
	if _, err = runtime.ControlService().ApplySitePublication(ctx, runtime.Control.AdminPrincipal, "unknown", PublicationRequest{
		OperationID: newOperationID(), ExpectedGeneration: controlPublication.Generation,
	}); !errors.Is(err, achrix.ErrDenied) {
		t.Fatalf("control-origin publication reached unknown site: %v", err)
	}

	siteB := runtime.Sites["site-b"]
	originalRootSync := siteB.Publication.rootSync
	rootSyncAttempts := 0
	siteB.Publication.rootSync = func(path string) error {
		rootSyncAttempts++
		if rootSyncAttempts <= 2 {
			return errors.New("injected publication root sync failure")
		}
		return originalRootSync(path)
	}
	ambiguousOperationID := newOperationID()
	if _, err = runtime.ControlService().ApplySitePublication(ctx, runtime.Control.AdminPrincipal, "site-b", PublicationRequest{
		OperationID: ambiguousOperationID, ExpectedGeneration: controlPublication.Generation,
	}); !errors.Is(err, ErrEditorialUnknownOutcome) {
		t.Fatalf("post-rename sync failure did not return unknown outcome: %v", err)
	}
	if siteB.Publication.pendingDurability == nil || siteB.Publication.pendingDurability.generation == nil {
		t.Fatal("post-rename sync failure did not retain unresolved durability state")
	}
	ambiguousGeneration := siteB.Publication.pendingDurability.generation.manifest.Generation
	activeB := siteB.Publication.state.Load()
	if activeB == nil || len(activeB.generations) == 0 ||
		activeB.generations[0].manifest.Generation != controlPublication.Generation {
		t.Fatal("unresolved generation became active before durability confirmation")
	}
	if _, err = runtime.ControlService().SitePublicationOperation(ctx, runtime.Control.AdminPrincipal, "site-b", ambiguousOperationID); !errors.Is(err, ErrEditorialUnknownOutcome) {
		t.Fatalf("immediate reconciliation falsely reported commit: %v", err)
	}
	resolvedOutcome, err := runtime.ControlService().SitePublicationOperation(ctx, runtime.Control.AdminPrincipal, "site-b", ambiguousOperationID)
	if err != nil || resolvedOutcome.Generation != ambiguousGeneration {
		t.Fatalf("durability reconciliation=%#v err=%v want generation=%s", resolvedOutcome, err, ambiguousGeneration)
	}
	activeB = siteB.Publication.state.Load()
	if activeB == nil || len(activeB.generations) == 0 ||
		activeB.generations[0].manifest.Generation != ambiguousGeneration {
		t.Fatal("durably reconciled generation did not become active")
	}
	siteB.Publication.rootSync = originalRootSync

	operation := newOperationID()
	firstRequest := PublicationRequest{OperationID: operation, ContentID: fixture.PostID, Route: "/news/launch/"}
	first, err := site.Publication.Apply(ctx, actor, firstRequest)
	if err != nil {
		t.Fatalf("initial publication: %v", err)
	}
	if !validEditorialID(first.Generation) || first.Route != "/news/launch/" {
		t.Fatalf("initial publication result=%#v", first)
	}
	replay, err := site.Publication.Apply(ctx, actor, firstRequest)
	if err != nil || replay.Generation != first.Generation {
		t.Fatalf("publication replay=%#v err=%v", replay, err)
	}
	conflictingReplay := firstRequest
	conflictingReplay.Route = "/different/"
	if _, err = site.Publication.Apply(ctx, actor, conflictingReplay); !errors.Is(err, ErrEditorialConflict) {
		t.Fatalf("operation ID accepted different publication request: %v", err)
	}

	home := request("a.rixa.test:19443", "/")
	if home.Code != http.StatusOK {
		t.Fatalf("home status=%d body=%s", home.Code, home.Body.String())
	}
	for _, expected := range []string{
		"lang=\"fa\" dir=\"rtl\"",
		"<h1 dir=\"auto\">خانه</h1>",
		"<link rel=\"canonical\" href=\"https://a.rixa.test:19443/\">",
		"\"@type\":\"WebPage\"",
		"\"@type\":\"BreadcrumbList\"",
	} {
		if !strings.Contains(home.Body.String(), expected) {
			t.Fatalf("home missing %q", expected)
		}
	}
	if strings.Contains(home.Body.String(), "\"author\"") {
		t.Fatal("public schema invented an author")
	}

	post := request("a.rixa.test:19443", "/news/launch/")
	if post.Code != http.StatusOK {
		t.Fatalf("post status=%d body=%s", post.Code, post.Body.String())
	}
	if strings.Contains(post.Body.String(), "نسخه دوم English") {
		t.Fatal("unpublished newer draft leaked into first public generation")
	}
	defaultRoute := defaultPublicationRoute(ContentPost, fixture.PostID)
	if defaultRoute != "/news/launch/" {
		if response := request("a.rixa.test:19443", defaultRoute); response.Code != http.StatusNotFound {
			t.Fatalf("never-public default route leaked as alias: route=%s status=%d", defaultRoute, response.Code)
		}
	}
	for _, expected := range []string{
		"<h1 dir=\"auto\">نوشته English</h1>",
		"alt=\"تصویر اصلی\"",
		"\"@type\":\"BlogPosting\"",
		"\"@type\":\"BreadcrumbList\"",
		"<meta name=\"robots\" content=\"index, follow, max-snippet:-1\">",
	} {
		if !strings.Contains(post.Body.String(), expected) {
			t.Fatalf("post missing %q", expected)
		}
	}
	for _, forbidden := range []string{sourceAssetID, "data-media-id", "StorageRoot", "<script src="} {
		if strings.Contains(post.Body.String(), forbidden) {
			t.Fatalf("public post leaked/used forbidden value %q", forbidden)
		}
	}
	if strings.Count(post.Body.String(), "<script") != 1 || !strings.Contains(post.Body.String(), "type=\"application/ld+json\"") {
		t.Fatal("public page contains unexpected client script")
	}
	assetPath := extractPublishedAssetPath(t, post.Body.String())
	asset := request("a.rixa.test:19443", assetPath)
	if asset.Code != http.StatusOK || asset.Header().Get("Content-Type") != "image/png" {
		t.Fatalf("public image status=%d type=%q", asset.Code, asset.Header().Get("Content-Type"))
	}
	if asset.Header().Get("Cache-Control") != generatedFileCache ||
		strings.Contains(asset.Header().Get("Cache-Control"), "immutable") ||
		asset.Header().Get("X-Content-Type-Options") != "nosniff" || asset.Body.Len() == 0 {
		t.Fatal("public image did not use withdrawal-sensitive hardened serving")
	}
	if response := request("b.rixa.test:19443", assetPath); response.Code != http.StatusNotFound {
		t.Fatalf("site A public representation crossed into site B: status=%d", response.Code)
	}
	privateGuess := request("a.rixa.test:19443", "/assets/"+strings.ToLower(sourceAssetID))
	if privateGuess.Code != http.StatusNotFound {
		t.Fatalf("private source ID became public route: %d", privateGuess.Code)
	}

	sitemap := request("a.rixa.test:19443", "/sitemap.xml")
	if sitemap.Code != http.StatusOK ||
		!strings.Contains(sitemap.Body.String(), "https://a.rixa.test:19443/news/launch/") ||
		!strings.Contains(sitemap.Body.String(), "https://a.rixa.test:19443/") {
		t.Fatalf("sitemap incoherent: status=%d body=%s", sitemap.Code, sitemap.Body.String())
	}
	robots := request("a.rixa.test:19443", "/robots.txt")
	if robots.Code != http.StatusOK ||
		!strings.Contains(robots.Body.String(), "User-agent: TrainingBot") ||
		!strings.Contains(robots.Body.String(), "# purpose: ai-training") ||
		!strings.Contains(robots.Body.String(), "Sitemap: https://a.rixa.test:19443/sitemap.xml") {
		t.Fatalf("robots policy missing: status=%d body=%s", robots.Code, robots.Body.String())
	}
	if spoofed := request("unknown.rixa.test:19443", "/news/launch/"); spoofed.Code != http.StatusNotFound {
		t.Fatalf("spoofed host reached public site: %d", spoofed.Code)
	}

	beforeTheme := site.Publication.State(fixture.PostID, ContentPost)
	appearance, err := site.Editorial.Appearance(ctx, actor)
	if err != nil {
		t.Fatalf("appearance before theme publication: %v", err)
	}
	themeInput := appearance.input()
	themeInput.Theme = "light"
	updatedTheme, err := site.Editorial.SaveAppearance(ctx, actor, newOperationID(), appearance.Revision, themeInput)
	if err != nil || updatedTheme.Revision != appearance.Revision+1 {
		t.Fatalf("theme-only appearance update=%#v err=%v", updatedTheme, err)
	}
	themePublication, err := site.Publication.Apply(ctx, actor, PublicationRequest{
		OperationID: newOperationID(), ExpectedGeneration: first.Generation,
	})
	if err != nil || themePublication.Generation == first.Generation {
		t.Fatalf("theme-only publication=%#v err=%v", themePublication, err)
	}
	afterTheme := site.Publication.State(fixture.PostID, ContentPost)
	if afterTheme.CanonicalRoute != beforeTheme.CanonicalRoute ||
		afterTheme.ActiveRevision != beforeTheme.ActiveRevision ||
		!afterTheme.FirstPublishedAt.Equal(beforeTheme.FirstPublishedAt) ||
		!afterTheme.ModifiedAt.Equal(beforeTheme.ModifiedAt) {
		t.Fatalf("theme publication changed stable page identity/source revision: before=%#v after=%#v", beforeTheme, afterTheme)
	}
	themePage := request("a.rixa.test:19443", "/news/launch/")
	if themePage.Code != http.StatusOK ||
		!strings.Contains(themePage.Body.String(), "alt=\"تصویر اصلی\"") ||
		!strings.Contains(themePage.Body.String(), "<link rel=\"canonical\" href=\"https://a.rixa.test:19443/news/launch/\">") ||
		!strings.Contains(themePage.Body.String(), "data-theme=\"light\"") {
		t.Fatalf("theme-only publication lost stable meaning: status=%d body=%s", themePage.Code, themePage.Body.String())
	}

	correctedBody := "<p>corrected public body</p><figure data-media-id=\"" + sourceAssetID + "\"><figcaption>شرح اصلاح‌شده</figcaption></figure>"
	corrected, err := site.Editorial.Save(ctx, actor, newOperationID(), fixture.PostID, 2, "نسخه اصلاح‌شده", correctedBody)
	if err != nil || corrected.Revision != 3 {
		t.Fatalf("correct public revision=%#v err=%v", corrected, err)
	}
	intent, err := site.Editorial.SetPublicationIntent(ctx, actor, newOperationID(), fixture.PostID, 3, 4, 3)
	if err != nil || intent.PublicationIntentVersion != 5 {
		t.Fatalf("corrected publication intent=%#v err=%v", intent, err)
	}
	second, err := site.Publication.Apply(ctx, actor, PublicationRequest{
		OperationID: newOperationID(), ExpectedGeneration: themePublication.Generation,
		ContentID: fixture.PostID, Route: "/news/launch/",
	})
	if err != nil || second.Generation == themePublication.Generation {
		t.Fatalf("corrected publication=%#v err=%v", second, err)
	}
	correctedPage := request("a.rixa.test:19443", "/news/launch/")
	if correctedPage.Code != http.StatusOK || !strings.Contains(correctedPage.Body.String(), "corrected public body") {
		t.Fatalf("corrected page not active: status=%d body=%s", correctedPage.Code, correctedPage.Body.String())
	}

	third, err := site.Publication.Apply(ctx, actor, PublicationRequest{
		OperationID: newOperationID(), ExpectedGeneration: second.Generation,
		ContentID: fixture.PostID, Route: "/updates/",
	})
	if err != nil {
		t.Fatalf("reroute publication: %v", err)
	}
	oldRoute := request("a.rixa.test:19443", "/news/launch/")
	if oldRoute.Code != http.StatusPermanentRedirect || oldRoute.Header().Get("Location") != "/updates/" {
		t.Fatalf("old route status=%d location=%q", oldRoute.Code, oldRoute.Header().Get("Location"))
	}
	if current := request("a.rixa.test:19443", "/updates/"); current.Code != http.StatusOK {
		t.Fatalf("new route status=%d", current.Code)
	}

	cleared, err := site.Editorial.ClearPublicationIntent(ctx, actor, newOperationID(), fixture.PostID, 3, 5)
	if err != nil || cleared.PublicationIntentVersion != 6 || cleared.PublicationIntentRevision != 0 {
		t.Fatalf("clear publication intent=%#v err=%v", cleared, err)
	}
	withdrawOperation := newOperationID()
	fourth, err := site.Publication.Apply(ctx, actor, PublicationRequest{
		OperationID: withdrawOperation, ExpectedGeneration: third.Generation,
		ContentID: fixture.PostID, Route: "/updates/",
	})
	if err != nil {
		t.Fatalf("withdraw publication: %v", err)
	}
	for _, route := range []string{"/updates/", "/news/launch/"} {
		withdrawn := request("a.rixa.test:19443", route)
		if withdrawn.Code != http.StatusGone || !strings.Contains(withdrawn.Header().Get("X-Robots-Tag"), "noindex") {
			t.Fatalf("withdrawn route %s status=%d", route, withdrawn.Code)
		}
	}
	withdrawnSitemap := request("a.rixa.test:19443", "/sitemap.xml")
	if strings.Contains(withdrawnSitemap.Body.String(), "/updates/") || strings.Contains(withdrawnSitemap.Body.String(), "/news/launch/") {
		t.Fatal("withdrawn post remained in sitemap")
	}
	if withdrawnAsset := request("a.rixa.test:19443", assetPath); withdrawnAsset.Code != http.StatusNotFound {
		t.Fatalf("withdrawn image representation remained public: status=%d", withdrawnAsset.Code)
	}

	badBody := "<figure data-media-id=\"" + sourceAssetID + "\"><figcaption></figcaption></figure>"
	bad, err := site.Editorial.Create(ctx, actor, newOperationID(), ContentPost, "unsafe image description", badBody)
	if err != nil {
		t.Fatalf("create unsafe-public fixture: %v", err)
	}
	badIntent, err := site.Editorial.SetPublicationIntent(ctx, actor, newOperationID(), bad.ID, 1, 1, 1)
	if err != nil || badIntent.PublicationIntentVersion != 2 {
		t.Fatalf("unsafe-public intent=%#v err=%v", badIntent, err)
	}
	if _, err = site.Publication.Apply(ctx, actor, PublicationRequest{
		OperationID: newOperationID(), ExpectedGeneration: fourth.Generation,
		ContentID: bad.ID, Route: defaultPublicationRoute(ContentPost, bad.ID),
	}); !errors.Is(err, ErrEditorialInvalid) {
		t.Fatalf("public image without useful description was accepted: %v", err)
	}
	if state := site.Publication.State(fixture.HomePageID, ContentPage); state.Generation != fourth.Generation {
		t.Fatalf("failed generation replaced active state: %#v", state)
	}
	if _, err = site.Editorial.ClearPublicationIntent(ctx, actor, newOperationID(), bad.ID, 1, 2); err != nil {
		t.Fatalf("cleanup unsafe-public intent: %v", err)
	}

	// Control-origin publication uses the same retained site snapshot, but
	// binds a reviewed content head/intent and one currently authorized site.
	control := runtime.ControlService()
	controlActor := runtime.Control.AdminPrincipal
	selectedHead, err := control.HeadSiteContent(ctx, controlActor, "site-a", fixture.PostID)
	if err != nil || selectedHead.Revision != 3 || selectedHead.PublicationIntentVersion != 6 || selectedHead.PublicationIntentRevision != 0 {
		t.Fatalf("selected withdrawn head=%#v err=%v", selectedHead, err)
	}
	selectedState, err := control.SitePublicationState(ctx, controlActor, "site-a", fixture.PostID, ContentPost)
	if err != nil || selectedState.Generation != fourth.Generation || selectedState.ActiveRevision != 0 {
		t.Fatalf("selected withdrawn state=%#v err=%v", selectedState, err)
	}
	otherGeneration := siteB.Publication.State(fixture.PostID, ContentPost).Generation
	if _, err = control.SitePublicationState(ctx, runtime.Sites["site-a"].adminPrincipal, "site-a", fixture.PostID, ContentPost); !errors.Is(err, achrix.ErrDenied) {
		t.Fatalf("site administrator read control origin: %v", err)
	}
	selectedIntent, err := control.SetSitePublicationIntent(ctx, controlActor, "site-a", newOperationID(), fixture.PostID, selectedHead.Revision, selectedHead.PublicationIntentVersion, 3)
	if err != nil || selectedIntent.PublicationIntentVersion != 7 || selectedIntent.PublicationIntentRevision != 3 {
		t.Fatalf("delegated selected intent=%#v err=%v", selectedIntent, err)
	}
	selectedRequest := PublicationRequest{
		OperationID: newOperationID(), ExpectedGeneration: fourth.Generation,
		ContentID: fixture.PostID, Route: "/news/control/",
		ExpectedHead: 3, ExpectedIntentVersion: 7, ExpectedIntentRevision: 3,
	}
	stale := selectedRequest
	stale.OperationID = newOperationID()
	stale.ExpectedHead = 2
	if _, err = control.ApplySitePublication(ctx, controlActor, "site-a", stale); !errors.Is(err, ErrEditorialConflict) {
		t.Fatalf("stale reviewed head admitted: %v", err)
	}
	stale = selectedRequest
	stale.OperationID = newOperationID()
	stale.ExpectedIntentVersion = 6
	if _, err = control.ApplySitePublication(ctx, controlActor, "site-a", stale); !errors.Is(err, ErrEditorialConflict) {
		t.Fatalf("stale reviewed intent admitted: %v", err)
	}
	if _, err = control.ApplySitePublication(ctx, runtime.Sites["site-a"].adminPrincipal, "site-a", selectedRequest); !errors.Is(err, achrix.ErrDenied) {
		t.Fatalf("site admin obtained control publication grant: %v", err)
	}
	selectedResult, err := control.ApplySitePublication(ctx, controlActor, "site-a", selectedRequest)
	if err != nil || !validEditorialID(selectedResult.Generation) || selectedResult.Route != "/news/control/" {
		t.Fatalf("selected control publication=%#v err=%v", selectedResult, err)
	}
	selectedOperation, err := control.SitePublicationOperation(ctx, controlActor, "site-a", selectedRequest.OperationID)
	if err != nil || selectedOperation.Generation != selectedResult.Generation || selectedOperation.ContentID != fixture.PostID {
		t.Fatalf("selected control operation=%#v err=%v", selectedOperation, err)
	}
	selectedReplay, err := control.ApplySitePublication(ctx, controlActor, "site-a", selectedRequest)
	if err != nil || selectedReplay.Generation != selectedResult.Generation {
		t.Fatalf("lost selected acknowledgement replay=%#v err=%v", selectedReplay, err)
	}
	altered := selectedRequest
	altered.Route = "/news/replaced/"
	if _, err = control.ApplySitePublication(ctx, controlActor, "site-a", altered); !errors.Is(err, ErrEditorialConflict) {
		t.Fatalf("same ID accepted changed reviewed request: %v", err)
	}
	if response := request("a.rixa.test:19443", "/news/control/"); response.Code != http.StatusOK {
		t.Fatalf("selected public route not active: %d", response.Code)
	}
	if response := request("b.rixa.test:19443", "/news/control/"); response.Code != http.StatusNotFound {
		t.Fatalf("selected control publication crossed sites: %d", response.Code)
	}
	selectedCleared, err := control.ClearSitePublicationIntent(ctx, controlActor, "site-a", newOperationID(), fixture.PostID, 3, 7)
	if err != nil || selectedCleared.PublicationIntentVersion != 8 || selectedCleared.PublicationIntentRevision != 0 {
		t.Fatalf("selected withdrawal intent=%#v err=%v", selectedCleared, err)
	}
	withdraw := PublicationRequest{
		OperationID: newOperationID(), ExpectedGeneration: selectedResult.Generation,
		ContentID: fixture.PostID, Route: "/news/control/",
		ExpectedHead: 3, ExpectedIntentVersion: 8, ExpectedIntentRevision: 0,
	}
	withdrawnByControl, err := control.ApplySitePublication(ctx, controlActor, "site-a", withdraw)
	if err != nil || withdrawnByControl.Generation == selectedResult.Generation {
		t.Fatalf("selected control withdrawal=%#v err=%v", withdrawnByControl, err)
	}
	if response := request("a.rixa.test:19443", "/news/control/"); response.Code != http.StatusGone {
		t.Fatalf("selected withdrawn route still public: %d", response.Code)
	}
	if siteB.Publication.State(fixture.PostID, ContentPost).Generation != otherGeneration {
		t.Fatal("selected site A mutation changed site B's public generation")
	}

	resolvedSite := runtime.Config.Sites[0]
	reloaded, err := newPublicationService(resolvedSite, site.App, site.Editorial, site.Media)
	if err != nil {
		t.Fatalf("rebuild publication service: %v", err)
	}
	if err = reloaded.Ready(); err != nil {
		t.Fatalf("reload active generation: %v", err)
	}
	operationResult, err := reloaded.Operation(ctx, actor, withdrawOperation)
	if err != nil || operationResult.Generation != fourth.Generation {
		t.Fatalf("restart operation reconciliation=%#v err=%v", operationResult, err)
	}
	req := httptest.NewRequest(http.MethodGet, "https://a.rixa.test:19443/", nil)
	req.Host = "a.rixa.test:19443"
	req.TLS = &tls.ConnectionState{}
	rec := httptest.NewRecorder()
	reloaded.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("reloaded static generation status=%d", rec.Code)
	}
	testReviewedHeadSavePublicationOrdering(t, ctx, runtime, actor)
}

func extractPublishedAssetPath(t *testing.T, body string) string {
	t.Helper()
	const prefix = "<img src=\""
	start := strings.Index(body, prefix)
	if start < 0 {
		t.Fatal("public image element missing")
	}
	start += len(prefix)
	end := strings.Index(body[start:], "\"")
	if end < 0 {
		t.Fatal("public image src is malformed")
	}
	path := body[start : start+end]
	if !strings.HasPrefix(path, "/assets/") {
		t.Fatalf("public image path=%q", path)
	}
	return path
}

// Test the two possible orderings against real PostgreSQL and a real public
// generation: a saved-head writer that wins the gate invalidates a reviewed
// Apply; an Apply that owns the gate prevents a later Save from committing
// until its public generation has durably activated.
func testReviewedHeadSavePublicationOrdering(t *testing.T, ctx context.Context, runtime *Runtime, siteActor achrix.Principal) {
	t.Helper()
	a := runtime.Sites["site-a"]
	b := runtime.Sites["site-b"]
	controlActor := runtime.Control.AdminPrincipal
	control := runtime.ControlService()
	post, err := a.Editorial.Create(ctx, siteActor, newOperationID(), ContentPost,
		"Reviewed public head", "<p>Initial public revision</p>")
	if err != nil {
		t.Fatal(err)
	}
	intended, err := control.SetSitePublicationIntent(ctx, controlActor, "site-a", newOperationID(),
		post.ID, post.Revision, post.PublicationIntentVersion, 1)
	if err != nil || intended.PublicationIntentRevision != 1 {
		t.Fatalf("selected review intent: %#v %v", intended, err)
	}
	priorGeneration := a.Publication.State(post.ID, ContentPost).Generation
	request := PublicationRequest{
		OperationID: newOperationID(), ExpectedGeneration: priorGeneration,
		ContentID: post.ID, Route: defaultPublicationRoute(ContentPost, post.ID),
		ExpectedHead: 1, ExpectedIntentVersion: intended.PublicationIntentVersion, ExpectedIntentRevision: 1,
	}
	// Order A: Save holds the publication gate while its row is locked by a
	// separate SQL transaction. Apply must wait and see the committed new head.
	conn, err := pgx.Connect(ctx, runtime.Config.Sites[0].DSN)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close(context.Background())
	tx, err := conn.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var head int64
	if err = tx.QueryRow(ctx, "SELECT head_revision FROM rixa.content_items WHERE id=$1 FOR UPDATE", post.ID).Scan(&head); err != nil || head != 1 {
		_ = tx.Rollback(context.Background())
		t.Fatalf("lock reviewed content head=%d err=%v", head, err)
	}
	type saved struct {
		revision ContentRevision
		err      error
	}
	saveFirst := make(chan saved, 1)
	go func() {
		revision, e := a.Editorial.Save(ctx, siteActor, newOperationID(), post.ID, 1,
			"Saved first", "<p>New unmarked draft</p>")
		saveFirst <- saved{revision, e}
	}()
	deadline := time.NewTimer(3 * time.Second)
	ticker := time.NewTicker(5 * time.Millisecond)
	gateAcquired := false
waitForSave:
	for !gateAcquired {
		select {
		case <-ticker.C:
			gateAcquired = len(a.Editorial.publicationGate) == 1
		case <-deadline.C:
			break waitForSave
		case <-ctx.Done():
			break waitForSave
		}
	}
	ticker.Stop()
	deadline.Stop()
	if !gateAcquired {
		_ = tx.Rollback(context.Background())
		t.Fatal("first Save did not acquire publication source gate before SQL row lock")
	}
	applyFirst := make(chan error, 1)
	go func() {
		_, e := control.ApplySitePublication(ctx, controlActor, "site-a", request)
		applyFirst <- e
	}()
	if err = tx.Rollback(ctx); err != nil {
		t.Fatal(err)
	}
	first := <-saveFirst
	if first.err != nil || first.revision.Revision != 2 {
		t.Fatalf("Save-first revision: %#v %v", first.revision, first.err)
	}
	if err = <-applyFirst; !errors.Is(err, ErrEditorialConflict) {
		t.Fatalf("stale reviewed Apply after committed Save: %v", err)
	}
	if current := a.Publication.State(post.ID, ContentPost); current.Generation != priorGeneration {
		t.Fatalf("stale Apply changed public generation: %#v", current)
	}
	if _, e := control.SitePublicationOperation(ctx, controlActor, "site-a", request.OperationID); !errors.Is(e, ErrEditorialNotFound) {
		t.Fatalf("stale Apply incorrectly recorded a public commit: %v", e)
	}

	// Order B: hold Apply at the durable root sync after the reviewed head
	// and snapshot. Save must wait until that whole public decision finishes.
	originalSync := a.Publication.rootSync
	entered := make(chan struct{}, 1)
	release := make(chan struct{})
	released := false
	defer func() {
		if !released {
			close(release)
		}
		a.Publication.rootSync = originalSync
	}()
	a.Publication.rootSync = func(path string) error {
		select {
		case entered <- struct{}{}:
		default:
		}
		select {
		case <-release:
		case <-ctx.Done():
			return ctx.Err()
		}
		return originalSync(path)
	}
	request.OperationID = newOperationID()
	request.ExpectedHead = 2 // deliberately publishing historical revision 1
	type applied struct {
		result PublicationResult
		err    error
	}
	appliedFirst := make(chan applied, 1)
	go func() {
		result, e := control.ApplySitePublication(ctx, controlActor, "site-a", request)
		appliedFirst <- applied{result, e}
	}()
	select {
	case <-entered:
	case outcome := <-appliedFirst:
		t.Fatalf("Apply ended before synchronization barrier: %#v", outcome)
	case <-time.After(8 * time.Second):
		t.Fatal("Apply never reached durable activation barrier")
	}
	saveAfter := make(chan saved, 1)
	go func() {
		rev, e := a.Editorial.Save(ctx, siteActor, newOperationID(), post.ID, 2,
			"Later draft", "<p>New private source after publication</p>")
		saveAfter <- saved{rev, e}
	}()
	select {
	case outcome := <-saveAfter:
		t.Fatalf("later Save bypassed active publication gate: %#v", outcome)
	case <-time.After(120 * time.Millisecond):
	}
	currentHead, e := a.Editorial.Head(ctx, siteActor, post.ID)
	if e != nil || currentHead.Revision != 2 {
		t.Fatalf("reviewed head changed before Apply finished: %#v %v", currentHead, e)
	}
	// Intent and appearance still share the same fence.
	for _, op := range []struct {
		name string
		call func(context.Context) error
	}{
		{"intent", func(c context.Context) error {
			_, e := a.Editorial.SetPublicationIntent(c, siteActor, newOperationID(), post.ID, 2, intended.PublicationIntentVersion, 1)
			return e
		}},
		{"appearance", func(c context.Context) error {
			appearance, e := a.Editorial.Appearance(c, siteActor)
			if e != nil {
				return e
			}
			_, e = a.Editorial.SaveAppearance(c, siteActor, newOperationID(), appearance.Revision, appearance.input())
			return e
		}},
	} {
		bounded, cancel := context.WithTimeout(ctx, 160*time.Millisecond)
		e := op.call(bounded)
		cancel()
		if !errors.Is(e, context.DeadlineExceeded) {
			t.Fatalf("%s mutation bypassed source gate: %v", op.name, e)
		}
	}
	// Another site's Save must not wait for site A's publication gate.
	bDraft, e := b.Editorial.Create(ctx, b.adminPrincipal, newOperationID(), ContentPost, "Independent B", "<p>site B</p>")
	if e != nil {
		t.Fatal(e)
	}
	bCtx, cancelB := context.WithTimeout(ctx, 3*time.Second)
	other, e := b.Editorial.Save(bCtx, b.adminPrincipal, newOperationID(), bDraft.ID, 1,
		"Site B still responsive", "<p>unrelated</p>")
	cancelB()
	if e != nil || other.Revision != 2 {
		t.Fatalf("site B Save blocked by site A publication: %#v %v", other, e)
	}
	close(release)
	released = true
	result := <-appliedFirst
	if result.err != nil || !validEditorialID(result.result.Generation) {
		t.Fatalf("Apply-first publication: %#v %v", result.result, result.err)
	}
	later := <-saveAfter
	if later.err != nil || later.revision.Revision != 3 {
		t.Fatalf("later Save did not complete after Apply: %#v %v", later.revision, later.err)
	}
	state := a.Publication.State(post.ID, ContentPost)
	if state.Generation != result.result.Generation || state.ActiveRevision != 1 {
		t.Fatalf("historical published revision or generation changed: %#v", state)
	}
	replay, e := control.ApplySitePublication(ctx, controlActor, "site-a", request)
	if e != nil || replay.Generation != result.result.Generation {
		t.Fatalf("lost-ack replay after later Save: %#v %v", replay, e)
	}
	changed := request
	changed.ExpectedHead = 3
	if _, e = control.ApplySitePublication(ctx, controlActor, "site-a", changed); !errors.Is(e, ErrEditorialConflict) {
		t.Fatalf("same operation ID accepted new reviewed head: %v", e)
	}
}
