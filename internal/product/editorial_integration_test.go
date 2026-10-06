// SPDX-License-Identifier: MPL-2.0

package product

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/AChWorks/achrix"
	"github.com/jackc/pgx/v5"
)

func testEditorialRuntime(t *testing.T, ctx context.Context, runtime *Runtime, aAdmin, bAdmin achrix.Principal, assetID string, assetRevision int64, dsnA, dsnB string) {
	t.Helper()
	a := runtime.Sites["site-a"].Editorial
	b := runtime.Sites["site-b"].Editorial
	if a == nil || b == nil {
		t.Fatal("editorial services were not composed")
	}

	appearanceA, err := a.Appearance(ctx, aAdmin)
	if err != nil {
		t.Fatalf("site A default appearance: %v", err)
	}
	if appearanceA.Revision != 1 || appearanceA.SiteTitle != "site-a" || appearanceA.SiteLanguage != "en" || appearanceA.HomeMode != "latest_posts" || appearanceA.Theme != "light" || !appearanceA.HeaderShowTitle {
		t.Fatalf("site A default appearance = %#v", appearanceA)
	}
	appearanceB, err := b.Appearance(ctx, bAdmin)
	if err != nil {
		t.Fatalf("site B default appearance: %v", err)
	}
	if appearanceB.SiteTitle != "site-b" || appearanceB.Revision != 1 {
		t.Fatalf("site B default appearance = %#v", appearanceB)
	}

	createOp := newOperationID()
	body := `<div>سلام English <b>bold</b></div><figure data-media-id="` + assetID + `"><figcaption>تصویر اصلی</figcaption></figure>`
	post, err := a.Create(ctx, aAdmin, createOp, ContentPost, "نوشته English", body)
	if err != nil {
		t.Fatalf("site A create post: %v", err)
	}
	if post.Revision != 1 || post.Kind != ContentPost || len(post.Media) != 1 || post.Media[0].AssetID != assetID || post.Media[0].AssetRevision != assetRevision {
		t.Fatalf("created post = %#v", post)
	}
	if !strings.Contains(post.BodyHTML, `<p dir="auto">سلام English <strong>bold</strong></p>`) {
		t.Fatalf("post body was not canonicalized: %s", post.BodyHTML)
	}

	replayed, err := a.Create(ctx, aAdmin, createOp, ContentPost, "نوشته English", body)
	if err != nil || replayed.ID != post.ID || replayed.Revision != post.Revision {
		t.Fatalf("exact create replay = %#v err=%v", replayed, err)
	}
	if _, err = a.Create(ctx, aAdmin, createOp, ContentPost, "different intent", body); !errors.Is(err, ErrEditorialConflict) {
		t.Fatalf("create operation ID reused with different payload: %v", err)
	}

	// Simulate a client losing the successful response: retain only operation_id and
	// recover the committed resource through the explicit inspection path.
	lostOp := newOperationID()
	if _, err = a.Create(ctx, aAdmin, lostOp, ContentPage, "خانه", `<p>صفحه خانه</p>`); err != nil {
		t.Fatalf("simulated lost-response mutation: %v", err)
	}
	lostOutcome, err := a.Operation(ctx, aAdmin, lostOp)
	if err != nil {
		t.Fatalf("lost-response operation lookup: %v", err)
	}
	if lostOutcome.Kind != "content.create" || lostOutcome.ResultRevision != 1 || !validEditorialID(lostOutcome.ResourceID) {
		t.Fatalf("lost-response outcome = %#v", lostOutcome)
	}
	homePage, err := a.Head(ctx, aAdmin, lostOutcome.ResourceID)
	if err != nil || homePage.Kind != ContentPage {
		t.Fatalf("recovered lost-response page = %#v err=%v", homePage, err)
	}

	saveOp := newOperationID()
	savedBody := `<p>نسخه دوم English</p><figure data-media-id="` + assetID + `"><figcaption>caption</figcaption></figure>`
	saved, err := a.Save(ctx, aAdmin, saveOp, post.ID, 1, "ویرایش دوم", savedBody)
	if err != nil {
		t.Fatalf("save second revision: %v", err)
	}
	if saved.Revision != 2 {
		t.Fatalf("saved revision = %d", saved.Revision)
	}
	replaySave, err := a.Save(ctx, aAdmin, saveOp, post.ID, 1, "ویرایش دوم", savedBody)
	if err != nil || replaySave.Revision != 2 {
		t.Fatalf("exact save replay = %#v err=%v", replaySave, err)
	}
	if _, err = a.Save(ctx, aAdmin, saveOp, post.ID, 1, "ویرایش دوم", `<p>different payload</p>`); !errors.Is(err, ErrEditorialConflict) {
		t.Fatalf("save operation ID reused with different payload: %v", err)
	}
	if _, err = a.Save(ctx, aAdmin, newOperationID(), post.ID, 1, "stale", `<p>stale</p>`); !errors.Is(err, ErrEditorialConflict) {
		t.Fatalf("stale save did not conflict: %v", err)
	}
	first, err := a.Revision(ctx, aAdmin, post.ID, 1)
	if err != nil || first.Title != "نوشته English" {
		t.Fatalf("immutable first revision = %#v err=%v", first, err)
	}
	saveOutcome, err := a.Operation(ctx, aAdmin, saveOp)
	if err != nil || saveOutcome.ResourceID != post.ID || saveOutcome.ResultRevision != 2 {
		t.Fatalf("save operation outcome = %#v err=%v", saveOutcome, err)
	}

	publicationOp := newOperationID()
	withIntent, err := a.SetPublicationIntent(ctx, aAdmin, publicationOp, post.ID, 2, 1)
	if err != nil {
		t.Fatalf("set publication intent: %v", err)
	}
	if withIntent.Revision != 2 || withIntent.PublicationIntentRevision != 1 {
		t.Fatalf("publication intent mutated source unexpectedly: %#v", withIntent)
	}
	if _, err = a.SetPublicationIntent(ctx, aAdmin, publicationOp, post.ID, 2, 2); !errors.Is(err, ErrEditorialConflict) {
		t.Fatalf("publication operation ID reused with different revision: %v", err)
	}

	if err = runtime.Sites["site-a"].Media.Delete(ctx, aAdmin, assetID, assetRevision); !errors.Is(err, achrix.ErrDenied) {
		t.Fatalf("site admin could directly delete retained Media: %v", err)
	}
	if _, err = b.Create(ctx, bAdmin, newOperationID(), ContentPost, "cross-site media", `<figure data-media-id="`+assetID+`"><figcaption>x</figcaption></figure>`); !errors.Is(err, ErrEditorialInvalid) {
		t.Fatalf("site B accepted site A Media reference: %v", err)
	}
	if _, err = b.Head(ctx, bAdmin, post.ID); !errors.Is(err, ErrEditorialNotFound) {
		t.Fatalf("site B observed site A content: %v", err)
	}
	if _, err = b.Head(ctx, aAdmin, post.ID); !errors.Is(err, achrix.ErrDenied) {
		t.Fatalf("site A principal entered site B editorial service: %v", err)
	}
	if _, err = a.Operation(ctx, bAdmin, saveOp); !errors.Is(err, achrix.ErrDenied) {
		t.Fatalf("site B principal inspected site A operation: %v", err)
	}

	appearanceInput := appearanceA.input()
	appearanceInput.SiteTitle = "نمونه Example"
	appearanceInput.SiteDescription = "توضیح English"
	appearanceInput.SiteLanguage = "fa"
	appearanceInput.HomeMode = "page"
	appearanceInput.HomePageID = homePage.ID
	appearanceInput.HeaderTagline = "سلام world"
	appearanceInput.FooterText = "پابرگ"
	appearanceInput.Theme = "dark"
	appearanceOp := newOperationID()
	updatedAppearance, err := a.SaveAppearance(ctx, aAdmin, appearanceOp, 1, appearanceInput)
	if err != nil {
		t.Fatalf("save appearance: %v", err)
	}
	if updatedAppearance.Revision != 2 || updatedAppearance.SiteLanguage != "fa" || updatedAppearance.HomePageID != homePage.ID || updatedAppearance.Theme != "dark" {
		t.Fatalf("updated appearance = %#v", updatedAppearance)
	}
	replayAppearance, err := a.SaveAppearance(ctx, aAdmin, appearanceOp, 1, appearanceInput)
	if err != nil || replayAppearance.Revision != 2 {
		t.Fatalf("appearance exact replay = %#v err=%v", replayAppearance, err)
	}
	changedAppearance := appearanceInput
	changedAppearance.Theme = "light"
	if _, err = a.SaveAppearance(ctx, aAdmin, appearanceOp, 1, changedAppearance); !errors.Is(err, ErrEditorialConflict) {
		t.Fatalf("appearance operation ID reused with different payload: %v", err)
	}
	if _, err = a.SaveAppearance(ctx, aAdmin, newOperationID(), 1, appearanceInput); !errors.Is(err, ErrEditorialConflict) {
		t.Fatalf("stale appearance save did not conflict: %v", err)
	}
	invalidHome := updatedAppearance.input()
	invalidHome.HomePageID = post.ID
	if _, err = a.SaveAppearance(ctx, aAdmin, newOperationID(), 2, invalidHome); !errors.Is(err, ErrEditorialInvalid) {
		t.Fatalf("post accepted as home page: %v", err)
	}

	bPost, err := b.Create(ctx, bAdmin, newOperationID(), ContentPost, "site B only", `<p>site B</p>`)
	if err != nil {
		t.Fatalf("site B create: %v", err)
	}
	if _, err = a.Head(ctx, aAdmin, bPost.ID); !errors.Is(err, ErrEditorialNotFound) {
		t.Fatalf("site A observed site B content: %v", err)
	}
	appearanceBAfter, err := b.Appearance(ctx, bAdmin)
	if err != nil || appearanceBAfter.Revision != 1 || appearanceBAfter.SiteTitle != "site-b" || appearanceBAfter.Theme != "light" {
		t.Fatalf("site B appearance changed with site A: %#v err=%v", appearanceBAfter, err)
	}

	assertEditorialDatabaseIsolation(t, ctx, dsnA, dsnB, post.ID, bPost.ID, assetID)
}

