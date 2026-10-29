// SPDX-License-Identifier: MPL-2.0

package product

import (
	"github.com/AChWorks/achrix"
	"github.com/AChWorks/achrix/admin"
	"github.com/AChWorks/achrix/audit"
	"github.com/AChWorks/achrix/identity"
	"github.com/AChWorks/achrix/media"
	"github.com/AChWorks/achrix/multisite"
)

// Version identifies the built Rixa product artifact. Release packaging may set
// this variable with -ldflags; source/development builds remain explicit.
var Version = "development"

// FoundationVersions identifies every independently versioned AChrix module
// selected by this product build. Core alone cannot identify a compatible set.
// Return a new map so callers cannot change later diagnostics or transfer checks.
func FoundationVersions() map[string]string {
	return map[string]string{
		"github.com/AChWorks/achrix":           achrix.Version(),
		"github.com/AChWorks/achrix/admin":     admin.Version(),
		"github.com/AChWorks/achrix/audit":     audit.Version(),
		"github.com/AChWorks/achrix/identity":  identity.Version(),
		"github.com/AChWorks/achrix/media":     media.Version(),
		"github.com/AChWorks/achrix/multisite": multisite.Version(),
	}
}
