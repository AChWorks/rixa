// SPDX-License-Identifier: MPL-2.0
package serverinstall

import (
	"os"
	"sort"
	"strings"
)

var transportKeys = map[string]bool{"HTTP_PROXY": true, "HTTPS_PROXY": true, "ALL_PROXY": true, "NO_PROXY": true, "http_proxy": true, "https_proxy": true, "all_proxy": true, "no_proxy": true, "SSL_CERT_FILE": true, "SSL_CERT_DIR": true}

func transportEnvironment() map[string]string {
	out := map[string]string{}
	for k := range transportKeys {
		if v, ok := os.LookupEnv(k); ok && v != "" {
			out[k] = v
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}
func transportFile(env map[string]string) []byte {
	// An empty environment file remains a valid non-empty private object.
	lines := []string{"# Standard verification transport settings; private operator data."}
	keys := make([]string, 0, len(env))
	for k := range env {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		v := strings.NewReplacer("\\", "\\\\", "\"", "\\\"").Replace(env[k])
		lines = append(lines, k+"=\""+v+"\"")
	}
	return []byte(strings.Join(lines, "\n") + "\n")
}
