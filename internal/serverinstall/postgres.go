// SPDX-License-Identifier: MPL-2.0
package serverinstall

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"time"

	"github.com/AChWorks/achrix/installation"
)

var _ PostgresFacade = (*Manager)(nil)

func (m *Manager) SavePostgresDraft(ctx context.Context, d PostgresDraft) (PostgresResult, error) {
	if e := m.authorize(); e != nil {
		return PostgresResult{}, e
	}
	if p := ValidatePostgresDraft(d); len(p) > 0 {
		return PostgresResult{Result: Result{OperationKind: "postgres-prerequisite", OperationID: d.OperationID, State: "draft", Code: "invalid-input", Problems: p, Actions: []string{"configure"}}}, ErrInput
	}
	unlock, e := m.lock(ctx, true)
	if e != nil {
		return PostgresResult{}, e
	}
	defer unlock()
	if e = privateDir(m.postgresStateDir(), m.ownerUID, true); e != nil {
		return PostgresResult{}, e
	}
	if owner, e := m.postgresOwner(); e != nil {
		return PostgresResult{}, e
	} else if owner != "" && owner != d.OperationID {
		return PostgresResult{}, ErrBusy
	}
	if old, e := m.loadPostgres(d.OperationID); e == nil {
		if old.Frozen != "" || len(old.Attempts) > 0 {
			return postgresResult(old), ErrState
		}
		old.Draft = d
		old.State, old.Stage, old.Code, old.Message, old.ReviewToken = "draft", "", "draft-saved", "Private PostgreSQL prerequisite draft saved. Review the exact dedicated-host package effects before applying.", ""
		if e = m.savePostgres(old); e != nil {
			return PostgresResult{}, e
		}
		return postgresResult(old), nil
	} else if !errors.Is(e, os.ErrNotExist) {
		return PostgresResult{}, e
	}
	token, e := postgresRandom()
	if e != nil {
		return PostgresResult{}, e
	}
	control, e := postgresRandom()
	if e != nil {
		return PostgresResult{}, e
	}
	site, e := postgresRandom()
	if e != nil {
		return PostgresResult{}, e
	}
	controlVerifier, e := newPostgresVerifier(control)
	if e != nil {
		return PostgresResult{}, e
	}
	siteVerifier, e := newPostgresVerifier(site)
	if e != nil {
		return PostgresResult{}, e
	}
	j := &postgresJournal{Schema: postgresSchema, Draft: d, Token: token, ControlPassword: control, SitePassword: site, ControlVerifier: controlVerifier, SiteVerifier: siteVerifier, State: "draft", Code: "draft-saved", Message: "Private PostgreSQL prerequisite draft saved. Review the exact dedicated-host package effects before applying.", Attempts: map[string]string{}}
	if e = m.savePostgres(j); e != nil {
		return PostgresResult{}, e
	}
	return postgresResult(j), nil
}
func (m *Manager) LoadPostgresDraft(ctx context.Context, id string) (PostgresDraft, error) {
	if e := m.authorize(); e != nil {
		return PostgresDraft{}, e
	}
	if e := ctx.Err(); e != nil {
		return PostgresDraft{}, e
	}
	j, e := m.loadPostgres(id)
	if e != nil {
		return PostgresDraft{}, e
	}
	return j.Draft, nil
}
func (m *Manager) ReviewPostgres(ctx context.Context, id string) (PostgresResult, error) {
	unlock, e := m.lock(ctx, false)
	if e != nil {
		return PostgresResult{}, e
	}
	defer unlock()
	j, e := m.loadPostgres(id)
	if e != nil {
		return PostgresResult{}, e
	}
	if j.Frozen != "" {
		return postgresResult(j), ErrState
	}
	b := newPostgresBackend(m)
	problems := b.review(ctx, j)
	j.State, j.Stage, j.Code, j.Message, j.ReviewToken = "ready", "review", "ready", "Exact prerequisite inputs and package effects reviewed. Apply provisions only this clean dedicated PostgreSQL18 profile.", ""
	if len(problems) > 0 {
		j.State, j.Code, j.Message = "blocked", "operator-action-required", "Preserve existing resources. Supply compatible databases normally, or reconcile the reported unsupported/ambiguous prerequisite."
	} else {
		j.ReviewToken, e = postgresRandom()
		if e != nil {
			return PostgresResult{}, e
		}
		j.ReviewedAt = time.Now().UTC().Format(time.RFC3339Nano)
	}
	if e = m.savePostgres(j); e != nil {
		return PostgresResult{}, e
	}
	r := postgresResult(j)
	r.Problems = problems
	if len(problems) > 0 {
		return r, ErrPrerequisite
	}
	return r, nil
}
func (b *postgresBackend) review(ctx context.Context, j *postgresJournal) []Problem {
	fail := func(code, message string) []Problem { return []Problem{{"postgresql", code, message}} }
	if p := ValidatePostgresDraft(j.Draft); len(p) > 0 {
		return p
	}
	j.Packages = nil
	j.CommonPackages = nil
	j.AssessmentID = ""
	j.Progress = installation.Progress{}

	owner, e := b.m.postgresOwner()
	if e != nil || owner != "" && owner != j.Draft.OperationID {
		return fail("prerequisite-owner", "A retained PostgreSQL prerequisite owns this host. Inspect that operation instead of creating another.")
	}
	baseline, host, e := b.cleanHost(ctx, j)
	if e != nil {
		return fail("dedicated-clean-host-required", "This profile requires Debian13/amd64/systemd and positively unused PostgreSQL packages, accounts, paths, services and port on a dedicated host. Existing/shared/panel-managed or unknown resources require supplied compatible databases or operator reconciliation.")
	}
	image, e := currentManagerImage()
	if e != nil {
		return fail("manager-path", "Restart the trusted manager from root-owned non-writable ancestors before reviewing.")
	}
	helpers := b.helpers()
	if len(helpers) != 4 {
		return fail("base-tools-required", "Trusted apt-get, dpkg-query, dpkg-deb and Debian archive keyring are required on this supported base.")
	}
	dpkgConfig, de := b.dpkgConfiguration()
	if de != nil {
		return fail("dpkg-configuration-unsupported", "This clean profile requires standard dpkg configuration without foreign hooks, forced effects or relocated package state.")
	}
	j.DpkgConfigSHA256 = dpkgConfig
	j.Baseline, j.HostSHA256, j.Helpers, j.ReviewedManagerSHA256 = baseline, host, helpers, digest(image)
	if e = b.prepareAPT(ctx, j); e != nil {
		return fail("authenticated-apt-required", "Official PGDG key or scoped signed Debian/PGDG metadata could not be authenticated. Inspect connectivity, base CA trust and protected planning cache; insecure repositories are never accepted.")
	}
	// Initial selection is version-specific. Exact reviewed versions replace these
	// selector values before any package installation or privileged hook.
	out, e := b.apt(ctx, j, []string{"--simulate", "--no-install-recommends", "--no-remove", "install", "postgresql-18", "postgresql-client-18"})
	if e != nil {
		return fail("package-plan-required", "The signed repository did not produce a supported version-specific PostgreSQL18 plan.")
	}
	packages, e := parsePostgresAPTPlan(out, baseline)
	server := postgresPackageVersionFor(packages, "postgresql-18")
	if e != nil || !strings.HasPrefix(server, "18.") || !strings.Contains(server, ".pgdg13+") || postgresPackageVersionFor(packages, "postgresql-client-18") == "" || postgresPackageVersionFor(packages, "postgresql-common") == "" {
		return fail("package-plan-unsupported", "The plan must add the version-specific official PG18 server/client and exact new dependencies without upgrading or removing any installed package.")
	}
	commonVersion := postgresPackageVersionFor(packages, "postgresql-common")
	j.Packages = packages
	common, e := b.simulate(ctx, j, []PostgresPackage{{"postgresql-common", commonVersion}})
	if e != nil || !postgresPackageSubset(common, packages) || postgresPackageVersionFor(common, "postgresql-common") == "" || postgresPackageVersionFor(common, "postgresql-18") != "" {
		return fail("common-plan-unsupported", "PostgreSQL common tooling must be installable as an exact subset before the server, so implicit main-cluster creation can be disabled first.")
	}
	j.CommonPackages = common
	plan := postgresPublicPlan(j)
	if plan == nil || plan.Assessment == nil {
		return fail("assessment-required", "The shared prerequisite assessment could not bind this exact package plan.")
	}
	j.AssessmentID = plan.Assessment.ID()
	return nil
}
func postgresReviewEvidence(j *postgresJournal) string {
	b, _ := json.Marshal(struct {
		Draft                     PostgresDraft
		Baseline                  map[string]string
		Host, Manager, Key, Lists string
		Helpers                   map[string]string
		Packages, Common          []PostgresPackage
	}{j.Draft, j.Baseline, j.HostSHA256, j.ReviewedManagerSHA256, j.KeySHA256, j.ListsSHA256, j.Helpers, j.Packages, j.CommonPackages})
	return digest(b)
}
func (m *Manager) ApplyPostgres(ctx context.Context, id, token string) (PostgresResult, error) {
	unlock, e := m.lock(ctx, false)
	if e != nil {
		return PostgresResult{}, e
	}
	defer unlock()
	j, e := m.loadPostgres(id)
	if e != nil {
		return PostgresResult{}, e
	}
	if j.Frozen != "" {
		return postgresResult(j), nil
	}
	reviewed, e := time.Parse(time.RFC3339Nano, j.ReviewedAt)
	if j.State != "ready" || e != nil || time.Since(reviewed) < 0 || time.Since(reviewed) > 30*time.Minute || !hashSyntax.MatchString(token) || subtle.ConstantTimeCompare([]byte(token), []byte(j.ReviewToken)) != 1 {
		r := postgresResult(j)
		r.ReviewToken = ""
		r.Code, r.Message = "review-stale", "Review the current prerequisite inputs and exact package effects again before applying."
		return r, ErrState
	}
	b := newPostgresBackend(m)
	baseline, host, e := b.cleanHost(ctx, j)
	if e != nil || !reflect.DeepEqual(baseline, j.Baseline) || host != j.HostSHA256 || !b.reviewedAPT(j) {
		j.State, j.Code, j.Message, j.ReviewToken = "blocked", "review-evidence-changed", "Host, tools or signed package metadata changed; inspect it and review again.", ""
		if e = m.savePostgres(j); e != nil {
			return PostgresResult{}, e
		}
		return postgresResult(j), ErrState
	}
	image, e := currentManagerImage()
	if e != nil || digest(image) != j.ReviewedManagerSHA256 {
		return postgresResult(j), ErrManagerChanged
	}
	manager, e := m.snapshotManager()
	if e != nil {
		return postgresResult(j), e
	}
	if filepath.Base(manager) != j.ReviewedManagerSHA256 {
		return postgresResult(j), ErrManagerChanged
	}
	path := filepath.Join(m.postgresStateDir(), "owner")
	if e = writeNew(path, []byte(id+"\n"), 0600); e != nil {
		owner, re := m.postgresOwner()
		if re != nil || owner != id {
			return postgresResult(j), ErrBusy
		}
	}
	j.ManagerSHA256, j.Frozen = filepath.Base(manager), postgresIntentDigest(j)
	j.Progress.PlanID = j.AssessmentID
	j.Stage = "executor"
	return m.queuePostgres(ctx, j)
}
func postgresExecutorName(j *postgresJournal) string {
	return "rixa-postgres-" + j.Draft.OperationID + "-" + strconv.Itoa(j.ExecutorGeneration) + ".service"
}
func (m *Manager) queuePostgres(ctx context.Context, j *postgresJournal) (PostgresResult, error) {
	j.ExecutorGeneration++
	j.ExecutorAcknowledged = false
	j.Attempts["executor"] = "attempted"
	j.State, j.Code, j.Message = "applying", "queued", "A fixed PostgreSQL prerequisite executor is queued independently of this terminal; refresh status."
	if e := m.savePostgres(j); e != nil {
		return PostgresResult{}, e
	}
	_, e := m.run(ctx, "/usr/bin/systemd-run", "--unit="+strings.TrimSuffix(postgresExecutorName(j), ".service"), "--property=Type=exec", "--property=RuntimeMaxSec=2400", "--property=KillMode=control-group", "--collect", filepath.Join(managerRoot, j.ManagerSHA256), "manage", "internal-postgres-apply", j.Draft.OperationID)
	if e != nil {
		return m.postgresFail(j, "executor-acknowledgement-unknown", "Inspect the exact executor "+postgresExecutorName(j)+" and retained state; an unknown start is never replayed.", e)
	}
	j.ExecutorAcknowledged = true
	if e = m.savePostgres(j); e != nil {
		return PostgresResult{}, e
	}
	return postgresResult(j), nil
}
func (m *Manager) postgresFenced(ctx context.Context, j *postgresJournal) bool {
	return j.ExecutorGeneration > 0 && (j.ExecutorAcknowledged || j.Attempts["executor"] == "verified") && m.unitQuiesced(ctx, postgresExecutorName(j), true)
}
func (m *Manager) postgresResumable(ctx context.Context, j *postgresJournal) bool {
	if j.Frozen == "" || j.ExecutorGeneration >= 16 || (j.State != "applying" && j.State != "reconciliation-required") || !m.postgresFenced(ctx, j) || !postgresManagerIntact(j) {
		return false
	}
	owner, e := m.postgresOwner()
	if e != nil || owner != j.Draft.OperationID {
		return false
	}
	b := newPostgresBackend(m)
	if !b.reviewedAPT(j) || b.allowedInventory(ctx, j) != nil {
		return false
	}
	if j.Attempts["cluster"] != "" && b.observeCluster(ctx, j) != nil {
		return false
	}
	if (j.Stage == "control-db" || j.Stage == "site-db") && j.Attempts[j.Stage] == "attempted" {
		return b.databaseOwned(ctx, j, strings.TrimSuffix(j.Stage, "-db")) == nil
	}
	return true
}
func (m *Manager) ResumePostgres(ctx context.Context, id string) (PostgresResult, error) {
	unlock, e := m.lock(ctx, false)
	if e != nil {
		return PostgresResult{}, e
	}
	defer unlock()
	j, e := m.loadPostgres(id)
	if e != nil {
		return PostgresResult{}, e
	}
	if !m.postgresResumable(ctx, j) {
		return postgresResult(j), ErrState
	}
	image, e := currentManagerImage()
	if e != nil || digest(image) != j.ManagerSHA256 {
		r := postgresResult(j)
		r.Code, r.Message = "manager-changed", "Resume with the exact retained manager at "+filepath.Join(managerRoot, j.ManagerSHA256)+"."
		return r, ErrManagerChanged
	}
	return m.queuePostgres(ctx, j)
}
func (m *Manager) PostgresStatus(ctx context.Context, id string) (PostgresResult, error) {
	if e := m.authorize(); e != nil {
		return PostgresResult{}, e
	}
	if e := ctx.Err(); e != nil {
		return PostgresResult{}, e
	}
	j, e := m.loadPostgres(id)
	if e != nil {
		return PostgresResult{}, e
	}
	r := postgresResult(j)
	r.ReviewToken = ""
	if j.State == "applying" || j.State == "reconciliation-required" {
		out, e := m.run(ctx, "/usr/bin/systemctl", "show", "--property=ActiveState", "--value", postgresExecutorName(j))
		if e == nil && (strings.TrimSpace(string(out)) == "active" || strings.TrimSpace(string(out)) == "activating") {
			r.Message = "Prerequisite executor is active; refresh retained progress."
			return r, nil
		}
		if m.postgresResumable(ctx, j) {
			r.Actions = []string{"resume", "status"}
		}
		if j.State == "applying" {
			r.State, r.Code, r.Message = "reconciliation-required", "executor-not-active", "The executor is no longer active. Inspect retained package/cluster intent before further effects."
		}
	}
	if j.State == "installed" {
		if e = newPostgresBackend(m).verify(ctx, j); e != nil {
			r.Code, r.Message = "prerequisite-unhealthy", "Owned prerequisite is retained, but package/cluster/database checks did not verify. Inspect the exact owning operation; no password reset or database replacement is offered."
			return r, e
		}
		r.Code, r.Message = "healthy", "Owned version-specific PostgreSQL18, loopback cluster, separate least-privilege database identities and password connectivity are verified."
	}
	return r, nil
}

func postgresIntentDigest(j *postgresJournal) string {
	return digest([]byte(postgresReviewEvidence(j) + ":" + j.Token + ":" + j.ControlPassword + ":" + j.SitePassword + ":" + j.ReviewToken + ":" + j.AssessmentID + ":" + j.DpkgConfigSHA256 + ":" + j.ControlVerifier + ":" + j.SiteVerifier))
}
