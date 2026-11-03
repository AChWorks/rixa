// SPDX-License-Identifier: MPL-2.0
package serverinstall

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// The same byte-pinned atomic replacement that applies the real runtime
// configuration is exercised against disposable 0600 files, never on /etc.
func TestAddSiteFileActivationAtomicIdempotentAndFailClosed(t *testing.T) {
	uid, gid := uint32(os.Geteuid()), uint32(os.Getegid())
	dir := t.TempDir()
	file := filepath.Join(dir, "rixa.json")
	old, next := []byte("one-site-private-config"), []byte("two-site-private-config")
	if err := os.WriteFile(file, old, 0600); err != nil {
		t.Fatal(err)
	}
	if err := replaceOwnedExactFile(file, old, next, uid, gid); err != nil {
		t.Fatalf("valid candidate was not atomically installed: %v", err)
	}
	for i := 0; i < 2; i++ {
		if err := replaceOwnedExactFile(file, old, next, uid, gid); err != nil {
			t.Fatalf("verified candidate could not be safely resumed: %v", err)
		}
	}
	actual, err := readPrivate(file, int(uid), 1<<20)
	if err != nil || !bytes.Equal(actual, next) {
		t.Fatalf("incorrect activated bytes: %v", err)
	}
	info, err := os.Lstat(file)
	if err != nil || info.Mode().Perm() != 0600 || fileUID(info) != uid || fileGID(info) != gid {
		t.Fatal("atomic replacement did not preserve private owner/mode")
	}
	if matches, _ := filepath.Glob(filepath.Join(dir, ".rixa-topology-*")); len(matches) != 0 {
		t.Fatal("atomic replacement left an unverified temporary file")
	}
	if err := os.WriteFile(file, []byte("operator-modified"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := replaceOwnedExactFile(file, old, next, uid, gid); !errors.Is(err, ErrState) {
		t.Fatalf("operator drift overwritten or silently adopted: %v", err)
	}
	if b, err := os.ReadFile(file); err != nil || string(b) != "operator-modified" {
		t.Fatal("existing unknown data was modified")
	}
	if err := os.Remove(file); err != nil {
		t.Fatal(err)
	}
	if err := replaceOwnedExactFile(file, old, next, uid, gid); !errors.Is(err, ErrState) {
		t.Fatal("missing private target was implicitly created")
	}
	if err := os.WriteFile(file+"-other", old, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(file+"-other", file); err != nil {
		t.Fatal(err)
	}
	if err := replaceOwnedExactFile(file, old, next, uid, gid); !errors.Is(err, ErrState) {
		t.Fatal("untrusted symlink was followed")
	}
	if b, err := os.ReadFile(file + "-other"); err != nil || !bytes.Equal(b, old) {
		t.Fatal("symlink target data was modified")
	}
	// A test path must never be authorized as a production activation path.
	if err := replaceOwnedExact(file, old, next, uid, gid); !errors.Is(err, ErrState) {
		t.Fatal("production activation admitted an unowned arbitrary path")
	}
}

func TestAddSiteActivationReconcilesCrashBetweenTwoAtomicFiles(t *testing.T) {
	uid, gid := uint32(os.Geteuid()), uint32(os.Getegid())
	dir := t.TempDir()
	config, env := filepath.Join(dir, "rixa.json"), filepath.Join(dir, "service.env")
	oldConfig, newConfig := []byte("one-site-config"), []byte("two-site-config")
	oldEnv, newEnv := []byte("original-db-private\n"), []byte("original-db-private\nsite-b-db-private\n")
	for _, f := range []struct {
		path string
		b    []byte
	}{{config, oldConfig}, {env, oldEnv}} {
		if err := os.WriteFile(f.path, f.b, 0600); err != nil {
			t.Fatal(err)
		}
	}
	// Simulated crash after the environment's durable atomic rename and
	// before the configuration rename. Restart must neither overwrite unknown
	// bytes nor append a duplicate site binding.
	if err := replaceOwnedExactFile(env, oldEnv, newEnv, uid, gid); err != nil {
		t.Fatal(err)
	}
	if b, err := os.ReadFile(config); err != nil || !bytes.Equal(b, oldConfig) {
		t.Fatal("the first rename unexpectedly changed the second file")
	}
	for i := 0; i < 2; i++ {
		if err := replaceOwnedExactFile(env, oldEnv, newEnv, uid, gid); err != nil {
			t.Fatal(err)
		}
		if err := replaceOwnedExactFile(config, oldConfig, newConfig, uid, gid); err != nil {
			t.Fatal(err)
		}
	}
	for _, f := range []struct {
		path string
		want []byte
	}{{config, newConfig}, {env, newEnv}} {
		actual, err := readPrivate(f.path, int(uid), 1<<20)
		if err != nil || !bytes.Equal(actual, f.want) {
			t.Fatalf("interrupted two-file continuation did not converge: %s %v", f.path, err)
		}
	}
}
