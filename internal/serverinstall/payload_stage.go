// SPDX-License-Identifier: MPL-2.0
package serverinstall

import (
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/sys/unix"
)

func openPayloadDirectory(path string, mode os.FileMode) (*os.File, error) {
	real, e := filepath.EvalSymlinks(path)
	if e != nil || real != path {
		return nil, ErrState
	}
	fd, e := unix.Open(path, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if e != nil {
		return nil, ErrState
	}
	f := os.NewFile(uintptr(fd), path)
	i, e := f.Stat()
	if e != nil || !i.IsDir() || i.Mode().Perm() != mode || fileUID(i) != uint32(os.Geteuid()) || fileGID(i) != uint32(os.Getegid()) {
		f.Close()
		return nil, ErrState
	}
	return f, nil
}

// Extraction stays behind a private parent on the destination filesystem.
// Authentication of the separate protected archive must have succeeded first.
func newPayloadStage(releases string) (string, error) {
	if os.Geteuid() == 0 && !trustedRootParent(releases) {
		return "", ErrState
	}
	parent, e := openPayloadDirectory(releases, 0755)
	if e != nil {
		return "", e
	}
	defer parent.Close()
	dir, e := os.MkdirTemp(releases, ".payload-")
	if e != nil {
		return "", e
	}
	ok := false
	defer func() {
		if !ok {
			_ = os.RemoveAll(dir)
		}
	}()
	stage, e := openPayloadDirectory(dir, 0700)
	if e != nil {
		return "", e
	}
	if e = stage.Chmod(0700); e == nil {
		e = stage.Sync()
	}
	e2 := stage.Close()
	if e != nil || e2 != nil {
		return "", ErrState
	}
	out := filepath.Join(dir, "release")
	if e = mkdirOwnedNew(out, 0755, os.Geteuid(), os.Getegid()); e != nil {
		return "", e
	}
	ok = true
	return out, nil
}

// A descriptor-relative no-replace rename cannot overwrite even an empty
// destination directory introduced after an earlier absence observation.
func activatePayloadStage(out, releases, archiveSHA string) error {
	stagePath := filepath.Dir(out)
	if !hashSyntax.MatchString(archiveSHA) || filepath.Base(out) != "release" || filepath.Dir(stagePath) != releases || !strings.HasPrefix(filepath.Base(stagePath), ".payload-") {
		return ErrState
	}
	if os.Geteuid() == 0 && !trustedRootParent(releases) {
		return ErrState
	}
	parent, e := openPayloadDirectory(releases, 0755)
	if e != nil {
		return e
	}
	defer parent.Close()
	stage, e := openPayloadDirectory(stagePath, 0700)
	if e != nil {
		return e
	}
	defer stage.Close()
	payload, e := openPayloadDirectory(out, 0755)
	if e != nil {
		return e
	}
	if e = payload.Sync(); e != nil {
		payload.Close()
		return e
	}
	if e = payload.Close(); e != nil {
		return e
	}
	if e = unix.Renameat2(int(stage.Fd()), "release", int(parent.Fd()), archiveSHA, unix.RENAME_NOREPLACE); e != nil {
		return e
	}
	return parent.Sync()
}
