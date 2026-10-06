// SPDX-License-Identifier: MPL-2.0

package product

import (
	"archive/tar"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSiteTransferArchiveRoundTrip(t *testing.T) {
	base := t.TempDir()
	source := filepath.Join(base, "source")
	if err := os.Mkdir(source, 0o700); err != nil {
		t.Fatal(err)
	}
	nested := filepath.Join(source, "nested")
	if err := os.Mkdir(nested, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(nested, "data.bin"), []byte("site-transfer-proof"), 0o600); err != nil {
		t.Fatal(err)
	}
	archive := filepath.Join(base, "bundle.tar")
	artifact, entries, err := archiveSiteTransferRoot(source, archive)
	if err != nil {
		t.Fatal(err)
	}
	if artifact.Size < 1 || len(artifact.SHA256) != 64 || len(entries) != 2 {
		t.Fatalf("archive evidence=%#v entries=%#v", artifact, entries)
	}
	stage := filepath.Join(base, "stage")
	if err = os.Mkdir(stage, 0o700); err != nil {
		t.Fatal(err)
	}
	if err = extractSiteTransferArchive(archive, stage, entries); err != nil {
		t.Fatal(err)
	}
	got, err := inspectSiteTransferRoot(stage)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != len(entries) || got[0] != entries[0] || got[1] != entries[1] {
		t.Fatalf("round-trip tree=%#v want=%#v", got, entries)
	}
}

func TestSiteTransferArchiveRejectsUnsafeEntries(t *testing.T) {
	for _, test := range []struct {
		name     string
		header   tar.Header
		expected []SiteTransferArchiveEntry
	}{
		{
			name: "traversal",
			header: tar.Header{Name: "../escape", Typeflag: tar.TypeReg, Mode: 0o600, Size: 1},
			expected: []SiteTransferArchiveEntry{{Path: "safe", Type: "file", Mode: 0o600, Size: 1, SHA256: strings.Repeat("0", 64)}},
		},
		{
			name: "symlink",
			header: tar.Header{Name: "safe", Typeflag: tar.TypeSymlink, Linkname: "elsewhere", Mode: 0o600},
			expected: []SiteTransferArchiveEntry{{Path: "safe", Type: "file", Mode: 0o600, Size: 0, SHA256: strings.Repeat("0", 64)}},
		},
		{
			name: "hardlink",
			header: tar.Header{Name: "safe", Typeflag: tar.TypeLink, Linkname: "other", Mode: 0o600},
			expected: []SiteTransferArchiveEntry{{Path: "safe", Type: "file", Mode: 0o600, Size: 0, SHA256: strings.Repeat("0", 64)}},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			base := t.TempDir()
			archive := filepath.Join(base, "unsafe.tar")
			f, err := os.OpenFile(archive, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
			if err != nil {
				t.Fatal(err)
			}
			tw := tar.NewWriter(f)
			if err = tw.WriteHeader(&test.header); err != nil {
				t.Fatal(err)
			}
			if test.header.Size > 0 {
				if _, err = tw.Write([]byte("x")); err != nil {
					t.Fatal(err)
				}
			}
			if err = tw.Close(); err != nil {
				t.Fatal(err)
			}
			if err = f.Close(); err != nil {
				t.Fatal(err)
			}
			stage := filepath.Join(base, "stage")
			if err = os.Mkdir(stage, 0o700); err != nil {
				t.Fatal(err)
			}
			if err = extractSiteTransferArchive(archive, stage, test.expected); !errors.Is(err, ErrSiteTransfer) {
				t.Fatalf("unsafe archive accepted: %v", err)
			}
		})
	}
}

func TestSiteTransferArchiveRejectsIncompleteManifest(t *testing.T) {
	base := t.TempDir()
	archive := filepath.Join(base, "incomplete.tar")
	f, err := os.OpenFile(archive, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	tw := tar.NewWriter(f)
	body := []byte("a")
	if err = tw.WriteHeader(&tar.Header{Name: "a", Typeflag: tar.TypeReg, Mode: 0o600, Size: int64(len(body))}); err != nil {
		t.Fatal(err)
	}
	if _, err = tw.Write(body); err != nil {
		t.Fatal(err)
	}
	if err = tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err = f.Close(); err != nil {
		t.Fatal(err)
	}
	stage := filepath.Join(base, "stage")
	if err = os.Mkdir(stage, 0o700); err != nil {
		t.Fatal(err)
	}
	sumA := "ca978112ca1bbdcafac231b39a23dc4da786eff8147c4e72b9807785afee48bb"
	expected := []SiteTransferArchiveEntry{
		{Path: "a", Type: "file", Mode: 0o600, Size: 1, SHA256: sumA},
		{Path: "b", Type: "file", Mode: 0o600, Size: 1, SHA256: strings.Repeat("0", 64)},
	}
	if err = extractSiteTransferArchive(archive, stage, expected); !errors.Is(err, ErrSiteTransfer) {
		t.Fatalf("incomplete archive accepted: %v", err)
	}
}

func TestSiteTransferPostgresEnvironmentIsLocalAndSecretSafe(t *testing.T) {
	env, database, err := siteTransferPostgresEnvironment("postgres://user:secret@127.0.0.1:5432/site?sslmode=disable")
	if err != nil {
		t.Fatal(err)
	}
	if database != "site" {
		t.Fatalf("database=%q", database)
	}
	joined := strings.Join(env, "\n")
	for _, want := range []string{"PGHOST=127.0.0.1", "PGUSER=user", "PGPASSWORD=secret", "PGDATABASE=site", "PGSSLMODE=disable"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("environment missing %q", want)
		}
	}
	if strings.Contains(joined, "postgres://user:secret") {
		t.Fatal("full DSN leaked into subprocess environment")
	}
	if _, _, err = siteTransferPostgresEnvironment("postgres://user:secret@db.example.com:5432/site?sslmode=require"); !errors.Is(err, ErrSiteTransfer) {
		t.Fatalf("remote transfer profile accepted: %v", err)
	}
}

func TestSiteTransferArchivePathAdmission(t *testing.T) {
	for _, value := range []string{"file", "dir/file", "a-b_c.1"} {
		if !validSiteTransferArchivePath(value) {
			t.Fatalf("valid path rejected: %q", value)
		}
	}
	for _, value := range []string{"", ".", "..", "../x", "/absolute", "dir/../x", "dir\\x", "dir//x"} {
		if validSiteTransferArchivePath(value) {
			t.Fatalf("unsafe path accepted: %q", value)
		}
	}
}
