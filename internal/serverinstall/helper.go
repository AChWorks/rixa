// SPDX-License-Identifier: MPL-2.0
package serverinstall

import (
	"path/filepath"
)

func (m *Manager) helperDir(j *journal) string {
	return filepath.Join(managerRoot, "helpers", j.HelperSHA256)
}
func (m *Manager) snapshotHelper(j *journal) error {
	if !trustedExecutable(j.HelperPath) {
		return ErrAuthority
	}
	data, e := readRegular(j.HelperPath, 64<<20)
	if e != nil || digest(data) != j.HelperSHA256 {
		return ErrState
	}
	for _, p := range []string{managerRoot, managerRoot + "/helpers", m.helperDir(j)} {
		if e = ownedPublicDir(p); e != nil {
			return e
		}
	}
	target := filepath.Join(m.helperDir(j), "gh")
	if e = writeNew(target, data, 0755); e != nil {
		h, he := hashFile(target)
		if he != nil || h != j.HelperSHA256 || !trustedExecutable(target) {
			return ErrState
		}
	}
	return nil
}