func assertEditorialDatabaseIsolation(t *testing.T, ctx context.Context, dsnA, dsnB, aContentID, bContentID, assetID string) {
	t.Helper()
	a, err := pgx.Connect(ctx, dsnA)
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close(context.Background())
	b, err := pgx.Connect(ctx, dsnB)
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close(context.Background())

	var count int
	if err = a.QueryRow(ctx, "SELECT count(*) FROM rixa.content_items WHERE id=$1", aContentID).Scan(&count); err != nil || count != 1 {
		t.Fatalf("site A editorial row count=%d err=%v", count, err)
	}
	if err = b.QueryRow(ctx, "SELECT count(*) FROM rixa.content_items WHERE id=$1", aContentID).Scan(&count); err != nil || count != 0 {
		t.Fatalf("site A content leaked to site B: count=%d err=%v", count, err)
	}
	if err = a.QueryRow(ctx, "SELECT count(*) FROM rixa.content_items WHERE id=$1", bContentID).Scan(&count); err != nil || count != 0 {
		t.Fatalf("site B content leaked to site A: count=%d err=%v", count, err)
	}
	if err = a.QueryRow(ctx, "SELECT count(*) FROM rixa.content_media_refs WHERE asset_id=$1", assetID).Scan(&count); err != nil || count < 2 {
		t.Fatalf("site A retained Media reference evidence: count=%d err=%v", count, err)
	}
	if err = b.QueryRow(ctx, "SELECT count(*) FROM rixa.content_media_refs WHERE asset_id=$1", assetID).Scan(&count); err != nil || count != 0 {
		t.Fatalf("site A Media reference leaked to site B: count=%d err=%v", count, err)
	}
	if err = a.QueryRow(ctx, "SELECT count(*) FROM rixa.appearance_revisions").Scan(&count); err != nil || count != 2 {
		t.Fatalf("site A appearance revisions count=%d err=%v", count, err)
	}
	if err = b.QueryRow(ctx, "SELECT count(*) FROM rixa.appearance_revisions").Scan(&count); err != nil || count != 1 {
		t.Fatalf("site B appearance revisions count=%d err=%v", count, err)
	}
}
