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

func TestArtifactFetchCLICanceledReturnsTruthfulReceipt(t *testing.T) {
	parent := t.TempDir()
	if err := os.Chmod(parent, 0o700); err != nil {
		t.Fatal(err)
	}
	outputDir := filepath.Join(parent, "new-download")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	var output bytes.Buffer
	err := runArtifact(ctx, []string{"fetch", "--release", "v0.2.0", "--output", outputDir}, &output)
	if err == nil {
		t.Fatal("canceled fetch succeeded")
	}
	var result map[string]any
	if json.Unmarshal(output.Bytes(), &result) != nil || result["schema"] != "rixa.artifact-fetch.v1" || result["status"] != "not-verified" || result["code"] != "canceled" {
		t.Fatalf("fetch CLI result: %s", output.String())
	}
	if _, err := os.Stat(outputDir); !os.IsNotExist(err) {
		t.Fatal("canceled fetch created output")
	}
}
func TestArtifactFetchCLIRejectsAmbiguousSelection(t *testing.T) {
	var output bytes.Buffer
	err := runArtifact(context.Background(), []string{"fetch", "--release", "v0.2.0", "--channel", "stable", "--output", filepath.Join(t.TempDir(), "new")}, &output)
	if err == nil || !strings.Contains(output.String(), "\"code\":\"invalid-input\"") {
		t.Fatalf("ambiguous selection: %s / %v", output.String(), err)
	}
}
