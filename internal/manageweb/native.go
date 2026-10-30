// SPDX-License-Identifier: MPL-2.0
package manageweb

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"
)

// Native calls may include the publisher's bounded fresh verification (45s).
// Keep their request lifetime separate from the Admin presentation budget.
// Cancellation and the earlier permit deadline still propagate to the facade.
const nativeRequestTimeout = 2 * time.Minute
const nativeResponseGrace = 5 * time.Second

var errNativeAccessEnded = errors.New("console access ended during native request")

func nativePath(path string) bool {
	switch path {
	case "/admin/operation/status", "/admin/operation/configure",
		"/admin/operation/review", "/admin/operation/apply",
		"/admin/operation/resume", "/admin/operation/remove":
		return true
	}
	return false
}

func (c *Console) native(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), nativeRequestTimeout)
	defer cancel()
	r = r.WithContext(ctx)
	deadline, _ := ctx.Deadline()
	readDeadline := time.Now().Add(8 * time.Second)
	if deadline.Before(readDeadline) {
		readDeadline = deadline
	}
	controller := http.NewResponseController(w)
	if controller.SetReadDeadline(readDeadline) != nil ||
		controller.SetWriteDeadline(deadline.Add(nativeResponseGrace)) != nil {
		c.fail(w, r, 503, "unavailable")
		return
	}
	if r.ContentLength > 32<<10 {
		c.fail(w, r, 400, "invalid_request")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 32<<10)
	defer r.Body.Close()
	actor, err := c.AuthenticateRequest(r, r.Method == http.MethodPost)
	if err != nil {
		if r.Method == http.MethodGet {
			http.Redirect(w, r, "/admin/login", http.StatusSeeOther)
		} else {
			c.fail(w, r, 401, "access_required")
		}
		return
	}
	// The same released Authenticator/Authorizer contracts gate the Shell page
	// and this finite product transport. No native action gains authority from
	// being visible in navigation or from a preceding page request.
	if c.Authorize(ctx, actor, capability, c.target()) != nil {
		c.fail(w, r, 403, "denied")
		return
	}
	copy := r.Clone(ctx)
	copy.URL.Path = strings.TrimPrefix(r.URL.Path, "/admin/operation")
	c.operation(w, copy, c.localizer(r))
}

func nativeStatusCode(ctx context.Context, err error) int {
	if err != nil && (ctx.Err() != nil || errors.Is(err, errNativeAccessEnded)) {
		return http.StatusServiceUnavailable
	}
	return statusCode(err)
}
func requestFailure(ctx context.Context) string {
	if ctx.Err() == context.DeadlineExceeded {
		return "request_timeout"
	}
	if ctx.Err() != nil {
		return "request_cancelled"
	}
	return ""
}
func (c *Console) nativeFail(w http.ResponseWriter, r *http.Request, status int, key string) {
	if cause := requestFailure(r.Context()); cause != "" {
		status, key = http.StatusServiceUnavailable, cause
	}
	c.fail(w, r, status, key)
}

// Body decoding may wait while access expires or the console is revoked.
// Recheck the same session and exact capability before entering a facade.
func (c *Console) authorizeEffect(w http.ResponseWriter, r *http.Request) bool {
	if r.Context().Err() != nil {
		c.nativeFail(w, r, 503, "unavailable")
		return false
	}
	actor, err := c.AuthenticateRequest(r, true)
	if err != nil || c.Authorize(r.Context(), actor, capability, c.target()) != nil {
		c.nativeFail(w, r, 403, "denied")
		return false
	}
	return true
}

// Once a facade may have committed, ended authority is an unknown acknowledgement,
// never a 401/403 claiming the request was rejected before effects.
func (c *Console) nativeResultError(r *http.Request, err error, ownClose bool) error {
	err = errors.Join(err, r.Context().Err())
	c.mutex.Lock()
	live := c.deps.now().Before(c.expires) && (!c.closed || ownClose)
	c.mutex.Unlock()
	if !live {
		err = errors.Join(err, errNativeAccessEnded)
	}
	return err
}
