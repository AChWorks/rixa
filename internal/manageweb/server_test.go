// SPDX-License-Identifier: MPL-2.0
package manageweb

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"errors"
	"io"
	"math/big"
	"mime/multipart"
	"net"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/AChWorks/rixa/internal/releaseartifact"
	"github.com/AChWorks/rixa/internal/serverinstall"
)

type fakeNative struct {
	mutex     sync.Mutex
	result    serverinstall.Result
	draft     serverinstall.Draft
	updated   serverinstall.UpdateDraft
	calls     map[string]int
	token     string
	loadError error
}

func (f *fakeNative) get(action string) serverinstall.Result {
	f.mutex.Lock()
	defer f.mutex.Unlock()
	f.calls[action]++
	r := f.result
	r.Actions = append([]string{}, r.Actions...)
	return r
}
func (f *fakeNative) Status(context.Context, string) (serverinstall.Result, error) {
	return f.get("status"), nil
}
func (f *fakeNative) LoadDraft(context.Context, string) (serverinstall.Draft, error) {
	f.mutex.Lock()
	defer f.mutex.Unlock()
	if f.loadError != nil {
		return serverinstall.Draft{}, f.loadError
	}
	if f.draft.Release == "" {
		return serverinstall.Draft{}, os.ErrNotExist
	}
	return f.draft, nil
}
func (f *fakeNative) SaveDraft(_ context.Context, d serverinstall.Draft) (serverinstall.Result, error) {
	f.mutex.Lock()
	defer f.mutex.Unlock()
	f.calls["configure"]++
	f.draft = d
	f.result.State = "draft"
	f.result.Code = "draft-saved"
	f.result.Actions = []string{"configure", "review", "status"}
	f.result.ReviewToken = ""
	return f.result, nil
}
func (f *fakeNative) Review(context.Context, string) (serverinstall.Result, error) {
	f.mutex.Lock()
	defer f.mutex.Unlock()
	f.calls["review"]++
	f.result.State = "ready"
	f.result.Actions = []string{"configure", "review", "apply", "status"}
	f.result.ReviewToken = strings.Repeat("a", 64)
	return f.result, nil
}
func (f *fakeNative) Apply(_ context.Context, _ string, token string) (serverinstall.Result, error) {
	f.mutex.Lock()
	defer f.mutex.Unlock()
	f.calls["apply"]++
	f.token = token
	f.result.State = "applying"
	f.result.Actions = []string{"status"}
	f.result.ReviewToken = ""
	return f.result, nil
}
func (f *fakeNative) Resume(context.Context, string) (serverinstall.Result, error) {
	return f.get("resume"), nil
}
func (f *fakeNative) Remove(context.Context, string) (serverinstall.Result, error) {
	f.mutex.Lock()
	defer f.mutex.Unlock()
	f.calls["remove"]++
	f.result.State = "removed"
	f.result.Actions = []string{"status"}
	return f.result, nil
}
func (f *fakeNative) UpdateStatus(ctx context.Context, id string) (serverinstall.Result, error) {
	return f.Status(ctx, id)
}
func (f *fakeNative) LoadUpdateDraft(context.Context, string) (serverinstall.UpdateDraft, error) {
	f.mutex.Lock()
	defer f.mutex.Unlock()
	if f.loadError != nil {
		return serverinstall.UpdateDraft{}, f.loadError
	}
	if f.updated.Release == "" {
		return serverinstall.UpdateDraft{}, os.ErrNotExist
	}
	return f.updated, nil
}
func (f *fakeNative) SaveUpdateDraft(_ context.Context, d serverinstall.UpdateDraft) (serverinstall.Result, error) {
	f.mutex.Lock()
	defer f.mutex.Unlock()
	f.calls["configure"]++
	f.updated = d
	f.result.State = "draft"
	f.result.Actions = []string{"configure", "review", "status"}
	return f.result, nil
}
func (f *fakeNative) ReviewUpdate(ctx context.Context, id string) (serverinstall.Result, error) {
	return f.Review(ctx, id)
}
func (f *fakeNative) ApplyUpdate(ctx context.Context, id, token string) (serverinstall.Result, error) {
	return f.Apply(ctx, id, token)
}
func (f *fakeNative) ResumeUpdate(ctx context.Context, id string) (serverinstall.Result, error) {
	return f.Resume(ctx, id)
}
func (f *fakeNative) RemoveUpdatedInstallation(ctx context.Context, id string) (serverinstall.Result, error) {
	return f.Remove(ctx, id)
}

