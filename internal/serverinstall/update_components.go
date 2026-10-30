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
func validCandidateCheckReceipt(status string, sites int, core string, observed, authenticated map[string]string) bool {
	return status == "ok" && sites == 1 && validUpdateComponents(authenticated) && core == authenticated["github.com/AChWorks/achrix"] && reflect.DeepEqual(observed, authenticated)
}
