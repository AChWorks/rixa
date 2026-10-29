// SPDX-License-Identifier: MPL-2.0

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestArtifactCLIUnsignedInputIsNotVerified(t *testing.T) {
	dir := t.TempDir()
	archive := filepath.Join(dir, "legacy.tar.gz")
	if err := os.WriteFile(archive, []byte("unsigned legacy archive"), 0o600); err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	err := runArtifact(context.Background(), []string{"verify", "--archive", archive, "--bundle", filepath.Join(dir, "missing.sigstore.json"), "--release", "v0.1.0", "--source-commit", strings.Repeat("a", 40)}, &output)
	if err == nil {
		t.Fatal("unsigned archive accepted")
	}
	var result map[string]any
	if json.Unmarshal(output.Bytes(), &result) != nil || result["status"] != "not-verified" || result["code"] != "attestation-required" {
		t.Fatalf("unsigned CLI result: %s", output.String())
	}
	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) != 1 {
		t.Fatal("artifact verification modified the input directory")
	}
}
