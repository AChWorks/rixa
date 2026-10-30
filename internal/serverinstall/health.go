// SPDX-License-Identifier: MPL-2.0
package serverinstall

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/AChWorks/achrix/identity"
)

func (m *Manager) checkOwnedService(j *journal) error {
	if !trustedRootParent(unitPath) || !trustedRootParent("/etc/rixa/service.env") {
		return ErrState
	}
	if e := m.observeAccount(j); e != nil {
		return e
	}
	if e := m.checkPayload(j); e != nil {
		return e
	}
	info, e := os.Lstat(unitPath)
	if e != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0644 || info.Size() != int64(len(m.serviceBytes(j))) || fileUID(info) != 0 {
		return ErrState
	}
	b, e := os.ReadFile(unitPath)
	if e != nil || digest(b) != j.UnitSHA256 || !bytes.Equal(b, m.serviceBytes(j)) {
		return ErrState
	}
	env, e := readPrivate("/etc/rixa/service.env", 0, 16<<10)
	if e != nil || !bytes.Equal(env, environmentFile(j.Draft)) {
		return ErrState
	}
	return nil
}
func properties(b []byte) map[string]string {
	p := map[string]string{}
	for _, line := range strings.Split(strings.TrimSpace(string(b)), "\n") {
		kv := strings.SplitN(line, "=", 2)
		if len(kv) == 2 {
			p[kv[0]] = kv[1]
		}
	}
	return p
}
func (m *Manager) validateInstalled(ctx context.Context, j *journal) error {
	if e := m.checkOwnedService(j); e != nil {
		return e
	}
	if e := m.loadedServiceOwned(ctx, j, true); e != nil {
		return e
	}

	config, e := readPrivate(runtimeConfigPath, int(j.UID), 1<<20)
	if e != nil || !bytes.Equal(config, configBytes(j.Draft, j.ControlPrincipal, j.SitePrincipal)) {
		return ErrState
	}
	client, e := m.localClient(j)
	if e != nil {
		return e
	}
	defer client.CloseIdleConnections()
	for _, origin := range []string{j.Draft.ControlOrigin, j.Draft.SiteOrigin} {
		req, e := http.NewRequestWithContext(ctx, http.MethodGet, origin+"/admin/login", nil)
		if e != nil {
			return ErrInput
		}
		resp, e := client.Do(req)
		if e != nil {
			return ErrPrerequisite
		}
		_, re := io.Copy(io.Discard, io.LimitReader(resp.Body, 2<<20))
		_ = resp.Body.Close()
		if re != nil || resp.StatusCode != http.StatusOK {
			return ErrPrerequisite
		}
	}
	return nil
}
func (m *Manager) localClient(j *journal) (*http.Client, error) {
	cert, e := readPrivate(m.tlsSnapshotPath(j, false), m.ownerUID, 1<<20)
	if e != nil || digest(cert) != j.CertSHA256 {
		return nil, ErrState
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(cert) {
		return nil, ErrInput
	}
	host, port, e := net.SplitHostPort(j.Draft.Listen)
	if e != nil {
		return nil, ErrInput
	}
	ip := net.ParseIP(host)
	if ip == nil {
		return nil, ErrInput
	}
	if ip.IsUnspecified() {
		host = "127.0.0.1"
		if ip.To4() == nil {
			host = "::1"
		}
	}
	endpoint := net.JoinHostPort(host, port)
	transport := &http.Transport{TLSClientConfig: &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12}, DisableKeepAlives: true,
		DialContext: func(ctx context.Context, network, address string) (net.Conn, error) {
			return (&net.Dialer{Timeout: 3 * time.Second}).DialContext(ctx, "tcp", endpoint)
		},
		TLSHandshakeTimeout: 3 * time.Second, ResponseHeaderTimeout: 5 * time.Second}
	return &http.Client{Transport: transport, Timeout: 7 * time.Second, CheckRedirect: func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse }}, nil
}
func (m *Manager) postflight(ctx context.Context, j *journal) error {
	client, e := m.localClient(j)
	if e != nil {
		return e
	}
	defer client.CloseIdleConnections()
	for _, p := range []struct{ origin, login, password, principal, page string }{
		{j.Draft.ControlOrigin, j.Draft.ControlLogin, j.Draft.ControlPassword, j.ControlPrincipal, "/admin/sites/"},
		{j.Draft.SiteOrigin, j.Draft.SiteLogin, j.Draft.SitePassword, j.SitePrincipal, "/admin/content/"},
	} {
		if e = verifyLogin(ctx, client, p.origin, p.login, p.password, p.principal, p.page); e != nil {
			return e
		}
	}
	return nil
}
func verifyLogin(ctx context.Context, client *http.Client, origin, login, password, principal, page string) (err error) {
	payload, _ := json.Marshal(map[string]string{"login": login, "password": password})
	req, e := http.NewRequestWithContext(ctx, http.MethodPost, origin+"/auth/login", bytes.NewReader(payload))
	if e != nil {
		return ErrInput
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Origin", origin)
	req.Header.Set("X-Identity-Request", "1")
	resp, e := client.Do(req)
	if e != nil {
		return ErrState
	} // an unknown login is not replayed
	body, e := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	_ = resp.Body.Close()
	if e != nil || resp.StatusCode != http.StatusOK {
		return ErrPrerequisite
	}
	var session struct {
		Principal string `json:"principal"`
		CSRF      string `json:"csrf"`
		Expires   string `json:"expires_at"`
	}
	if json.Unmarshal(body, &session) != nil || session.CSRF == "" {
		return ErrState
	}
	var cookie *http.Cookie
	for _, c := range resp.Cookies() {
		if c.Name == identity.CookieName {
			cookie = c
		}
	}
	if cookie == nil || len(cookie.Value) > 1024 {
		return ErrState
	}
	// Every positively identified session is revoked even when page proof fails.
	// Unknown logout acknowledgement blocks completion; no repeated mutation.
	defer func() {
		revokeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 7*time.Second)
		defer cancel()
		q, e := http.NewRequestWithContext(revokeCtx, http.MethodPost, origin+"/auth/logout", nil)
		if e != nil {
			err = errors.Join(err, ErrState)
			return
		}
		q.Header.Set("Origin", origin)
		q.Header.Set("X-Identity-Request", "1")
		q.Header.Set("X-CSRF-Token", session.CSRF)
		q.AddCookie(cookie)
		r, e := client.Do(q)
		if e != nil {
			err = errors.Join(err, ErrState)
			return
		}
		_, _ = io.Copy(io.Discard, io.LimitReader(r.Body, 64<<10))
		_ = r.Body.Close()
		if r.StatusCode != http.StatusNoContent {
			err = errors.Join(err, ErrState)
		} else {
			probe, e := http.NewRequestWithContext(revokeCtx, http.MethodGet, origin+"/auth/csrf", nil)
			if e != nil {
				err = errors.Join(err, ErrState)
			} else {
				probe.Header.Set("Origin", origin)
				probe.Header.Set("X-Identity-Request", "1")
				probe.AddCookie(cookie)
				proof, e := client.Do(probe)
				if e != nil {
					err = errors.Join(err, ErrState)
				} else {
					_, _ = io.Copy(io.Discard, io.LimitReader(proof.Body, 64<<10))
					_ = proof.Body.Close()
					if proof.StatusCode != http.StatusUnauthorized {
						err = errors.Join(err, ErrState)
					}
				}
			}
		}
		cookie.Value = ""
		session.CSRF = ""
	}()
	if session.Principal != principal {
		return ErrState
	}
	q, e := http.NewRequestWithContext(ctx, http.MethodGet, origin+page, nil)
	if e != nil {
		return ErrInput
	}
	q.AddCookie(cookie)
	r, e := client.Do(q)
	if e != nil {
		return ErrPrerequisite
	}
	_, e = io.Copy(io.Discard, io.LimitReader(r.Body, 2<<20))
	_ = r.Body.Close()
	if e != nil || r.StatusCode != http.StatusOK {
		return ErrPrerequisite
	}
	return nil
}

