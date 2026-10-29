// SPDX-License-Identifier: MPL-2.0
package serverinstall

import (
	"os"
	"path/filepath"
)

// Before any path-based privileged mutation, every existing ancestor must be
// root-owned, non-writable by other principals and free of symlink indirection.
func trustedRootParent(path string) bool {
	if !safePath(path) {
		return false
	}
	for p := filepath.Dir(path); ; p = filepath.Dir(p) {
		i, e := os.Lstat(p)
		if e != nil || !i.IsDir() || i.Mode()&os.ModeSymlink != 0 || fileUID(i) != 0 || i.Mode().Perm()&0022 != 0 {
			return false
		}
		if p == "/" {
			return true
		}
	}
}
