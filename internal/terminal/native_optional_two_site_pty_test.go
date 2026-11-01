// SPDX-License-Identifier: MPL-2.0

package terminal

import "testing"

func TestNativePTYExplicitOptionalTwoSiteEntryPreservesDefaultPath(t *testing.T) {
	p := startPTY(t, "native-fields")
	position := p.await(t, 0, "Configure optional two-site installation")
	// From the default first action, Up lands on Exit; another Up lands on
	// the dedicated two-site choice. The first default choice is unchanged.
	p.send(t, "\x1b[A\x1b[A\r")
	p.await(t, position, "Configure · 5 / 20")
	p.send(t, "\x03")
	receipt := p.finish(t)
	assertNativePTYNoEffectsOrSecrets(t, p, receipt)
}
