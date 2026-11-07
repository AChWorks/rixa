// SPDX-License-Identifier: MPL-2.0

package product

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"html/template"
	"log/slog"
	"net/http"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/AChWorks/achrix"
	"github.com/AChWorks/achrix/bridge"
	"github.com/AChWorks/achrix/identity"
	"github.com/AChWorks/achrix/oauth"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

const (
	remoteOAuthScope    = "rixa.content"
	remoteListTool      = "rixa.content.list"
	remoteHeadTool      = "rixa.content.head"
	remoteCreateTool    = "rixa.content.create_draft"
	remoteSaveTool      = "rixa.content.save_draft"
	remoteOperationTool = "rixa.content.own_operation"
)

// Remote is an explicitly enabled, site-owned OAuth + MCP resource. It is not
// a Control credential proxy. Its resource URL, issuer and database are bound
// to the management origin and this site's own Identity and editorial services.
type siteRemote struct {
	issuer   *oauth.Server
	endpoint *bridge.Endpoint
	pool     *pgxpool.Pool
	clients  map[string]RemoteClientConfig
	origin   string
	resource string
}

func newSiteRemote(site ResolvedSite, accounts *identity.Service, editor *EditorialService,
	access *SiteAccess, siteApp *achrix.Application, operator achrix.Principal, logger *slog.Logger) (*siteRemote, error) {
	if site.Remote == nil || !site.Remote.Enabled {
		return nil, nil
	}
	if site.ManagementOrigin == "" || site.PreviewOrigin == "" || accounts == nil || editor == nil ||
		access == nil || siteApp == nil || logger == nil {
		return nil, ErrConfiguration
	}
	pool, err := pgxpool.New(context.Background(), site.DSN)
	if err != nil {
		return nil, fmt.Errorf("remote pool: %w", ErrConfiguration)
	}
	closeOnFailure := func(err error) (*siteRemote, error) { pool.Close(); return nil, err }

	issuerURL := site.ManagementOrigin + "/oauth"
	resourceURL := site.ManagementOrigin + "/mcp"
	enrolled := make([]oauth.Client, 0, len(site.Remote.Clients))
	clients := make(map[string]RemoteClientConfig, len(site.Remote.Clients))
	for _, config := range site.Remote.Clients {
		c := oauth.Client{
			ID: config.ID, Name: config.Name, RedirectURIs: append([]string(nil), config.RedirectURIs...),
			Scopes: []string{remoteOAuthScope}, Enabled: config.Enabled,
		}
		enrolled = append(enrolled, c)
		clients[config.ID] = config
	}
	remote := &siteRemote{pool: pool, clients: clients, origin: site.ManagementOrigin, resource: resourceURL}

	// No caller-selected site, actor, client or channel can become authority:
	// OAuth supplies the independently verified product account and client.
	permit := func(ctx context.Context, grant oauth.Grant) error {
		if grant.Issuer != issuerURL || grant.Resource != resourceURL ||
			len(grant.Scopes) != 1 || grant.Scopes[0] != remoteOAuthScope {
			return oauth.ErrInvalidGrant
		}
		client, exists := clients[grant.ClientID]
		if !exists || !client.Enabled || !validEditorialID(grant.Subject) {
			return oauth.ErrInvalidGrant
		}
		account, err := accounts.Account(ctx, operator, grant.Subject)
		if errors.Is(err, identity.ErrNotFound) || errors.Is(err, achrix.ErrDenied) {
			return oauth.ErrInvalidGrant
		}
		if err != nil {
			return oauth.ErrUnavailable
		}
		if !account.Enabled || account.ID != grant.Subject {
			return oauth.ErrInvalidGrant
		}
		// A current site-local content.read grant is mandatory even for
		// authorized external editors; Service authorization still runs for
		// each distinct operation, including write.
		if err := siteApp.Authorize(ctx, achrix.Principal(grant.Subject), CapabilityContentRead, ContentCollectionTarget); err != nil {
			if errors.Is(err, achrix.ErrDenied) {
				return oauth.ErrInvalidGrant
			}
			return oauth.ErrUnavailable
		}
		return nil
	}
	issuer, err := oauth.New(oauth.Config{
		Issuer: issuerURL, Resource: resourceURL, Clients: enrolled, Pool: pool,
		ClientLookup: func(_ context.Context, id string) (oauth.Client, error) {
			c, ok := clients[id]
			if !ok || !c.Enabled {
				return oauth.Client{}, oauth.ErrInvalidGrant
			}
			return oauth.Client{
				ID: c.ID, Name: c.Name, RedirectURIs: append([]string(nil), c.RedirectURIs...),
				Scopes: []string{remoteOAuthScope}, Enabled: true,
			}, nil
		},
		Session: func(ctx context.Context, r *http.Request) (oauth.Session, error) {
			cookie, err := r.Cookie(identity.CookieName)
			if err != nil || cookie.Value == "" || r.TLS == nil {
				return oauth.Session{}, oauth.ErrInvalidGrant
			}
			principal, err := accounts.Authenticate(ctx, cookie.Value)
			if err != nil || !validEditorialID(string(principal)) {
				return oauth.Session{}, oauth.ErrInvalidGrant
			}
			// Bind consent to the exact current opaque cookie. Logging out or
			// rotating the cookie ends an unfinished consent, not issued grants.
			return oauth.Session{Subject: string(principal), Binding: sha256.Sum256([]byte(cookie.Value))}, nil
		},
		Permit: permit,
		RenderConsent: func(w http.ResponseWriter, r *http.Request, view oauth.ConsentView) {
			// The issuer resolves ClientID from validated pre-enrollment, not
			// a consent form field or an untrusted capability hint.
			client, ok := clients[view.ClientID]
			if !ok || !client.Enabled {
				http.Error(w, "invalid enrolled client", http.StatusForbidden)
				return
			}
			renderRixaOAuthConsent(w, view, client.AllowDraftWrites)
		},
		Diagnostic: func(ctx context.Context, phase, outcome string, cause error) {
			logRemoteOAuthDiagnostic(logger, site.ID, issuerURL, oauth.RequestID(ctx), phase, outcome, cause)
		},
	})
	if err != nil {
		return closeOnFailure(fmt.Errorf("remote oauth: %w", err))
	}
	remote.issuer = issuer

	resource, err := bridge.New(bridge.Config{
		ResourceURL: resourceURL, AuthorizationServers: []string{issuerURL},
		Scopes: []string{remoteOAuthScope},
		Verify: func(ctx context.Context, token string) (bridge.Credential, error) {
			grant, expires, err := issuer.VerifyAccess(ctx, resourceURL, token)
			if err != nil {
				if errors.Is(err, oauth.ErrInvalidGrant) {
					return bridge.Credential{}, bridge.ErrInvalidCredential
				}
				return bridge.Credential{}, err
			}
			return bridge.Credential{
				Caller: bridge.Caller{Principal: achrix.Principal(grant.Subject),
					ClientID: grant.ClientID, ConnectionID: grant.ID},
				ExpiresAt: expires, Scopes: slices.Clone(grant.Scopes),
			}, nil
		},
		Admit: func(ctx context.Context, caller bridge.Caller) error {
			// VerifyAccess already required current client, account status,
			// original grant/resource, and live content.read permission. It
			// never yields a caller provided by an MCP tool argument.
			if c, ok := clients[caller.ClientID]; !ok || !c.Enabled ||
				!validEditorialID(string(caller.Principal)) {
				return achrix.ErrDenied
			}
			return nil
		},
		Gate: func(_ context.Context, caller bridge.Caller, operation string) error {
			c, ok := clients[caller.ClientID]
			if !ok || !c.Enabled {
				return achrix.ErrDenied
			}
			switch operation {
			case remoteListTool, remoteHeadTool:
				return nil
			case remoteCreateTool, remoteSaveTool, remoteOperationTool:
				if c.AllowDraftWrites {
					return nil
				}
			}
			return achrix.ErrDenied
		},
		Diagnostic: func(_ context.Context, d bridge.Diagnostic) {
			logRemoteBridgeDiagnostic(logger, site.ID, resourceURL, d)
		},
	}, func(reg *bridge.Registrar) error {
		return registerRemoteContentTools(reg, editor)
	})
	if err != nil {
		return closeOnFailure(fmt.Errorf("remote bridge: %w", err))
	}
	remote.endpoint = resource
	return remote, nil
}

