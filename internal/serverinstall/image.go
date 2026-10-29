// SPDX-License-Identifier: MPL-2.0
package serverinstall

import (
	"io"
	"os"
	"path/filepath"
)

// currentManagerImage reads the kernel's actual executing image. A pathname
// replacement is stale even when the old menu is still alive.
func currentManagerImage() ([]byte, error) {
	path, e := os.Executable()
	if e != nil {
		return nil, ErrState
	}
	path, e = filepath.EvalSymlinks(path)
	if e != nil {
		return nil, ErrManagerChanged
	}
	if !trustedExecutable(path) {
		return nil, ErrAuthority
	}
	f, e := os.Open("/proc/self/exe")
	if e != nil {
		return nil, ErrState
	}
	defer f.Close()
	return readBoundImage(f, path)
}
func readBoundImage(image *os.File, path string) ([]byte, error) {
	actual, e := image.Stat()
	if e != nil || !actual.Mode().IsRegular() || actual.Size() < 1 || actual.Size() > 64<<20 {
		return nil, ErrState
	}
	named, e := os.Lstat(path)
	if e != nil || !named.Mode().IsRegular() || !os.SameFile(actual, named) {
		return nil, ErrManagerChanged
	}
	data, e := io.ReadAll(io.LimitReader(image, (64<<20)+1))
	after, ae := os.Lstat(path)
	imageAfter, ie := image.Stat()
	if e != nil || ae != nil || ie != nil || int64(len(data)) != actual.Size() || len(data) > 64<<20 || !after.Mode().IsRegular() || !os.SameFile(actual, after) || !os.SameFile(actual, imageAfter) || !actual.ModTime().Equal(imageAfter.ModTime()) || !actual.ModTime().Equal(after.ModTime()) {
		return nil, ErrState
	}
	return data, nil
}
