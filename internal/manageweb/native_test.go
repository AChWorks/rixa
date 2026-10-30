// SPDX-License-Identifier: MPL-2.0
package manageweb

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/AChWorks/rixa/internal/serverinstall"
)

type waitingNative struct {
	*fakeNative
	delay              time.Duration
	entered            chan time.Duration
	finished           chan error
	once               sync.Once
	ignoreCancellation bool
}

func (f *waitingNative) wait(ctx context.Context) error {
	deadline, _ := ctx.Deadline()
	f.once.Do(func() { f.entered <- time.Until(deadline) })
	timer := time.NewTimer(f.delay)
	defer timer.Stop()
	var err error
	select {
	case <-timer.C:
	case <-ctx.Done():
		err = ctx.Err()
	}
	f.finished <- err
	if f.ignoreCancellation {
		return nil
	}
	return err
}
func (f *waitingNative) Review(ctx context.Context, id string) (serverinstall.Result, error) {
	if err := f.wait(ctx); err != nil {
		return f.get("interrupted"), err
	}
	return f.fakeNative.Review(ctx, id)
}
func (f *waitingNative) Apply(ctx context.Context, id, token string) (serverinstall.Result, error) {
	if err := f.wait(ctx); err != nil {
		return f.get("interrupted"), err
	}
	return f.fakeNative.Apply(ctx, id, token)
}
func (f *waitingNative) ReviewUpdate(ctx context.Context, id string) (serverinstall.Result, error) {
	return f.Review(ctx, id)
}
func (f *waitingNative) ApplyUpdate(ctx context.Context, id, token string) (serverinstall.Result, error) {
	return f.Apply(ctx, id, token)
}
func budgetFixture(t *testing.T, kind string, delay time.Duration, http2 ...bool) (*harness, *waitingNative) {
	t.Helper()
	h := fixture(t, func(o *Options, _ *dependencies, f *fakeNative) {
		f.draft = serverinstall.Draft{OperationID: o.Scope.Operation, Release: "v1.2.3"}
		f.result.State = "ready"
		f.result.Actions = []string{"configure", "review", "apply", "status"}
		if kind == "update" {
			o.Scope = Scope{Kind: "update", Operation: "fixture-update", Installation: "fixture-install", Actions: o.Scope.Actions}
			f.updated = serverinstall.UpdateDraft{OperationID: o.Scope.Operation, InstallationOperation: o.Scope.Installation, Release: "v1.2.3"}
			f.result.OperationID = o.Scope.Operation
			f.result.OperationKind = "update"
			f.result.InstallationOperation = o.Scope.Installation
		}
	}, http2...)
	h.login(t)
	f := &waitingNative{fakeNative: h.backend, delay: delay, entered: make(chan time.Duration, 1), finished: make(chan error, 1)}
	h.console.backend = f
	h.console.updater = f
	return h, f
}

// These real TLS requests outlive Shell v0.2.0's ten-second context/write
// deadline. Both native modes and both verification-bearing calls must work,
// retaining exact confirmation and the native facade as the only effect owner.
func TestNativeRequestsOutlivePresentationBudget(t *testing.T) {
	for _, kind := range []string{"install", "update"} {
		for _, action := range []string{"review", "apply"} {
			t.Run(kind+"/"+action, func(t *testing.T) {
				t.Parallel()
				h, f := budgetFixture(t, kind, 10200*time.Millisecond)
				h.client.Timeout = 20 * time.Second
				body := "{}"
				if action == "apply" {
					body = `{"review_token":"` + strings.Repeat("a", 64) + `"}`
				}
				started := time.Now()
				code, data, _ := h.call(t, "POST", "/admin/operation/"+action, body, true)
				elapsed := time.Since(started)
				var result response
				if code != 200 || json.Unmarshal(data, &result) != nil || elapsed < 10*time.Second {
					t.Fatalf("long native response status=%d duration=%s", code, elapsed)
				}
				remaining := <-f.entered
				if remaining < 110*time.Second || remaining > nativeRequestTimeout {
					t.Fatalf("native deadline not finite and independent: %s", remaining)
				}
				if err := <-f.finished; err != nil {
					t.Fatal("native request cancelled by presentation budget")
				}
				h.backend.mutex.Lock()
				defer h.backend.mutex.Unlock()
				if h.backend.calls[action] != 1 {
					t.Fatal("native action replayed or omitted")
				}
				if action == "review" && (len(result.ReviewToken) != 64 || result.Review["release"] != "v1.2.3") {
					t.Fatal("review lost its fresh native confirmation or summary")
				}
				if action == "apply" && (result.State != "applying" || h.backend.token != strings.Repeat("a", 64)) {
					t.Fatal("Apply lost exact native token or queued state")
				}
			})
		}
	}
}

