// SPDX-License-Identifier: MPL-2.0

package terminal

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/AChWorks/rixa/internal/serverinstall"
)

type updateTestFacade struct {
	draft                                     serverinstall.UpdateDraft
	saves, reviews, applies, resumes, removes int
	failSave                                  bool
	ready                                     bool
	recovery                                  bool
	resumeAllowed                             bool
	installed                                 bool
	token                                     string
}

func updateTestDraft() serverinstall.UpdateDraft {
	return serverinstall.UpdateDraft{OperationID: "pty-case", InstallationOperation: "owner-install", ArchivePath: "/private/candidate.tar.gz", BundlePath: "/private/candidate.tar.gz.sigstore.json", Release: "v0.5.0", SourceCommit: strings.Repeat("a", 40)}
}
func (f *updateTestFacade) result(state, code string, actions ...string) serverinstall.Result {
	return serverinstall.Result{OperationID: f.draft.OperationID, OperationKind: "update", InstallationOperation: f.draft.InstallationOperation, ActiveRelease: "v0.4.0", Release: f.draft.Release, State: state, Code: code, Actions: actions}
}
func (f *updateTestFacade) SaveUpdateDraft(_ context.Context, d serverinstall.UpdateDraft) (serverinstall.Result, error) {
	f.saves++
	f.draft = d
	if f.failSave && f.saves == 1 {
		return f.result("draft", "unavailable", "configure", "status"), errors.New("Update journal temporarily unavailable; retained entries can be saved again.")
	}
	return f.result("draft", "saved", "configure", "review", "status"), nil
}
func (f *updateTestFacade) LoadUpdateDraft(context.Context, string) (serverinstall.UpdateDraft, error) {
	return f.draft, nil
}
func (f *updateTestFacade) ReviewUpdate(context.Context, string) (serverinstall.Result, error) {
	f.reviews++
	r := f.result("ready", "ready", "configure", "review", "apply", "status")
	r.ReviewToken = "exact-update-token"
	return r, nil
}
func (f *updateTestFacade) ApplyUpdate(_ context.Context, _ string, token string) (serverinstall.Result, error) {
	f.applies++
	f.token = token
	return f.result("applying", "queued", "status"), nil
}
func (f *updateTestFacade) ResumeUpdate(context.Context, string) (serverinstall.Result, error) {
	f.resumes++
	return f.result("applying", "resumed", "status"), nil
}
func (f *updateTestFacade) UpdateStatus(context.Context, string) (serverinstall.Result, error) {
	if f.resumeAllowed {
		return f.result("blocked", "safe-to-resume", "resume", "status"), nil
	}
	if f.installed {
		return f.result("installed", "installed", "status", "remove"), nil
	}
	if f.recovery {
		return f.result("reconciliation-required", "inspect", "status"), nil
	}
	if f.ready {
		r := f.result("ready", "ready", "configure", "review", "apply", "status")
		r.ReviewToken = "another-client-token"
		return r, nil
	}
	return f.result("draft", "saved", "configure", "review", "status"), nil
}
func (f *updateTestFacade) RemoveUpdatedInstallation(context.Context, string) (serverinstall.Result, error) {
	f.removes++
	return f.result("removed", "removed", "status"), nil
}

func TestUpdateAdapterPreservesTypedMetadataAndExistingFacade(t *testing.T) {
	f := &updateTestFacade{draft: updateTestDraft()}
	a := &updateAdapter{backend: f, installation: f.draft.InstallationOperation}
	presentation := updatePresentation(f.draft)
	if presentation.ControlPassword != "" || presentation.ControlDSN != "" || presentation.Language != "" || presentation.Listen != "" {
		t.Fatal("update collected installation configuration")
	}
	if !reflect.DeepEqual(a.selected(presentation), f.draft) {
		t.Fatal("update metadata changed")
	}
	got, err := a.SaveDraft(context.Background(), presentation)
	if err != nil || got.OperationKind != "update" || got.InstallationOperation != "owner-install" || f.saves != 1 {
		t.Fatalf("wrong adapter: %#v %v", got, err)
	}
	restored, err := a.LoadDraft(context.Background(), "pty-case")
	if err != nil || !reflect.DeepEqual(restored, presentation) {
		t.Fatalf("restore: %#v %v", restored, err)
	}
}
func TestUpdateMenusCannotInventBackendCapabilities(t *testing.T) {
	got := updateMenuActions([]string{"status", "invented-action"})
	if len(got) != 3 || got[0].id != "status" || got[1].id != "details" || got[2].id != "exit" {
		t.Fatalf("invented action: %#v", got)
	}
	var out strings.Builder
	if err := RunUpdate(context.Background(), nil, UpdateOptions{}); err == nil {
		t.Fatal("nil updater accepted")
	}
	d := testDraft()
	err := RunUpdate(context.Background(), &updateTestFacade{}, UpdateOptions{Options: Options{Output: &out, Prefill: &d}})
	if err == nil || out.Len() != 0 {
		t.Fatal("installation credentials accepted for update")
	}
	parent := updateTestDraft()
	err = RunUpdate(context.Background(), &updateTestFacade{}, UpdateOptions{Options: Options{Output: &out}, InstallationOperation: "different-install", UpdatePrefill: &parent})
	if err == nil || out.Len() != 0 {
		t.Fatal("conflicting parent selector accepted")
	}
	if err := RunUpdate(context.Background(), &updateTestFacade{}, UpdateOptions{Options: Options{Output: &out}}); !errors.Is(err, ErrTerminal) || out.Len() != 0 {
		t.Fatalf("nonterminal update: %v", err)
	}
}
func TestUpdateOverviewAndConfirmationDistinguishScope(t *testing.T) {
	f := &updateTestFacade{draft: updateTestDraft()}
	r := f.result("ready", "ready")
	overview := strings.Join(overview("v0.4.0", r, ""), "\n")
	for _, want := range []string{"Update operation: pty-case", "Installation operation: owner-install", "Active release: v0.4.0", "Candidate release: v0.5.0"} {
		if !strings.Contains(overview, want) {
			t.Fatalf("scope missing %q: %s", want, overview)
		}
	}
	_, word, lines := updateConfirmation("apply", "pty-case", r, "owner-install")
	text := strings.Join(lines, "\n")
	if word != "APPLY" || !strings.Contains(text, "binary reversal does not roll back database") || strings.Contains(text, "creates the reviewed user") {
		t.Fatalf("wrong update confirmation: %s", text)
	}
}
