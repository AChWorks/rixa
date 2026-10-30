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
}
type response struct {
	Review          map[string]string `json:"review,omitempty"`
	NativeOperation string            `json:"native_operation_id,omitempty"`
	Operation       string            `json:"operation_id"`
	Kind            string            `json:"kind"`
	State           string            `json:"state"`
	Code            string            `json:"code"`
	Message         string            `json:"message"`
	Stage           string            `json:"stage,omitempty"`
	Release         string            `json:"release,omitempty"`
	Active          string            `json:"active_release,omitempty"`
	ReviewToken     string            `json:"review_token,omitempty"`
	AdminURL        string            `json:"admin_url,omitempty"`
	Actions         []string          `json:"actions"`
	Problems        []publicProblem   `json:"problems,omitempty"`
	Artifact        publicArtifact    `json:"artifact"`
	AccessClosed    bool              `json:"access_closed"`
}

func (c *Console) status(ctx context.Context) (serverinstall.Result, error) {
	if c.options.Scope.Kind == "update" {
		return c.updater.UpdateStatus(ctx, c.options.Scope.Operation)
	}
	return c.backend.Status(ctx, c.options.Scope.Operation)
}
func (c *Console) present(l *localize.Localizer, r serverinstall.Result, action string) response {
	v := response{Operation: c.options.Scope.Operation, Kind: c.options.Scope.Kind, State: r.State, Code: r.Code, Stage: r.Stage, Release: r.Release, Active: r.ActiveRelease, ReviewToken: r.ReviewToken, AdminURL: r.AdminURL, Actions: []string{}}
	if !c.sameNativeOperation(r) {
		v.NativeOperation = r.OperationID
	}
	v.Message = text(l, "state_"+r.State)
	if v.Message == "state_"+r.State {
		v.Message = text(l, "state_unknown")
	}
	for _, a := range r.Actions {
		if c.allowed[a] && (a == "status" || c.sameNativeOperation(r)) {
			v.Actions = append(v.Actions, a)
		}
	}
	for _, p := range r.Problems {
		v.Problems = append(v.Problems, publicProblem{p.Field, p.Code, text(l, "correct_field")})
	}
	c.mutex.Lock()
	v.Artifact = publicArtifact{c.artifact.Release, c.artifact.Source, c.artifact.SHA256}
	terminal := c.sameNativeOperation(r) && (r.State == "removed" || r.State == "rejected" || (r.State == "installed" && (c.allowed["apply"] || c.allowed["resume"])))
	if terminal && action != "page" {
		c.closed = true
		c.sessionHash = [32]byte{}
	}
	v.AccessClosed = c.closed
	c.mutex.Unlock()
	if action != "review" || !c.sameNativeOperation(r) || v.AccessClosed {
		v.ReviewToken = ""
	}
	return v
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
	return nil
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
		jsonResponse(w, statusCode(e), c.present(view.Localizer, result, "status"))
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
				result, e = c.backend.Resume(r.Context(), c.options.Scope.Operation)
			case "remove":
				result, e = c.backend.Remove(r.Context(), c.options.Scope.Operation)
			}
		}
	}
	if result.OperationID == "" {
		c.fail(w, r, statusCode(e), "unavailable")
		return
	}
	value := c.present(view.Localizer, result, action)
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
