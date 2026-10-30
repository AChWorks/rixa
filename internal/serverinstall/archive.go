// SPDX-License-Identifier: MPL-2.0
package serverinstall

import (
	"archive/tar"
	"bufio"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"debug/buildinfo"
	"encoding/hex"
	"errors"
	"golang.org/x/sys/unix"
	"io"
	"os"
	"path"
	"path/filepath"
	"reflect"
	"runtime/debug"
	"strings"

	"github.com/AChWorks/rixa/internal/product"
	"github.com/AChWorks/rixa/internal/releaseartifact"
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

type packageInfo struct {
	Rixa       string            `json:"rixa"`
	AChrix     string            `json:"achrix"`
	Components map[string]string `json:"achrix_components"`
	Source     string            `json:"source_commit"`
	Go         string            `json:"go"`
	Target     string            `json:"target"`
}

// The legacy installer retains its exact compiled component-set contract.
func extractAuthenticated(archive, output string, d Draft) (string, error) {
	return extractPackageProfile(archive, output, d, true, nil)
}

// Updates execute the authenticated candidate's own migrate/check commands.
// Its component graph is bound to BUILDINFO and Go's compiled dependency table,
// rather than incidentally requiring the manager's selected version values.
func extractUpdateAuthenticated(archive, output string, d Draft) (string, map[string]string, error) {
	var metadata packageInfo
	binary, e := extractPackageProfile(archive, output, d, false, &metadata)
	if e != nil {
		return "", nil, e
	}
	return binary, metadata.Components, nil
}
func extractPackageProfile(archive, output string, d Draft, currentComponents bool, authenticated *packageInfo) (string, error) {
	f, e := os.Open(archive)
	if e != nil {
		return "", ErrState
	}
	defer f.Close()
	buffered := bufio.NewReader(f)
	gz, e := gzip.NewReader(buffered)
	if e != nil {
		return "", ErrInput
	}
	defer gz.Close()
	gz.Multistream(false)
	tr := tar.NewReader(gz)
	prefix := "rixa-" + d.Release + "-linux-amd64"
	seen := map[string]bool{}
	var total int64
	for count := 0; ; count++ {
		h, e := tr.Next()
		if e == io.EOF {
			break
		}
		if e != nil || count >= 256 {
			return "", ErrInput
		}
		name := strings.TrimSuffix(h.Name, "/")
		if name == "" || path.Clean(name) != name || strings.Contains(name, "\\") || name != prefix && !strings.HasPrefix(name, prefix+"/") || seen[name] || h.Size < 0 || h.Size > 64<<20 {
			return "", ErrInput
		}
		if h.Format == tar.FormatPAX || len(h.PAXRecords) != 0 {
			return "", ErrInput
		}
		seen[name] = true
		rel := strings.TrimPrefix(strings.TrimPrefix(name, prefix), "/")
		if rel == "" {
			if h.Typeflag != tar.TypeDir {
				return "", ErrInput
			}
			continue
		}
		target := filepath.Join(output, filepath.FromSlash(rel))
		if !strings.HasPrefix(target, output+string(filepath.Separator)) {
			return "", ErrInput
		}
		switch h.Typeflag {
		case tar.TypeDir:
			if h.Size != 0 {
				return "", ErrInput
			}
			if e = archiveDirectories(output, target); e != nil {
				return "", ErrState
			}
		case tar.TypeReg, tar.TypeRegA:
			total += h.Size
			if total > 256<<20 {
				return "", ErrInput
			}
			if e = archiveDirectories(output, filepath.Dir(target)); e != nil {
				return "", ErrState
			}
			mode := os.FileMode(0644)
			if rel == "rixa" {
				mode = 0755
			}
			out, e := os.OpenFile(target, os.O_CREATE|os.O_EXCL|os.O_WRONLY|unix.O_NOFOLLOW, 0600)
			if e != nil {
				return "", ErrState
			}
			if e = out.Chmod(mode); e != nil {
				_ = out.Close()
				return "", ErrState
			}
			n, ce := io.CopyN(out, tr, h.Size)
			if ce == nil {
				ce = out.Sync()
			}
			ce = errors.Join(ce, out.Close())
			if ce != nil || n != h.Size {
				return "", ErrState
			}
		default:
			return "", ErrInput // links, devices, FIFO and sparse entries rejected
		}
	}
	// A current package has one gzip member and only zero tar block padding.
	padding, e := io.ReadAll(io.LimitReader(gz, (1<<20)+1))
	if e != nil || len(padding) > 1<<20 {
		return "", ErrInput
	}
	for _, b := range padding {
		if b != 0 {
			return "", ErrInput
		}
	}
	if _, e = buffered.Peek(1); e != io.EOF {
		return "", ErrInput
	}

	data, e := os.ReadFile(filepath.Join(output, "BUILDINFO.json"))
	if e != nil || len(data) > 64<<10 {
		return "", ErrInput
	}
	var info packageInfo
	if e = decode(data, &info); e != nil || !validPackageIdentity(info, d, currentComponents) {
		return "", ErrInput
	}
	for _, file := range []string{"rixa", "LICENSE", "INSTALL.md", "THIRD_PARTY_MODULES.txt", "config/rixa.example.json", ".env.example"} {
		i, e := os.Lstat(filepath.Join(output, file))
		if e != nil || !i.Mode().IsRegular() || i.Size() < 1 {
			return "", ErrInput
		}
	}
	// Legal files are kept with the installed payload, rather than extracting only
	// a binary and silently dropping third-party notices.
	list, e := os.ReadFile(filepath.Join(output, "THIRD_PARTY_MODULES.txt"))
	if e != nil || len(list) > 64<<10 {
		return "", ErrInput
	}
	for _, line := range strings.Split(strings.TrimSpace(string(list)), "\n") {
		fields := strings.Split(line, "\t")
		if len(fields) != 3 || !strings.HasPrefix(fields[2], "dep-") || strings.ContainsAny(fields[2], "/\\.") {
			return "", ErrInput
		}
		files, e := os.ReadDir(filepath.Join(output, "licenses", fields[2]))
		if e != nil || len(files) == 0 {
			return "", ErrInput
		}
	}
	if e = validateCompiledProfile(filepath.Join(output, "rixa"), info, !currentComponents); e != nil {
		return "", e
	}
	if authenticated != nil {
		*authenticated = info
	}
	binary, e := os.ReadFile(filepath.Join(output, "rixa"))
	if e != nil {
		return "", ErrState
	}
	return digest(binary), nil
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

func validateCompiled(path string, metadata packageInfo) error {
	return validateCompiledProfile(path, metadata, false)
}
func validateCompiledProfile(path string, metadata packageInfo, allComponents bool) error {
	info, e := buildinfo.ReadFile(path)
	if e != nil {
		return ErrInput
	}
	return validateCompiledInfo(info, metadata, allComponents)
}
func validateCompiledInfo(info *debug.BuildInfo, metadata packageInfo, allComponents bool) error {
	if allComponents && !validUpdateComponents(metadata.Components) {
		return ErrInput
	}
	if info == nil || (allComponents && info.Main.Replace != nil) || info.Path != "github.com/AChWorks/rixa/cmd/rixa" || info.Main.Path != "github.com/AChWorks/rixa" || info.GoVersion != metadata.Go {
		return ErrInput
	}
	settings := map[string]string{}
	for _, s := range info.Settings {
		settings[s.Key] = s.Value
	}
	if settings["GOOS"] != "linux" || settings["GOARCH"] != "amd64" || settings["CGO_ENABLED"] != "0" {
		return ErrInput
	}
	observed := map[string]string{}
	for _, dep := range info.Deps {
		if dep.Replace != nil {
			return ErrInput
		}
		if _, wanted := metadata.Components[dep.Path]; wanted {
			if _, duplicate := observed[dep.Path]; duplicate {
				return ErrInput
			}
			observed[dep.Path] = dep.Version
		} else if allComponents && (dep.Path == "github.com/AChWorks/achrix" || strings.HasPrefix(dep.Path, "github.com/AChWorks/achrix/")) {
			return ErrInput
		}
	}
	if !reflect.DeepEqual(observed, metadata.Components) {
		return ErrInput
	}
	return nil
}

func validPackageIdentity(info packageInfo, d Draft, currentComponents bool) bool {
	if info.Rixa != d.Release || info.Source != d.SourceCommit || info.Target != "linux-amd64" || info.AChrix != info.Components["github.com/AChWorks/achrix"] {
		return false
	}
	if currentComponents {
		return reflect.DeepEqual(info.Components, product.FoundationVersions())
	}
	return validUpdateComponents(info.Components)
}