func TestNativeRequestKeepsPermitExpiry(t *testing.T) {
	h, f := budgetFixture(t, "install", time.Minute)
	h.console.mutex.Lock()
	h.console.expires = time.Now().Add(200 * time.Millisecond)
	h.console.mutex.Unlock()
	code, data, _ := h.call(t, "POST", "/admin/operation/apply", `{"review_token":"`+strings.Repeat("a", 64)+`"}`, true)
	var result response
	if code != 503 || json.Unmarshal(data, &result) != nil || len(result.Problems) != 1 || result.Problems[0].Code != "request_timeout" || result.Problems[0].Message == "request_timeout" {
		t.Fatal("expiry did not retain safe outcome/timeout guidance")
	}
	if err := <-f.finished; !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal("permit deadline did not cancel facade")
	}
	if h.backend.calls["apply"] != 0 {
		t.Fatal("expired native request applied")
	}
	if code, _, _ := h.call(t, "GET", "/admin/operation/status", "", false); code != 303 {
		t.Fatal("expired native session retained")
	}
}

func TestNativeRequestKeepsClientCancellation(t *testing.T) {
	h, f := budgetFixture(t, "update", time.Minute)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	request, _ := http.NewRequestWithContext(ctx, "POST", h.console.origin+"/admin/operation/apply", strings.NewReader(`{"review_token":"`+strings.Repeat("a", 64)+`"}`))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Origin", h.console.origin)
	request.Header.Set("X-Identity-Request", "1")
	request.Header.Set("X-CSRF-Token", h.csrf)
	done := make(chan error, 1)
	go func() {
		response, err := h.client.Do(request)
		if response != nil {
			_ = response.Body.Close()
		}
		done <- err
	}()
	select {
	case <-f.entered:
	case <-time.After(3 * time.Second):
		t.Fatal("native call not reached")
	}
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatal("client cancellation not observed")
	}
	select {
	case err := <-f.finished:
		if !errors.Is(err, context.Canceled) {
			t.Fatal("facade context detached from client")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("native facade did not observe cancellation")
	}
	h.backend.mutex.Lock()
	defer h.backend.mutex.Unlock()
	if h.backend.calls["apply"] != 0 {
		t.Fatal("cancelled native request applied")
	}
}

func TestNativeTransportMethodAndRouteBoundary(t *testing.T) {
	h := fixture(t, nil)
	h.login(t)
	for _, test := range []struct {
		method, path, body string
		want               int
	}{
		{"GET", "/admin/operation/apply", "", 404},
		{"POST", "/admin/operation/status", "{}", 404},
		{"POST", "/admin/operation/apply/", "{}", 404},
		{"POST", "/admin/operation/arbitrary", "{}", 404},
		{"POST", "/admin/operation/configure", strings.Repeat("x", 33<<10), 400},
	} {
		code, _, _ := h.call(t, test.method, test.path, test.body, true)
		if code != test.want {
			t.Fatalf("%s %s status=%d", test.method, test.path, code)
		}
	}
	if h.backend.calls["configure"] != 0 || h.backend.calls["apply"] != 0 {
		t.Fatal("unsupported transport request reached native mutation")
	}
}

func TestNativeRechecksAuthorityAfterBody(t *testing.T) {
	h := fixture(t, nil)
	h.login(t)
	f := &delayedStatusNative{fakeNative: h.backend, reached: make(chan struct{})}
	h.console.backend = f
	reader, writer := io.Pipe()
	defer reader.Close()
	defer writer.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	request, _ := http.NewRequestWithContext(ctx, "POST", h.console.origin+"/admin/operation/configure", reader)
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Origin", h.console.origin)
	request.Header.Set("X-Identity-Request", "1")
	request.Header.Set("X-CSRF-Token", h.csrf)
	done := make(chan int, 1)
	go func() {
		response, err := h.client.Do(request)
		if err != nil {
			done <- 0
			return
		}
		_, _ = io.Copy(io.Discard, response.Body)
		_ = response.Body.Close()
		done <- response.StatusCode
	}()
	select {
	case <-f.reached:
	case <-ctx.Done():
		t.Fatal("gate did not inspect operation")
	}
	if err := h.console.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := io.WriteString(writer, "{}"); err != nil {
		t.Fatal(err)
	}
	_ = writer.Close()
	if code := <-done; code != 403 || h.backend.calls["configure"] != 0 {
		t.Fatal("revocation during body decoding reached facade")
	}
}

