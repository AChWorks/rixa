// SPDX-License-Identifier: MPL-2.0

package product

import (
	"archive/tar"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"syscall"
)

func archiveSiteTransferRoot(root, output string) (SiteTransferArtifact, []SiteTransferArchiveEntry, error) {
	before, err := inspectSiteTransferRoot(root)
	if err != nil {
		return SiteTransferArtifact{}, nil, err
	}
	file, err := os.OpenFile(output, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return SiteTransferArtifact{}, nil, fmt.Errorf("%w: create transfer archive", ErrSiteTransfer)
	}
	writer := &boundedTransferWriter{writer: file, hash: sha256.New(), max: maxSiteTransferArchiveBytes}
	tw := tar.NewWriter(writer)
	for _, entry := range before {
		header := &tar.Header{
			Name: entry.Path,
			Mode: int64(entry.Mode),
			Size: entry.Size,
			Uid: 0, Gid: 0,
		}
		switch entry.Type {
		case "dir":
			header.Typeflag = tar.TypeDir
			header.Size = 0
		case "file":
			header.Typeflag = tar.TypeReg
		default:
			_ = tw.Close()
			_ = file.Close()
			_ = os.Remove(output)
			return SiteTransferArtifact{}, nil, fmt.Errorf("%w: unsupported archive entry", ErrSiteTransfer)
		}
		if err = tw.WriteHeader(header); err != nil {
			_ = tw.Close()
			_ = file.Close()
			_ = os.Remove(output)
			return SiteTransferArtifact{}, nil, fmt.Errorf("%w: write archive header", ErrSiteTransfer)
		}
		if entry.Type == "file" {
			source := filepath.Join(root, filepath.FromSlash(entry.Path))
			input, openErr := os.Open(source)
			if openErr != nil {
				_ = tw.Close()
				_ = file.Close()
				_ = os.Remove(output)
				return SiteTransferArtifact{}, nil, fmt.Errorf("%w: read archive source", ErrSiteTransfer)
			}
			hash := sha256.New()
			n, copyErr := io.Copy(tw, io.TeeReader(io.LimitReader(input, entry.Size+1), hash))
			closeErr := input.Close()
			if copyErr != nil || closeErr != nil || n != entry.Size || hex.EncodeToString(hash.Sum(nil)) != entry.SHA256 {
				_ = tw.Close()
				_ = file.Close()
				_ = os.Remove(output)
				return SiteTransferArtifact{}, nil, fmt.Errorf("%w: archive source changed during capture", ErrSiteTransfer)
			}
		}
	}
	closeTarErr := tw.Close()
	syncErr := file.Sync()
	closeErr := file.Close()
	if closeTarErr != nil || syncErr != nil || closeErr != nil || writer.err != nil || writer.count < 1 {
		_ = os.Remove(output)
		if writer.err != nil {
			return SiteTransferArtifact{}, nil, writer.err
		}
		return SiteTransferArtifact{}, nil, fmt.Errorf("%w: persist transfer archive", ErrSiteTransfer)
	}
	after, err := inspectSiteTransferRoot(root)
	if err != nil || !reflect.DeepEqual(before, after) {
		_ = os.Remove(output)
		return SiteTransferArtifact{}, nil, fmt.Errorf("%w: private root changed during capture", ErrSiteTransfer)
	}
	return SiteTransferArtifact{
		File: filepath.Base(output), Size: writer.count, SHA256: hex.EncodeToString(writer.hash.Sum(nil)),
	}, before, nil
}

func inspectSiteTransferRoot(root string) ([]SiteTransferArchiveEntry, error) {
	info, err := os.Lstat(root)
	if err != nil || !info.IsDir() || info.Mode().Perm() != 0o700 || info.Mode()&os.ModeSymlink != 0 {
		return nil, fmt.Errorf("%w: private root", ErrSiteTransfer)
	}
	var entries []SiteTransferArchiveEntry
	var total int64
	err = filepath.WalkDir(root, func(path string, d os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if path == root {
			return nil
		}
		if len(entries) >= maxSiteTransferEntries {
			return ErrSiteTransferLimited
		}
		rel, relErr := filepath.Rel(root, path)
		if relErr != nil {
			return relErr
		}
		rel = filepath.ToSlash(rel)
		if !validSiteTransferArchivePath(rel) {
			return ErrSiteTransfer
		}
		current, statErr := os.Lstat(path)
		if statErr != nil || current.Mode()&os.ModeSymlink != 0 {
			return ErrSiteTransfer
		}
		switch {
		case current.IsDir():
			if current.Mode().Perm() != 0o700 {
				return ErrSiteTransfer
			}
			entries = append(entries, SiteTransferArchiveEntry{Path: rel, Type: "dir", Mode: 0o700})
		case current.Mode().IsRegular():
			if current.Mode().Perm() != 0o600 {
				return ErrSiteTransfer
			}
			stat, ok := current.Sys().(*syscall.Stat_t)
			if !ok || stat.Nlink != 1 {
				return ErrSiteTransfer
			}
			if current.Size() < 0 || total > maxSiteTransferArchiveBytes-current.Size() {
				return ErrSiteTransferLimited
			}
			sum, size, hashErr := hashTransferFile(path, current.Size())
			if hashErr != nil || size != current.Size() {
				return ErrSiteTransfer
			}
			total += current.Size()
			entries = append(entries, SiteTransferArchiveEntry{
				Path: rel, Type: "file", Mode: 0o600, Size: current.Size(), SHA256: sum,
			})
		default:
			return ErrSiteTransfer
		}
		return nil
	})
	if err != nil {
		if errors.Is(err, ErrSiteTransferLimited) {
			return nil, ErrSiteTransferLimited
		}
		return nil, fmt.Errorf("%w: inspect private root", ErrSiteTransfer)
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Path < entries[j].Path })
	return entries, nil
}

