// SPDX-License-Identifier: MPL-2.0
package serverinstall

import (
	"github.com/AChWorks/rixa/internal/releasepackage"
	"reflect"
)

var updateComponentPaths = releasepackage.ComponentPaths()

func validUpdateComponents(components map[string]string) bool {
	return releasepackage.ValidComponents(components)
}

// A signed candidate must report the exact number of sites in the retained
// topology and the matching single/multisite runtime. Authenticating its
// component graph alone cannot authorize a one-site readiness receipt for
// an independently committed second site.
func validCandidateCheckReceipt(status string, sites int, multiSite bool, expectedSites int, core string, observed, authenticated map[string]string) bool {
	return (expectedSites == 1 || expectedSites == 2) &&
		status == "ok" && sites == expectedSites && multiSite == (expectedSites == 2) &&
		validUpdateComponents(authenticated) &&
		core == authenticated["github.com/AChWorks/achrix"] &&
		reflect.DeepEqual(observed, authenticated)
}
