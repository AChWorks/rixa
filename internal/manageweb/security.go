// SPDX-License-Identifier: MPL-2.0
package manageweb

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/AChWorks/achrix"
	shell "github.com/AChWorks/achrix/admin"
	"github.com/AChWorks/achrix/admin/localize"
	"github.com/AChWorks/rixa/internal/serverinstall"
)

type Console struct {
	options                          Options
	origin, stage, cookieName        string
	backend                          serverinstall.Facade
	updater                          serverinstall.UpdateFacade
	deps                             dependencies
	shell                            *shell.Shell
	catalog                          *localize.Catalog
	allowed                          map[string]bool
	mutex                            sync.Mutex
	effects                          sync.Mutex
	permitHash, sessionHash, csrfKey [32]byte
	permitInfo                       os.FileInfo
	expires                          time.Time
	used, closed                     bool
	attempts                         int
	slots                            chan struct{}
	artifact                         artifact
	artifactAttempts                 int
}

func (c *Console) liveLocked() bool { return !c.closed && c.deps.now().Before(c.expires) }
func (c *Console) sameOrigin(r *http.Request) bool {
	if values := r.Header.Values("Origin"); len(values) > 0 {
		return len(values) == 1 && values[0] == c.origin
	}
	values := r.Header.Values("Referer")
	if len(values) != 1 {
		return false
	}
	u, e := url.Parse(values[0])
	return e == nil && u.Scheme == "https" && u.Host == strings.TrimPrefix(c.origin, "https://") && u.User == nil
}
func (c *Console) current(r *http.Request) (string, bool) {
	cookies := r.Cookies()
	raw := ""
	count := 0
	for _, v := range cookies {
		if v.Name == c.cookieName {
			count++
			if count > 1 {
				return "", false
			}
			raw = v.Value
		}
	}
	if len(raw) != 43 {
		return "", false
	}
	hash := sha256.Sum256([]byte(raw))
	c.mutex.Lock()
	defer c.mutex.Unlock()
	return raw, c.liveLocked() && c.used && subtle.ConstantTimeCompare(hash[:], c.sessionHash[:]) == 1
}
func (c *Console) csrf(raw string) string {
	h := hmac.New(sha256.New, c.csrfKey[:])
	_, _ = h.Write([]byte(raw))
	return base64.RawURLEncoding.EncodeToString(h.Sum(nil))
}
func (c *Console) AuthenticateRequest(r *http.Request, mutation bool) (achrix.Principal, error) {
	raw, ok := c.current(r)
	if !ok {
		return "", shell.ErrUnauthenticated
	}
	if mutation {
		values := r.Header.Values("X-CSRF-Token")
		if !c.sameOrigin(r) || r.Header.Get("X-Identity-Request") != "1" || len(values) != 1 || subtle.ConstantTimeCompare([]byte(values[0]), []byte(c.csrf(raw))) != 1 {
			return "", shell.ErrUnauthenticated
		}
	}
	return achrix.Principal(principal), nil
}
func (c *Console) Authorize(_ context.Context, actor achrix.Principal, cap, target string) error {
	c.mutex.Lock()
	defer c.mutex.Unlock()
	if !c.liveLocked() || actor != achrix.Principal(principal) || cap != capability || target != c.target() {
		return achrix.ErrDenied
	}
	return nil
}
func headers(w http.ResponseWriter) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.Header().Set("Content-Security-Policy", "default-src 'none'; script-src 'self'; style-src 'self'; img-src 'self'; connect-src 'self'; form-action 'self'; base-uri 'none'; frame-ancestors 'none'")
	w.Header().Set("X-Frame-Options", "DENY")
	w.Header().Set("X-Robots-Tag", "noindex, nofollow")
}
func (c *Console) serve(w http.ResponseWriter, r *http.Request) {
	headers(w)
	peer, _, e := net.SplitHostPort(r.RemoteAddr)
	if e != nil || net.ParseIP(peer) == nil || !net.ParseIP(peer).IsLoopback() || r.TLS == nil || r.Host != strings.TrimPrefix(c.origin, "https://") || r.URL.RawQuery != "" || r.URL.RawPath != "" {
		c.fail(w, r, 400, "invalid_request")
		return
	}
	if r.Method != http.MethodGet && r.Method != http.MethodPost {
		c.fail(w, r, 405, "invalid_request")
		return
	}
	for name := range r.Header {
		if strings.EqualFold(name, "Forwarded") || strings.HasPrefix(strings.ToLower(name), "x-forwarded-") {
			c.fail(w, r, 400, "invalid_request")
			return
		}
	}
	if r.Header.Get("Sec-Fetch-Site") == "cross-site" {
		c.fail(w, r, 403, "denied")
		return
	}
	select {
	case c.slots <- struct{}{}:
		defer func() { <-c.slots }()
	default:
		c.fail(w, r, 429, "busy")
		return
	}
	c.mutex.Lock()
	expiry := c.expires
	c.mutex.Unlock()
	ctx, cancel := context.WithDeadline(r.Context(), expiry)
	defer cancel()
	r = r.WithContext(ctx)
	switch {
	case r.URL.Path == "/" || r.URL.Path == "/admin/login":
		if r.Method != http.MethodGet {
			c.fail(w, r, 405, "invalid_request")
			return
		}
		http.Redirect(w, r, "/access", http.StatusSeeOther)
	case r.URL.Path == "/console-assets/console.js" && r.Method == http.MethodGet:
		assetResponse(w, "text/javascript; charset=utf-8", client)
	case r.URL.Path == "/console-assets/console.css" && r.Method == http.MethodGet:
		assetResponse(w, "text/css; charset=utf-8", style)
	case r.URL.Path == "/access":
		c.access(w, r)
	case strings.HasPrefix(r.URL.Path, "/language/"):
		if r.Method != http.MethodGet {
			c.fail(w, r, 405, "invalid_request")
			return
		}
		tag := strings.TrimPrefix(r.URL.Path, "/language/")
		if _, e := c.catalog.For(tag); e != nil {
			c.fail(w, r, 400, "invalid_language")
			return
		}
		http.SetCookie(w, &http.Cookie{Name: "rixa-console-language", Value: tag, Path: "/", Secure: true, HttpOnly: true, SameSite: http.SameSiteStrictMode, MaxAge: 86400})
		dest := "/access"
		if _, ok := c.current(r); ok {
			dest = "/admin/operation/"
		}
		http.Redirect(w, r, dest, http.StatusSeeOther)
	case strings.HasPrefix(r.URL.Path, "/console-auth/"):
		c.auth(w, r)
	case strings.HasPrefix(r.URL.Path, "/artifact/"):
		c.artifacts(w, r)
	case strings.HasPrefix(r.URL.Path, "/admin/"):
		c.shell.Handler().ServeHTTP(w, r)
	default:
		c.fail(w, r, 404, "invalid_request")
	}
}
func (c *Console) auth(w http.ResponseWriter, r *http.Request) {
	if !c.sameOrigin(r) || r.Header.Get("X-Identity-Request") != "1" {
		c.fail(w, r, 401, "access_required")
		return
	}
	switch r.URL.Path {
	case "/console-auth/login":
		if r.Method != http.MethodPost {
			c.fail(w, r, 405, "invalid_request")
			return
		}
		var input struct {
			Permit string `json:"permit"`
		}
		if decode(w, r, &input, 2048) != nil {
			c.fail(w, r, 400, "invalid_request")
			return
		}
		c.mutex.Lock()
		if !c.liveLocked() || c.used || c.attempts >= 5 {
			c.mutex.Unlock()
			c.fail(w, r, 401, "access_expired")
			return
		}
		c.attempts++
		hash := sha256.Sum256([]byte(input.Permit))
		if len(input.Permit) != 43 || subtle.ConstantTimeCompare(hash[:], c.permitHash[:]) != 1 {
			c.mutex.Unlock()
			c.fail(w, r, 401, "access_required")
			return
		}
		raw := make([]byte, 32)
		if _, e := rand.Read(raw); e != nil {
			c.mutex.Unlock()
			c.fail(w, r, 503, "unavailable")
			return
		}
		token := base64.RawURLEncoding.EncodeToString(raw)
		c.sessionHash = sha256.Sum256([]byte(token))
		c.used = true
		seconds := int(c.expires.Sub(c.deps.now()).Seconds())
		c.mutex.Unlock()
		http.SetCookie(w, &http.Cookie{Name: c.cookieName, Value: token, Path: "/", Secure: true, HttpOnly: true, SameSite: http.SameSiteStrictMode, MaxAge: seconds})
		jsonResponse(w, 200, map[string]string{"location": "/admin/operation/"})
	case "/console-auth/csrf":
		if r.Method != http.MethodGet {
			c.fail(w, r, 405, "invalid_request")
			return
		}
		raw, ok := c.current(r)
		if !ok {
			c.fail(w, r, 401, "access_expired")
			return
		}
		jsonResponse(w, 200, map[string]string{"csrf": c.csrf(raw)})
	case "/console-auth/logout":
		if r.Method != http.MethodPost {
			c.fail(w, r, 405, "invalid_request")
			return
		}
		if _, e := c.AuthenticateRequest(r, true); e != nil {
			c.fail(w, r, 401, "access_required")
			return
		}
		c.mutex.Lock()
		c.closed = true
		c.sessionHash = [32]byte{}
		c.mutex.Unlock()
		http.SetCookie(w, &http.Cookie{Name: c.cookieName, Path: "/", Secure: true, HttpOnly: true, SameSite: http.SameSiteStrictMode, MaxAge: -1})
		w.WriteHeader(http.StatusNoContent)
	default:
		c.fail(w, r, 404, "invalid_request")
	}
}
func decode(w http.ResponseWriter, r *http.Request, v any, limit int64) error {
	if r.Header.Get("Content-Type") != "application/json" {
		return errors.New("JSON required")
	}
	r.Body = http.MaxBytesReader(w, r.Body, limit)
	defer r.Body.Close()
	data, err := io.ReadAll(r.Body)
	if err != nil {
		return err
	}
	guard := json.NewDecoder(strings.NewReader(string(data)))
	token, err := guard.Token()
	if err != nil || token != json.Delim('{') {
		return errors.New("one JSON object required")
	}
	seen := map[string]bool{}
	for guard.More() {
		key, err := guard.Token()
		name, ok := key.(string)
		if err != nil || !ok || seen[name] {
			return errors.New("duplicate JSON field")
		}
		seen[name] = true
		var raw json.RawMessage
		if guard.Decode(&raw) != nil {
			return errors.New("invalid JSON value")
		}
	}
	if token, err = guard.Token(); err != nil || token != json.Delim('}') {
		return errors.New("invalid JSON object")
	}
	if guard.Decode(new(any)) != io.EOF {
		return errors.New("one JSON document required")
	}
	decoder := json.NewDecoder(strings.NewReader(string(data)))
	decoder.DisallowUnknownFields()
	return decoder.Decode(v)
}
func jsonResponse(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
func (c *Console) language(r *http.Request) string {
	if cookie, e := r.Cookie("rixa-console-language"); e == nil {
		if _, e = c.catalog.For(cookie.Value); e == nil {
			return cookie.Value
		}
	}
	return "en"
}
func (c *Console) localizer(r *http.Request) *localize.Localizer {
	l, _ := c.catalog.For(c.language(r))
	return l
}
func text(l *localize.Localizer, key string) string {
	s, e := l.Text(domain, key, nil)
	if e != nil {
		return key
	}
	return s
}
func (c *Console) fail(w http.ResponseWriter, r *http.Request, status int, key string) {
	jsonResponse(w, status, map[string]string{"code": key, "message": text(c.localizer(r), key)})
}
