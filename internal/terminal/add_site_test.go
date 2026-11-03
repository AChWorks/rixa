// SPDX-License-Identifier: MPL-2.0
package terminal

import (
	"context"
	"errors"
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/AChWorks/rixa/internal/serverinstall"
)

type addSiteTestFacade struct {
	draft                                         serverinstall.AddSiteDraft
	saves, reviews, applies, resumes, inspections int
	token                                         string
	ready, recovery, resumable                    bool
	installed                                     bool
	notFound                                      bool
	failSaveOnce                                  bool
}

func testAddSiteDraft() serverinstall.AddSiteDraft {
	return serverinstall.AddSiteDraft{
		OperationID: "pty-case",
		AddSiteInspectionDraft: serverinstall.AddSiteInspectionDraft{
			InstallationOperation: "owner-install",
			SiteBOrigin:           "https://site-b.rixa.test:8443",
			SiteBDSN:              "postgres://b-user:DB-SECRET-CANARY@127.0.0.1:5432/bdb?sslmode=disable",
			SiteBLogin:            "b-admin", SiteBPassword: "PASSWORD-SECRET-CANARY",
		},
	}
}
func (f *addSiteTestFacade) result(state, code string, actions ...string) serverinstall.Result {
	return serverinstall.Result{OperationID: f.draft.OperationID,
		InstallationOperation: f.draft.InstallationOperation, OperationKind: "add-site",
		State: state, Code: code, Actions: actions}
}
func (f *addSiteTestFacade) InspectAddSite(context.Context, serverinstall.AddSiteInspectionDraft) (serverinstall.Result, error) {
	f.inspections++
	return f.result("inspected", "prerequisites-verified"), nil
}
func (f *addSiteTestFacade) SaveAddSiteDraft(_ context.Context, d serverinstall.AddSiteDraft) (serverinstall.Result, error) {
	f.saves++
	f.draft = d
	if f.failSaveOnce && f.saves == 1 {
		return f.result("draft", "save-unavailable", "review", "status"),
			errors.New("private Add Site admission unavailable: " + d.SiteBDSN + " " + d.SiteBPassword)
	}
	return f.result("draft", "draft-saved", "review", "status"), nil
}
func (f *addSiteTestFacade) LoadAddSiteDraft(context.Context, string) (serverinstall.AddSiteDraft, error) {
	return f.draft, nil
}
func (f *addSiteTestFacade) ReviewAddSite(context.Context, string) (serverinstall.Result, error) {
	f.reviews++
	r := f.result("ready", "ready", "review", "apply", "status")
	r.ReviewToken = "exact-add-site-review-token"
	return r, nil
}
func (f *addSiteTestFacade) AddSiteStatus(context.Context, string) (serverinstall.Result, error) {
	switch {
	case f.notFound:
		return serverinstall.Result{}, os.ErrNotExist
	case f.resumable:
		return f.result("reconciliation-required", "safe-to-resume", "resume", "status"), nil
	case f.recovery:
		return f.result("reconciliation-required", "inspect", "status"), nil
	case f.ready:
		r := f.result("ready", "ready", "review", "apply", "status")
		// Deliberately simulate the unsafe case of a foreign session's token
		// being visible on Status. No token may be adopted for Apply.
		r.ReviewToken = "foreign-session-token"
		return r, nil
	case f.installed:
		return f.result("installed", "healthy", "status"), nil
	default:
		return f.result("draft", "draft-saved", "review", "status"), nil
	}
}
func (f *addSiteTestFacade) ApplyAddSite(_ context.Context, _ string, token string) (serverinstall.Result, error) {
	f.applies++
	f.token = token
	return f.result("applying", "queued", "status"), nil
}
func (f *addSiteTestFacade) ResumeAddSite(context.Context, string) (serverinstall.Result, error) {
	f.resumes++
	return f.result("applying", "resumed", "status"), nil
}