func (s *siteRemote) serve(w http.ResponseWriter, r *http.Request) bool {
	if s == nil || r.TLS == nil {
		return false
	}
	switch r.URL.Path {
	case "/oauth/authorize", "/oauth/token", "/oauth/revoke", "/.well-known/oauth-authorization-server/oauth":
		s.issuer.ServeHTTP(w, r)
	case "/mcp":
		s.endpoint.ServeHTTP(w, r)
	case "/.well-known/oauth-protected-resource/mcp":
		s.endpoint.MetadataHandler().ServeHTTP(w, r)
	default:
		return false
	}
	return true
}

func (s *siteRemote) close() {
	if s != nil && s.pool != nil {
		s.pool.Close()
	}
}

// The INFO event is the normal operational record for every Bridge outcome,
// including failures, uncertain writes, and post-dispatch delivery errors.
// Raw causes are intentionally confined to a DEBUG diagnostic detail event.
func logRemoteBridgeDiagnostic(logger *slog.Logger, site, resource string, d bridge.Diagnostic) {
	attrs := []any{
		"site", site, "resource", resource,
		"request_id", d.Request.ID,
		"user", string(d.Request.Caller.Principal),
		"client", d.Request.Caller.ClientID,
		"connection", d.Request.Caller.ConnectionID,
		"channel", d.Request.Channel,
		"operation", d.Request.Operation,
		"phase", d.Phase, "dispatched", d.Dispatched,
		"outcome", string(d.Outcome), "code", d.Code,
		"reference", d.Reference,
	}
	if d.Cause != nil {
		attrs = append(attrs, "cause_type", fmt.Sprintf("%T", d.Cause))
	}
	logger.Info("remote bridge operation", attrs...)
	if d.Cause != nil && logger.Enabled(context.Background(), slog.LevelDebug) {
		logger.Debug("remote bridge protected cause", append(attrs, "cause_detail", remoteProtectedCause(d.Cause))...)
	}
}

