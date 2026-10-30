// SPDX-License-Identifier: MPL-2.0
package manageweb

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"os"
	"slices"
	"strings"

	shell "github.com/AChWorks/achrix/admin"
	"github.com/AChWorks/achrix/admin/localize"
	"github.com/AChWorks/rixa/internal/serverinstall"
)

type artifact struct{ Release, Source, Archive, Bundle, SHA256 string }
type publicArtifact struct {
	Release string `json:"release"`
	Source  string `json:"source_commit"`
	SHA256  string `json:"sha256"`
}
type publicProblem struct {
	Field   string `json:"field"`
	Code    string `json:"code"`
	Message string `json:"message"`
	Detail  string `json:"technical_detail,omitempty"`
}
type response struct {
	Review            map[string]string `json:"review,omitempty"`
	NativeOperation   string            `json:"native_operation_id,omitempty"`
	Operation         string            `json:"operation_id"`
	Kind              string            `json:"kind"`
	State             string            `json:"state"`
	Code              string            `json:"code"`
	Message           string            `json:"message"`
	TechnicalDetail   string            `json:"technical_detail,omitempty"`
	TechnicalLanguage string            `json:"technical_language,omitempty"`
	Stage             string            `json:"stage,omitempty"`
	Release           string            `json:"release,omitempty"`
	Active            string            `json:"active_release,omitempty"`
	ReviewToken       string            `json:"review_token,omitempty"`
	AdminURL          string            `json:"admin_url,omitempty"`
	Actions           []string          `json:"actions"`
	Problems          []publicProblem   `json:"problems,omitempty"`
	Artifact          publicArtifact    `json:"artifact"`
	ArtifactUnsaved   bool              `json:"artifact_unsaved"`
	ArtifactAttempt   string            `json:"artifact_attempt,omitempty"`
	AccessClosed      bool              `json:"access_closed"`
}

func (c *Console) status(ctx context.Context) (serverinstall.Result, error) {
	if c.options.Scope.Kind == "update" {
		return c.updater.UpdateStatus(ctx, c.options.Scope.Operation)
	}
	return c.backend.Status(ctx, c.options.Scope.Operation)
}
func (c *Console) present(request *http.Request, l *localize.Localizer, r serverinstall.Result, action string, nativeErr error, supplied ...string) response {
	v := response{Operation: c.options.Scope.Operation, Kind: c.options.Scope.Kind, State: r.State, Code: r.Code, Stage: r.Stage, Release: r.Release, Active: r.ActiveRelease, ReviewToken: r.ReviewToken, AdminURL: r.AdminURL, Actions: []string{}}
	if !c.sameNativeOperation(r) {
		v.NativeOperation = r.OperationID
	}
	complete := nativeErr == nil && positivelyComplete(r)
	v.Message = text(l, "state_"+r.State)
	if r.State == "installed" && !complete {
		v.Message = text(l, "state_installed_unconfirmed")
	}
	if v.Message == "state_"+r.State {
		v.Message = text(l, "state_unknown")
	}
	for _, a := range r.Actions {
		if c.allowed[a] && (a == "status" || c.sameNativeOperation(r)) {
			v.Actions = append(v.Actions, a)
		}
	}
	redact, available := c.diagnosticRedactor(request, r.ReviewToken, supplied...)
	if available {
		v.TechnicalDetail = redact(r.Message)
		v.TechnicalLanguage = "en"
	} else {
		v.TechnicalDetail = text(l, "diagnostic_unavailable")
		v.TechnicalLanguage = l.Locale().Tag
	}
	for i, p := range r.Problems {
		if i >= 32 {
			break
		}
		detail := ""
		if available {
			detail = redact(p.Message)
		}
		v.Problems = append(v.Problems, publicProblem{Field: p.Field, Code: p.Code, Message: text(l, "correct_field"), Detail: detail})
	}
	a, attempt, unsaved, _ := c.artifactState(request.Context())
	v.Artifact = publicArtifact{a.Release, a.Source, a.SHA256}
	v.ArtifactAttempt = attempt
	v.ArtifactUnsaved = unsaved
	if unsaved {
		v.Actions = slices.DeleteFunc(v.Actions, func(action string) bool { return action == "review" || action == "apply" })
	}
	c.mutex.Lock()
	terminal := nativeErr == nil && c.sameNativeOperation(r) && (r.State == "removed" || r.State == "rejected" || (complete && (c.allowed["apply"] || c.allowed["resume"])))
	if terminal && action != "page" {
		c.closed = true
		c.sessionHash = [32]byte{}
	}
	v.AccessClosed = c.closed
	c.mutex.Unlock()
	if action != "review" || !c.sameNativeOperation(r) || v.AccessClosed || unsaved {
		v.ReviewToken = ""
	}
	return v
}