func extractSiteTransferArchive(archivePath, target string, expected []SiteTransferArchiveEntry) error {
	if len(expected) > maxSiteTransferEntries {
		return ErrSiteTransferLimited
	}
	expectedByPath := make(map[string]SiteTransferArchiveEntry, len(expected))
	for _, entry := range expected {
		if !validSiteTransferArchivePath(entry.Path) || expectedByPath[entry.Path].Path != "" {
			return fmt.Errorf("%w: invalid archive manifest", ErrSiteTransfer)
		}
		expectedByPath[entry.Path] = entry
	}
	rootInfo, err := os.Lstat(target)
	if err != nil || !rootInfo.IsDir() || rootInfo.Mode().Perm() != 0o700 {
		return fmt.Errorf("%w: restore staging root", ErrSiteTransfer)
	}

	file, err := os.Open(archivePath)
	if err != nil {
		return fmt.Errorf("%w: open transfer archive", ErrSiteTransfer)
	}
	defer file.Close()
	tr := tar.NewReader(io.LimitReader(file, maxSiteTransferArchiveBytes+1))
	seen := make(map[string]bool, len(expected))
	for {
		header, nextErr := tr.Next()
		if errors.Is(nextErr, io.EOF) {
			break
		}
		if nextErr != nil {
			return fmt.Errorf("%w: read transfer archive", ErrSiteTransfer)
		}
		if !validSiteTransferArchivePath(header.Name) || seen[header.Name] ||
			header.Linkname != "" || header.Uid != 0 || header.Gid != 0 {
			return fmt.Errorf("%w: unsafe archive entry", ErrSiteTransfer)
		}
		want, ok := expectedByPath[header.Name]
		if !ok {
			return fmt.Errorf("%w: archive entry not present in manifest", ErrSiteTransfer)
		}
		seen[header.Name] = true
		if uint32(header.Mode)&0o777 != want.Mode || header.Size != want.Size {
			return fmt.Errorf("%w: archive entry metadata mismatch", ErrSiteTransfer)
		}
		destination := filepath.Join(target, filepath.FromSlash(header.Name))
		if !pathContains(target, destination) {
			return fmt.Errorf("%w: archive path escapes target", ErrSiteTransfer)
		}
		parent := filepath.Dir(destination)
		if parent != target {
			parentInfo, parentErr := os.Lstat(parent)
			if parentErr != nil || !parentInfo.IsDir() || parentInfo.Mode().Perm() != 0o700 {
				return fmt.Errorf("%w: archive parent order/permissions", ErrSiteTransfer)
			}
		}
		switch want.Type {
		case "dir":
			if header.Typeflag != tar.TypeDir || header.Size != 0 {
				return fmt.Errorf("%w: archive directory type mismatch", ErrSiteTransfer)
			}
			if err = os.Mkdir(destination, 0o700); err != nil {
				return fmt.Errorf("%w: extract archive directory", ErrSiteTransfer)
			}
		case "file":
			if header.Typeflag != tar.TypeReg && header.Typeflag != tar.TypeRegA {
				return fmt.Errorf("%w: archive file type mismatch", ErrSiteTransfer)
			}
			output, createErr := os.OpenFile(destination, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
			if createErr != nil {
				return fmt.Errorf("%w: extract archive file", ErrSiteTransfer)
			}
			hash := sha256.New()
			n, copyErr := io.Copy(io.MultiWriter(output, hash), io.LimitReader(tr, want.Size+1))
			syncErr := output.Sync()
			closeErr := output.Close()
			if copyErr != nil || syncErr != nil || closeErr != nil || n != want.Size ||
				hex.EncodeToString(hash.Sum(nil)) != want.SHA256 {
				return fmt.Errorf("%w: extracted archive file integrity", ErrSiteTransfer)
			}
		default:
			return fmt.Errorf("%w: unsupported archive entry type", ErrSiteTransfer)
		}
	}
	if len(seen) != len(expected) {
		return fmt.Errorf("%w: archive is incomplete", ErrSiteTransfer)
	}
	got, err := inspectSiteTransferRoot(target)
	if err != nil || !reflect.DeepEqual(got, expected) {
		return fmt.Errorf("%w: extracted archive tree mismatch", ErrSiteTransfer)
	}
	return syncTransferDirectory(target)
}

func validSiteTransferArchivePath(path string) bool {
	if path == "" || len(path) > maxSiteTransferPathBytes || strings.Contains(path, "\\") ||
		strings.ContainsRune(path, 0) || strings.HasPrefix(path, "/") {
		return false
	}
	clean := filepath.ToSlash(filepath.Clean(filepath.FromSlash(path)))
	if clean != path || path == "." || path == ".." || strings.HasPrefix(path, "../") {
		return false
	}
	for _, part := range strings.Split(path, "/") {
		if part == "" || part == "." || part == ".." {
			return false
		}
	}
	return true
}