func TestNativePostCommitRevocationIsUnknownAcknowledgement(t *testing.T) {
	h, f := budgetFixture(t, "install", 100*time.Millisecond)
	done := make(chan struct{})
	var code int
	var data []byte
	go func() {
		code, data, _ = h.call(t, "POST", "/admin/operation/apply", `{"review_token":"`+strings.Repeat("a", 64)+`"}`, true)
		close(done)
	}()
	<-f.entered
	h.console.mutex.Lock()
	h.console.closed = true
	h.console.mutex.Unlock()
	<-done
	var result response
	if code != 503 || json.Unmarshal(data, &result) != nil || result.State != "applying" || len(result.Problems) != 1 || result.Problems[0].Code != "request_access_closed" || h.backend.calls["apply"] != 1 {
		t.Fatal("post-facade revocation lost committed result or claimed pre-effect denial")
	}
}

func TestNativeExpiredReviewNeverIssuesConfirmation(t *testing.T) {
	h, f := budgetFixture(t, "install", time.Minute)
	f.ignoreCancellation = true // simulate a facade result arriving just after its deadline
	h.console.mutex.Lock()
	h.console.expires = time.Now().Add(150 * time.Millisecond)
	h.console.mutex.Unlock()
	code, data, _ := h.call(t, "POST", "/admin/operation/review", "{}", true)
	var result response
	if code != 503 || json.Unmarshal(data, &result) != nil || result.State != "ready" || result.ReviewToken != "" || result.Review != nil || h.backend.calls["review"] != 1 {
		t.Fatal("late review issued a usable confirmation after expiry")
	}
}