type harness struct {
	console     *Console
	server      *httptest.Server
	client      *http.Client
	backend     *fakeNative
	permit      string
	csrf        string
	verifyCalls int
	fetchCalls  int
}

func certFiles(t *testing.T, dir string) (string, string, tls.Certificate) {
	t.Helper()
	key, e := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if e != nil {
		t.Fatal(e)
	}
	template := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "private fixture"}, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour), IPAddresses: []net.IP{net.ParseIP("127.0.0.1")}, KeyUsage: x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}, BasicConstraintsValid: true, IsCA: true}
	der, e := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if e != nil {
		t.Fatal(e)
	}
	private, e := x509.MarshalPKCS8PrivateKey(key)
	if e != nil {
		t.Fatal(e)
	}
	certificate, keyPath := filepath.Join(dir, "tls.pem"), filepath.Join(dir, "tls.key")
	if os.WriteFile(certificate, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0600) != nil || os.WriteFile(keyPath, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: private}), 0600) != nil {
		t.Fatal("certificate fixture")
	}
	pair, e := tls.LoadX509KeyPair(certificate, keyPath)
	if e != nil {
		t.Fatal(e)
	}
	return certificate, keyPath, pair
}
func fixture(t *testing.T, modify func(*Options, *dependencies, *fakeNative)) *harness {
	t.Helper()
	root := t.TempDir()
	if e := os.Chmod(root, 0700); e != nil {
		t.Fatal(e)
	}
	certificate, key, pair := certFiles(t, root)
	stage := filepath.Join(root, "staging")
	if e := os.Mkdir(stage, 0700); e != nil {
		t.Fatal(e)
	}
	server := httptest.NewUnstartedServer(nil)
	native := &fakeNative{result: serverinstall.Result{OperationID: "fixture-install", State: "not-installed", Code: "not-installed", Actions: []string{"configure", "status"}}, calls: map[string]int{}}
	h := &harness{server: server, backend: native}
	options := Options{Listen: server.Listener.Addr().String(), Certificate: certificate, Key: key, RuntimeCertificate: certificate, RuntimeKey: key, PermitFile: filepath.Join(root, "permit"), StorageRoot: stage, Lifetime: 10 * time.Minute, ManagerVersion: "fixture", Scope: Scope{Kind: "install", Operation: "fixture-install", Actions: []string{"configure", "review", "apply", "resume", "status", "remove"}}}
	deps := dependencies{owner: os.Geteuid(), now: time.Now, verify: func(_ context.Context, in releaseartifact.Request) (releaseartifact.Result, error) {
		h.verifyCalls++
		if !strings.HasPrefix(in.ArchivePath, stage+string(os.PathSeparator)) {
			t.Error("verification left private staging")
		}
		return releaseartifact.Result{Status: "verified", SHA256: strings.Repeat("b", 64)}, nil
	}, fetch: func(context.Context, releaseartifact.FetchRequest) (releaseartifact.FetchResult, error) {
		h.fetchCalls++
		return releaseartifact.FetchResult{}, releaseartifact.ErrNoRelease
	}}
	if modify != nil {
		modify(&options, &deps, native)
	}
	c, e := newConsole(options, native, deps)
	if e != nil {
		_ = server.Listener.Close()
		for _, p := range []string{root, stage, certificate, key} {
			info, _ := os.Lstat(p)
			actual, _ := filepath.EvalSymlinks(p)
			t.Logf("fixture path=%s canonical=%s mode=%v owner=%d", p, actual, info.Mode(), os.Geteuid())
		}
		t.Fatal(e)
	}
	h.console = c
	b, e := os.ReadFile(options.PermitFile)
	if e != nil {
		t.Fatal(e)
	}
	h.permit = strings.TrimSpace(string(b))
	server.Config.Handler = c.Handler()
	server.TLS = &tls.Config{Certificates: []tls.Certificate{pair}}
	server.StartTLS()
	h.client = server.Client()
	jar, _ := cookiejar.New(nil)
	h.client.Jar = jar
	h.client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	t.Cleanup(func() {
		server.Close()
		if e := c.Close(); e != nil {
			t.Error(e)
		}
	})
	return h
}
func (h *harness) call(t *testing.T, method, path, body string, csrf bool) (int, []byte, *http.Response) {
	t.Helper()
	req, e := http.NewRequest(method, h.console.origin+path, strings.NewReader(body))
	if e != nil {
		t.Fatal(e)
	}
	req.Header.Set("Origin", h.console.origin)
	req.Header.Set("X-Identity-Request", "1")
	if method == "POST" {
		req.Header.Set("Content-Type", "application/json")
	}
	if csrf {
		req.Header.Set("X-CSRF-Token", h.csrf)
	}
	response, e := h.client.Do(req)
	if e != nil {
		t.Fatal(e)
	}
	defer response.Body.Close()
	data, e := io.ReadAll(response.Body)
	if e != nil {
		t.Fatal(e)
	}
	return response.StatusCode, data, response
}
func (h *harness) login(t *testing.T) {
	t.Helper()
	body, _ := json.Marshal(map[string]string{"permit": h.permit})
	code, _, response := h.call(t, "POST", "/console-auth/login", string(body), false)
	if code != 200 {
		t.Fatalf("permit login status %d", code)
	}
	cookies := response.Cookies()
	if len(cookies) != 1 || !cookies[0].Secure || !cookies[0].HttpOnly || cookies[0].SameSite != http.SameSiteStrictMode || cookies[0].Path != "/" || cookies[0].Domain != "" {
		t.Fatal("permit cookie boundary")
	}
	code, b, _ := h.call(t, "GET", "/console-auth/csrf", "", false)
	if code != 200 {
		t.Fatal("csrf status")
	}
	var result map[string]string
	if json.Unmarshal(b, &result) != nil || len(result["csrf"]) != 43 {
		t.Fatal("csrf value")
	}
	h.csrf = result["csrf"]
}
func TestConsolePermitAndIngressBoundaries(t *testing.T) {
	h := fixture(t, nil)
	code, _, _ := h.call(t, "GET", "/admin/operation/", "", false)
	if code != 303 {
		t.Fatalf("anonymous operation status %d", code)
	}
	if h.backend.calls["status"] != 0 {
		t.Fatal("anonymous request reached native facade")
	}
	bad, _ := json.Marshal(map[string]string{"permit": strings.Repeat("x", 43)})
	req, _ := http.NewRequest("POST", h.console.origin+"/console-auth/login", bytes.NewReader(bad))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Identity-Request", "1")
	req.Header.Set("Origin", "https://foreign.invalid")
	r, e := h.client.Do(req)
	if e != nil {
		t.Fatal(e)
	}
	_ = r.Body.Close()
	if r.StatusCode != 401 {
		t.Fatal("foreign Origin admitted")
	}
	if h.console.attempts != 0 {
		t.Fatal("foreign origin consumed permit admission")
	}
	h.login(t)
	for _, test := range []struct {
		name   string
		mutate func(*http.Request)
		want   int
	}{
		{"host", func(r *http.Request) { r.Host = "foreign.invalid" }, 400},
		{"forwarded", func(r *http.Request) { r.Header.Set("Forwarded", "host=foreign.invalid") }, 400},
		{"duplicate-origin", func(r *http.Request) { r.Header.Add("Origin", h.console.origin) }, 401},
		{"csrf", func(r *http.Request) { r.Header.Del("X-CSRF-Token") }, 401},
		{"query", func(r *http.Request) { r.URL.RawQuery = "operation=another" }, 400},
	} {
		t.Run(test.name, func(t *testing.T) {
			req, _ := http.NewRequest("POST", h.console.origin+"/admin/operation/review", strings.NewReader("{}"))
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("Origin", h.console.origin)
			req.Header.Set("X-Identity-Request", "1")
			req.Header.Set("X-CSRF-Token", h.csrf)
			test.mutate(req)
			r, e := h.client.Do(req)
			if e != nil {
				t.Fatal(e)
			}
			_ = r.Body.Close()
			if r.StatusCode != test.want {
				t.Fatalf("status %d want %d", r.StatusCode, test.want)
			}
		})
	}
	if h.backend.calls["review"] != 0 {
		t.Fatal("invalid request reached effect facade")
	}
}
func TestConsoleSingleUseExpiryAndProductCookies(t *testing.T) {
	h := fixture(t, nil)
	req, _ := http.NewRequest("GET", h.console.origin+"/admin/operation/status", nil)
	req.AddCookie(&http.Cookie{Name: "__Host-AChrix-Session", Value: "unrelated-product-session"})
	r, e := h.client.Do(req)
	if e != nil {
		t.Fatal(e)
	}
	_ = r.Body.Close()
	if r.StatusCode != 303 {
		t.Fatal("product session granted infrastructure access")
	}
	h.login(t)
	body, _ := json.Marshal(map[string]string{"permit": h.permit})
	code, _, _ := h.call(t, "POST", "/console-auth/login", string(body), false)
	if code != 401 {
		t.Fatal("consumed permit reused")
	}
	code, _, _ = h.call(t, "POST", "/console-auth/logout", "{}", true)
	if code != 204 {
		t.Fatal("logout rejected")
	}
	code, _, _ = h.call(t, "GET", "/console-auth/csrf", "", false)
	if code != 401 {
		t.Fatal("revoked console session retained")
	}
	h2 := fixture(t, nil)
	h2.login(t)
	h2.console.mutex.Lock()
	h2.console.expires = time.Now().Add(-time.Second)
	h2.console.mutex.Unlock()
	code, _, _ = h2.call(t, "GET", "/console-auth/csrf", "", false)
	if code != 401 {
		t.Fatal("expired session retained")
	}
}
func TestConsoleStrictJSONAndScopedNativeActions(t *testing.T) {
	h := fixture(t, nil)
	h.login(t)
	for _, body := range []string{`{"control_origin":"x","operation_id":"foreign"}`, `{"control_origin":"a","control_origin":"b"}`, `{} {}`, strings.Repeat("x", 33<<10)} {
		code, _, _ := h.call(t, "POST", "/admin/operation/configure", body, true)
		if code != 400 {
			t.Fatalf("invalid shape status %d", code)
		}
	}
	if h.backend.calls["configure"] != 0 {
		t.Fatal("invalid JSON changed native draft")
	}
	h.console.mutex.Lock()
	h.console.artifact = artifact{"v1.2.3", strings.Repeat("a", 40), "/fixed/archive", "/fixed/bundle", strings.Repeat("b", 64)}
	h.console.mutex.Unlock()
	input := configureInput{ControlOrigin: "https://control.example:9443", SiteOrigin: "https://site.example:9443", Listen: "0.0.0.0:9443", ControlDSN: "CONTROL-SECRET", SiteDSN: "SITE-SECRET", ControlLogin: "control", SiteLogin: "site", ControlPassword: "CONTROL-PASSWORD-فارسی", SitePassword: "SITE-PASSWORD", Language: "en"}
	b, _ := json.Marshal(input)
	code, _, _ := h.call(t, "POST", "/admin/operation/configure", string(b), true)
	if code != 200 {
		t.Fatal("configure")
	}
	code, page, _ := h.call(t, "GET", "/admin/operation/", "", false)
	if code != 200 {
		t.Fatalf("page %d %s", code, page)
	}
	for _, secret := range []string{input.ControlDSN, input.SiteDSN, input.ControlPassword, input.SitePassword, h.permit} {
		if bytes.Contains(page, []byte(secret)) {
			t.Fatal("private input leaked in HTML")
		}
	}
	if h.backend.draft.OperationID != "fixture-install" || h.backend.draft.TLSKeyPath != h.console.options.RuntimeKey || h.backend.draft.ArchivePath != "/fixed/archive" {
		t.Fatal("native target bindings changed")
	}
	input.ControlDSN = ""
	input.SiteDSN = ""
	input.ControlPassword = ""
	input.SitePassword = ""
	input.ControlOrigin = "https://corrected.example:9443"
	b, _ = json.Marshal(input)
	code, _, _ = h.call(t, "POST", "/admin/operation/configure", string(b), true)
	if code != 200 || h.backend.draft.ControlDSN != "CONTROL-SECRET" || h.backend.draft.ControlPassword != "CONTROL-PASSWORD-فارسی" {
		t.Fatal("saved secret not retained")
	}
	code, data, _ := h.call(t, "POST", "/admin/operation/review", "{}", true)
	if code != 200 {
		t.Fatal("review")
	}
	var reviewed response
	if json.Unmarshal(data, &reviewed) != nil || len(reviewed.ReviewToken) != 64 {
		t.Fatal("exact review token absent")
	}
	code, _, _ = h.call(t, "POST", "/admin/operation/apply", `{"review_token":"wrong"}`, true)
	if code != 400 || h.backend.calls["apply"] != 0 {
		t.Fatal("wrong confirmation forwarded")
	}
	payload, _ := json.Marshal(map[string]string{"review_token": reviewed.ReviewToken})
	code, data, _ = h.call(t, "POST", "/admin/operation/apply", string(payload), true)
	if code != 200 || h.backend.token != reviewed.ReviewToken || h.backend.calls["apply"] != 1 {
		t.Fatal("exact token not forwarded")
	}
	code, _, _ = h.call(t, "POST", "/admin/operation/resume", "{}", true)
	if code != 409 || h.backend.calls["resume"] != 0 {
		t.Fatal("invented resume bypassed backend actions")
	}
}
func TestConsoleRejectsImplicitChildOperationTakeover(t *testing.T) {
	h := fixture(t, func(_ *Options, _ *dependencies, f *fakeNative) {
		f.result = serverinstall.Result{OperationKind: "update", InstallationOperation: "fixture-install", OperationID: "different-update", State: "reconciliation-required", Code: "inspect", Actions: []string{"resume", "remove", "status"}}
	})
	h.login(t)
	code, body, _ := h.call(t, "GET", "/admin/operation/status", "", false)
	if code != 200 {
		t.Fatal(code)
	}
	var r response
	if json.Unmarshal(body, &r) != nil || len(r.Actions) != 1 || r.Actions[0] != "status" {
		t.Fatal("parent permit silently acquired child actions")
	}
	code, _, _ = h.call(t, "POST", "/admin/operation/resume", "{}", true)
	if code != 409 || h.backend.calls["resume"] != 0 {
		t.Fatal("parent resumed different update")
	}
}
func TestConsoleExplicitRemovalAndCompletionLockout(t *testing.T) {
	h := fixture(t, func(o *Options, _ *dependencies, f *fakeNative) {
		o.Scope.Actions = []string{"status", "remove"}
		f.result.State = "installed"
		f.result.Actions = []string{"status", "remove"}
	})
	h.login(t)
	code, _, _ := h.call(t, "POST", "/admin/operation/remove", `{"confirmation":"WRONG"}`, true)
	if code != 400 || h.backend.calls["remove"] != 0 {
		t.Fatal("removal confirmation bypassed")
	}
	code, b, _ := h.call(t, "POST", "/admin/operation/remove", `{"confirmation":"REMOVE"}`, true)
	if code != 200 {
		t.Fatal(code)
	}
	var result response
	_ = json.Unmarshal(b, &result)
	if !result.AccessClosed || h.backend.calls["remove"] != 1 {
		t.Fatal("completion access not closed")
	}
	code, _, _ = h.call(t, "GET", "/console-auth/csrf", "", false)
	if code != 401 {
		t.Fatal("completion session remained live")
	}
}
func TestConsoleUploadAuthenticationBeforeNativeAndNoBrowserPaths(t *testing.T) {
	h := fixture(t, nil)
	h.login(t)
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	_ = writer.WriteField("release", "v1.2.3")
	_ = writer.WriteField("source_commit", strings.Repeat("a", 40))
	for _, name := range []string{"archive", "bundle"} {
		part, e := writer.CreateFormFile(name, "../../untrusted-name")
		if e != nil {
			t.Fatal(e)
		}
		_, _ = part.Write([]byte(name + "-bytes"))
	}
	_ = writer.Close()
	req, _ := http.NewRequest("POST", h.console.origin+"/artifact/upload", bytes.NewReader(body.Bytes()))
	req.Header.Set("Content-Type", writer.FormDataContentType())
	req.Header.Set("Origin", h.console.origin)
	req.Header.Set("X-Identity-Request", "1")
	req.Header.Set("X-CSRF-Token", h.csrf)
	r, e := h.client.Do(req)
	if e != nil {
		t.Fatal(e)
	}
	data, _ := io.ReadAll(r.Body)
	_ = r.Body.Close()
	if r.StatusCode != 200 || h.verifyCalls != 1 {
		t.Fatalf("upload status %d", r.StatusCode)
	}
	if bytes.Contains(data, []byte(h.console.stage)) || h.backend.calls["configure"] != 0 || h.backend.calls["apply"] != 0 {
		t.Fatal("upload leaked path or executed native operation")
	}
	code, _, _ := h.call(t, "POST", "/artifact/fetch", `{"url":"https://untrusted.invalid"}`, true)
	if code != 400 || h.fetchCalls != 0 {
		t.Fatal("arbitrary fetch URL accepted")
	}
}
func TestConsoleCatalogMetadataAndSourceShape(t *testing.T) {
	h := fixture(t, func(o *Options, _ *dependencies, _ *fakeNative) {
		root := filepath.Join(filepath.Dir(o.PermitFile), "catalogs")
		if os.Mkdir(root, 0700) != nil {
			t.Fatal("catalog root")
		}
		body := `{"schema":1,"locale":"ar","name":"Installed fixture","direction":"rtl","domain":"rixa-manager","messages":{"access_title":{"other":"Installed <text>"}}}`
		if os.WriteFile(filepath.Join(root, "fixture.json"), []byte(body), 0600) != nil {
			t.Fatal("catalog file")
		}
		o.CatalogRoot = root
	})
	code, b, _ := h.call(t, "GET", "/access", "", false)
	if code != 200 || !bytes.Contains(b, []byte(`lang="en" dir="ltr"`)) {
		t.Fatal("English default absent")
	}
	code, _, _ = h.call(t, "GET", "/language/fa", "", false)
	if code != 303 {
		t.Fatal("Persian selection")
	}
	code, b, _ = h.call(t, "GET", "/access", "", false)
	if code != 200 || !bytes.Contains(b, []byte(`lang="fa" dir="rtl"`)) || !bytes.Contains(b, []byte("مجوز")) {
		t.Fatal("Persian catalog absent")
	}
	code, _, _ = h.call(t, "GET", "/language/ar", "", false)
	if code != 303 {
		t.Fatal("installed selection")
	}
	code, b, _ = h.call(t, "GET", "/access", "", false)
	if code != 200 || !bytes.Contains(b, []byte(`lang="ar" dir="rtl"`)) || !bytes.Contains(b, []byte("Installed &lt;text&gt;")) || bytes.Contains(b, []byte("Installed <text>")) {
		t.Fatal("installed catalog/direction/escaping")
	}
}