// A retained installed state can still need reconciliation or be unhealthy.
// Only these positive native receipts, without offered recovery, close access.
func positivelyComplete(r serverinstall.Result) bool {
	return r.State == "installed" && !slices.Contains(r.Actions, "resume") &&
		(r.Code == "healthy" || r.Code == "installed" || r.Code == "updated")
}
func statusCode(err error) int {
	switch {
	case err == nil:
		return 200
	case errors.Is(err, serverinstall.ErrInput):
		return 400
	case errors.Is(err, serverinstall.ErrAuthority):
		return 403
	case errors.Is(err, serverinstall.ErrBusy), errors.Is(err, serverinstall.ErrState), errors.Is(err, serverinstall.ErrManagerChanged):
		return 409
	default:
		return 503
	}
}
func (c *Console) gate(ctx context.Context, action string) error {
	if !c.allowed[action] {
		return serverinstall.ErrAuthority
	}
	r, e := c.status(ctx)
	if e != nil && r.OperationID == "" {
		return e
	}
	if action != "status" && !c.sameNativeOperation(r) {
		return serverinstall.ErrState
	}
	if action != "status" && !slices.Contains(r.Actions, action) {
		return serverinstall.ErrState
	}
	if action == "review" || action == "apply" {
		_, _, unsaved, err := c.artifactState(ctx)
		if err != nil {
			return err
		}
		if unsaved {
			return serverinstall.ErrState
		}
	}
	return nil
}

// Staging never edits a native draft. Compare exact private references as well
// as public identity so even a newly staged copy of the same release needs Save.
// The paths stay server-side; a failed read cannot make an unsaved selection ready.
func (c *Console) artifactState(ctx context.Context) (artifact, string, bool, error) {
	c.mutex.Lock()
	a, attempt := c.artifact, c.artifactAttempt
	c.mutex.Unlock()
	if a.Archive == "" {
		return a, attempt, false, nil
	}
	var archive, bundle, release, source string
	var err error
	if c.options.Scope.Kind == "update" {
		var d serverinstall.UpdateDraft
		d, err = c.updater.LoadUpdateDraft(ctx, c.options.Scope.Operation)
		archive, bundle, release, source = d.ArchivePath, d.BundlePath, d.Release, d.SourceCommit
	} else {
		var d serverinstall.Draft
		d, err = c.backend.LoadDraft(ctx, c.options.Scope.Operation)
		archive, bundle, release, source = d.ArchivePath, d.BundlePath, d.Release, d.SourceCommit
	}
	if errors.Is(err, os.ErrNotExist) {
		return a, attempt, true, nil
	}
	return a, attempt, err != nil || archive != a.Archive || bundle != a.Bundle || release != a.Release || source != a.Source, err
}

type configureInput struct {
	ControlOrigin   string `json:"control_origin"`
	SiteOrigin      string `json:"site_origin"`
	Listen          string `json:"listen"`
	ControlDSN      string `json:"control_dsn"`
	SiteDSN         string `json:"site_dsn"`
	ControlLogin    string `json:"control_login"`
	SiteLogin       string `json:"site_login"`
	ControlPassword string `json:"control_password"`
	SitePassword    string `json:"site_password"`
	Language        string `json:"language"`
}

