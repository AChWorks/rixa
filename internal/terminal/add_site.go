// SPDX-License-Identifier: MPL-2.0
package terminal

import (
	"context"
	"errors"
	"os"

	"github.com/AChWorks/rixa/internal/serverinstall"
)

// AddSiteOptions presents the existing typed after-install site lifecycle.
// A new site never inherits the original installation's secrets or draft.
type AddSiteOptions struct {
	Options
	InstallationOperation string
	AddSitePrefill        *serverinstall.AddSiteDraft
}

// RunAddSite uses the existing terminal renderer, field editor, confirmation
// and review-token fencing. It does not introduce another effect path.
func RunAddSite(ctx context.Context, backend serverinstall.AddSiteEffects, options AddSiteOptions) error {
	if backend == nil {
		return errors.New("the selected manager does not support Add Site effects")
	}
	if options.Options.Prefill != nil {
		return errors.New("Add Site accepts only Add Site prefill, not installation credentials")
	}
	adapter := &addSiteAdapter{backend: backend, installation: options.InstallationOperation}
	common := options.Options
	if options.AddSitePrefill != nil {
		prefill := *options.AddSitePrefill
		if adapter.installation != "" && prefill.InstallationOperation != "" &&
			adapter.installation != prefill.InstallationOperation {
			return errors.New("prefill installation_operation must match --installation")
		}
		if prefill.InstallationOperation != "" {
			adapter.installation = prefill.InstallationOperation
		}
		draft := addSitePresentation(prefill)
		common.Prefill = &draft
	}
	return runManagement(ctx, adapter, common, adapter)
}

type addSiteAdapter struct {
	backend      serverinstall.AddSiteEffects
	installation string
}