func TestConsoleReviewSummaryAndLoadFailure(t *testing.T) {
	h := fixture(t, func(_ *Options, _ *dependencies, f *fakeNative) {
		f.draft = serverinstall.Draft{OperationID: "fixture-install", Release: "v1.2.3", SourceCommit: strings.Repeat("a", 40), ControlOrigin: "https://saved-control.example:9443", ControlDSN: "postgres://control:CANARY-password@127.0.0.1:5432/control?sslmode=disable", SiteDSN: "PRIVATE-DSN-CANARY", ControlPassword: "CANARY-admin-password"}
		f.result.State = "draft"
		f.result.Actions = []string{"configure", "review", "status"}
	})
	h.login(t)
	code, b, _ := h.call(t, "POST", "/admin/operation/review", "{}", true)
	var r response
	if code != 200 || json.Unmarshal(b, &r) != nil || r.Review["control_origin"] != "https://saved-control.example:9443" || r.Review["control_dsn"] != "postgres://control@127.0.0.1:5432/control?sslmode=disable" || r.Review["site_dsn"] != "private" {
		t.Fatalf("saved native review summary %d %s", code, b)
	}
	if bytes.Contains(b, []byte("CANARY")) {
		t.Fatal("private draft returned by review")
	}
	h.backend.mutex.Lock()
	h.backend.loadError = errors.New("protected journal unavailable")
	h.backend.mutex.Unlock()
	code, _, _ = h.call(t, "POST", "/admin/operation/configure", `{"control_origin":"https://replacement.example"}`, true)
	if code != 503 || h.backend.calls["configure"] != 0 || h.backend.draft.ControlOrigin != "https://saved-control.example:9443" {
		t.Fatal("load failure overwritten as new draft")
	}
	code, b, _ = h.call(t, "POST", "/admin/operation/review", "{}", true)
	if code != 503 || bytes.Contains(b, []byte("review_token")) {
		t.Fatal("review token returned without readable saved summary")
	}
}
func TestConsoleUpdateScopeAndSecretReplacementDenied(t *testing.T) {
	h := fixture(t, func(o *Options, _ *dependencies, f *fakeNative) {
		o.Scope.Kind = "update"
		o.Scope.Operation = "fixture-update"
		o.Scope.Installation = "fixture-install"
		f.result = serverinstall.Result{OperationID: "fixture-update", OperationKind: "update", State: "not-installed", Actions: []string{"configure", "status"}}
	})
	h.login(t)
	code, _, _ := h.call(t, "POST", "/admin/operation/configure", `{"control_dsn":"replacement"}`, true)
	if code != 400 || h.backend.calls["configure"] != 0 {
		t.Fatal("update accepted replacement runtime secrets")
	}
	h.console.mutex.Lock()
	h.console.artifact = artifact{"v1.2.4", strings.Repeat("c", 40), "/fixed/archive", "/fixed/bundle", strings.Repeat("b", 64)}
	h.console.mutex.Unlock()
	code, _, _ = h.call(t, "POST", "/admin/operation/configure", "{}", true)
	if code != 200 || h.backend.updated.OperationID != "fixture-update" || h.backend.updated.InstallationOperation != "fixture-install" || h.backend.updated.ArchivePath != "/fixed/archive" {
		t.Fatal("update target binding")
	}
	h.backend.mutex.Lock()
	h.backend.result.InstallationOperation = "fixture-install"
	h.backend.mutex.Unlock()
	code, b, _ := h.call(t, "POST", "/admin/operation/review", "{}", true)
	var r response
	if code != 200 || json.Unmarshal(b, &r) != nil || r.Review["release"] != "v1.2.4" || len(r.ReviewToken) != 64 {
		t.Fatalf("update review %d %s", code, b)
	}
	body, _ := json.Marshal(map[string]string{"review_token": r.ReviewToken})
	code, _, _ = h.call(t, "POST", "/admin/operation/apply", string(body), true)
	if code != 200 || h.backend.calls["apply"] != 1 || h.backend.token != r.ReviewToken {
		t.Fatal("update exact review not forwarded")
	}
}
func TestConsoleBrowserPreview(t *testing.T) {
	marker := os.Getenv("RIXA_MANAGEWEB_PREVIEW")
	if marker == "" {
		t.Skip("explicit isolated browser fixture only")
	}
	if !filepath.IsAbs(marker) {
		t.Fatal("absolute preview marker required")
	}
	h := fixture(t, nil)
	b, e := json.Marshal(map[string]string{"origin": h.console.origin, "permit": h.permit})
	if e != nil || os.WriteFile(marker, b, 0600) != nil {
		t.Fatal("private fake preview marker")
	}
	defer os.Remove(marker)
	stop := marker + ".stop"
	defer os.Remove(stop)
	deadline := time.NewTimer(3 * time.Minute)
	defer deadline.Stop()
	tick := time.NewTicker(100 * time.Millisecond)
	defer tick.Stop()
	for {
		select {
		case <-deadline.C:
			return
		case <-tick.C:
			if _, e := os.Stat(stop); e == nil {
				return
			}
		}
	}
}
