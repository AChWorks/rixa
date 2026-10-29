// SPDX-License-Identifier: MPL-2.0

package releaseartifact

import (
	"context"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

// Each FIFO regression runs in a bounded child process. A blocking open cannot
// leave a test goroutine stuck or stall the remaining package suite.
func TestFetchSpecialReadbackIsBounded(t *testing.T) {
	for _, stage := range []string{"archive", "checksum"} {
		t.Run(stage, func(t *testing.T) {
			self, err := os.Executable()
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			command := exec.CommandContext(ctx, self, "-test.run=^TestFetchSpecialReadbackChild$", "-test.count=1")
			command.Env = append(os.Environ(), "RIXA_FETCH_SPECIAL_CHILD="+stage)
			data, err := command.CombinedOutput()
			if ctx.Err() != nil {
				t.Fatalf("special-file readback blocked: %s", data)
			}
			if err != nil {
				t.Fatalf("special-file child failed: %v\n%s", err, data)
			}
		})
	}
}

func TestFetchSpecialReadbackChild(t *testing.T) {
	stage := os.Getenv("RIXA_FETCH_SPECIAL_CHILD")
	if stage == "" {
		t.Skip("bounded parent selects the special-file stage")
	}
	f := newFetchFixture(t)
	output := privateFetchOutput(t)
	makeFIFO := func(path string) {
		if err := os.Remove(path); err != nil {
			t.Fatal(err)
		}
		if err := syscall.Mkfifo(path, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	verifier := f.verify
	switch stage {
	case "archive":
		verifier = func(ctx context.Context, r Request) (Result, error) {
			result, err := f.verify(ctx, r)
			makeFIFO(r.ArchivePath)
			return result, err
		}
	case "checksum":
		f.override = func(r *http.Request) (*http.Response, error) {
			if r.URL.String() == f.release.Assets[2].DownloadURL {
				makeFIFO(filepath.Join(output, f.release.Assets[1].Name))
			}
			return nil, nil
		}
	default:
		t.Fatalf("unexpected stage %q", stage)
	}
	result, err := fetch(context.Background(), FetchRequest{OutputDir: output, ReleaseTag: "v1.2.3"}, f.client(), verifier)
	if err == nil || result.Status == "verified" || result.ArchivePath != "" {
		t.Fatalf("special input accepted: %#v %v", result, err)
	}
	want := "output-changed"
	if stage == "checksum" {
		want = "checksum-mismatch"
	}
	if result.Code != want {
		t.Fatalf("special input reason %q, want %q", result.Code, want)
	}
}

func TestFetchRejectsUnsafeReadbackPermissions(t *testing.T) {
	f := newFetchFixture(t)
	output := privateFetchOutput(t)
	verifier := func(ctx context.Context, r Request) (Result, error) {
		result, err := f.verify(ctx, r)
		if err := os.Chmod(r.ArchivePath, 0o666); err != nil {
			t.Fatal(err)
		}
		return result, err
	}
	result, err := fetch(context.Background(), FetchRequest{OutputDir: output, ReleaseTag: "v1.2.3"}, f.client(), verifier)
	if err == nil || result.Code != "output-changed" || result.Status == "verified" {
		t.Fatalf("unsafe readback mode accepted: %#v %v", result, err)
	}
}

func TestFetchFinalizationDoesNotReplaceUnexpectedName(t *testing.T) {
	f := newFetchFixture(t)
	output := privateFetchOutput(t)
	final := filepath.Join(output, f.release.Assets[0].Name)
	marker := []byte("introduced final must remain untouched")
	f.override = func(r *http.Request) (*http.Response, error) {
		if r.URL.String() == f.release.Assets[0].DownloadURL {
			if err := os.WriteFile(final, marker, 0o600); err != nil {
				t.Fatal(err)
			}
		}
		return nil, nil
	}
	result, err := fetch(context.Background(), FetchRequest{OutputDir: output, ReleaseTag: "v1.2.3"}, f.client(), f.verify)
	data, readErr := os.ReadFile(final)
	if err == nil || result.Status == "verified" || result.Code != "download-failed" || f.verifyCalls != 0 || readErr != nil || string(data) != string(marker) {
		t.Fatalf("unexpected final replaced: %#v %v / %s %v", result, err, data, readErr)
	}
	if _, err := os.Stat(final + ".partial"); err != nil {
		t.Fatal("owned partial evidence lost")
	}
}
