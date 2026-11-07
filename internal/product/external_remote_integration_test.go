// SPDX-License-Identifier: MPL-2.0
package product

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/tls"
	"encoding/base64"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/AChWorks/achrix"
	"github.com/AChWorks/achrix/identity"
	"github.com/jackc/pgx/v5"
)

const remoteTestOrigin = "https://a.manage.remote.test"
const remoteTestResource = remoteTestOrigin + "/mcp"
const remoteTestCallback = "https://oauth-client.remote.test/callback"

// Real HTTPS + real PostgreSQL end-to-end proving that an actual product
// session receives an OAuth grant and executes ONLY the owning site service.
// This is test/staging proof for the released AChrix OAuth preview, not a
// claim of hosted ChatGPT/Claude/Gateway interoperability or production support.
func TestRemoteOAuthMCPRealSiteDraftAndRevocation(t *testing.T) {
	adminDSN := os.Getenv("RIXA_TEST_POSTGRES_DSN")
	if adminDSN == "" {
		if os.Getenv("CI") != "" {
			t.Fatal("RIXA_TEST_POSTGRES_DSN required for real remote integration")
		}
		t.Skip("real PostgreSQL is required for OAuth/MCP integration")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	dsns := createTestDatabases(t, ctx, adminDSN)
	dir := t.TempDir()
	sites := []SiteConfig{
		{ID: "site-a", Origin: "https://a.public.remote.test", ManagementOrigin: remoteTestOrigin,
			PreviewOrigin: "https://a.preview.remote.test", DatabaseEnv: "RIXA_REMOTE_A",
			MediaRoot: filepath.Join(dir, "ma"), PublicRoot: filepath.Join(dir, "pa"),
			PublicPolicy: testPublicPolicy(),
			Remote: &RemoteConfig{Enabled: true, Clients: []RemoteClientConfig{
				{ID: "test-editor", Name: "Test editor", RedirectURIs: []string{remoteTestCallback}, Enabled: true, AllowDraftWrites: true},
				{ID: "test-reader", Name: "Test reader", RedirectURIs: []string{remoteTestCallback}, Enabled: true},
			}},
		},
		{ID: "site-b", Origin: "https://b.public.remote.test", ManagementOrigin: "https://b.manage.remote.test",
			PreviewOrigin: "https://b.preview.remote.test", DatabaseEnv: "RIXA_REMOTE_B",
			MediaRoot: filepath.Join(dir, "mb"), PublicRoot: filepath.Join(dir, "pb"),
			PublicPolicy: testPublicPolicy(),
			// Disabling the connector keeps client registration metadata without
			// installing its database schema or exposing an MCP/OAuth handler.
			Remote: &RemoteConfig{Enabled: false, Clients: []RemoteClientConfig{{
				ID: "retained-disabled-client", Name: "Retained but inert",
				RedirectURIs: []string{remoteTestCallback}, Enabled: true,
			}}},
		},
	}
	for _, site := range sites {
		if err := os.Mkdir(site.MediaRoot, 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.Mkdir(site.PublicRoot, 0700); err != nil {
			t.Fatal(err)
		}
	}
	hosts := []string{"control.remote.test", "a.public.remote.test", "a.manage.remote.test",
		"a.preview.remote.test", "b.public.remote.test", "b.manage.remote.test", "b.preview.remote.test"}
	cert, key, roots := writeTestCertificate(t, dir, hosts)
	config := Config{Listen: "127.0.0.1:19443", TLS: TLSConfig{CertFile: cert, KeyFile: key},
		Language: "en", StartupTimeout: 20 * time.Second, ShutdownTimeout: 10 * time.Second,
		Control: ControlConfig{Origin: "https://control.remote.test", DatabaseEnv: "RIXA_REMOTE_CONTROL"},
		Sites:   sites,
	}
	env := mapLookup(map[string]string{"RIXA_REMOTE_CONTROL": dsns["control"], "RIXA_REMOTE_A": dsns["a"], "RIXA_REMOTE_B": dsns["b"]})
	if err := config.validateStructure(); err != nil {
		t.Fatal(err)
	}
	if err := Migrate(ctx, config, env); err != nil {
		t.Fatalf("remote migration: %v", err)
	}
	// Applying OAuth only to site A must not create an unused issuer schema on B.
	for _, tc := range []struct {
		dsn  string
		want bool
	}{{dsns["a"], true}, {dsns["b"], false}} {
		conn, err := pgx.Connect(ctx, tc.dsn)
		if err != nil {
			t.Fatal(err)
		}
		var installed bool
		err = conn.QueryRow(ctx, "SELECT to_regnamespace('oauth') IS NOT NULL").Scan(&installed)
		_ = conn.Close(ctx)
		if err != nil || installed != tc.want {
			t.Fatalf("OAuth schema expected %v, got %v (%v)", tc.want, installed, err)
		}
	}
	ctl, err := BootstrapAdmin(ctx, config, "control", "remote-control", testControlPassword, env, nil)
	if err != nil {
		t.Fatal(err)
	}
	aAdmin, err := BootstrapAdmin(ctx, config, "site:site-a", "remote-a", testSiteAPassword, env, nil)
	if err != nil {
		t.Fatal(err)
	}
	bAdmin, err := BootstrapAdmin(ctx, config, "site:site-b", "remote-b", testSiteBPassword, env, nil)
	if err != nil {
		t.Fatal(err)
	}
	config.Control.AdminPrincipal = ctl.Account.ID
	config.Sites[0].AdminPrincipal = aAdmin.Account.ID
	config.Sites[1].AdminPrincipal = bAdmin.Account.ID
	resolved, err := config.ResolveRuntime(ctx, env)
	if err != nil {
		t.Fatal(err)
	}
	runtime, err := BuildRuntime(resolved, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := runtime.Start(ctx); err != nil {
		t.Fatal(err)
	}
	defer func() {
		stopCtx, c := context.WithTimeout(context.Background(), 10*time.Second)
		defer c()
		if err := runtime.Shutdown(stopCtx); err != nil {
			t.Errorf("shutdown: %v", err)
		}
	}()

	pair, err := tls.LoadX509KeyPair(cert, key)
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewUnstartedServer(runtime.Handler)
	// The official SDK rejects a public canonical Host on a loopback TCP
	// LocalAddr as DNS rebinding. Exercise an actual non-loopback HTTPS socket
	// rather than disabling its protection or rewriting the request Host.
	listener := remoteTestNonLoopbackListener(t)
	if err := srv.Listener.Close(); err != nil {
		t.Fatal(err)
	}
	srv.Listener = listener
	srv.TLS = &tls.Config{Certificates: []tls.Certificate{pair}, MinVersion: tls.VersionTLS12}
	srv.StartTLS()
	defer srv.Close()
	transport := &http.Transport{
		TLSClientConfig: &tls.Config{RootCAs: roots, MinVersion: tls.VersionTLS12},
		DialContext: func(ctx context.Context, _ string, _ string) (net.Conn, error) {
			return (&net.Dialer{Timeout: 3 * time.Second}).DialContext(ctx, "tcp", srv.Listener.Addr().String())
		},
	}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, Timeout: 10 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}

	site := runtime.Sites["site-a"]
	admin := site.adminPrincipal
	writer, err := site.Identity.CreateAccount(ctx, admin, "remote-writer", "Writer-Password-2026!")
	if err != nil {
		t.Fatal(err)
	}
	reader, err := site.Identity.CreateAccount(ctx, admin, "remote-reader", "Reader-Password-2026!")
	if err != nil {
		t.Fatal(err)
	}
	_, err = site.Identity.CreateAccount(ctx, admin, "remote-without", "Without-Password-2026!")
	if err != nil {
		t.Fatal(err)
	}
	_, err = site.Access.Set(ctx, admin, writer.ID, CapabilityContentRead, true, 0)
	if err != nil {
		t.Fatal(err)
	}
	_, err = site.Access.Set(ctx, admin, writer.ID, CapabilityContentEdit, true, 0)
	if err != nil {
		t.Fatal(err)
	}
	_, err = site.Access.Set(ctx, admin, reader.ID, CapabilityContentRead, true, 0)
	if err != nil {
		t.Fatal(err)
	}
	writerSession, err := site.Identity.Login(ctx, "remote-writer", "Writer-Password-2026!", "")
	if err != nil {
		t.Fatal(err)
	}
	readerSession, err := site.Identity.Login(ctx, "remote-reader", "Reader-Password-2026!", "")
	if err != nil {
		t.Fatal(err)
	}
	noSession, err := site.Identity.Login(ctx, "remote-without", "Without-Password-2026!", "")
	if err != nil {
		t.Fatal(err)
	}

	do := func(method, url string, form url.Values, token, cookie, origin string) (int, http.Header, []byte) {
		t.Helper()
		var body io.Reader
		if form != nil {
			body = strings.NewReader(form.Encode())
		}
		req, err := http.NewRequestWithContext(ctx, method, url, body)
		if err != nil {
			t.Fatal(err)
		}
		if form != nil {
			req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		}
		if cookie != "" {
			req.AddCookie(&http.Cookie{Name: identity.CookieName, Value: cookie})
		}
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		if origin != "" {
			req.Header.Set("Origin", origin)
		}
		res, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		payload, err := io.ReadAll(io.LimitReader(res.Body, 1<<20))
		if err != nil {
			t.Fatal(err)
		}
		return res.StatusCode, res.Header, payload
	}

	// Remote endpoints never appear on public, preview, or an unconfigured site.
	for _, base := range []string{"https://a.public.remote.test", "https://a.preview.remote.test",
		"https://b.manage.remote.test"} {
		status, _, _ := do("GET", base+"/mcp", nil, "", "", "")
		if status != http.StatusNotFound {
			t.Fatalf("unowned remote endpoint %s = %d", base, status)
		}
	}

	consentValues := func(body []byte) (string, string) {
		t.Helper()
		pattern := func(name string) string {
			re := regexp.MustCompile(`name="` + name + `" value="([^"]+)"`)
			match := re.FindSubmatch(body)
			if len(match) != 2 {
				t.Fatalf("consent %s is missing: %s", name, string(body))
			}
			return string(match[1])
		}
		return pattern("transaction"), pattern("csrf")
	}
	issue := func(clientID, cookie string) string {
		t.Helper()
		verifier := strings.Repeat("a", 43)
		digest := sha256.Sum256([]byte(verifier))
		args := url.Values{
			"response_type": {"code"}, "resource": {remoteTestResource},
			"client_id": {clientID}, "redirect_uri": {remoteTestCallback},
			"code_challenge_method": {"S256"}, "code_challenge": {base64.RawURLEncoding.EncodeToString(digest[:])},
			"scope": {remoteOAuthScope}, "state": {"stable-request-state"},
		}
		// A query-string hint is not enrollment authority: even an
		// untrusted request to elevate a read-only client cannot alter
		// the displayed effective capability.
		args.Set("allow_draft_writes", "true")
		status, _, consent := do("GET", remoteTestOrigin+"/oauth/authorize?"+args.Encode(), nil, "", cookie, "")
		if status != 200 {
			t.Fatalf("consent GET = %d, %s", status, string(consent))
		}
		if !bytes.Contains(consent, []byte("does not grant publishing or Theme modification")) {
			t.Fatalf("consent failed to disclose non-publishing boundary for %s", clientID)
		}
		if clientID == "test-reader" {
			if !bytes.Contains(consent, []byte("(read-only)")) ||
				bytes.Contains(consent, []byte("can create and save private drafts")) {
				t.Fatalf("untrusted consent hint escalated the read-only client: %s", consent)
			}
		} else if !bytes.Contains(consent, []byte("can create and save private drafts")) ||
			bytes.Contains(consent, []byte("(read-only)")) {
			t.Fatalf("write-enabled client consent omitted effective operations: %s", consent)
		}
		tx, csrf := consentValues(consent)
		grantForm := url.Values{"transaction": {tx}, "csrf": {csrf}, "decision": {"allow"}}
		// A valid CSRF is not enough when Origin is untrusted.
		status, _, _ = do("POST", remoteTestOrigin+"/oauth/authorize", grantForm, "", cookie, "https://evil.remote.test")
		if status != http.StatusForbidden {
			t.Fatalf("untrusted consent Origin = %d", status)
		}
		status, headers, reply := do("POST", remoteTestOrigin+"/oauth/authorize", grantForm, "", cookie, remoteTestOrigin)
		if status != http.StatusSeeOther {
			t.Fatalf("consent POST = %d %s", status, string(reply))
		}
		location, err := url.Parse(headers.Get("Location"))
		if err != nil {
			t.Fatal(err)
		}
		if location.Query().Get("iss") != remoteTestOrigin+"/oauth" || location.Query().Get("state") != "stable-request-state" {
			t.Fatalf("invalid issuer/state redirect: %s", headers.Get("Location"))
		}
		code := location.Query().Get("code")
		if code == "" {
			t.Fatalf("missing OAuth code: %s", headers.Get("Location"))
		}
		status, _, _ = do("POST", remoteTestOrigin+"/oauth/authorize", grantForm, "", cookie, remoteTestOrigin)
		if status == http.StatusSeeOther {
			t.Fatal("one-use consent was replayed")
		}
		tokenForm := url.Values{
			"grant_type": {"authorization_code"}, "client_id": {clientID},
			"code": {code}, "redirect_uri": {remoteTestCallback},
			"code_verifier": {verifier}, "resource": {remoteTestResource},
		}
		status, _, jsonBody := do("POST", remoteTestOrigin+"/oauth/token", tokenForm, "", "", "")
		if status != 200 {
			t.Fatalf("OAuth code exchange = %d: %s", status, string(jsonBody))
		}
		var result struct {
			AccessToken  string `json:"access_token"`
			RefreshToken string `json:"refresh_token"`
		}
		if err := json.Unmarshal(jsonBody, &result); err != nil {
			t.Fatal(err)
		}
		if result.AccessToken == "" || result.RefreshToken == "" {
			t.Fatalf("missing token response fields")
		}
		return result.AccessToken
	}
	// An ungranted real product user may log in locally but cannot consent.
	noArgs := url.Values{"response_type": {"code"}, "client_id": {"test-reader"}, "redirect_uri": {remoteTestCallback},
		"resource": {remoteTestResource}, "scope": {remoteOAuthScope}, "code_challenge_method": {"S256"},
		"code_challenge": {base64.RawURLEncoding.EncodeToString(sha256.New().Sum(make([]byte, 0)))}}
	status, _, _ := do("GET", remoteTestOrigin+"/oauth/authorize?"+noArgs.Encode(), nil, "", noSession.Token, "")
	if status == 200 {
		t.Fatal("ungranted user was offered consent")
	}
	writerToken := issue("test-editor", writerSession.Token)
	readerToken := issue("test-editor", readerSession.Token)
	readonlyClientToken := issue("test-reader", writerSession.Token)
	newMCPRequest := func(token, name string, args any) *http.Request {
		t.Helper()
		params := map[string]any{"name": name, "arguments": args}
		data, err := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "method": "tools/call", "params": params})
		if err != nil {
			t.Fatal(err)
		}
		req, err := http.NewRequestWithContext(ctx, "POST", remoteTestResource, strings.NewReader(string(data)))
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Accept", "application/json, text/event-stream")
		req.Header.Set("Mcp-Protocol-Version", "2025-11-25")
		return req
	}
	mcpCall := func(token, name string, args any) (int, map[string]any) {
		t.Helper()
		res, err := client.Do(newMCPRequest(token, name, args))
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		raw, err := io.ReadAll(io.LimitReader(res.Body, 1<<20))
		if err != nil {
			t.Fatal(err)
		}
		var result map[string]any
		if err := json.Unmarshal(raw, &result); err != nil {
			t.Fatalf("invalid MCP response: %d %q (%v)", res.StatusCode, string(raw), err)
		}
		return res.StatusCode, result
	}
	requireResult := func(token, name string, args any) map[string]any {
		t.Helper()
		code, data := mcpCall(token, name, args)
		if code != 200 {
			t.Fatalf("MCP %s HTTP=%d result=%v", name, code, data)
		}
		result, _ := data["result"].(map[string]any)
		if result == nil || result["isError"] == true {
			t.Fatalf("MCP %s failed: %#v", name, data)
		}
		structured, _ := result["structuredContent"].(map[string]any)
		if structured == nil {
			t.Fatalf("MCP %s has no typed result: %#v", name, data)
		}
		return structured
	}
	requireDenied := func(token, name string, args any) {
		t.Helper()
		code, result := mcpCall(token, name, args)
		if code != 200 {
			if code == 401 || code == 403 {
				return
			}
			t.Fatalf("unexpected MCP denial %d: %#v", code, result)
		}
		data, _ := result["result"].(map[string]any)
		if data == nil || data["isError"] != true {
			t.Fatalf("unauthorized MCP %s returned success: %#v", name, result)
		}
	}

	op1 := newOperationID()
	input := map[string]any{"operation_id": op1, "kind": "page", "title": "Private first page", "body_html": "<p>Draft one</p>"}
	requireDenied(readonlyClientToken, remoteCreateTool, input)
	requireDenied(readerToken, remoteCreateTool, input)
	// A client loses the response body after the request is submitted:
	// it has the stable operation ID but no output or generated content ID.
	// Only an authorized own-operation lookup may recover the committed
	// result; do not send another write with a new operation ID.
	discarded, err := client.Do(newMCPRequest(writerToken, remoteCreateTool, input))
	if err != nil {
		t.Fatal(err)
	}
	if err := discarded.Body.Close(); err != nil {
		t.Fatal(err)
	}
	own := requireResult(writerToken, remoteOperationTool, map[string]any{"operation_id": op1})
	contentID, _ := own["resource_id"].(string)
	if !validEditorialID(contentID) || own["result_revision"] != "1" {
		t.Fatalf("lost-response reconciliation did not recover the committed result: %#v", own)
	}
	requireDenied(readerToken, remoteOperationTool, map[string]any{"operation_id": op1})
	head := requireResult(readerToken, remoteHeadTool, map[string]any{"id": contentID})
	if head["body_html"] != "<p dir=\"auto\">Draft one</p>" {
		t.Fatalf("unexpected canonical body %#v", head)
	}
	list := requireResult(writerToken, remoteListTool, map[string]any{"limit": 5})
	if items, _ := list["items"].([]any); len(items) == 0 {
		t.Fatalf("missing saved draft: %#v", list)
	}
	op2 := newOperationID()
	saved := requireResult(writerToken, remoteSaveTool, map[string]any{
		"operation_id": op2, "id": contentID, "expected_revision": "1",
		"title": "Private second revision", "body_html": "<p>Draft two</p>",
	})
	if saved["revision"] != "2" {
		t.Fatalf("CAS saved wrong revision: %#v", saved)
	}
	requireDenied(writerToken, remoteSaveTool, map[string]any{
		"operation_id": newOperationID(), "id": contentID, "expected_revision": "1",
		"title": "Stale", "body_html": "<p>stale</p>",
	})
	if receipt := requireResult(writerToken, remoteOperationTool, map[string]any{"operation_id": op2}); receipt["result_revision"] != "2" {
		t.Fatalf("missing saved operation: %#v", receipt)
	}
	// Revoking a product grant must affect the next Bridge request without
	// reconnecting or editing an external AI ACL.
	grant, err := site.Access.Set(ctx, admin, writer.ID, CapabilityContentRead, false, 1)
	if err != nil {
		t.Fatal(err)
	}
	code, _ := mcpCall(writerToken, remoteHeadTool, map[string]any{"id": contentID})
	if code != 401 && code != 403 {
		t.Fatalf("revoked site read grant: HTTP %d", code)
	}
	_, err = site.Access.Set(ctx, admin, writer.ID, CapabilityContentRead, true, grant.Revision)
	if err != nil {
		t.Fatal(err)
	}
	requireResult(writerToken, remoteHeadTool, map[string]any{"id": contentID})

	// Product session logout does not revoke independently issued OAuth grants.
	if err := site.Identity.Logout(ctx, writerSession.Token); err != nil {
		t.Fatal(err)
	}
	requireResult(writerToken, remoteHeadTool, map[string]any{"id": contentID})
	status, _, _ = do("POST", remoteTestOrigin+"/oauth/revoke",
		url.Values{"client_id": {"test-editor"}, "token": {writerToken}}, "", "", "")
	if status != 200 {
		t.Fatalf("OAuth revocation status %d", status)
	}
	code, _ = mcpCall(writerToken, remoteHeadTool, map[string]any{"id": contentID})
	if code != 401 && code != 403 {
		t.Fatalf("explicit grant revocation not enforced: %d", code)
	}
	if _, err := site.Editorial.Head(ctx, achrix.Principal(reader.ID), contentID); err != nil {
		t.Fatal(err)
	}
}

// remoteTestNonLoopbackListener exercises the official SDK's default Host
// admission on an actual routable TLS socket, not a falsified LocalAddr.
// CI must fail clearly when no such socket can be provisioned.
func remoteTestNonLoopbackListener(t *testing.T) net.Listener {
	t.Helper()
	interfaces, err := net.Interfaces()
	if err != nil {
		t.Fatal(err)
	}
	for _, iface := range interfaces {
		if iface.Flags&net.FlagUp == 0 || iface.Flags&net.FlagLoopback != 0 {
			continue
		}
		addrs, e := iface.Addrs()
		if e != nil {
			continue
		}
		for _, addr := range addrs {
			ipNet, ok := addr.(*net.IPNet)
			if !ok {
				continue
			}
			ip := ipNet.IP.To4()
			if ip == nil || ip.IsLoopback() || ip.IsUnspecified() || ip.IsMulticast() {
				continue
			}
			listener, e := net.Listen("tcp", net.JoinHostPort(ip.String(), "0"))
			if e == nil {
				return listener
			}
		}
	}
	t.Fatal("real OAuth/MCP test requires an available non-loopback IPv4 listener to validate the canonical HTTPS Host")
	return nil
}
