// SPDX-License-Identifier: MPL-2.0

package terminal

import (
	"context"
	"errors"

	"github.com/AChWorks/rixa/internal/serverinstall"
)

// UpdateOptions selects an update operation without collecting replacement
// credentials or rewriting the retained installation's configuration.
type UpdateOptions struct {
	Options
	InstallationOperation string
	UpdatePrefill         *serverinstall.UpdateDraft
}

// RunUpdate reuses the same terminal session, input handling, action fencing and
// confirmations as installation while dispatching only the typed update facade.
func RunUpdate(ctx context.Context, backend serverinstall.UpdateFacade, options UpdateOptions) error {
	if backend == nil {
		return errors.New("native update backend is required")
	}
	if options.Options.Prefill != nil {
		return errors.New("an update menu requires update prefill, not installation credentials")
	}
	adapter := &updateAdapter{backend: backend, installation: options.InstallationOperation}
	common := options.Options
	if options.UpdatePrefill != nil {
		draft := *options.UpdatePrefill
		if adapter.installation != "" && draft.InstallationOperation != "" && adapter.installation != draft.InstallationOperation {
			return errors.New("prefill installation_operation must match --installation")
		}
		if draft.InstallationOperation != "" {
			adapter.installation = draft.InstallationOperation
		}
		presentation := updatePresentation(draft)
		common.Prefill = &presentation
	}
	return runManagement(ctx, adapter, common, adapter)
}

type updateAdapter struct {
	backend      serverinstall.UpdateFacade
	installation string
}

func updatePresentation(d serverinstall.UpdateDraft) serverinstall.Draft {
	return serverinstall.Draft{
		OperationID: d.OperationID, ArchivePath: d.ArchivePath, BundlePath: d.BundlePath,
		Release: d.Release, SourceCommit: d.SourceCommit,
	}
}
func (u *updateAdapter) selected(d serverinstall.Draft) serverinstall.UpdateDraft {
	return serverinstall.UpdateDraft{
		OperationID: d.OperationID, InstallationOperation: u.installation,
		ArchivePath: d.ArchivePath, BundlePath: d.BundlePath, Release: d.Release, SourceCommit: d.SourceCommit,
	}
}
func (u *updateAdapter) annotate(r serverinstall.Result) serverinstall.Result {
	r.OperationKind = "update"
	if r.InstallationOperation == "" {
		r.InstallationOperation = u.installation
	}
	return r
}
func (u *updateAdapter) SaveDraft(ctx context.Context, d serverinstall.Draft) (serverinstall.Result, error) {
	r, err := u.backend.SaveUpdateDraft(ctx, u.selected(d))
	return u.annotate(r), err
}
func (u *updateAdapter) LoadDraft(ctx context.Context, id string) (serverinstall.Draft, error) {
	d, err := u.backend.LoadUpdateDraft(ctx, id)
	if err != nil {
		return serverinstall.Draft{}, err
	}
	u.installation = d.InstallationOperation
	return updatePresentation(d), nil
}
func (u *updateAdapter) Review(ctx context.Context, id string) (serverinstall.Result, error) {
	r, err := u.backend.ReviewUpdate(ctx, id)
	return u.annotate(r), err
}
func (u *updateAdapter) Apply(ctx context.Context, id, token string) (serverinstall.Result, error) {
	r, err := u.backend.ApplyUpdate(ctx, id, token)
	return u.annotate(r), err
}
func (u *updateAdapter) Resume(ctx context.Context, id string) (serverinstall.Result, error) {
	r, err := u.backend.ResumeUpdate(ctx, id)
	return u.annotate(r), err
}
func (u *updateAdapter) Status(ctx context.Context, id string) (serverinstall.Result, error) {
	r, err := u.backend.UpdateStatus(ctx, id)
	return u.annotate(r), err
}
func (u *updateAdapter) Remove(ctx context.Context, id string) (serverinstall.Result, error) {
	r, err := u.backend.RemoveUpdatedInstallation(ctx, id)
	return u.annotate(r), err
}
func (u *updateAdapter) configure(ctx context.Context, s *session, d *serverinstall.Draft, current serverinstall.Result) (serverinstall.Result, bool, error) {
	fields := []field{
		{"installation_operation", "Installation operation", "The existing owning native installation operation. Its frozen manager and data remain retained.", false, &u.installation},
		{"archive", "Candidate release archive", "Absolute path to the authenticated compatible candidate archive.", false, &d.ArchivePath},
		{"bundle", "Candidate attestation bundle", "Absolute path to the candidate's adjacent .sigstore.json; no signature bypass.", false, &d.BundlePath},
		{"release", "Candidate release tag", "Exact candidate release tag returned by authenticated discovery.", false, &d.Release},
		{"source_commit", "Candidate source commit", "Exact 40-character source commit of that candidate.", false, &d.SourceCommit},
	}
	return configureFields(ctx, s, u, d, current, fields, 0, func() []serverinstall.Problem {
		return serverinstall.ValidateUpdateDraft(u.selected(*d))
	})
}
func updateMenuActions(allowed []string) []menuAction {
	return filterMenuActions(allowed, []menuAction{
		{"configure", "Configure update"}, {"review", "Review update"},
		{"apply", "Apply reviewed update"}, {"resume", "Resume retained update"},
		{"status", "Refresh status"}, {"remove", "Remove executable and service"},
	})
}
func updateConfirmation(action, id string, r serverinstall.Result, installation string) (string, string, []string) {
	title, word := "Start reviewed update?", "APPLY"
	if action == "resume" {
		title, word = "Resume the retained update?", "RESUME"
	}
	lines := []string{
		"Update operation: " + SafeText(id),
		"Installation operation: " + SafeText(installation),
		"Active release: " + SafeText(r.ActiveRelease),
		"Candidate release: " + SafeText(r.Release),
		"Only the reviewed owned Rixa service and artifact will be changed.",
		"The service may be stopped during explicit migration and activation.",
		"Retain configuration, credentials, databases, site data and original Media.",
		"No account bootstrap, password reset or database major upgrade.",
		"A binary reversal does not roll back database changes.",
		"Only the exact reviewed operation will be queued.",
		"Closing this menu does not stop an already queued update.",
	}
	return title, word, lines
}