func logRemoteOAuthDiagnostic(logger *slog.Logger, site, issuer, requestID, phase, outcome string, cause error) {
	if cause == nil {
		return
	}
	attrs := []any{
		"site", site, "issuer", issuer, "request_id", requestID,
		"phase", phase, "outcome", outcome,
		"cause_type", fmt.Sprintf("%T", cause),
	}
	logger.Info("remote oauth failure", attrs...)
	if logger.Enabled(context.Background(), slog.LevelDebug) {
		logger.Debug("remote oauth protected cause", append(attrs, "cause_detail", remoteProtectedCause(cause))...)
	}
}

// Preserve technical root-cause context for authorized DEBUG diagnostics
// while excluding common credential-bearing spans and private payloads.
// Do not log raw unbounded errors in the normal INFO operational channel.
var (
	remoteURLCredentials = regexp.MustCompile(`(?i)\b((?:postgres(?:ql)?|https?|redis(?:s)?|mysql)://)[^\s/@]+@`)
	remoteAuthorization  = regexp.MustCompile(`(?i)\b((?:bearer|basic)\s+)[a-z0-9._~+/-]+=*`)
	remoteSecretField    = regexp.MustCompile(`(?i)\b((?:password|passwd|access[_-]?token|refresh[_-]?token|api[_-]?key|client[_-]?secret|authorization|cookie|secret|dsn|database[_-]?url)"?\s*(?:=|:)\s*)(?:"(?:\\.|[^"])*"|'[^']*'|[^\s&,;}]+)`)
	remotePrivateField   = regexp.MustCompile(`(?is)\b((?:body_html|payload|raw_html)"?\s*(?:=|:)\s*)[^\r\n]+`)
	remoteHTMLPayload    = regexp.MustCompile(`(?s)<[^>]+>.*?</[^>]+>`)
)

