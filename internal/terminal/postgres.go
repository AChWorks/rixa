// SPDX-License-Identifier: MPL-2.0

package terminal

import (
	"context"
	"errors"
	"fmt"
	"strconv"

	"github.com/AChWorks/rixa/internal/serverinstall"
)

type PostgresOptions struct {
	Options
	PostgresPrefill *serverinstall.PostgresDraft
}

func RunPostgres(ctx context.Context, backend serverinstall.PostgresFacade, options PostgresOptions) error {
	if backend == nil {
		return errors.New("native PostgreSQL prerequisite backend is required")
	}
	if options.Options.Prefill != nil {
		return errors.New("PostgreSQL prerequisites do not accept installation credentials")
	}
	a := &postgresAdapter{backend: backend, draft: serverinstall.PostgresDraft{Port: 55432}}
	common := options.Options
	if options.PostgresPrefill != nil {
		a.draft = *options.PostgresPrefill
		common.Prefill = &serverinstall.Draft{OperationID: a.draft.OperationID}
	}
	return runManagement(ctx, a, common, a)
}

type postgresAdapter struct {
	backend       serverinstall.PostgresFacade
	draft         serverinstall.PostgresDraft
	plan          *serverinstall.PostgresPlan
	portText      string
	dedicatedText string
	editing       bool
}

func (p *postgresAdapter) kind() string  { return "postgres" }
func (p *postgresAdapter) title() string { return "RIXA / PostgreSQL prerequisites" }
func (p *postgresAdapter) annotate(r serverinstall.Result) serverinstall.Result {
	r.OperationKind = "postgres"
	return r
}
func (p *postgresAdapter) accept(r serverinstall.PostgresResult, e error) (serverinstall.Result, error) {
	p.plan = r.Plan
	return p.annotate(r.Result), e
}
func (p *postgresAdapter) SaveDraft(ctx context.Context, d serverinstall.Draft) (serverinstall.Result, error) {
	p.draft.OperationID = d.OperationID
	r, e := p.backend.SavePostgresDraft(ctx, p.draft)
	return p.accept(r, e)
}
func (p *postgresAdapter) LoadDraft(ctx context.Context, id string) (serverinstall.Draft, error) {
	d, e := p.backend.LoadPostgresDraft(ctx, id)
	if e != nil {
		return serverinstall.Draft{}, e
	}
	p.draft = d
	p.editing = false
	return serverinstall.Draft{OperationID: d.OperationID}, nil
}
func (p *postgresAdapter) Review(ctx context.Context, id string) (serverinstall.Result, error) {
	r, e := p.backend.ReviewPostgres(ctx, id)
	return p.accept(r, e)
}
func (p *postgresAdapter) Apply(ctx context.Context, id, token string) (serverinstall.Result, error) {
	r, e := p.backend.ApplyPostgres(ctx, id, token)
	return p.accept(r, e)
}
func (p *postgresAdapter) Resume(ctx context.Context, id string) (serverinstall.Result, error) {
	r, e := p.backend.ResumePostgres(ctx, id)
	return p.accept(r, e)
}
func (p *postgresAdapter) Status(ctx context.Context, id string) (serverinstall.Result, error) {
	r, e := p.backend.PostgresStatus(ctx, id)
	return p.accept(r, e)
}
func (p *postgresAdapter) Remove(context.Context, string) (serverinstall.Result, error) {
	return serverinstall.Result{}, errors.New("PostgreSQL prerequisites are retained; removal is not a supported action")
}
func (p *postgresAdapter) actions(allowed []string) []menuAction {
	return filterMenuActions(allowed, []menuAction{{"configure", "Configure clean-host prerequisites"}, {"review", "Review exact prerequisite effects"}, {"apply", "Apply reviewed prerequisites"}, {"resume", "Resume retained prerequisites"}, {"status", "Refresh status"}})
}
func (p *postgresAdapter) help() []string {
	return []string{
		"This explicitly selected profile provisions PostgreSQL 18 on a clean dedicated supported host.",
		"Existing or shared PostgreSQL uses the normal supplied database path.",
		"Review shows exact package versions, cluster, roles/databases and retained resources.",
		"Apply queues only that reviewed plan; a queue is not completion.",
		"Inspect unknown effects before the backend offers Resume.",
		"Credentials stay private and can prefill a new native installation.",
		"Ordinary Rixa removal retains PostgreSQL, packages and databases.",
		"No major upgrade, password reset, TLS issuance or unrelated service replacement.",
	}
}
func (p *postgresAdapter) details() []string {
	if p.plan == nil {
		return nil
	}
	lines := []string{"", "Profile: " + SafeText(p.plan.Profile), "Cluster: " + SafeText(p.plan.Cluster), fmt.Sprintf("Loopback port: %d", p.plan.Port)}
	for _, pkg := range append(append([]serverinstall.PostgresPackage{}, p.plan.CommonPackages...), p.plan.Packages...) {
		lines = append(lines, "Package: "+SafeText(pkg.Name)+" = "+SafeText(pkg.Version))
	}
	for _, effect := range p.plan.Effects {
		lines = append(lines, "Effect: "+SafeText(effect))
	}
	for _, retained := range p.plan.Retained {
		lines = append(lines, "Retain: "+SafeText(retained))
	}
	return lines
}
func (p *postgresAdapter) confirmation(action, id string, _ serverinstall.Result) (string, string, []string) {
	title, word := "Apply reviewed PostgreSQL prerequisites?", "APPLY"
	if action == "resume" {
		title, word = "Resume retained PostgreSQL prerequisites?", "RESUME"
	}
	lines := []string{"Operation: " + SafeText(id), "Only the exact reviewed dedicated-host prerequisite plan will be queued.", "No existing/shared cluster adoption or database major upgrade.", "No passwords or DSNs will be displayed.", "Closing this menu does not cancel an already queued executor."}
	return title, word, append(lines, p.details()...)
}
func (p *postgresAdapter) configure(ctx context.Context, s *session, d *serverinstall.Draft, r serverinstall.Result) (serverinstall.Result, bool, error) {
	if !p.editing {
		p.portText = strconv.Itoa(p.draft.Port)
		p.dedicatedText = ""
		if p.draft.DedicatedHost {
			p.dedicatedText = "DEDICATED"
		}
		p.editing = true
	}
	fields := []field{
		{"port", "PostgreSQL loopback port", "An explicitly unused unprivileged port. Existing listener ownership is never replaced.", false, &p.portText},
		{"dedicated_host", "Dedicated clean host", "Type DEDICATED only for an authorized clean dedicated supported host. Existing/shared PostgreSQL uses supplied databases.", false, &p.dedicatedText},
	}
	validate := func() []serverinstall.Problem {
		p.draft.Port, _ = strconv.Atoi(p.portText)
		p.draft.DedicatedHost = p.dedicatedText == "DEDICATED"
		p.draft.OperationID = d.OperationID
		return serverinstall.ValidatePostgresDraft(p.draft)
	}
	return configureFields(ctx, s, p, d, r, fields, 0, validate)
}
