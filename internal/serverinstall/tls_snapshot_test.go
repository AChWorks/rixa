// SPDX-License-Identifier: MPL-2.0
package serverinstall

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestTLSSnapshotsRejectChangedSecondInputWithoutPinningDraft(t *testing.T) {
	m := testManager(t)
	j := reviewedJournal(validDraft())
	source := t.TempDir()
	j.Draft.TLSCertPath = filepath.Join(source, "cert")
	j.Draft.TLSKeyPath = filepath.Join(source, "key")
	cert, key := []byte("reviewed certificate"), []byte("reviewed private key")
	if e := writeNew(j.Draft.TLSCertPath, cert, 0600); e != nil {
		t.Fatal(e)
	}
	if e := writeNew(j.Draft.TLSKeyPath, []byte("key changed after review"), 0600); e != nil {
		t.Fatal(e)
	}
	j.CertSHA256, j.KeySHA256 = digest(cert), digest(key)
	if p := m.snapshotTLSInputs(j); len(p) != 1 || p[0].Field != "tls_key" || p[0].Code != "evidence-changed" {
		t.Fatalf("second input drift not diagnosed: %+v", p)
	}
	if _, e := os.Lstat(m.tlsSnapshotPath(j, false)); !os.IsNotExist(e) {
		t.Fatal("first input copied before both were admitted")
	}

	// Model a process/filesystem failure after only the first exclusive copy.
	oldSnapshot := m.tlsSnapshotPath(j, false)
	if e := writeNew(oldSnapshot, cert, 0600); e != nil {
		t.Fatal(e)
	}
	if e := writeNew(m.tlsSnapshotPath(j, true), []byte("unknown existing input"), 0600); e != nil {
		t.Fatal(e)
	}
	if e := os.WriteFile(j.Draft.TLSKeyPath, key, 0600); e != nil {
		t.Fatal(e)
	}
	if p := m.snapshotTLSInputs(j); len(p) != 1 || p[0].Field != "tls_key" || p[0].Code != "snapshot-conflict" {
		t.Fatalf("unknown copy silently admitted: %+v", p)
	}

	// A corrected explicit Review can choose a different pair without replacing
	// either previous copy, even after a partial earlier snapshot attempt.
	cert, key = []byte("next reviewed certificate"), []byte("next reviewed key")
	if e := os.WriteFile(j.Draft.TLSCertPath, cert, 0600); e != nil {
		t.Fatal(e)
	}
	if e := os.WriteFile(j.Draft.TLSKeyPath, key, 0600); e != nil {
		t.Fatal(e)
	}
	j.CertSHA256, j.KeySHA256 = digest(cert), digest(key)
	if p := m.snapshotTLSInputs(j); len(p) != 0 {
		t.Fatalf("corrected pre-freeze draft wedged: %+v", p)
	}
	for _, isKey := range []bool{false, true} {
		assertOwnedMode(t, m.tlsSnapshotPath(j, isKey), 0600)
	}
	b, e := os.ReadFile(oldSnapshot)
	if e != nil || string(b) != "reviewed certificate" {
		t.Fatal("older private snapshot overwritten")
	}
	if p := m.snapshotTLSInputs(j); len(p) != 0 {
		t.Fatalf("identical verified snapshots rejected: %+v", p)
	}
	j.Frozen = strings.Repeat("f", 64)
	if p := m.snapshotTLSInputs(j); len(p) != 1 || p[0].Code != "frozen-operation" {
		t.Fatal("frozen operation accepted snapshot changes")
	}
}