func remoteProtectedCause(cause error) string {
	if cause == nil {
		return ""
	}
	detail := cause.Error()
	detail = remoteURLCredentials.ReplaceAllString(detail, "${1}[redacted]@")
	detail = remoteAuthorization.ReplaceAllString(detail, "${1}[redacted]")
	detail = remotePrivateField.ReplaceAllString(detail, "${1}[private content]")
	detail = remoteSecretField.ReplaceAllString(detail, "${1}[redacted]")
	detail = remoteHTMLPayload.ReplaceAllString(detail, "[private markup]")
	// A malicious/long source error is not allowed to produce unbounded
	// diagnostic output or multiple synthetic log lines.
	if len(detail) > 1024 {
		detail = detail[:1024]
	}
	return strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return ' '
		}
		return r
	}, detail)
}

var consentTemplate = template.Must(template.New("rixa-consent").Parse(`<!doctype html>
<html lang="en"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1">
<title>Connect to Rixa</title></head><body><main>
<h1>Authorize Rixa access</h1>
<p><strong>{{.ClientName}}</strong> requests private content access as <strong>{{.Subject}}</strong>.</p>
{{if .AllowDraftWrites}}<p>This client can read authorized private content and can create and save private drafts when your current Rixa edit permissions allow it.</p>
{{else}}<p>This client can inspect authorized private content (read-only). It cannot create or save drafts.</p>{{end}}
<p>This connection does not grant publishing or Theme modification.</p>
<form method="post" action="{{.FormAction}}">
<input type="hidden" name="transaction" value="{{.Transaction}}">
<input type="hidden" name="csrf" value="{{.CSRF}}">
<button type="submit" name="decision" value="allow">Authorize</button>
<button type="submit" name="decision" value="deny">Deny</button>
</form></main></body></html>`))

func renderRixaOAuthConsent(w http.ResponseWriter, view oauth.ConsentView, allowDraftWrites bool) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := consentTemplate.Execute(w, struct {
		oauth.ConsentView
		AllowDraftWrites bool
	}{ConsentView: view, AllowDraftWrites: allowDraftWrites}); err != nil {
		// AChrix's issuer owns the CSRF and one-use pending transaction.
		// Consent rendering failure never implies consent approval.
		return
	}
}

type remoteListInput struct {
	Cursor string `json:"cursor,omitempty"`
	Limit  int    `json:"limit,omitempty"`
}
type remoteHeadInput struct {
	ID string `json:"id"`
}
type remoteCreateInput struct {
	OperationID string      `json:"operation_id"`
	Kind        ContentKind `json:"kind"`
	Title       string      `json:"title"`
	BodyHTML    string      `json:"body_html"`
}
type remoteSaveInput struct {
	OperationID      string `json:"operation_id"`
	ID               string `json:"id"`
	ExpectedRevision string `json:"expected_revision"`
	Title            string `json:"title"`
	BodyHTML         string `json:"body_html"`
}
type remoteOperationInput struct {
	OperationID string `json:"operation_id"`
}
type remoteContentSummary struct {
	ID       string      `json:"id"`
	Kind     ContentKind `json:"kind"`
	Revision string      `json:"revision"`
	Title    string      `json:"title"`
}
type remoteListOutput struct {
	Items      []remoteContentSummary `json:"items"`
	NextCursor string                 `json:"next_cursor,omitempty"`
}
type remoteContentOutput struct {
	ID          string      `json:"id"`
	Kind        ContentKind `json:"kind"`
	Revision    string      `json:"revision"`
	Title       string      `json:"title"`
	BodyHTML    string      `json:"body_html"`
	OperationID string      `json:"operation_id,omitempty"`
}
type remoteOperationOutput struct {
	OperationID    string `json:"operation_id"`
	Kind           string `json:"kind"`
	ResourceID     string `json:"resource_id"`
	ResultRevision string `json:"result_revision"`
}