func addSitePresentation(d serverinstall.AddSiteDraft) serverinstall.Draft {
	return serverinstall.Draft{
		OperationID: d.OperationID, SiteBOrigin: d.SiteBOrigin, SiteBDSN: d.SiteBDSN,
		SiteBLogin: d.SiteBLogin, SiteBPassword: d.SiteBPassword,
	}
}
func (a *addSiteAdapter) selected(d serverinstall.Draft) serverinstall.AddSiteDraft {
	return serverinstall.AddSiteDraft{
		OperationID: d.OperationID, AddSiteInspectionDraft: serverinstall.AddSiteInspectionDraft{
			InstallationOperation: a.installation, SiteBOrigin: d.SiteBOrigin,
			SiteBDSN: d.SiteBDSN, SiteBLogin: d.SiteBLogin, SiteBPassword: d.SiteBPassword,
		},
	}
}
func (a *addSiteAdapter) kind() string  { return "add-site" }
func (a *addSiteAdapter) title() string { return "RIXA / Add Site management" }
func (a *addSiteAdapter) annotate(r serverinstall.Result) serverinstall.Result {
	r.OperationKind = "add-site"
	if r.InstallationOperation != "" && a.installation == "" {
		// A frozen Add Site can expose safe Status, but LoadAddSiteDraft
		// intentionally refuses to return its private completed draft.
		// Restore the real parent label from the public retained receipt.
		a.installation = r.InstallationOperation
	}
	if r.InstallationOperation == "" {
		r.InstallationOperation = a.installation
	}
	// Only unfrozen drafts can be corrected. The backend independently
	// rejects an attempted rewrite of a committed/frozen operation.
	if r.State == "draft" || r.State == "blocked" || r.State == "ready" ||
		r.State == "not-installed" {
		hasConfigure, hasInspect := false, false
		for _, action := range r.Actions {
			hasConfigure = hasConfigure || action == "configure"
			hasInspect = hasInspect || action == "inspect"
		}
		if !hasInspect && r.State != "not-installed" {
			r.Actions = append([]string{"inspect"}, r.Actions...)
		}
		if !hasConfigure {
			r.Actions = append([]string{"configure"}, r.Actions...)
		}
	}
	return r
}
func (a *addSiteAdapter) inspect(ctx context.Context, d serverinstall.Draft) (serverinstall.Result, error) {
	r, err := a.backend.InspectAddSite(ctx, a.selected(d).AddSiteInspectionDraft)
	return a.annotate(r), err
}
func (a *addSiteAdapter) SaveDraft(ctx context.Context, d serverinstall.Draft) (serverinstall.Result, error) {
	r, err := a.backend.SaveAddSiteDraft(ctx, a.selected(d))
	return a.annotate(r), err
}
func (a *addSiteAdapter) LoadDraft(ctx context.Context, id string) (serverinstall.Draft, error) {
	d, err := a.backend.LoadAddSiteDraft(ctx, id)
	if err != nil {
		return serverinstall.Draft{}, err
	}
	a.installation = d.InstallationOperation
	return addSitePresentation(d), nil
}
func (a *addSiteAdapter) Review(ctx context.Context, id string) (serverinstall.Result, error) {
	r, err := a.backend.ReviewAddSite(ctx, id)
	return a.annotate(r), err
}
func (a *addSiteAdapter) Apply(ctx context.Context, id, token string) (serverinstall.Result, error) {
	r, err := a.backend.ApplyAddSite(ctx, id, token)
	return a.annotate(r), err
}
func (a *addSiteAdapter) Resume(ctx context.Context, id string) (serverinstall.Result, error) {
	r, err := a.backend.ResumeAddSite(ctx, id)
	return a.annotate(r), err
}
func (a *addSiteAdapter) Status(ctx context.Context, id string) (serverinstall.Result, error) {
	r, err := a.backend.AddSiteStatus(ctx, id)
	if errors.Is(err, os.ErrNotExist) {
		return a.annotate(serverinstall.Result{
			OperationID: id, State: "not-installed", Code: "not-installed",
			Message: "No Add Site operation exists with this ID; configure a new private draft.",
			Actions: []string{"configure", "status"},
		}), nil
	}
	return a.annotate(r), err
}
func (a *addSiteAdapter) Remove(context.Context, string) (serverinstall.Result, error) {
	return serverinstall.Result{}, errors.New("Add Site cannot remove a single site; ordinary retained-data Remove belongs to the original installation")
}
func (a *addSiteAdapter) actions(allowed []string) []menuAction {
	return filterMenuActions(allowed, []menuAction{
		{"configure", "Configure new site"}, {"inspect", "Inspect site-B prerequisites"}, {"review", "Review Add Site"},
		{"apply", "Apply reviewed Add Site"}, {"resume", "Resume retained Add Site"},
		{"status", "Refresh Add Site status"},
	})
}
func (a *addSiteAdapter) help() []string {
	return []string{
		"Add Site extends one healthy single-site native installation.",
		"Use its exact original installation operation and a distinct Add Site operation.",
		"Supply an independent site-B HTTPS origin, PostgreSQL18 DB and administrator.",
		"The original pinned TLS certificate must already cover site-B.",
		"Configure saves privately; Inspect observes prerequisites without changing the journal.",
		"Review rechecks the exact host/database state and obtains its own Apply token.",
		"Apply queues the exact reviewed Add Site, never a guessed or foreign token.",
		"Resume is offered only when the backend proves a safe retained stage.",
		"Closing this menu does not stop the persistent executor.",
		"Ordinary Remove belongs to the installation and retains both sites' data.",
		"Do not change the original service/TLS or purge data through this menu.",
		"The operator terminal stays English independently of the web language.",
	}
}
func (a *addSiteAdapter) details() []string {
	return []string{"Original installation: " + SafeText(a.installation), "Site-B is independently owned; original journals remain immutable."}
}
func (a *addSiteAdapter) confirmation(action, id string, r serverinstall.Result) (string, string, []string) {
	title, word := "Start reviewed Add Site?", "APPLY"
	installation := r.InstallationOperation
	if installation == "" {
		installation = a.installation
	}
	if action == "resume" {
		title, word = "Resume the retained Add Site?", "RESUME"
	}
	return title, word, []string{
		"Add Site operation: " + SafeText(id),
		"Original installation: " + SafeText(installation),
		"Recorded state: " + SafeText(r.State) + " / " + SafeText(r.Stage),
		"The existing installation and site-A remain independently owned.",
		"The new site uses a distinct database and administrator.",
		"Use only the existing pinned TLS certificate covering all sites.",
		"The existing service may stop for a bounded explicit activation.",
		"All changes run through the native journal and fixed executor.",
		"Apply requires the exact in-session Review token.",
		"Closing the menu never cancels an already queued executor.",
		"Unknown effects require native Status, not guessed retries.",
	}
}
func (a *addSiteAdapter) configure(ctx context.Context, s *session, d *serverinstall.Draft, r serverinstall.Result) (serverinstall.Result, bool, error) {
	fields := []field{
		{"installation_operation", "Original installation operation", "The existing single-site owning installation operation ID; not the new Add Site ID.", false, &a.installation},
		{"site_b_origin", "New site-B URL", "A distinct HTTPS origin on the existing pinned TLS listener.", false, &d.SiteBOrigin},
		{"site_b_dsn", "New site-B database URL", "A third isolated loopback PostgreSQL18 owner/password URL. Hidden; Ctrl-U clears a saved value.", true, &d.SiteBDSN},
		{"site_b_login", "New site-B administrator", "An independent login; do not reuse an existing site administrator.", false, &d.SiteBLogin},
		{"site_b_password", "New site-B password", "Hidden private bootstrap secret; empty Enter retains saved input.", true, &d.SiteBPassword},
	}
	// Authoritative validation is the same backend SaveAddSiteDraft used by
	// the direct CLI; its typed Problem fields drive correction here.
	return configureFields(ctx, s, a, d, r, fields, 0, func() []serverinstall.Problem { return nil })
}
