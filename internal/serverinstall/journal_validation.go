// SPDX-License-Identifier: MPL-2.0
package serverinstall

import (
	"bytes"
	"encoding/json"
	"io"
	"regexp"
	"strings"
)

var hashSyntax = regexp.MustCompile(`^[0-9a-f]{64}$`)
var databaseSyntax = regexp.MustCompile(`^[0-9]+:[0-9]+$`)
var codeSyntax = regexp.MustCompile(`^[a-z0-9-]{1,64}$`)
var knownStages = map[string]bool{"executor": true, "payload": true, "account": true, "runtime-paths": true, "prepare": true, "migrate": true, "bootstrap-control": true, "bootstrap-site": true, "setup": true, "check": true, "service": true, "postflight": true, "remove": true}

func validJournal(j *journal) bool {
	for k, v := range j.TransportEnv {
		if !transportKeys[k] || len(v) > 4096 || strings.ContainsAny(v, "\r\n\x00") {
			return false
		}
	}
	if j.Attempts == nil || !codeSyntax.MatchString(j.Code) || len(j.Message) > 2048 || j.Stage != "" && j.Stage != "review" && !knownStages[j.Stage] {
		return false
	}
	for _, h := range []string{j.Frozen, j.ArchiveSHA256, j.BinarySHA256, j.ManagerSHA256, j.CertSHA256, j.KeySHA256, j.UnitSHA256, j.ReviewToken, j.HelperSHA256} {
		if h != "" && !hashSyntax.MatchString(h) {
			return false
		}
	}
	if j.ExecutorGeneration < 0 || j.ExecutorGeneration > 16 {
		return false
	}
	for k, v := range j.Attempts {
		if !knownStages[k] || v != "attempted" && v != "verified" {
			return false
		}
	}
	for _, p := range []string{j.ControlPrincipal, j.SitePrincipal} {
		if p != "" && !principalSyntax.MatchString(p) {
			return false
		}
	}
	for k, v := range j.DatabaseTargets {
		if k != "control" && k != "site-a" || !databaseSyntax.MatchString(v) {
			return false
		}
	}
	if len(j.DatabaseTargets) != 0 && len(j.DatabaseTargets) != 2 {
		return false
	}
	if j.ControlPrincipal != "" && j.Attempts["bootstrap-control"] != "verified" || j.SitePrincipal != "" && j.Attempts["bootstrap-site"] != "verified" {
		return false
	}
	if j.Frozen == "" {
		if len(j.Attempts) != 0 || j.ExecutorGeneration != 0 || j.ExecutorAcknowledged || j.UID != 0 || j.GID != 0 || j.ManagerSHA256 != "" {
			return false
		}
		if j.State != "draft" && j.State != "ready" && j.State != "blocked" {
			return false
		}
	} else {
		if !hashSyntax.MatchString(j.ManagerSHA256) || !hashSyntax.MatchString(j.ArchiveSHA256) || !hashSyntax.MatchString(j.CertSHA256) || !hashSyntax.MatchString(j.KeySHA256) || len(j.DatabaseTargets) != 2 || j.ExecutorGeneration < 1 || j.ReviewToken == "" || !hashSyntax.MatchString(j.HelperSHA256) || !safePath(j.HelperPath) {
			return false
		}
		if j.State == "draft" || j.State == "ready" || j.State == "blocked" {
			return false
		}
	}
	if j.State == "ready" && (j.ReviewToken == "" || j.ArchiveSHA256 == "" || j.CertSHA256 == "" || j.KeySHA256 == "" || len(j.DatabaseTargets) != 2) {
		return false
	}
	if j.State == "installed" || j.State == "removed" {
		if j.UID == 0 || j.GID == 0 || j.ControlPrincipal == "" || j.SitePrincipal == "" || j.BinarySHA256 == "" || j.UnitSHA256 == "" || j.Draft.ControlPassword != "" || j.Draft.SitePassword != "" {
			return false
		}
		for _, s := range []string{"payload", "account", "runtime-paths", "prepare", "migrate", "bootstrap-control", "bootstrap-site", "setup", "check", "service", "postflight"} {
			if j.Attempts[s] != "verified" {
				return false
			}
		}
	}
	if j.State == "removed" && j.Attempts["remove"] != "verified" {
		return false
	}
	d := j.Draft
	if d.ControlPassword == "" && j.Attempts["bootstrap-control"] == "verified" {
		d.ControlPassword = strings.Repeat("x", 15)
	}
	if d.SitePassword == "" && j.Attempts["bootstrap-site"] == "verified" {
		d.SitePassword = strings.Repeat("x", 15)
	}
	return len(ValidateDraft(d)) == 0
}
func uniqueJSON(data []byte) bool {
	d := json.NewDecoder(bytes.NewReader(data))
	d.UseNumber()
	var walk func(int) bool
	walk = func(depth int) bool {
		if depth > 32 {
			return false
		}
		t, e := d.Token()
		if e != nil {
			return false
		}
		delim, ok := t.(json.Delim)
		if !ok {
			return true
		}
		switch delim {
		case '{':
			seen := map[string]bool{}
			for d.More() {
				key, e := d.Token()
				s, ok := key.(string)
				if e != nil || !ok || seen[s] {
					return false
				}
				seen[s] = true
				if !walk(depth + 1) {
					return false
				}
			}
			end, e := d.Token()
			return e == nil && end == json.Delim('}')
		case '[':
			for d.More() {
				if !walk(depth + 1) {
					return false
				}
			}
			end, e := d.Token()
			return e == nil && end == json.Delim(']')
		default:
			return false
		}
	}
	if !walk(0) {
		return false
	}
	_, e := d.Token()
	return e == io.EOF
}
