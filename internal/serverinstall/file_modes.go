// SPDX-License-Identifier: MPL-2.0
package serverinstall

import (
	"github.com/AChWorks/rixa/internal/releasepackage"
	"os"
)

func mkdirOwnedNew(path string, mode os.FileMode, uid, gid int) error {
	return packageError(releasepackage.MkdirNew(path, mode, uid, gid))
}
func archiveDirectories(base, target string) error {
	return packageError(releasepackage.Directories(base, target))
}