func (m *Manager) loadedServiceOwned(ctx context.Context, j *journal, requireActive bool) error {
	b, e := m.run(ctx, "/usr/bin/systemctl", "show", "--property=FragmentPath", "--property=DropInPaths", "--property=ActiveState", "--property=MainPID", "rixa.service")
	if e != nil {
		return e
	}
	p := properties(b)
	if p["FragmentPath"] != unitPath || p["DropInPaths"] != "" {
		return ErrState
	}
	state := p["ActiveState"]
	if state != "active" && state != "inactive" && state != "failed" {
		return ErrState
	}
	pid, e := strconv.ParseUint(p["MainPID"], 10, 32)
	if e != nil {
		return ErrState
	}
	if pid == 0 {
		if requireActive || state == "active" {
			return ErrPrerequisite
		}
		return nil
	}
	proc := "/proc/" + strconv.FormatUint(pid, 10)
	target, e := os.Readlink(proc + "/exe")
	if e != nil || target != m.binaryPath(j) {
		return ErrState
	}
	pi, e := os.Stat(proc)
	if e != nil || fileUID(pi) != j.UID {
		return ErrState
	}
	statusFile, e := os.Open(proc + "/status")
	if e != nil {
		return ErrState
	}
	status, e := io.ReadAll(io.LimitReader(statusFile, (64<<10)+1))
	closeErr := statusFile.Close()
	if e != nil || closeErr != nil || len(status) > 64<<10 || !runtimeCredentialsMatch(status, j.UID, j.GID) {
		return ErrState
	}
	actual, e := os.Stat(proc + "/exe")
	if e != nil {
		return ErrState
	}
	expected, e := os.Stat(m.binaryPath(j))
	if e != nil || !os.SameFile(actual, expected) {
		return ErrState
	}
	if requireActive && state != "active" {
		return ErrPrerequisite
	}
	return nil
}