func TestNativeServeCancellationReachesFacade(t *testing.T) {
	h, f := budgetFixture(t, "install", time.Minute)
	// Reuse the fake-only fixture's private TLS and exact free loopback port to
	// exercise the actual Serve ancestry; no real native engine is constructed.
	h.server.Close()
	owner, cancelOwner := context.WithCancel(context.Background())
	defer cancelOwner()
	ready := make(chan struct{})
	stopped := make(chan error, 1)
	go func() { stopped <- h.console.Serve(owner, func(string) { close(ready) }) }()
	select {
	case <-ready:
	case err := <-stopped:
		t.Fatalf("fake-only console failed to start: %v", err)
	case <-time.After(3 * time.Second):
		t.Fatal("fake-only console readiness timeout")
	}
	requestDone := make(chan struct{})
	go func() {
		h.call(t, "POST", "/admin/operation/apply", `{"review_token":"`+strings.Repeat("a", 64)+`"}`, true)
		close(requestDone)
	}()
	select {
	case <-f.entered:
	case <-time.After(3 * time.Second):
		t.Fatal("native call not reached")
	}
	cancelOwner()
	select {
	case err := <-f.finished:
		if !errors.Is(err, context.Canceled) {
			t.Fatal("owning console cancellation detached")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("owning console did not cancel verifier work")
	}
	select {
	case <-requestDone:
	case <-time.After(3 * time.Second):
		t.Fatal("cancelled acknowledgement did not finish")
	}
	select {
	case err := <-stopped:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("fake-only console did not stop")
	}
	if h.backend.calls["apply"] != 0 {
		t.Fatal("owner-cancelled native request applied")
	}
}

func TestNativeSingleHeaderAndAdmissionBoundaries(t *testing.T) {
	h := fixture(t, nil)
	h.login(t)
	for _, test := range []struct {
		name   string
		mutate func(*http.Request)
		want   int
	}{
		{"duplicate-csrf", func(r *http.Request) { r.Header.Add("X-CSRF-Token", h.csrf) }, 401},
		{"duplicate-identity", func(r *http.Request) { r.Header.Add("X-Identity-Request", "1") }, 401},
		{"empty-origin-with-referer", func(r *http.Request) { r.Header.Set("Origin", ""); r.Header.Set("Referer", h.console.origin+"/") }, 401},
		{"credential-referer", func(r *http.Request) {
			r.Header.Del("Origin")
			r.Header.Set("Referer", strings.Replace(h.console.origin, "https://", "https://user@", 1)+"/")
		}, 401},
		{"forwarded-peer", func(r *http.Request) { r.Header.Set("X-Forwarded-For", "127.0.0.1") }, 400},
		{"cross-site", func(r *http.Request) { r.Header.Set("Sec-Fetch-Site", "cross-site") }, 403},
	} {
		t.Run(test.name, func(t *testing.T) {
			request, _ := http.NewRequest("POST", h.console.origin+"/admin/operation/configure", strings.NewReader("{}"))
			request.Header.Set("Content-Type", "application/json")
			request.Header.Set("Origin", h.console.origin)
			request.Header.Set("X-Identity-Request", "1")
			request.Header.Set("X-CSRF-Token", h.csrf)
			test.mutate(request)
			response, err := h.client.Do(request)
			if err != nil {
				t.Fatal(err)
			}
			_ = response.Body.Close()
			if response.StatusCode != test.want {
				t.Fatalf("status %d", response.StatusCode)
			}
		})
	}
	for range cap(h.console.slots) {
		h.console.slots <- struct{}{}
	}
	code, _, _ := h.call(t, "POST", "/admin/operation/configure", "{}", true)
	for range cap(h.console.slots) {
		<-h.console.slots
	}
	if code != 429 || h.backend.calls["configure"] != 0 {
		t.Fatal("native path bypassed shared admission or authentication")
	}
}

type lateDraftNative struct{ *fakeNative }

func (f *lateDraftNative) LoadDraft(ctx context.Context, id string) (serverinstall.Draft, error) {
	<-ctx.Done()
	return f.fakeNative.LoadDraft(ctx, id)
}
func TestNativeLatePresentationCannotIssueSuccess(t *testing.T) {
	for _, action := range []string{"review", "status"} {
		t.Run(action, func(t *testing.T) {
			h, _ := budgetFixture(t, "install", 0)
			h.console.backend = &lateDraftNative{h.backend}
			h.console.mutex.Lock()
			h.console.expires = time.Now().Add(150 * time.Millisecond)
			h.console.mutex.Unlock()
			method, body := "POST", "{}"
			if action == "status" {
				method, body = "GET", ""
			}
			code, data, _ := h.call(t, method, "/admin/operation/"+action, body, true)
			var result response
			if code != 503 || json.Unmarshal(data, &result) != nil || result.State != "ready" || result.ReviewToken != "" || result.Review != nil {
				t.Fatal("presentation issued success or confirmation after authority expired")
			}
		})
	}
}

// Negotiate each real TLS protocol explicitly: HTTP/1-only httptest defaults
// cannot establish the lifetime of an HTTP/2 stream after body completion.
func TestNativeCompletedBodyReadDeadlineProtocols(t *testing.T) {
	for _, h2 := range []bool{false, true} {
		name, want := "HTTP1", 1
		if h2 {
			name, want = "HTTP2", 2
		}
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			h, f := budgetFixture(t, "update", 10200*time.Millisecond, h2)
			h.client.Timeout = 20 * time.Second
			started := time.Now()
			code, data, raw := h.call(t, "POST", "/admin/operation/apply", `{"review_token":"`+strings.Repeat("a", 64)+`"}`, true)
			var result response
			elapsed := time.Since(started)
			if raw.ProtoMajor != want {
				t.Fatalf("negotiated protocol %s", raw.Proto)
			}
			if code != 200 || json.Unmarshal(data, &result) != nil || result.State != "applying" {
				t.Fatalf("protocol=%s status=%d elapsed=%s body=%s", raw.Proto, code, elapsed, data)
			}
			if err := <-f.finished; err != nil {
				t.Fatalf("protocol=%s facade cancelled: %v", raw.Proto, err)
			}
			if elapsed < 10*time.Second {
				t.Fatal("native operation did not cross the8s read deadline")
			}
			t.Logf("protocol=%s elapsed=%s state=%s", raw.Proto, elapsed, result.State)
		})
	}
}
