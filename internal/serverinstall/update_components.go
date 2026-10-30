// SPDX-License-Identifier: MPL-2.0
package serverinstall

import (
	"reflect"
	"regexp"
	"strings"
)

// This is a bounded native profile, not a dependency resolver. Version values
// belong to the authenticated candidate. New component identities or major
// module paths require an explicit profile/compatibility contract.
var updateComponentPaths = []string{
	"github.com/AChWorks/achrix",
	"github.com/AChWorks/achrix/admin",
	"github.com/AChWorks/achrix/audit",
	"github.com/AChWorks/achrix/identity",
	"github.com/AChWorks/achrix/installation",
	"github.com/AChWorks/achrix/media",
	"github.com/AChWorks/achrix/multisite",
}
var componentTagSyntax = regexp.MustCompile(`^v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(-[0-9A-Za-z]+([.-][0-9A-Za-z]+)*)?$`)
var componentPseudoSyntax = regexp.MustCompile(`(-|\.)[0-9]{14}-[0-9a-f]{12}$`)

func validUpdateComponents(components map[string]string) bool {
	if len(components) != len(updateComponentPaths) {
		return false
	}
	for _, path := range updateComponentPaths {
		version := components[path]
		if len(version) > 128 || !componentTagSyntax.MatchString(version) || componentPseudoSyntax.MatchString(version) {
			return false
		}
		if i := strings.IndexByte(version, '-'); i >= 0 {
			for _, part := range strings.Split(version[i+1:], ".") {
				if len(part) > 1 && part[0] == '0' && strings.Trim(part, "0123456789") == "" {
					return false
				}
			}
		}
	}
	return true
}
func validCandidateCheckReceipt(status string, sites int, core string, observed, authenticated map[string]string) bool {
	return status == "ok" && sites == 1 && validUpdateComponents(authenticated) && core == authenticated["github.com/AChWorks/achrix"] && reflect.DeepEqual(observed, authenticated)
}
