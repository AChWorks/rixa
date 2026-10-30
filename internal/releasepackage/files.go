// SPDX-License-Identifier: MPL-2.0
package releasepackage

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"syscall"

	"golang.org/x/sys/unix"
)

// Only a successful exclusive mkdir permits changing its ownership/mode.
// Existing paths are returned untouched for the caller's strict admission.
func MkdirNew(path string, mode os.FileMode, uid, gid int) error {
	if e := os.Mkdir(path, 0700); e != nil {
		return e
	}
	named, e := os.Lstat(path)
	if e != nil || !named.IsDir() || fileUID(named) != uint32(os.Geteuid()) {
		return ErrState
	}
	fd, e := unix.Open(path, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if e != nil {
		return ErrState
	}
	f := os.NewFile(uintptr(fd), path)
	defer f.Close()
	created, e := f.Stat()
	if e != nil || !os.SameFile(named, created) {
		return ErrState
	}
	if fileUID(created) != uint32(uid) || fileGID(created) != uint32(gid) {
		if e = f.Chown(uid, gid); e != nil {
			return ErrState
		}
	}
	if e = f.Chmod(mode); e != nil {
		return ErrState
	}
	after, e := os.Lstat(path)
	if e != nil || !after.IsDir() || !os.SameFile(created, after) || after.Mode().Perm() != mode || fileUID(after) != uint32(uid) || fileGID(after) != uint32(gid) {
		return ErrState
	}
	return f.Sync()
}
func archiveDirectories(base, target string) error {
	relative, e := filepath.Rel(base, target)
	if e != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return ErrInput
	}
	if relative == "." {
		return nil
	}
	current := base
	for _, part := range strings.Split(relative, string(filepath.Separator)) {
		current = filepath.Join(current, part)
		if e = MkdirNew(current, 0755, os.Geteuid(), os.Getegid()); e != nil && !errors.Is(e, os.ErrExist) {
			return ErrState
		}
		i, e := os.Lstat(current)
		if e != nil || !i.IsDir() || i.Mode().Perm() != 0755 || fileUID(i) != uint32(os.Geteuid()) || fileGID(i) != uint32(os.Getegid()) {
			return ErrState
		}
	}
	return nil
}

func fileUID(i os.FileInfo) uint32 {
	s, ok := i.Sys().(*syscall.Stat_t)
	if !ok {
		return ^uint32(0)
	}
	return s.Uid
}
func fileGID(i os.FileInfo) uint32 {
	s, ok := i.Sys().(*syscall.Stat_t)
	if !ok {
		return ^uint32(0)
	}
	return s.Gid
}
func Directories(base, target string) error { return archiveDirectories(base, target) }
