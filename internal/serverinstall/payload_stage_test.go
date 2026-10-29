// SPDX-License-Identifier: MPL-2.0
package serverinstall

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/sys/unix"
)

func TestPayloadStageIsPrivateOnDestinationFilesystem(t *testing.T) {
	root := t.TempDir()
	management := filepath.Join(root, "management")
	releases := filepath.Join(root, "releases")
	if e := mkdirOwnedNew(management, 0700, os.Geteuid(), os.Getegid()); e != nil {
		t.Fatal(e)
	}
	if e := mkdirOwnedNew(releases, 0755, os.Geteuid(), os.Getegid()); e != nil {
		t.Fatal(e)
	}
	out, e := newPayloadStage(releases)
	if e != nil {
		t.Fatal(e)
	}
	defer os.RemoveAll(filepath.Dir(out))
	if filepath.Dir(filepath.Dir(out)) != releases || strings.HasPrefix(out, management+string(filepath.Separator)) {
		t.Fatal("payload staging follows private management storage instead of destination")
	}
	assertOwnedMode(t, filepath.Dir(out), 0700)
	assertOwnedMode(t, out, 0755)
	var sourceStat, destStat unix.Stat_t
	if e = unix.Stat(out, &sourceStat); e != nil {
		t.Fatal(e)
	}
	if e = unix.Stat(releases, &destStat); e != nil {
		t.Fatal(e)
	}
	if sourceStat.Dev != destStat.Dev {
		t.Fatal("activation would cross filesystems")
	}
	if e = writeNew(filepath.Join(out, "rixa"), []byte("authenticated payload fixture"), 0755); e != nil {
		t.Fatal(e)
	}
	sha := strings.Repeat("a", 64)
	if e = activatePayloadStage(out, releases, sha); e != nil {
		t.Fatal(e)
	}
	assertOwnedMode(t, filepath.Join(releases, sha), 0755)
	assertOwnedMode(t, filepath.Join(releases, sha, "rixa"), 0755)
	if _, e = os.Lstat(out); !os.IsNotExist(e) {
		t.Fatal("source remained after activation")
	}
	// Private verification storage has no activation dependency or payload.
	files, e := os.ReadDir(management)
	if e != nil || len(files) != 0 {
		t.Fatal("payload was staged in management storage")
	}
}

func TestPayloadActivationNeverReplacesExistingDestination(t *testing.T) {
	for _, kind := range []string{"empty-directory", "file", "symlink"} {
		t.Run(kind, func(t *testing.T) {
			releases := filepath.Join(t.TempDir(), "releases")
			if e := mkdirOwnedNew(releases, 0755, os.Geteuid(), os.Getegid()); e != nil {
				t.Fatal(e)
			}
			out, e := newPayloadStage(releases)
			if e != nil {
				t.Fatal(e)
			}
			defer os.RemoveAll(filepath.Dir(out))
			if e = writeNew(filepath.Join(out, "rixa"), []byte("candidate"), 0755); e != nil {
				t.Fatal(e)
			}
			dest := filepath.Join(releases, strings.Repeat("b", 64))
			switch kind {
			case "empty-directory":
				e = mkdirOwnedNew(dest, 0755, os.Geteuid(), os.Getegid())
			case "file":
				e = writeNew(dest, []byte("unrelated"), 0600)
			case "symlink":
				e = os.Symlink(out, dest)
			}
			if e != nil {
				t.Fatal(e)
			}
			before, e := os.Lstat(dest)
			if e != nil {
				t.Fatal(e)
			}
			if e = activatePayloadStage(out, releases, filepath.Base(dest)); !errors.Is(e, unix.EEXIST) {
				t.Fatalf("no-replace conflict was not preserved: %v", e)
			}
			after, e := os.Lstat(dest)
			if e != nil || !os.SameFile(before, after) || before.Mode() != after.Mode() {
				t.Fatal("unknown destination changed")
			}
			b, e := os.ReadFile(filepath.Join(out, "rixa"))
			if e != nil || string(b) != "candidate" {
				t.Fatal("failed activation consumed private candidate")
			}
		})
	}
}

func TestPayloadStageRefusesUnsafeOrUnrelatedPaths(t *testing.T) {
	root := t.TempDir()
	unsafe := filepath.Join(root, "unsafe")
	if e := mkdirOwnedNew(unsafe, 0700, os.Geteuid(), os.Getegid()); e != nil {
		t.Fatal(e)
	}
	if _, e := newPayloadStage(unsafe); e == nil {
		t.Fatal("unexpected destination mode accepted")
	}
	assertOwnedMode(t, unsafe, 0700)
	releases := filepath.Join(root, "releases")
	if e := mkdirOwnedNew(releases, 0755, os.Geteuid(), os.Getegid()); e != nil {
		t.Fatal(e)
	}
	link := filepath.Join(root, "link")
	if e := os.Symlink(releases, link); e != nil {
		t.Fatal(e)
	}
	if _, e := newPayloadStage(link); e == nil {
		t.Fatal("symlink staging parent accepted")
	}
	unrelated := filepath.Join(root, "management", "release")
	if e := activatePayloadStage(unrelated, releases, strings.Repeat("c", 64)); e == nil {
		t.Fatal("non-destination staging accepted")
	}
}