func (c *Console) configure(ctx context.Context, in configureInput) (serverinstall.Result, error) {
	c.mutex.Lock()
	a := c.artifact
	c.mutex.Unlock()
	if c.options.Scope.Kind == "update" {
		d, e := c.updater.LoadUpdateDraft(ctx, c.options.Scope.Operation)
		if e != nil {
			if !errors.Is(e, os.ErrNotExist) {
				return serverinstall.Result{}, e
			}
			d = serverinstall.UpdateDraft{}
		}
		d.OperationID = c.options.Scope.Operation
		d.InstallationOperation = c.options.Scope.Installation
		if a.Archive != "" {
			d.ArchivePath = a.Archive
			d.BundlePath = a.Bundle
			d.Release = a.Release
			d.SourceCommit = a.Source
		}
		return c.updater.SaveUpdateDraft(ctx, d)
	}
	d, e := c.backend.LoadDraft(ctx, c.options.Scope.Operation)
	if e != nil {
		if !errors.Is(e, os.ErrNotExist) {
			return serverinstall.Result{}, e
		}
		d = serverinstall.Draft{}
	}
	d.OperationID = c.options.Scope.Operation
	if a.Archive != "" {
		d.ArchivePath = a.Archive
		d.BundlePath = a.Bundle
		d.Release = a.Release
		d.SourceCommit = a.Source
	}
	d.TLSCertPath = c.options.RuntimeCertificate
	d.TLSKeyPath = c.options.RuntimeKey
	d.ControlOrigin = in.ControlOrigin
	d.SiteOrigin = in.SiteOrigin
	d.Listen = in.Listen
	d.ControlLogin = in.ControlLogin
	d.SiteLogin = in.SiteLogin
	d.Language = in.Language
	// An empty secret retains an already protected draft value; it never returns
	// that value to the browser. Initial empty values fail normal native validation.
	if in.ControlDSN != "" {
		d.ControlDSN = in.ControlDSN
	}
	if in.SiteDSN != "" {
		d.SiteDSN = in.SiteDSN
	}
	if in.ControlPassword != "" {
		d.ControlPassword = in.ControlPassword
	}
	if in.SitePassword != "" {
		d.SitePassword = in.SitePassword
	}
	return c.backend.SaveDraft(ctx, d)
}
func (c *Console) surface(w http.ResponseWriter, r *http.Request, view shell.Request) {
	if r.URL.Path == "/" && r.Method == http.MethodGet {
		c.page(w, r, view)
		return
	}
	if r.URL.Path == "/status" && r.Method == http.MethodGet {
		result, e := c.status(r.Context())
		jsonResponse(w, statusCode(e), c.present(r, view.Localizer, result, "status", e))
		return
	}
	if r.URL.Path == "/console.css" && r.Method == http.MethodGet {
		assetResponse(w, "text/css; charset=utf-8", style)
		return
	}
	if r.URL.Path == "/console.js" && r.Method == http.MethodGet {
		assetResponse(w, "text/javascript; charset=utf-8", client)
		return
	}
	action := strings.TrimPrefix(r.URL.Path, "/")
	if r.Method != http.MethodPost || !slices.Contains([]string{"configure", "review", "apply", "resume", "remove"}, action) {
		c.fail(w, r, 404, "invalid_request")
		return
	}
	if !c.effects.TryLock() {
		c.fail(w, r, 409, "busy")
		return
	}
	defer c.effects.Unlock()
	if e := c.gate(r.Context(), action); e != nil {
		c.fail(w, r, statusCode(e), "action_unavailable")
		return
	}
	var result serverinstall.Result
	var supplied []string
	var e error
	if action == "configure" {
		var in configureInput
		if decode(w, r, &in, 32<<10) != nil {
			c.fail(w, r, 400, "invalid_request")
			return
		}
		if c.options.Scope.Kind == "update" && in != (configureInput{}) {
			c.fail(w, r, 400, "invalid_request")
			return
		}
		supplied = []string{in.ControlDSN, in.SiteDSN, in.ControlPassword, in.SitePassword}
		result, e = c.configure(r.Context(), in)
	} else {
		var input struct {
			ReviewToken  string `json:"review_token,omitempty"`
			Confirmation string `json:"confirmation,omitempty"`
		}
		if decode(w, r, &input, 2048) != nil {
			c.fail(w, r, 400, "invalid_request")
			return
		}
		if (action != "apply" && input.ReviewToken != "") || (action != "remove" && input.Confirmation != "") || (action == "apply" && len(input.ReviewToken) != 64) || (action == "remove" && input.Confirmation != "REMOVE") {
			c.fail(w, r, 400, "invalid_request")
			return
		}
		supplied = append(supplied, input.ReviewToken)
		if c.options.Scope.Kind == "update" {
			switch action {
			case "review":
				result, e = c.updater.ReviewUpdate(r.Context(), c.options.Scope.Operation)
			case "apply":
				result, e = c.updater.ApplyUpdate(r.Context(), c.options.Scope.Operation, input.ReviewToken)
			case "resume":
				result, e = c.updater.ResumeUpdate(r.Context(), c.options.Scope.Operation)
			case "remove":
				result, e = c.updater.RemoveUpdatedInstallation(r.Context(), c.options.Scope.Operation)
			}
		} else {
			switch action {
			case "review":
				result, e = c.backend.Review(r.Context(), c.options.Scope.Operation)
			case "apply":
				result, e = c.backend.Apply(r.Context(), c.options.Scope.Operation, input.ReviewToken)
			case "resume":
				result, e = c.installationEffect(r.Context(), "resume")
			case "remove":
				result, e = c.installationEffect(r.Context(), "remove")
			}
		}
	}
	if result.OperationID == "" {
		c.fail(w, r, statusCode(e), "unavailable")
		return
	}
	value := c.present(r, view.Localizer, result, action, e, supplied...)
	if action == "review" && e == nil {
		summary, err := c.reviewSummary(r.Context())
		if err != nil {
			c.fail(w, r, 503, "unavailable")
			return
		}
		value.Review = summary
	}
	jsonResponse(w, statusCode(e), value)
}

