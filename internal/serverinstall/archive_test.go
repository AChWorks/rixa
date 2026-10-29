// SPDX-License-Identifier: MPL-2.0
package serverinstall

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/AChWorks/rixa/internal/product"
	"github.com/AChWorks/rixa/internal/releaseartifact"
)

func makeArchive(t *testing.T, entries []*tar.Header, bodies [][]byte) []byte {
	t.Helper()
	var buffer bytes.Buffer
	gz := gzip.NewWriter(&buffer)
	tw := tar.NewWriter(gz)
	for i, h := range entries {
		if e := tw.WriteHeader(h); e != nil {
			t.Fatal(e)
		}
		if len(bodies) > i && len(bodies[i]) > 0 {
			if _, e := tw.Write(bodies[i]); e != nil {
				t.Fatal(e)
			}
		}
	}
	if e := tw.Close(); e != nil {
		t.Fatal(e)
	}
	if e := gz.Close(); e != nil {
		t.Fatal(e)
	}
	return buffer.Bytes()
}
func extractFixture(t *testing.T, b []byte) error {
	t.Helper()
	dir := t.TempDir()
	archive := filepath.Join(dir, "archive.gz")
	if e := os.WriteFile(archive, b, 0600); e != nil {
		t.Fatal(e)
	}
	output := filepath.Join(dir, "out")
	if e := os.Mkdir(output, 0700); e != nil {
		t.Fatal(e)
	}
	_, e := extractAuthenticated(archive, output, validDraft())
	return e
}
func TestArchiveRejectsTraversalLinksSpecialDuplicatesAndSparse(t *testing.T) {
	prefix := "rixa-v0.2.0-linux-amd64/"
	for _, tc := range []struct {
		name    string
		headers []*tar.Header
		bodies  [][]byte
	}{
		{"traversal", []*tar.Header{{Name: prefix + "../outside", Mode: 0644, Typeflag: tar.TypeReg, Size: 1}}, [][]byte{[]byte("x")}},
		{"absolute", []*tar.Header{{Name: "/etc/passwd", Mode: 0644, Typeflag: tar.TypeReg, Size: 1}}, [][]byte{[]byte("x")}},
		{"symlink", []*tar.Header{{Name: prefix + "rixa", Typeflag: tar.TypeSymlink, Linkname: "/etc/passwd"}}, nil},
		{"hardlink", []*tar.Header{{Name: prefix + "rixa", Typeflag: tar.TypeLink, Linkname: "/etc/passwd"}}, nil},
		{"fifo", []*tar.Header{{Name: prefix + "rixa", Typeflag: tar.TypeFifo}}, nil},
		{"duplicate", []*tar.Header{{Name: prefix + "rixa", Mode: 0755, Typeflag: tar.TypeReg, Size: 1}, {Name: prefix + "rixa", Mode: 0755, Typeflag: tar.TypeReg, Size: 1}}, [][]byte{[]byte("x"), []byte("y")}},
		{"pax-sparse", []*tar.Header{{Name: prefix + "rixa", Mode: 0755, Typeflag: tar.TypeReg, Size: 1, Format: tar.FormatPAX, PAXRecords: map[string]string{"GNU.sparse.realsize": "4096"}}}, [][]byte{[]byte("x")}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if e := extractFixture(t, makeArchive(t, tc.headers, tc.bodies)); e == nil {
				t.Fatal("unsafe archive accepted")
			}
		})
	}
}
func TestArchiveRejectsExtraGzipMemberAndDamagedTrailer(t *testing.T) {
	b := makeArchive(t, nil, nil)
	joined := append(append([]byte{}, b...), b...)
	if e := extractFixture(t, joined); e == nil {
		t.Fatal("concatenated archive accepted")
	}
	damaged := append([]byte{}, b...)
	damaged[len(damaged)-4] ^= 0xff
	if e := extractFixture(t, damaged); e == nil {
		t.Fatal("damaged gzip trailer accepted")
	}
}
func TestArchiveMetadataIsNotCompiledIdentity(t *testing.T) {
	d := validDraft()
	info := packageInfo{Rixa: d.Release, AChrix: product.FoundationVersions()["github.com/AChWorks/achrix"], Components: product.FoundationVersions(), Source: d.SourceCommit, Go: "go1.27.3", Target: "linux-amd64"}
	b, _ := json.Marshal(info)
	names := []string{"BUILDINFO.json", "rixa", "LICENSE", "INSTALL.md", "THIRD_PARTY_MODULES.txt", "config/rixa.example.json", ".env.example", "licenses/dep-1234/LICENSE"}
	bodies := [][]byte{b, []byte("not a Go executable"), []byte("license"), []byte("install"), []byte("example/module\tv1\tdep-1234\n"), []byte("{}"), []byte("env"), []byte("notice")}
	headers := make([]*tar.Header, 0, len(names))
	for i, n := range names {
		headers = append(headers, &tar.Header{Name: "rixa-" + d.Release + "-linux-amd64/" + n, Typeflag: tar.TypeReg, Mode: 0644, Size: int64(len(bodies[i]))})
	}
	if e := extractFixture(t, makeArchive(t, headers, bodies)); e == nil {
		t.Fatal("metadata text substituted for actual compiled Go identity")
	}
}
func TestHashFileRejectsLinksAndOversizedInputs(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "file")
	if e := os.WriteFile(file, []byte("exact bytes"), 0600); e != nil {
		t.Fatal(e)
	}
	h, e := hashFile(file)
	if e != nil || h != digest([]byte("exact bytes")) {
		t.Fatal("bounded hash mismatch")
	}
	link := filepath.Join(dir, "link")
	_ = os.Symlink(file, link)
	if _, e = hashFile(link); e == nil {
		t.Fatal("hash followed symlink")
	}
	f, e := os.Create(filepath.Join(dir, "oversized"))
	if e != nil {
		t.Fatal(e)
	}
	_ = f.Truncate((64 << 20) + 1)
	_ = f.Close()
	if _, e = hashFile(filepath.Join(dir, "oversized")); e == nil {
		t.Fatal("oversized file hashed")
	}
}
func TestGenuinePublishedPayloadCompatibility(t *testing.T) {
	archive := os.Getenv("RIXA_NATIVE_SIGNED_ARCHIVE")
	if archive == "" {
		t.Skip("requires an actual published signed package; unit fixtures are not publisher proof")
	}
	d := validDraft()
	d.ArchivePath = archive
	d.BundlePath = os.Getenv("RIXA_NATIVE_SIGNED_BUNDLE")
	d.Release = os.Getenv("RIXA_NATIVE_RELEASE")
	d.SourceCommit = os.Getenv("RIXA_NATIVE_SOURCE")
	v, e := releaseartifact.Verify(context.Background(), releaseartifact.Request{ArchivePath: d.ArchivePath, BundlePath: d.BundlePath, ReleaseTag: d.Release, SourceCommit: d.SourceCommit})
	if e != nil || v.Status != "verified" {
		t.Fatalf("genuine publisher proof failed: code=%s field=%s err=%v", v.Code, v.Field, e)
	}
	out := filepath.Join(t.TempDir(), "release")
	if e = os.Mkdir(out, 0700); e != nil {
		t.Fatal(e)
	}
	hash, e := extractAuthenticated(d.ArchivePath, out, d)
	if e != nil {
		t.Fatalf("authenticated real package compatibility failed: %v", e)
	}
	if len(hash) != 64 {
		t.Fatal("missing compiled executable digest")
	}
	for _, name := range []string{"rixa", "config", "licenses"} {
		assertOwnedMode(t, filepath.Join(out, name), 0755)
	}
	for _, name := range []string{"BUILDINFO.json", "INSTALL.md", "config/rixa.example.json"} {
		assertOwnedMode(t, filepath.Join(out, name), 0644)
	}
	// A source mismatch remains a hard rejection even for genuine signed bytes.
	d.SourceCommit = "ffffffffffffffffffffffffffffffffffffffff"
	wrong := filepath.Join(t.TempDir(), "wrong")
	if e = mkdirOwnedNew(wrong, 0755, os.Geteuid(), os.Getegid()); e != nil {
		t.Fatal(e)
	}
	if _, e = extractAuthenticated(d.ArchivePath, wrong, d); !errors.Is(e, ErrInput) {
		t.Fatalf("wrong source did not fail compatibility: %v", e)
	}
}
