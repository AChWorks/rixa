// SPDX-License-Identifier: MPL-2.0
package manageweb

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"html"
	"io"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"sort"
	"strings"
	"syscall"
	"unicode"
)

// The native public diagnostic is English. Redaction uses actual protected or
// submitted values, not message matching, translation rules or guessed secrets.
func (c *Console) diagnosticRedactor(request *http.Request, reviewToken string, supplied ...string) (func(string) string, bool) {
	id := c.options.Scope.Operation
	if c.options.Scope.Kind == "update" {
		id = c.options.Scope.Installation
	}
	draft, err := c.backend.LoadDraft(request.Context(), id)
	if (err == nil && draft.OperationID != id) || (err != nil && (!errors.Is(err, os.ErrNotExist) || c.options.Scope.Kind == "update")) {
		return nil, false
	}
	file, err := os.OpenFile(c.options.PermitFile, os.O_RDONLY|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return nil, false
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !os.SameFile(info, c.permitInfo) || !info.Mode().IsRegular() ||
		info.Mode().Perm() != 0600 || info.Size() != 44 {
		return nil, false
	}
	permit, err := io.ReadAll(io.LimitReader(file, 45))
	if err != nil || len(permit) != 44 || permit[43] != '\n' {
		return nil, false
	}
	values := []string{draft.ControlDSN, draft.SiteDSN, draft.SiteBDSN, draft.ControlPassword, draft.SitePassword, draft.SiteBPassword,
		strings.TrimSuffix(string(permit), "\n"), request.Header.Get("X-CSRF-Token"), reviewToken}
	for _, cookie := range request.Cookies() {
		if cookie.Name == c.cookieName {
			values = append(values, cookie.Value, c.csrf(cookie.Value))
		}
	}
	values = append(values, supplied...)
	return diagnosticReplacer(values), true
}

var keywordPassword = regexp.MustCompile(`(?i)\bpassword\s*=\s*('(?:\\.|[^'])*'|[^\s]+)`)
var percentEscape = regexp.MustCompile(`%[0-9A-Fa-f]{2}`)

func diagnosticReplacer(values []string) func(string) string {
	replacements := map[string]string{}
	add := func(value, replacement string) {
		if value != "" {
			replacements[value] = replacement
		}
	}
	// Include the encoded forms that URL, HTML and JSON diagnostic boundaries use.
	// Full encoded DSNs need masking too; a base64 password is not a substring of
	// the base64 encoding of its surrounding DSN.
	addSecret := func(value, replacement string) {
		if value == "" {
			return
		}
		add(value, replacement)
		encoded, _ := json.Marshal(value)
		variants := []string{url.QueryEscape(value), url.PathEscape(value), strings.TrimPrefix(url.UserPassword("", value).String(), ":"), html.EscapeString(value),
			string(encoded[1 : len(encoded)-1]), base64.StdEncoding.EncodeToString([]byte(value)),
			base64.RawStdEncoding.EncodeToString([]byte(value)), base64.URLEncoding.EncodeToString([]byte(value)),
			base64.RawURLEncoding.EncodeToString([]byte(value))}
		for _, variant := range variants {
			if variant != value {
				add(variant, "[redacted]")
			}
			lower := percentEscape.ReplaceAllStringFunc(variant, strings.ToLower)
			if lower != variant {
				add(lower, "[redacted]")
			}
		}
	}
	for _, value := range values {
		addSecret(value, "[redacted]")
		if u, err := url.Parse(value); err == nil && u.User != nil {
			if password, ok := u.User.Password(); ok {
				addSecret(password, "[redacted]")
			}
		} else if start := strings.Index(value, "://"); start >= 0 {
			// Malformed URL escapes may prevent url.Parse; supplied userinfo still
			// identifies the exact candidate password, without interpreting an error.
			if end := strings.LastIndex(value, "@"); end > start+3 {
				userinfo := value[start+3 : end]
				if colon := strings.IndexByte(userinfo, ':'); colon >= 0 {
					password := userinfo[colon+1:]
					addSecret(password, "[redacted]")
					if decoded, e := url.PathUnescape(password); e == nil {
						addSecret(decoded, "[redacted]")
					}
				}
			}
		}
		for _, match := range keywordPassword.FindAllStringSubmatch(value, -1) {
			password := match[1]
			if strings.HasPrefix(password, "'") && strings.HasSuffix(password, "'") {
				password = password[1 : len(password)-1]
				password = strings.NewReplacer("\\'", "'", "\\\\", "\\").Replace(password)
			}
			addSecret(password, "[redacted]")
		}
	}
	keys := make([]string, 0, len(replacements))
	for key := range replacements {
		keys = append(keys, key)
	}
	sort.Slice(keys, func(i, j int) bool { return len(keys[i]) > len(keys[j]) })
	pairs := make([]string, 0, len(keys)*2)
	for _, key := range keys {
		pairs = append(pairs, key, replacements[key])
	}
	replacer := strings.NewReplacer(pairs...)
	return func(raw string) string {
		// Redact before truncation so bounds cannot expose a secret prefix.
		safe := replacer.Replace(strings.ToValidUTF8(raw, "�"))
		safe = strings.Map(func(r rune) rune {
			if (unicode.IsControl(r) && r != '\n' && r != '\t') || r == 0x061c || r == 0x200e || r == 0x200f ||
				(r >= 0x202a && r <= 0x202e) || (r >= 0x2066 && r <= 0x2069) {
				return -1
			}
			return r
		}, safe)
		runes := []rune(safe)
		if len(runes) > 4096 {
			safe = string(runes[:4096]) + "…"
		}
		return safe
	}
}