// The earlier presentation gate is advisory. Only the native strict seam binds
// the original operation under the native effect lock, after body decoding.
func (c *Console) installationEffect(ctx context.Context, action string) (serverinstall.Result, error) {
	strict, ok := c.backend.(serverinstall.InstallationEffects)
	if !ok {
		return serverinstall.Result{}, serverinstall.ErrAuthority
	}
	switch action {
	case "resume":
		return strict.ResumeInstallation(ctx, c.options.Scope.Operation)
	case "remove":
		return strict.RemoveInstallation(ctx, c.options.Scope.Operation)
	default:
		return serverinstall.Result{}, serverinstall.ErrInput
	}
}

func (c *Console) sameNativeOperation(r serverinstall.Result) bool {
	if r.OperationID != c.options.Scope.Operation {
		return false
	}
	if c.options.Scope.Kind == "install" {
		return r.OperationKind == "" || r.OperationKind == "install"
	}
	return r.OperationKind == "update" && (r.InstallationOperation == c.options.Scope.Installation || (r.State == "not-installed" && r.InstallationOperation == ""))
}

func (c *Console) reviewSummary(ctx context.Context) (map[string]string, error) {
	if c.options.Scope.Kind == "update" {
		d, e := c.updater.LoadUpdateDraft(ctx, c.options.Scope.Operation)
		if e != nil {
			return nil, e
		}
		return map[string]string{"release": d.Release, "source": d.SourceCommit}, nil
	}
	d, e := c.backend.LoadDraft(ctx, c.options.Scope.Operation)
	if e != nil {
		return nil, e
	}
	return map[string]string{"release": d.Release, "source": d.SourceCommit, "control_origin": d.ControlOrigin, "site_origin": d.SiteOrigin, "listen": d.Listen, "control_login": d.ControlLogin, "site_login": d.SiteLogin, "control_dsn": redactedDatabase(d.ControlDSN), "site_dsn": redactedDatabase(d.SiteDSN)}, nil
}
func redactedDatabase(raw string) string {
	u, e := url.Parse(raw)
	if e != nil || u.Host == "" || u.User == nil || (u.Scheme != "postgres" && u.Scheme != "postgresql") {
		return "private"
	}
	query := u.Query()
	if len(query) != 1 || query.Get("sslmode") != "disable" || len(query["sslmode"]) != 1 {
		return "private"
	}
	u.User = url.User(u.User.Username())
	return u.String()
}
