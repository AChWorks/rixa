// SPDX-License-Identifier: MPL-2.0
package serverinstall

import (
	"encoding/json"
	"net"
	"net/url"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"unicode"

	"github.com/AChWorks/rixa/internal/product"
)

var operationSyntax = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,47}$`)
var releaseSyntax = regexp.MustCompile(`^v[0-9]+\.[0-9]+\.[0-9]+([.-][0-9A-Za-z][0-9A-Za-z.-]*)?$`)
var commitSyntax = regexp.MustCompile(`^[0-9a-f]{40}$`)

func ValidateDraft(d Draft) []Problem {
	var p []Problem
	add := func(field, code, message string) { p = append(p, Problem{field, code, message}) }
	if !operationSyntax.MatchString(d.OperationID) {
		add("operation_id", "format", "Use 1–48 lowercase letters, numbers or hyphens, beginning with a letter or number.")
	}
	for _, f := range []struct{ name, value string }{{"archive", d.ArchivePath}, {"bundle", d.BundlePath}, {"tls_cert", d.TLSCertPath}, {"tls_key", d.TLSKeyPath}} {
		if !safePath(f.value) {
			add(f.name, "path", "Use one absolute canonical regular-file path without control characters.")
		}
	}
	if !releaseSyntax.MatchString(d.Release) || len(d.Release) > 128 {
		add("release", "format", "Choose the exact versioned Rixa release tag.")
	}
	if !commitSyntax.MatchString(d.SourceCommit) {
		add("source_commit", "format", "Use the exact 40-character lowercase source commit.")
	}
	origins := map[string]bool{}
	for _, f := range []struct{ name, value string }{{"control_origin", d.ControlOrigin}, {"site_origin", d.SiteOrigin}} {
		u, e := url.Parse(f.value)
		if e != nil || u == nil || u.Scheme != "https" || u.Hostname() == "" || u.User != nil || u.Path != "" || u.RawQuery != "" || u.Fragment != "" || u.Opaque != "" || u.String() != f.value || controlText(f.value) {
			add(f.name, "origin", "Use a distinct HTTPS origin without a path, query or credentials.")
			continue
		}
		key := strings.ToLower(u.Hostname())
		if origins[key] {
			add(f.name, "isolation", "Control and site require different hostnames.")
		}
		origins[key] = true
	}
	host, port, e := net.SplitHostPort(d.Listen)
	ip := net.ParseIP(host)
	number, numberErr := strconv.Atoi(port)
	if e != nil || numberErr != nil || number < 1 || number > 65535 || strconv.Itoa(number) != port || ip == nil || ip.IsLoopback() || ip.IsMulticast() || (!ip.IsUnspecified() && !ip.IsGlobalUnicast()) || port == "" {
		add("listen", "listener", "Use an explicit non-loopback IP and port, such as 0.0.0.0:8443.")
	}
	if numberErr == nil {
		for _, origin := range []struct{ name, value string }{{"control_origin", d.ControlOrigin}, {"site_origin", d.SiteOrigin}} {
			u, e := url.Parse(origin.value)
			if e == nil && u != nil {
				op := u.Port()
				if op == "" {
					op = "443"
				}
				if op != port {
					add(origin.name, "port", "The HTTPS origin port must match the listener port.")
				}
			}
		}
	}
	if d.Language != "en" && d.Language != "fa" {
		add("language", "language", "Choose en or fa.")
	}
	for _, f := range []struct{ name, value string }{{"control_login", d.ControlLogin}, {"site_login", d.SiteLogin}} {
		if e := product.ValidateBootstrapLogin(f.value); e != nil {
			add(f.name, "login", e.Error())
		}
	}
	for _, f := range []struct{ name, value string }{{"control_password", d.ControlPassword}, {"site_password", d.SitePassword}} {
		if e := product.ValidateBootstrapPassword(f.value); e != nil {
			add(f.name, "password", e.Error())
		}
	}

	for _, f := range []struct{ name, value string }{{"control_dsn", d.ControlDSN}, {"site_dsn", d.SiteDSN}} {
		u, e := url.Parse(f.value)
		valid := e == nil && u != nil && len(f.value) <= 4096 && !controlText(f.value)
		if valid {
			password, hasPassword := "", false
			if u.User != nil {
				password, hasPassword = u.User.Password()
			}
			ip := net.ParseIP(u.Hostname())
			port, e := strconv.Atoi(u.Port())
			q := u.Query()
			valid = (u.Scheme == "postgres" || u.Scheme == "postgresql") && u.User != nil && u.User.Username() != "" && hasPassword && password != "" && ip != nil && ip.IsLoopback() && e == nil && port > 0 && port <= 65535 && u.Path != "" && u.Path != "/" && u.Fragment == "" && u.RawPath == "" && len(q) == 1 && q.Get("sslmode") == "disable" && len(q["sslmode"]) == 1
		}
		if !valid {
			add(f.name, "database-profile", "This native profile needs an explicit loopback TCP PostgreSQL URL with user, password, port, database and only sslmode=disable; implicit PG environment/passfiles/peer credentials are unsupported.")
		}
	}

	if d.ControlDSN != "" && d.ControlDSN == d.SiteDSN {
		add("site_dsn", "isolation", "Control and site require distinct PostgreSQL databases.")
	}
	if len(p) == 0 {
		if _, e := product.ParseConfig(configBytes(d, "", "")); e != nil {
			add("listen", "configuration", "Origins, listener ports and product configuration must agree.")
		}
	}
	return p
}
func safePath(p string) bool {
	return filepath.IsAbs(p) && filepath.Clean(p) == p && p != "/" && len(p) <= 4096 && !controlText(p)
}
func controlText(s string) bool {
	for _, r := range s {
		if unicode.IsControl(r) {
			return true
		}
	}
	return false
}

func configBytes(d Draft, controlPrincipal, sitePrincipal string) []byte {
	// The file schema uses duration strings; secrets remain in the separate private
	// credential source and never enter the product configuration.
	v := map[string]any{
		"hosting_profile": "direct_tls", "listen": d.Listen, "language": d.Language,
		"startup_timeout": "15s", "shutdown_timeout": "15s",
		"tls":     map[string]string{"cert_file": "/etc/rixa/runtime/tls/cert.pem", "key_file": "/etc/rixa/runtime/tls/key.pem"},
		"control": map[string]string{"origin": d.ControlOrigin, "database_env": "RIXA_CONTROL_DSN", "admin_principal": controlPrincipal},
		"sites":   []any{map[string]any{"id": "site-a", "origin": d.SiteOrigin, "database_env": "RIXA_SITE_DSN", "admin_principal": sitePrincipal, "media_root": "/var/lib/rixa/site-a/media", "public_root": "/var/lib/rixa/site-a/public", "public_policy": map[string]any{"indexing": "noindex", "snippet": "none", "crawlers": []any{map[string]string{"user_agent": "*", "access": "disallow", "purpose": "search"}}}}},
	}
	b, _ := json.Marshal(v)
	return b
}
