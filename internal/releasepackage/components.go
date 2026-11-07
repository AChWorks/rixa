// SPDX-License-Identifier: MPL-2.0
package releasepackage

import (
	"regexp"
	"strings"
)

// Only two fully identified native package profiles are accepted: the
// published seven-component profile, and the new Bridge/OAuth nine-component
// profile. The authenticated candidate supplies exact values; any unlisted,
// partial, duplicate, replaced or pseudo-release graph remains invalid.
// A current installation additionally pins its exact compiled component map.
var publishedComponentPaths = []string{
	"github.com/AChWorks/achrix",
	"github.com/AChWorks/achrix/admin",
	"github.com/AChWorks/achrix/audit",
	"github.com/AChWorks/achrix/identity",
	"github.com/AChWorks/achrix/installation",
	"github.com/AChWorks/achrix/media",
	"github.com/AChWorks/achrix/multisite",
}
var updateComponentPaths = append(
	append([]string(nil), publishedComponentPaths...),
	"github.com/AChWorks/achrix/bridge",
	"github.com/AChWorks/achrix/oauth",
)
var componentTagSyntax = regexp.MustCompile(`^v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(-[0-9A-Za-z]+([.-][0-9A-Za-z]+)*)?$`)
var componentPseudoSyntax = regexp.MustCompile(`(-|\.)[0-9]{14}-[0-9a-f]{12}$`)

func ValidComponents(components map[string]string) bool {
	var paths []string
	switch len(components) {
	case len(publishedComponentPaths):
		paths = publishedComponentPaths
	case len(updateComponentPaths):
		paths = updateComponentPaths
	default:
		return false
	}
	for _, path := range paths {
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

func ComponentPaths() []string { return append([]string(nil), updateComponentPaths...) }