func TestAddSiteAdapterPreservesPrivateTypedContract(t *testing.T) {
	f := &addSiteTestFacade{draft: testAddSiteDraft()}
	a := &addSiteAdapter{backend: f, installation: f.draft.InstallationOperation}
	presentation := addSitePresentation(f.draft)
	if presentation.SiteBDSN == "" || presentation.ControlDSN != "" || presentation.SiteDSN != "" ||
		presentation.ControlPassword != "" || presentation.SitePassword != "" ||
		presentation.ArchivePath != "" || presentation.TLSCertPath != "" {
		t.Fatal("Add Site collected unrelated installation inputs")
	}
	if !reflect.DeepEqual(a.selected(presentation), f.draft) {
		t.Fatal("Add Site typed metadata drift")
	}
	r, err := a.SaveDraft(context.Background(), presentation)
	if err != nil || f.saves != 1 || r.OperationKind != "add-site" ||
		r.InstallationOperation != f.draft.InstallationOperation {
		t.Fatalf("wrong typed Add Site save: %#v %v", r, err)
	}
	restored, err := a.LoadDraft(context.Background(), "pty-case")
	if err != nil || !reflect.DeepEqual(restored, presentation) {
		t.Fatalf("Add Site source not restored: %#v %v", restored, err)
	}
	if _, err := a.Remove(context.Background(), "pty-case"); err == nil {
		t.Fatal("standalone single-site Remove unexpectedly offered")
	}
	for _, state := range []string{"draft", "ready", "blocked"} {
		r = a.annotate(f.result(state, "test", "status"))
		if len(r.Actions) < 2 || r.Actions[0] != "configure" {
			t.Fatalf("unfrozen %s could not be corrected: %#v", state, r.Actions)
		}
	}
	for _, state := range []string{"applying", "installed", "reconciliation-required"} {
		r = a.annotate(f.result(state, "test", "status"))
		if len(r.Actions) != 1 || r.Actions[0] != "status" {
			t.Fatalf("frozen %s exposed Configure: %#v", state, r.Actions)
		}
	}
	if got := a.actions([]string{"status", "remove", "unknown"}); len(got) != 3 ||
		got[0].id != "status" || got[1].id != "details" || got[2].id != "exit" {
		t.Fatalf("unapproved Add Site actions escaped: %#v", got)
	}
	r = f.result("ready", "ready")
	overview := strings.Join(overview("v0.8.0", r, ""), "\n")
	if !strings.Contains(overview, "Original installation: owner-install") ||
		strings.Contains(overview, "Recorded release:") {
		t.Fatalf("missing typed Add Site owner presentation: %s", overview)
	}
	title, word, lines := a.confirmation("apply", "pty-case", r)
	summary := strings.Join(lines, "\n")
	if title != "Start reviewed Add Site?" || word != "APPLY" ||
		!strings.Contains(summary, "Original installation: owner-install") ||
		strings.Contains(summary, f.draft.SiteBDSN) || strings.Contains(summary, f.draft.SiteBPassword) {
		t.Fatalf("incorrect Add Site confirmation: %s", summary)
	}
}

func TestAddSiteMenuCapabilityAndPrefillConflicts(t *testing.T) {
	var out strings.Builder
	if err := RunAddSite(context.Background(), nil, AddSiteOptions{}); err == nil {
		t.Fatal("missing Add Site effects accepted")
	}
	d := testAddSiteDraft()
	installation := serverinstall.Draft{OperationID: "accidentally-installation", ControlDSN: "UNRELATED-SECRET"}
	if err := RunAddSite(context.Background(), &addSiteTestFacade{}, AddSiteOptions{
		Options: Options{Output: &out, Prefill: &installation},
	}); err == nil || out.Len() != 0 {
		t.Fatal("ordinary installation secrets accepted for Add Site")
	}
	if err := RunAddSite(context.Background(), &addSiteTestFacade{}, AddSiteOptions{
		Options: Options{Output: &out}, InstallationOperation: "other-install", AddSitePrefill: &d,
	}); err == nil || out.Len() != 0 {
		t.Fatal("conflicting installation selector accepted")
	}
	if err := RunAddSite(context.Background(), &addSiteTestFacade{}, AddSiteOptions{
		Options: Options{Output: &out, OperationID: "other-operation"}, AddSitePrefill: &d,
	}); err == nil || out.Len() != 0 {
		t.Fatal("conflicting operation prefill accepted")
	}
	if err := RunAddSite(context.Background(), &addSiteTestFacade{}, AddSiteOptions{
		Options: Options{Output: &out},
	}); !errors.Is(err, ErrTerminal) || out.Len() != 0 {
		t.Fatalf("noninteractive TTY did not fail closed: %v", err)
	}
}

func TestAddSiteMenuNeverOffersRemove(t *testing.T) {
	f := &addSiteTestFacade{draft: testAddSiteDraft(), installed: true}
	adapter := &addSiteAdapter{backend: f, installation: "owner-install"}
	r, err := adapter.Status(context.Background(), "pty-case")
	if err != nil || r.State != "installed" {
		t.Fatal("installed Add Site status not retained")
	}
	for _, item := range adapter.actions(r.Actions) {
		if item.id == "remove" || item.id == "apply" || item.id == "configure" {
			t.Fatal("frozen Add Site menu exposed new effects:", item.id)
		}
	}
}

func TestAddSiteAbsentOperationOffersOnlyNewDraft(t *testing.T) {
	f := &addSiteTestFacade{draft: testAddSiteDraft(), notFound: true}
	a := &addSiteAdapter{backend: f, installation: "owner-install"}
	r, err := a.Status(context.Background(), "new-add-site")
	if err != nil || r.State != "not-installed" || r.OperationID != "new-add-site" {
		t.Fatalf("new Add Site was not discovered: %#v %v", r, err)
	}
	actions := a.actions(r.Actions)
	if len(actions) != 4 || actions[0].id != "configure" || actions[1].id != "status" {
		t.Fatalf("new operation offered unexpected effects: %#v", actions)
	}
}

func TestAddSiteFrozenStatusRestoresPublicParentWithoutPrivateDraft(t *testing.T) {
	f := &addSiteTestFacade{draft: testAddSiteDraft(), installed: true}
	a := &addSiteAdapter{backend: f}
	r, err := a.Status(context.Background(), "pty-case")
	if err != nil || r.State != "installed" || a.installation != "owner-install" {
		t.Fatalf("frozen Add Site parent identity was lost: %#v %v", r, err)
	}
	_, _, lines := a.confirmation("resume", "pty-case", r)
	if !strings.Contains(strings.Join(lines, "\n"), "Original installation: owner-install") {
		t.Fatal("Resume confirmation did not name exact native predecessor")
	}
}
