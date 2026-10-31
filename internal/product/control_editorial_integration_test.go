// SPDX-License-Identifier: MPL-2.0

package product

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sort"
	"sync/atomic"
	"testing"
	"time"

	"github.com/AChWorks/achrix"
	"github.com/jackc/pgx/v5"
)

func TestSelectedSiteDraftPaginationAndRecovery(t *testing.T) {
	adminDSN := os.Getenv("RIXA_TEST_POSTGRES_DSN")
	if adminDSN == "" {
		if os.Getenv("CI") != "" {
			t.Fatal("RIXA_TEST_POSTGRES_DSN is required in CI")
		}
		t.Skip("set RIXA_TEST_POSTGRES_DSN for selected-site draft integration proof")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 90*time.Second)
	defer cancel()
	dsns := createTestDatabases(t, ctx, adminDSN)
	for _, key := range []string{"a", "b"} {
		if err := migrateEditorial(ctx, dsns[key], "site-"+key); err != nil {
			t.Fatal(err)
		}
	}

	controlAdmin := achrix.Principal(testControlAdmin)
	aAdmin, bAdmin := achrix.Principal(testSiteAAdmin), achrix.Principal(testSiteBAdmin)
	operatorA, operatorB := operatorPrincipal(controlAdmin, "site-a"), operatorPrincipal(controlAdmin, "site-b")
	var controlDenied, siteDenied atomic.Bool
	var controlChecks, siteChecks atomic.Int64
	controlFixed := controlPolicy(controlAdmin,
		map[string]bool{"site-a": true, "site-b": true, "disabled": true, "unmanageable": true},
		map[string]bool{"site-a": true, "site-b": true})
	controlApp := draftTestApplication(t, achrix.PolicyFunc(func(ctx context.Context, actor achrix.Principal, capability, resource string) error {
		controlChecks.Add(1)
		if controlDenied.Load() {
			return achrix.ErrDenied
		}
		return controlFixed.Authorize(ctx, actor, capability, resource)
	}), newControlModule())
	siteFixed := sitePolicy(aAdmin, operatorA, nil)
	aApp := draftTestApplication(t, achrix.PolicyFunc(func(ctx context.Context, actor achrix.Principal, capability, resource string) error {
		siteChecks.Add(1)
		if siteDenied.Load() {
			return achrix.ErrDenied
		}
		return siteFixed.Authorize(ctx, actor, capability, resource)
	}), newSiteModule())
	bApp := draftTestApplication(t, sitePolicy(bAdmin, operatorB, nil), newSiteModule())
	aStore, err := newEditorialStore(dsns["a"])
	if err != nil {
		t.Fatal(err)
	}
	bStore, err := newEditorialStore(dsns["b"])
	if err != nil {
		t.Fatal(err)
	}
	a := &EditorialService{app: aApp, store: aStore, publicationGate: make(chan struct{}, 1)}
	b := &EditorialService{app: bApp, store: bStore, publicationGate: make(chan struct{}, 1)}
	control := &ControlService{
		control: &ControlRuntime{App: controlApp},
		sites: map[string]*SiteRuntime{
			"site-a": {Editorial: a, operator: operatorA},
			"site-b": {Editorial: b, operator: operatorB},
		},
	}

	// Enough rows to cross the old 100-item cutoff; equal timestamps exercise
	// the ID tie-breaker rather than depending on fixture insertion order.
	expected := seedDraftPageItems(t, ctx, dsns["a"], 123)
	for _, limit := range []int{17, 100} {
		var got []ContentSummary
		var cursor string
		for pages := 0; ; pages++ {
			if pages > 20 {
				t.Fatal("content continuation did not terminate")
			}
			page, err := control.ListSiteContentPage(ctx, controlAdmin, "site-a", cursor, limit)
			if err != nil || len(page.Items) > limit {
				t.Fatalf("content page: entries=%d err=%v", len(page.Items), err)
			}
			got = append(got, page.Items...)
			if page.NextCursor == "" {
				break
			}
			if page.NextCursor == cursor {
				t.Fatal("content continuation did not advance")
			}
			cursor = page.NextCursor
		}
		if len(got) != len(expected) {
			t.Fatalf("content continuation returned %d of %d entries", len(got), len(expected))
		}
		for i := range expected {
			if got[i].ID != expected[i].ID {
				t.Fatalf("content order/duplication at %d: got %s want %s", i, got[i].ID, expected[i].ID)
			}
		}
	}
	first, err := control.ListSiteContentPage(ctx, controlAdmin, "site-a", "", 0)
	if err != nil || len(first.Items) != 50 || first.NextCursor == "" {
		t.Fatalf("default content page: entries=%d cursor=%q err=%v", len(first.Items), first.NextCursor, err)
	}
	oldList, err := control.ListSiteContent(ctx, controlAdmin, "site-a")
	if err != nil || len(oldList) != 100 {
		t.Fatalf("old list API changed: entries=%d err=%v", len(oldList), err)
	}
	bPage, err := control.ListSiteContentPage(ctx, controlAdmin, "site-b", first.NextCursor, 17)
	if err != nil || len(bPage.Items) != 0 {
		t.Fatalf("another site's cursor leaked entries: %#v %v", bPage, err)
	}
	if _, err := a.ListPage(ctx, bAdmin, first.NextCursor, 17); !errors.Is(err, achrix.ErrDenied) {
		t.Fatalf("foreign actor used a valid cursor: %v", err)
	}

	createID := newOperationID()
	head, err := control.CreateSiteContent(ctx, controlAdmin, "site-a", createID, ContentPost, "Original", "<p>Original</p>")
	if err != nil || head.Actor != string(operatorA) {
		t.Fatalf("delegated create: %#v %v", head, err)
	}
	for revision := int64(2); revision <= 63; revision++ {
		head, err = control.SaveSiteContent(ctx, controlAdmin, "site-a", newOperationID(), head.ID, revision-1, fmt.Sprintf("Revision %d", revision), "<p>Saved body</p>")
		if err != nil || head.Revision != revision {
			t.Fatalf("save revision %d: %#v %v", revision, head, err)
		}
	}
	var cursor string
	var revisions []int64
	for pages := 0; ; pages++ {
		if pages > 10 {
			t.Fatal("history continuation did not terminate")
		}
		page, err := control.SiteContentRevisionsPage(ctx, controlAdmin, "site-a", head.ID, cursor, 11)
		if err != nil || len(page.Items) > 11 {
			t.Fatalf("history page: %#v %v", page, err)
		}
		for _, revision := range page.Items {
			revisions = append(revisions, revision.Revision)
		}
		if page.NextCursor == "" {
			break
		}
		cursor = page.NextCursor
	}
	if len(revisions) != 63 {
		t.Fatalf("history returned %d revisions, want 63", len(revisions))
	}
	for i, revision := range revisions {
		if revision != int64(63-i) {
			t.Fatalf("history missing/duplicated revision at %d: %d", i, revision)
		}
	}
	defaultHistory, err := control.SiteContentRevisionsPage(ctx, controlAdmin, "site-a", head.ID, "", 0)
	if err != nil || len(defaultHistory.Items) != 25 || defaultHistory.NextCursor == "" {
		t.Fatalf("default history page: entries=%d err=%v", len(defaultHistory.Items), err)
	}
	oldHistory, err := a.Revisions(ctx, aAdmin, head.ID)
	if err != nil || len(oldHistory) != 50 {
		t.Fatalf("old history API changed: entries=%d err=%v", len(oldHistory), err)
	}
	exhausted, err := control.SiteContentRevisionsPage(ctx, controlAdmin, "site-a", head.ID, encodeRevisionPageCursor(head.ID, 1), 11)
	if err != nil || len(exhausted.Items) != 0 || exhausted.NextCursor != "" {
		t.Fatalf("exhausted existing history: %#v %v", exhausted, err)
	}
	preview, err := control.PreviewSiteContent(ctx, controlAdmin, "site-a", head.ID, 1)
	if err != nil || preview.Title != "Original" || preview.Revision != 1 {
		t.Fatalf("immutable private preview: %#v %v", preview, err)
	}
	selected, err := control.HeadSiteContent(ctx, controlAdmin, "site-a", head.ID)
	if err != nil || selected.Revision != 63 {
		t.Fatalf("selected head: %#v %v", selected, err)
	}

	// A fresh facade dispatch must preserve domain CAS; two writers cannot both
	// advance the same expected head.
	start := make(chan struct{})
	results := make(chan error, 2)
	for i := 0; i < 2; i++ {
		go func(i int) {
			<-start
			_, err := control.SaveSiteContent(ctx, controlAdmin, "site-a", newOperationID(), head.ID, 63, fmt.Sprintf("Writer %d", i), "<p>Concurrent</p>")
			results <- err
		}(i)
	}
	close(start)
	success, conflict := 0, 0
	for i := 0; i < 2; i++ {
		err := <-results
		switch {
		case err == nil:
			success++
		case errors.Is(err, ErrEditorialConflict):
			conflict++
		default:
			t.Fatalf("concurrent save: %v", err)
		}
	}
	if success != 1 || conflict != 1 {
		t.Fatalf("CAS winners=%d conflicts=%d", success, conflict)
	}

	// Simulate a lost acknowledgement, then inspect and deliberately retry the
	// exact same request under its retained ID; no new resource may appear.
	lostID := newOperationID()
	if _, err := control.CreateSiteContent(ctx, controlAdmin, "site-a", lostID, ContentPage, "Lost response", "<p>Recover</p>"); err != nil {
		t.Fatal(err)
	}
	outcome, err := control.SiteEditorialOperation(ctx, controlAdmin, "site-a", lostID)
	if err != nil || outcome.Kind != "content.create" || outcome.ResultRevision != 1 {
		t.Fatalf("lost acknowledgement inspection: %#v %v", outcome, err)
	}
	replayed, err := control.CreateSiteContent(ctx, controlAdmin, "site-a", lostID, ContentPage, "Lost response", "<p>Recover</p>")
	if err != nil || replayed.ID != outcome.ResourceID {
		t.Fatalf("exact same-ID reconciliation: %#v %v", replayed, err)
	}
	if _, err := control.CreateSiteContent(ctx, controlAdmin, "site-a", lostID, ContentPage, "Changed", "<p>Recover</p>"); !errors.Is(err, ErrEditorialConflict) {
		t.Fatalf("changed-payload operation reuse: %v", err)
	}
	// Save acknowledgement recovery must return the retained exact revision even
	// though the submitted expected head is now stale.
	beforeSave, err := control.HeadSiteContent(ctx, controlAdmin, "site-a", head.ID)
	if err != nil {
		t.Fatal(err)
	}
	saveID := newOperationID()
	if _, err := control.SaveSiteContent(ctx, controlAdmin, "site-a", saveID, head.ID, beforeSave.Revision, "Lost save", "<p>Retained save</p>"); err != nil {
		t.Fatal(err)
	}
	saveOutcome, err := control.SiteEditorialOperation(ctx, controlAdmin, "site-a", saveID)
	if err != nil || saveOutcome.Kind != "content.save" || saveOutcome.ResourceID != head.ID || saveOutcome.ResultRevision != beforeSave.Revision+1 {
		t.Fatalf("lost save inspection: %#v %v", saveOutcome, err)
	}
	replayedSave, err := control.SaveSiteContent(ctx, controlAdmin, "site-a", saveID, head.ID, beforeSave.Revision, "Lost save", "<p>Retained save</p>")
	if err != nil || replayedSave.Revision != saveOutcome.ResultRevision {
		t.Fatalf("same-ID save reconciliation: %#v %v", replayedSave, err)
	}
	if _, err := control.SaveSiteContent(ctx, controlAdmin, "site-a", saveID, head.ID, beforeSave.Revision, "Changed save", "<p>Retained save</p>"); !errors.Is(err, ErrEditorialConflict) {
		t.Fatalf("changed save payload under retained ID: %v", err)
	}
	afterSave, err := control.HeadSiteContent(ctx, controlAdmin, "site-a", head.ID)
	if err != nil || afterSave.Revision != saveOutcome.ResultRevision {
		t.Fatalf("save reconciliation duplicated a revision: %#v %v", afterSave, err)
	}
	for name, call := range map[string]func() error{
		"head":    func() error { _, e := control.HeadSiteContent(ctx, controlAdmin, "site-b", head.ID); return e },
		"preview": func() error { _, e := control.PreviewSiteContent(ctx, controlAdmin, "site-b", head.ID, 1); return e },
		"history": func() error {
			_, e := control.SiteContentRevisionsPage(ctx, controlAdmin, "site-b", head.ID, "", 0)
			return e
		},
		"operation": func() error { _, e := control.SiteEditorialOperation(ctx, controlAdmin, "site-b", lostID); return e },
	} {
		if err := call(); !errors.Is(err, ErrEditorialNotFound) {
			t.Errorf("foreign %s crossed selected storage: %v", name, err)
		}
	}
	missingOperation := newOperationID()
	if _, err := control.SiteEditorialOperation(ctx, controlAdmin, "site-a", missingOperation); !errors.Is(err, ErrEditorialNotFound) {
		t.Fatalf("unknown operation lookup: %v", err)
	}

	calls := func(actor achrix.Principal, site string) []func() error {
		return []func() error{
			func() error { _, e := control.ListSiteContentPage(ctx, actor, site, "", 0); return e },
			func() error { _, e := control.HeadSiteContent(ctx, actor, site, head.ID); return e },
			func() error { _, e := control.PreviewSiteContent(ctx, actor, site, head.ID, 1); return e },
			func() error { _, e := control.SiteContentRevisionsPage(ctx, actor, site, head.ID, "", 0); return e },
			func() error { _, e := control.SiteEditorialOperation(ctx, actor, site, lostID); return e },
		}
	}
	for _, site := range []string{"disabled", "unknown", "unmanageable", "../site-a"} {
		for _, call := range calls(controlAdmin, site) {
			if err := call(); !errors.Is(err, achrix.ErrDenied) {
				t.Errorf("site %q not denied: %v", site, err)
			}
		}
	}
	for _, actor := range []achrix.Principal{aAdmin, bAdmin, operatorA, "forged"} {
		for _, call := range calls(actor, "site-a") {
			if err := call(); !errors.Is(err, achrix.ErrDenied) {
				t.Errorf("site/non-control principal %q not denied: %v", actor, err)
			}
		}
	}
	beforeChecks := controlChecks.Load()
	controlDenied.Store(true)
	for _, call := range calls(controlAdmin, "site-a") {
		if err := call(); !errors.Is(err, achrix.ErrDenied) {
			t.Errorf("revoked control grant not rechecked: %v", err)
		}
	}
	if controlChecks.Load()-beforeChecks != 5 {
		t.Fatalf("control authority not checked once per facade dispatch")
	}
	controlDenied.Store(false)
	beforeChecks = siteChecks.Load()
	siteDenied.Store(true)
	for _, call := range calls(controlAdmin, "site-a") {
		if err := call(); !errors.Is(err, achrix.ErrDenied) {
			t.Errorf("site domain policy was bypassed: %v", err)
		}
	}
	if siteChecks.Load()-beforeChecks != 5 {
		t.Fatalf("site domain authority not checked once per facade dispatch")
	}
}

