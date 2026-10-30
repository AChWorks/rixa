// SPDX-License-Identifier: MPL-2.0

// Package releasepackage validates and extracts an already authenticated Rixa
// package. Callers must authenticate protected archive bytes before calling;
// successful parsing grants no authority to install, update or execute them.
package releasepackage

import (
	"archive/tar"
	"bufio"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"debug/buildinfo"
	"encoding/hex"
	"encoding/json"
	"errors"
	"golang.org/x/sys/unix"
	"io"
	"os"
	"path"
	"path/filepath"
	"reflect"
	"runtime/debug"
	"strings"
)

var ErrInput = errors.New("invalid authenticated Rixa package")
var ErrState = errors.New("authenticated package staging requires inspection")

// Components preserves the legacy exact-manager component contract when set.
// A nil value requires the complete bounded published component graph instead.
type Expected struct {
	Release      string
	SourceCommit string
	Components   map[string]string
}
type Result struct {
	Metadata     Metadata
	BinarySHA256 string
}

func Extract(archive, output string, expected Expected) (Result, error) {
	var metadata Metadata
	binary, err := extract(archive, output, expected, &metadata)
	return Result{Metadata: metadata, BinarySHA256: binary}, err
}
func digest(b []byte) string { h := sha256.Sum256(b); return hex.EncodeToString(h[:]) }
func decode(b []byte, v any) error {
	if !uniqueJSON(b) {
		return ErrInput
	}
	d := json.NewDecoder(bytes.NewReader(b))
	d.DisallowUnknownFields()
	if d.Decode(v) != nil || d.Decode(new(any)) != io.EOF {
		return ErrInput
	}
	return nil
}

type Metadata struct {
	Rixa       string            `json:"rixa"`
	AChrix     string            `json:"achrix"`
	Components map[string]string `json:"achrix_components"`
	Source     string            `json:"source_commit"`
	Go         string            `json:"go"`
	Target     string            `json:"target"`
}

func extract(archive, output string, expected Expected, authenticated *Metadata) (string, error) {
	currentComponents := expected.Components != nil
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
	prefix := "rixa-" + expected.Release + "-linux-amd64"
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
	var info Metadata
	if e = decode(data, &info); e != nil || !ValidIdentity(info, expected) {
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
	if e = ValidateCompiled(filepath.Join(output, "rixa"), info, !currentComponents); e != nil {
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
func ValidateCompiled(path string, metadata Metadata, allComponents bool) error {
	info, e := buildinfo.ReadFile(path)
	if e != nil {
		return ErrInput
	}
	return ValidateCompiledInfo(info, metadata, allComponents)
}
func ValidateCompiledInfo(info *debug.BuildInfo, metadata Metadata, allComponents bool) error {
	if allComponents && !ValidComponents(metadata.Components) {
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

func ValidIdentity(info Metadata, expected Expected) bool {
	if info.Rixa != expected.Release || info.Source != expected.SourceCommit || info.Target != "linux-amd64" || info.AChrix != info.Components["github.com/AChWorks/achrix"] {
		return false
	}
	if expected.Components != nil {
		return reflect.DeepEqual(info.Components, expected.Components)
	}
	return ValidComponents(info.Components)
}
func uniqueJSON(data []byte) bool {
	d := json.NewDecoder(bytes.NewReader(data))
	d.UseNumber()
	var walk func(int) bool
	walk = func(depth int) bool {
		if depth > 32 {
			return false
		}
		t, e := d.Token()
		if e != nil {
			return false
		}
		delim, ok := t.(json.Delim)
		if !ok {
			return true
		}
		switch delim {
		case '{':
			seen := map[string]bool{}
			for d.More() {
				key, e := d.Token()
				s, ok := key.(string)
				if e != nil || !ok || seen[s] {
					return false
				}
				seen[s] = true
				if !walk(depth + 1) {
					return false
				}
			}
			end, e := d.Token()
			return e == nil && end == json.Delim('}')
		case '[':
			for d.More() {
				if !walk(depth + 1) {
					return false
				}
			}
			end, e := d.Token()
			return e == nil && end == json.Delim(']')
		default:
			return false
		}
	}
	if !walk(0) {
		return false
	}
	_, e := d.Token()
	return e == io.EOF
}
