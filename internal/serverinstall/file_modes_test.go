// SPDX-License-Identifier: MPL-2.0
package serverinstall

import (
	"archive/tar"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"testing"

	"github.com/AChWorks/rixa/internal/product"
	"golang.org/x/sys/unix"
)

func assertOwnedMode(t *testing.T, path string, mode os.FileMode) {
	t.Helper()
	i, e := os.Lstat(path)
	if e != nil {
		t.Fatal(e)
	}
	if i.Mode().Perm() != mode || fileUID(i) != uint32(os.Geteuid()) || fileGID(i) != uint32(os.Getegid()) {
		t.Fatalf("%s: expected owned %04o, got %v UID=%d GID=%d", path, mode, i.Mode(), fileUID(i), fileGID(i))
	}
}

func TestUmaskIndependentNativeCreation(t *testing.T) {
	cmd := exec.Command(os.Args[0], "-test.run=^TestUmaskCreationChild$", "-test.v", "--", "native-umask-child")
	if b, e := cmd.CombinedOutput(); e != nil {
		t.Fatalf("isolated umask regression: %v\n%s", e, b)
	} else {
		t.Logf("isolated umask077 subprocess:\n%s", b)
	}
}

func TestUmaskCreationChild(t *testing.T) {
	if !slices.Contains(os.Args, "native-umask-child") {
		t.Skip("helper executes only inside the isolated umask subprocess")
	}
	old := unix.Umask(077)
	defer unix.Umask(old)
	root := t.TempDir()
	for _, d := range []struct {
		name string
		mode os.FileMode
	}{{"manager", 0755}, {"payload", 0755}, {"runtime", 0700}} {
		p := filepath.Join(root, d.name)
		if e := mkdirOwnedNew(p, d.mode, os.Geteuid(), os.Getegid()); e != nil {
			t.Fatal(e)
		}
		assertOwnedMode(t, p, d.mode)
	}
	for _, f := range []struct {
		name string
		mode os.FileMode
	}{{"manager/rixa", 0755}, {"manager/gh", 0755}, {"payload/rixa", 0755}, {"rixa.service", 0644}, {"runtime/config.json", 0600}, {"runtime/transport.env", 0600}} {
		p := filepath.Join(root, f.name)
		if e := writeNew(p, []byte("owned fixture"), f.mode); e != nil {
			t.Fatal(e)
		}
		assertOwnedMode(t, p, f.mode)
	}
	t.Run("missing-first-draft", TestDraftReadMissingStateAllowsFirstSave)

	state := filepath.Join(root, "state")
	if e := privateDir(state, os.Geteuid(), true); e != nil {
		t.Fatal(e)
	}
	assertOwnedMode(t, state, 0700)
	m := New()
	m.stateRoot = state
	m.ownerUID = os.Geteuid()
	if _, e := m.SaveDraft(t.Context(), validDraft()); e != nil {
		t.Fatal(e)
	}
	assertOwnedMode(t, m.statePath(validDraft().OperationID), 0600)
	assertOwnedMode(t, filepath.Join(state, ".lock"), 0600)

	// Never repair adopted/unknown material merely because its mode differs.
	unknown := filepath.Join(root, "unknown")
	if e := os.Mkdir(unknown, 0700); e != nil {
		t.Fatal(e)
	}
	if e := mkdirOwnedNew(unknown, 0755, os.Geteuid(), os.Getegid()); !errors.Is(e, os.ErrExist) {
		t.Fatalf("existing directory adopted: %v", e)
	}
	assertOwnedMode(t, unknown, 0700)
	if e := archiveDirectories(root, unknown); e == nil {
		t.Fatal("unsafe existing extraction directory accepted")
	}
	assertOwnedMode(t, unknown, 0700)
	knownFile := filepath.Join(root, "unknown-file")
	if e := writeNew(knownFile, []byte("original"), 0600); e != nil {
		t.Fatal(e)
	}
	if e := writeNew(knownFile, []byte("replacement"), 0755); e == nil {
		t.Fatal("existing file replaced")
	}
	assertOwnedMode(t, knownFile, 0600)
	b, e := os.ReadFile(knownFile)
	if e != nil || string(b) != "original" {
		t.Fatal("existing contents changed")
	}
	link := filepath.Join(root, "link")
	if e = os.Symlink(unknown, link); e != nil {
		t.Fatal(e)
	}
	if e = mkdirOwnedNew(link, 0755, os.Geteuid(), os.Getegid()); !errors.Is(e, os.ErrExist) {
		t.Fatal("existing link admitted")
	}
	assertOwnedMode(t, unknown, 0700)

	// Exercise the extractor's real creation path; this dummy binary is rejected
	// at compiled identity, while every extracted mode must still be exact.
	d := validDraft()
	info := packageInfo{Rixa: d.Release, AChrix: product.FoundationVersions()["github.com/AChWorks/achrix"], Components: product.FoundationVersions(), Source: d.SourceCommit, Go: "go1.27.3", Target: "linux-amd64"}
	metadata, _ := json.Marshal(info)
	names := []string{"BUILDINFO.json", "rixa", "LICENSE", "INSTALL.md", "THIRD_PARTY_MODULES.txt", "config/rixa.example.json", ".env.example", "licenses/dep-1234/LICENSE"}
	bodies := [][]byte{metadata, []byte("not executable"), []byte("license"), []byte("install"), []byte("example/module\tv1\tdep-1234\n"), []byte("{}"), []byte("env"), []byte("notice")}
	headers := make([]*tar.Header, 0, len(names))
	for i, n := range names {
		headers = append(headers, &tar.Header{Name: "rixa-" + d.Release + "-linux-amd64/" + n, Typeflag: tar.TypeReg, Mode: 0777, Size: int64(len(bodies[i]))})
	}
	archive := filepath.Join(root, "archive.gz")
	if e = writeNew(archive, makeArchive(t, headers, bodies), 0600); e != nil {
		t.Fatal(e)
	}
	out := filepath.Join(root, "extracted")
	if e = mkdirOwnedNew(out, 0755, os.Geteuid(), os.Getegid()); e != nil {
		t.Fatal(e)
	}
	if _, e = extractAuthenticated(archive, out, d); e == nil {
		t.Fatal("dummy binary accepted")
	}
	for _, n := range []string{"config", "licenses", "licenses/dep-1234", "rixa"} {
		assertOwnedMode(t, filepath.Join(out, n), 0755)
	}
	for _, n := range []string{"BUILDINFO.json", "config/rixa.example.json", "licenses/dep-1234/LICENSE"} {
		assertOwnedMode(t, filepath.Join(out, n), 0644)
	}
	// Destination-local private staging and public activation preserve exact
	// modes under the restrictive umask as well.
	t.Run("destination-local-payload", TestPayloadStageIsPrivateOnDestinationFilesystem)
	t.Run("no-replace-activation", TestPayloadActivationNeverReplacesExistingDestination)
	// When supplied by the test operator, authenticate and extract a genuine
	// published payload under the same isolated restrictive umask too.
	if os.Getenv("RIXA_NATIVE_SIGNED_ARCHIVE") != "" {
		t.Run("genuine-published-payload", TestGenuinePublishedPayloadCompatibility)
	}
}