func draftTestApplication(t *testing.T, policy achrix.Policy, module achrix.Module) *achrix.Application {
	t.Helper()
	app, err := achrix.New(achrix.Config{StartupTimeout: time.Second, ShutdownTimeout: time.Second}, policy, module)
	if err != nil {
		t.Fatal(err)
	}
	if err := app.Start(t.Context()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		if err := app.Shutdown(ctx); err != nil {
			t.Errorf("draft application shutdown: %v", err)
		}
	})
	return app
}

func seedDraftPageItems(t *testing.T, ctx context.Context, dsn string, count int) []ContentSummary {
	t.Helper()
	conn := editorialMigrationConn(t, ctx, dsn)
	defer conn.Close(context.Background())
	items := make([]ContentSummary, 0, count)
	itemRows, revisionRows := make([][]any, 0, count), make([][]any, 0, count)
	for i := 0; i < count; i++ {
		item := ContentSummary{ID: randText(), Kind: ContentPost, HeadRevision: 1, Title: fmt.Sprintf("Saved %03d", i), UpdatedAt: time.Date(2026, 1, 1, 0, 0, 0, i/7*1000, time.UTC)}
		items = append(items, item)
		itemRows = append(itemRows, []any{item.ID, string(item.Kind), int64(1), "fixture", item.UpdatedAt})
		revisionRows = append(revisionRows, []any{item.ID, int64(1), item.Title, "<p dir=\"auto\">Fixture</p>", "fixture", item.UpdatedAt, newOperationID()})
	}
	if _, err := conn.CopyFrom(ctx, pgx.Identifier{"rixa", "content_items"}, []string{"id", "kind", "head_revision", "created_by", "created_at"}, pgx.CopyFromRows(itemRows)); err != nil {
		t.Fatal(err)
	}
	if _, err := conn.CopyFrom(ctx, pgx.Identifier{"rixa", "content_revisions"}, []string{"item_id", "revision", "title", "body_html", "actor", "created_at", "operation_id"}, pgx.CopyFromRows(revisionRows)); err != nil {
		t.Fatal(err)
	}
	sort.Slice(items, func(i, j int) bool {
		if items[i].UpdatedAt.Equal(items[j].UpdatedAt) {
			return items[i].ID < items[j].ID
		}
		return items[i].UpdatedAt.After(items[j].UpdatedAt)
	})
	return items
}