func toRemoteContent(v ContentRevision, op string) remoteContentOutput {
	return remoteContentOutput{
		ID: v.ID, Kind: v.Kind, Revision: strconv.FormatInt(v.Revision, 10),
		Title: v.Title, BodyHTML: v.BodyHTML, OperationID: op,
	}
}

func remoteFailure(err error, operationID string) error {
	switch {
	case errors.Is(err, achrix.ErrDenied):
		return &bridge.Fault{Code: "permission_denied", Message: "The current account is not authorized.",
			Action: "Check the site account and current permissions.", Outcome: bridge.NotExecuted}
	case errors.Is(err, ErrEditorialInvalid):
		return &bridge.Fault{Code: "invalid_input", Message: "Invalid content or operation input.",
			Action: "Check the typed input contract and bounded content fields.", Outcome: bridge.NotCommitted}
	case errors.Is(err, ErrEditorialConflict):
		return &bridge.Fault{Code: "stale_revision", Message: "The saved revision or operation identity conflicts.",
			Action: "Read the current head and inspect the original operation before changing inputs.", Outcome: bridge.NotCommitted}
	case errors.Is(err, ErrEditorialNotFound):
		return &bridge.Fault{Code: "not_found", Message: "No authorized record was found.",
			Action: "Inspect the original operation; absence is not proof an in-flight write failed.", Outcome: bridge.NotExecuted}
	case errors.Is(err, ErrEditorialUnknownOutcome):
		return &bridge.Fault{Code: "unknown_write", Message: "The write outcome requires reconciliation.",
			Action: "Inspect the original operation ID; do not replay with a new ID.", Outcome: bridge.Unknown,
			Reference: operationID}
	default:
		if operationID != "" {
			return &bridge.Fault{Code: "unknown_write", Message: "The write outcome is not confirmed.",
				Action: "Inspect this operation ID before deciding whether to retry.", Outcome: bridge.Unknown,
				Reference: operationID, Cause: err}
		}
		return &bridge.Fault{Code: "unavailable", Message: "The requested operation is unavailable.",
			Action: "Retry a read when the service is healthy.", Outcome: bridge.Unknown, Cause: err}
	}
}

// Effect hints describe the actual immutable draft/receipt semantics; Bridge
// and the owning product still enforce authorization independent of hints.
func remoteTool(name, description string, readOnly bool) *mcp.Tool {
	closed := false
	return &mcp.Tool{Name: name, Description: description, Annotations: &mcp.ToolAnnotations{
		ReadOnlyHint: readOnly, IdempotentHint: !readOnly,
		DestructiveHint: &closed, OpenWorldHint: &closed,
	}}
}

