// SPDX-License-Identifier: MPL-2.0
package serverinstall

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"github.com/AChWorks/rixa/internal/product"
	"github.com/AChWorks/rixa/internal/releaseartifact"
	"github.com/AChWorks/rixa/internal/releasepackage"
	"golang.org/x/sys/unix"
	"io"
	"os"
	"path/filepath"
	"runtime/debug"
)

func readRegular(path string, maximum int64) ([]byte, error) {
	f, e := os.OpenFile(path, os.O_RDONLY|unix.O_NONBLOCK|unix.O_NOFOLLOW, 0)
	if e != nil {
		return nil, ErrInput
	}
	defer f.Close()
	before, e := os.Lstat(path)
	if e != nil || !before.Mode().IsRegular() {
		return nil, ErrInput
	}
	info, e := f.Stat()
	if e != nil || !info.Mode().IsRegular() || !os.SameFile(before, info) || info.Size() < 1 || info.Size() > maximum {
		return nil, ErrInput
	}
	b, e := io.ReadAll(io.LimitReader(f, maximum+1))
	after, se := os.Lstat(path)
	if e != nil || se != nil || !os.SameFile(info, after) || int64(len(b)) > maximum || int64(len(b)) != info.Size() || !info.ModTime().Equal(after.ModTime()) {
		return nil, ErrInput
	}
	return b, nil
}
func frozenInputs(ctx context.Context, d Draft, dir string) (releaseartifact.Result, error) {
	// Copy first, then authenticate only the protected copies. No candidate bytes
	// are parsed as tar or executed before genuine publisher authentication.
	archive, e := readRegular(d.ArchivePath, releaseartifact.MaxArchiveBytes)
	if e != nil {
		return releaseartifact.Result{}, e
	}
	bundle, e := readRegular(d.BundlePath, releaseartifact.MaxBundleBytes)
	if e != nil {
		return releaseartifact.Result{}, e
	}
	ap := filepath.Join(dir, "archive.tar.gz")
	bp := filepath.Join(dir, "bundle.json")
	if e = writeNew(ap, archive, 0600); e != nil {
		return releaseartifact.Result{}, e
	}
	if e = writeNew(bp, bundle, 0600); e != nil {
		return releaseartifact.Result{}, e
	}
	return releaseartifact.Verify(ctx, releaseartifact.Request{ArchivePath: ap, BundlePath: bp, ReleaseTag: d.Release, SourceCommit: d.SourceCommit})
}
func writeNew(path string, b []byte, mode os.FileMode) error {
	f, e := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY|unix.O_NOFOLLOW, 0600)
	if e != nil {
		return ErrState
	}
	if e = f.Chmod(mode); e == nil {
		_, e = f.Write(b)
	}
	if e == nil {
		e = f.Sync()
	}
	return errors.Join(e, f.Close())
}

func hashFile(path string) (string, error) {
	f, e := os.OpenFile(path, os.O_RDONLY|unix.O_NONBLOCK|unix.O_NOFOLLOW, 0)
	if e != nil {
		return "", ErrState
	}
	defer f.Close()
	before, e := os.Lstat(path)
	if e != nil || !before.Mode().IsRegular() {
		return "", ErrState
	}
	info, e := f.Stat()
	if e != nil || !os.SameFile(before, info) || info.Size() < 1 || info.Size() > 64<<20 {
		return "", ErrState
	}
	h := sha256.New()
	n, e := io.Copy(h, io.LimitReader(f, (64<<20)+1))
	after, ae := os.Lstat(path)
	current, ce := f.Stat()
	if e != nil || ae != nil || ce != nil || n != info.Size() || n > 64<<20 || !after.Mode().IsRegular() || !os.SameFile(info, after) || !os.SameFile(info, current) || after.Size() != info.Size() || !after.ModTime().Equal(info.ModTime()) || !current.ModTime().Equal(info.ModTime()) {
		return "", ErrState
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

type packageInfo = releasepackage.Metadata

func packageError(e error) error {
	if errors.Is(e, releasepackage.ErrInput) {
		return ErrInput
	}
	if errors.Is(e, releasepackage.ErrState) {
		return ErrState
	}
	return e
}
func packageExpected(d Draft, current bool) releasepackage.Expected {
	e := releasepackage.Expected{Release: d.Release, SourceCommit: d.SourceCommit}
	if current {
		e.Components = product.FoundationVersions()
	}
	return e
}
func extractAuthenticated(archive, output string, d Draft) (string, error) {
	return extractPackageProfile(archive, output, d, true, nil)
}
func extractUpdateAuthenticated(archive, output string, d Draft) (string, map[string]string, error) {
	var metadata packageInfo
	binary, e := extractPackageProfile(archive, output, d, false, &metadata)
	if e != nil {
		return "", nil, e
	}
	return binary, metadata.Components, nil
}
func extractPackageProfile(archive, output string, d Draft, current bool, authenticated *packageInfo) (string, error) {
	r, e := releasepackage.Extract(archive, output, packageExpected(d, current))
	if e != nil {
		return "", packageError(e)
	}
	if authenticated != nil {
		*authenticated = r.Metadata
	}
	return r.BinarySHA256, nil
}
func validateCompiled(path string, metadata packageInfo) error {
	return validateCompiledProfile(path, metadata, false)
}
func validateCompiledProfile(path string, metadata packageInfo, all bool) error {
	return packageError(releasepackage.ValidateCompiled(path, metadata, all))
}
func validateCompiledInfo(info *debug.BuildInfo, metadata packageInfo, all bool) error {
	return packageError(releasepackage.ValidateCompiledInfo(info, metadata, all))
}
func validPackageIdentity(info packageInfo, d Draft, current bool) bool {
	return releasepackage.ValidIdentity(info, packageExpected(d, current))
}
