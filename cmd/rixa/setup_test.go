// SPDX-License-Identifier: MPL-2.0

package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestSetupCLIRejectsPrivateInvalidInputWithoutEffects(t *testing.T) {
	dir := t.TempDir()
	if err := os.Chmod(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(dir, "rixa.json")
	const retained = "retained configuration"
	if err := os.WriteFile(target, []byte(retained), 0o600); err != nil {
		t.Fatal(err)
	}
	source := filepath.Join(t.TempDir(), "candidate.json")
	const secret = "never-print-this-private-value"
	if err := os.WriteFile(source, []byte(`{"password":"`+secret+`"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	err = runSetup(context.Background(), []string{"apply", "--source", source, "--target", target, "--operation", "input-correction"}, func(string) (string, bool) { return "", false }, &output)
	if err == nil {
		t.Fatal("invalid configuration accepted")
	}
	if strings.Contains(output.String()+err.Error(), secret) {
		t.Fatal("private invalid input leaked")
	}
	data, readErr := os.ReadFile(target)
	if readErr != nil || string(data) != retained {
		t.Fatal("invalid setup altered retained configuration")
	}
	after, readErr := os.ReadDir(dir)
	if readErr != nil || len(after) != len(before) {
		t.Fatal("invalid setup created operation material")
	}
}

func TestSetupCLISourceCannotWaitOnFIFO(t *testing.T) {
	path := filepath.Join(t.TempDir(), "candidate")
	if err := syscall.Mkfifo(path, 0o600); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { _, err := readSetupSource(path); done <- err }()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("FIFO accepted as a configuration source")
		}
	case <-time.After(time.Second):
		t.Fatal("configuration source blocked on FIFO")
	}
}