func registerRemoteContentTools(reg *bridge.Registrar, editor *EditorialService) error {
	if err := bridge.AddTool(reg, remoteTool(remoteListTool, "List bounded private draft content in this verified Rixa site.", true),
		func(ctx context.Context, in remoteListInput) (remoteListOutput, error) {
			req, ok := bridge.RequestFromContext(ctx)
			if !ok {
				return remoteListOutput{}, errors.New("missing verified request")
			}
			page, err := editor.ListPage(ctx, req.Caller.Principal, in.Cursor, in.Limit)
			if err != nil {
				return remoteListOutput{}, remoteFailure(err, "")
			}
			out := remoteListOutput{Items: make([]remoteContentSummary, 0, len(page.Items)), NextCursor: page.NextCursor}
			for _, item := range page.Items {
				out.Items = append(out.Items, remoteContentSummary{ID: item.ID, Kind: item.Kind,
					Revision: strconv.FormatInt(item.HeadRevision, 10), Title: item.Title})
			}
			return out, nil
		}); err != nil {
		return err
	}
	if err := bridge.AddTool(reg, remoteTool(remoteHeadTool, "Read one private draft's current head in this verified Rixa site.", true),
		func(ctx context.Context, in remoteHeadInput) (remoteContentOutput, error) {
			req, ok := bridge.RequestFromContext(ctx)
			if !ok {
				return remoteContentOutput{}, errors.New("missing verified request")
			}
			item, err := editor.Head(ctx, req.Caller.Principal, in.ID)
			if err != nil {
				return remoteContentOutput{}, remoteFailure(err, "")
			}
			return toRemoteContent(item, ""), nil
		}); err != nil {
		return err
	}
	if err := bridge.AddTool(reg, remoteTool(remoteCreateTool, "Create a private draft using an exact reusable operation_id. Does not publish.", false),
		func(ctx context.Context, in remoteCreateInput) (remoteContentOutput, error) {
			req, ok := bridge.RequestFromContext(ctx)
			if !ok {
				return remoteContentOutput{}, errors.New("missing verified request")
			}
			item, err := editor.Create(ctx, req.Caller.Principal, in.OperationID, in.Kind, in.Title, in.BodyHTML)
			if err != nil {
				return remoteContentOutput{}, remoteFailure(err, in.OperationID)
			}
			bridge.Confirm(ctx, in.OperationID)
			return toRemoteContent(item, in.OperationID), nil
		}); err != nil {
		return err
	}
	if err := bridge.AddTool(reg, remoteTool(remoteSaveTool, "CAS-save private draft HTML. expected_revision is a decimal string; never publishes.", false),
		func(ctx context.Context, in remoteSaveInput) (remoteContentOutput, error) {
			req, ok := bridge.RequestFromContext(ctx)
			if !ok {
				return remoteContentOutput{}, errors.New("missing verified request")
			}
			expected, err := strconv.ParseInt(in.ExpectedRevision, 10, 64)
			if err != nil || expected < 1 || strings.HasPrefix(in.ExpectedRevision, "+") ||
				strconv.FormatInt(expected, 10) != in.ExpectedRevision {
				return remoteContentOutput{}, remoteFailure(ErrEditorialInvalid, in.OperationID)
			}
			item, err := editor.Save(ctx, req.Caller.Principal, in.OperationID, in.ID, expected, in.Title, in.BodyHTML)
			if err != nil {
				return remoteContentOutput{}, remoteFailure(err, in.OperationID)
			}
			bridge.Confirm(ctx, in.OperationID)
			return toRemoteContent(item, in.OperationID), nil
		}); err != nil {
		return err
	}
	return bridge.AddTool(reg, remoteTool(remoteOperationTool, "Inspect only your own committed create/save operation receipt. Absent does not prove rollback.", true),
		func(ctx context.Context, in remoteOperationInput) (remoteOperationOutput, error) {
			req, ok := bridge.RequestFromContext(ctx)
			if !ok {
				return remoteOperationOutput{}, errors.New("missing verified request")
			}
			receipt, err := editor.OwnOperation(ctx, req.Caller.Principal, in.OperationID)
			if err != nil {
				return remoteOperationOutput{}, remoteFailure(err, "")
			}
			return remoteOperationOutput{OperationID: receipt.ID, Kind: receipt.Kind,
				ResourceID: receipt.ResourceID, ResultRevision: strconv.FormatInt(receipt.ResultRevision, 10)}, nil
		})
}
