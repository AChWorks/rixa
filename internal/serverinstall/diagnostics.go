// SPDX-License-Identifier: MPL-2.0
package serverinstall

import (
	"fmt"
	"io"
	"log/slog"
	"net/url"
	"os"
	"path/filepath"
	"strings"
)

// Formatting a secret-bearing draft is always safe. JSON serialization is
// intentionally available only to the private persistence/input boundary.
func (d Draft) Format(state fmt.State, verb rune) {
	id := d.OperationID
	if !operationSyntax.MatchString(id) {
		id = "[invalid]"
	}
	_, _ = io.WriteString(state, "serverinstall.Draft{operation_id:"+id+", protected:[redacted]}")
}
func (d Draft) LogValue() slog.Value {
	id := d.OperationID
	if !operationSyntax.MatchString(id) {
		id = "[invalid]"
	}
	return slog.GroupValue(slog.String("operation_id", id), slog.String("protected", "[redacted]"))
}
func redactDiagnostic(s string, d Draft) string {
	for _, secret := range []string{d.ControlPassword, d.SitePassword, d.SiteBPassword} {
		if secret != "" {
			s = strings.ReplaceAll(s, secret, "[redacted]")
		}
	}
	for _, dsn := range []string{d.ControlDSN, d.SiteDSN, d.SiteBDSN} {
		u, e := url.Parse(dsn)
		if e == nil && u.User != nil {
			if password, ok := u.User.Password(); ok && password != "" {
				s = strings.ReplaceAll(s, password, "[redacted]")
				s = strings.ReplaceAll(s, url.QueryEscape(password), "[redacted]")
				s = strings.ReplaceAll(s, url.PathEscape(password), "[redacted]")
			}
		}
		// libpq syntax is not a URL. Keep endpoint/database context and mask its
		// password token, including escaped single-quoted values.
		if !strings.Contains(dsn, "://") {
			for _, word := range strings.Fields(dsn) {
				if strings.HasPrefix(strings.ToLower(word), "password=") {
					s = strings.ReplaceAll(s, word, "password=[redacted]")
				}
			}
		}
	}
	if len(s) > 32<<10 {
		s = s[:32<<10]
	}
	return s
}

func (m *Manager) retainDiagnostic(j *journal, text string) {
	path := filepath.Join(m.stateRoot, j.Draft.OperationID+".diagnostic")
	previous, e := readPrivate(path, m.ownerUID, 64<<10)
	if e != nil && !os.IsNotExist(e) {
		return
	}
	b := []byte(redactDiagnostic(string(previous)+"\n"+text, j.Draft))
	if len(b) > 64<<10 {
		b = b[len(b)-(64<<10):]
	}
	_ = writeAtomic(path, b, 0600)
}
