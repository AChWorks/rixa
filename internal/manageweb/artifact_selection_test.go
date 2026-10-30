// SPDX-License-Identifier: MPL-2.0
package manageweb

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"mime/multipart"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/AChWorks/rixa/internal/releaseartifact"
	"github.com/AChWorks/rixa/internal/serverinstall"
)

func artifactRequest(t *testing.T, h *harness, method, attempt, tag string) (int, response) {
	t.Helper()
	var request *http.Request
	if method == "fetch" {
		request, _ = http.NewRequest("POST", h.console.origin+"/artifact/fetch", strings.NewReader(`{"release":"`+tag+`"}`))
		request.Header.Set("Content-Type", "application/json")
	} else {
		var body bytes.Buffer
		writer := multipart.NewWriter(&body)
		for _, pair := range [][2]string{{"release", tag}, {"source_commit", strings.Repeat("a", 40)}} {
			if err := writer.WriteField(pair[0], pair[1]); err != nil {
				t.Fatal(err)
			}
		}
		for _, name := range []string{"archive", "bundle"} {
			part, err := writer.CreateFormFile(name, name)
			if err != nil {
				t.Fatal(err)
			}
			if _, err = io.WriteString(part, "bounded fake package"); err != nil {
				t.Fatal(err)
			}
		}
		if err := writer.Close(); err != nil {
			t.Fatal(err)
		}
		request, _ = http.NewRequest("POST", h.console.origin+"/artifact/upload", &body)
		request.Header.Set("Content-Type", writer.FormDataContentType())
	}
	request.Header.Set("Origin", h.console.origin)
	request.Header.Set("X-Identity-Request", "1")
	request.Header.Set("X-CSRF-Token", h.csrf)
	request.Header.Set("X-Rixa-Artifact-Attempt", attempt)
	result, err := h.client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer result.Body.Close()
	var got response
	if err = json.NewDecoder(result.Body).Decode(&got); err != nil {
		t.Fatal(err)
	}
	return result.StatusCode, got
}

func fakeStagedFetch(t *testing.T) func(context.Context, releaseartifact.FetchRequest) (releaseartifact.FetchResult, error) {
	t.Helper()
	return func(_ context.Context, in releaseartifact.FetchRequest) (releaseartifact.FetchResult, error) {
		if in.ReleaseTag == "v0.0.0-invalid" {
			return releaseartifact.FetchResult{}, releaseartifact.ErrNoRelease
		}
		if err := os.Mkdir(in.OutputDir, 0700); err != nil {
			return releaseartifact.FetchResult{}, err
		}
		archive, bundle := filepath.Join(in.OutputDir, "archive"), filepath.Join(in.OutputDir, "bundle")
		for _, p := range []string{archive, bundle} {
			if err := os.WriteFile(p, []byte("fake only"), 0600); err != nil {
				return releaseartifact.FetchResult{}, err
			}
		}
		return releaseartifact.FetchResult{Status: "verified", ReleaseTag: in.ReleaseTag, SourceCommit: strings.Repeat("c", 40), ArchivePath: archive, BundlePath: bundle, Verification: &releaseartifact.Result{Status: "verified", SHA256: strings.Repeat("d", 64)}}, nil
	}
}

// These tests use only private fake facades. They prove the presentation gate,
// not package authenticity or a native install/update.
func TestConsoleStagedArtifactMustReachSavedDraft(t *testing.T) {
	for _, kind := range []string{"install", "update"} {
		for _, method := range []string{"fetch", "upload"} {
			t.Run(kind+"/"+method, func(t *testing.T) {
				h := fixture(t, func(o *Options, d *dependencies, f *fakeNative) {
					d.fetch = fakeStagedFetch(t)
					f.result = serverinstall.Result{OperationID: "fixture-install", State: "ready", Actions: []string{"configure", "review", "apply", "status"}, ReviewToken: strings.Repeat("a", 64)}
					old := artifact{"v1.2.3", strings.Repeat("a", 40), "/old/archive", "/old/bundle", strings.Repeat("b", 64)}
					f.draft = serverinstall.Draft{OperationID: "fixture-install", Release: old.Release, SourceCommit: old.Source, ArchivePath: old.Archive, BundlePath: old.Bundle}
					if kind == "update" {
						o.Scope = Scope{Kind: "update", Operation: "fixture-update", Installation: "fixture-install", Actions: []string{"configure", "review", "apply", "status"}}
						f.result.OperationID = "fixture-update"
						f.result.OperationKind = "update"
						f.result.InstallationOperation = "fixture-install"
						f.updated = serverinstall.UpdateDraft{OperationID: "fixture-update", InstallationOperation: "fixture-install", Release: old.Release, SourceCommit: old.Source, ArchivePath: old.Archive, BundlePath: old.Bundle}
					}
				})
				h.login(t)
				attempt := "11111111-1111-4111-8111-111111111111"
				code, staged := artifactRequest(t, h, method, attempt, "v1.2.4")
				if code != 200 || !staged.ArtifactUnsaved || staged.ArtifactAttempt != attempt {
					t.Fatalf("staging state: %d %+v", code, staged)
				}
				for _, action := range []string{"review", "apply"} {
					body := "{}"
					if action == "apply" {
						body = `{"review_token":"` + strings.Repeat("a", 64) + `"}`
					}
					code, _, _ := h.call(t, "POST", "/admin/operation/"+action, body, true)
					if code != 409 || h.backend.calls[action] != 0 {
						t.Fatalf("unsaved selection reached %s: %d", action, code)
					}
				}
				code, data, _ := h.call(t, "GET", "/admin/operation/status", "", false)
				var status response
				if code != 200 || json.Unmarshal(data, &status) != nil || !status.ArtifactUnsaved || status.ArtifactAttempt != attempt || status.ReviewToken != "" || slices.Contains(status.Actions, "apply") || slices.Contains(status.Actions, "review") {
					t.Fatalf("status did not retain unsaved selection: %d %s", code, data)
				}
				code, page, _ := h.call(t, "GET", "/admin/operation/", "", false)
				if code != 200 || !bytes.Contains(page, []byte("This authenticated package is not in the saved draft.")) {
					t.Fatal("reload hides staged/saved mismatch")
				}
				code, _, _ = h.call(t, "POST", "/admin/operation/configure", "{}", true)
				if code != 200 {
					t.Fatalf("Save failed %d", code)
				}
				code, data, _ = h.call(t, "POST", "/admin/operation/review", "{}", true)
				var ready response
				if code != 200 || json.Unmarshal(data, &ready) != nil || ready.ArtifactUnsaved || len(ready.ReviewToken) != 64 || ready.Review["release"] != "v1.2.4" {
					t.Fatalf("new saved draft not reviewed: %d %s", code, data)
				}
				h.backend.mutex.Lock()
				d, u := h.backend.draft, h.backend.updated
				h.backend.mutex.Unlock()
				h.console.mutex.Lock()
				a := h.console.artifact
				h.console.mutex.Unlock()
				if kind == "update" {
					if u.ArchivePath != a.Archive || u.BundlePath != a.Bundle || u.Release != a.Release || u.SourceCommit != a.Source {
						t.Fatal("update tuple not saved")
					}
				} else if d.ArchivePath != a.Archive || d.BundlePath != a.Bundle || d.Release != a.Release || d.SourceCommit != a.Source {
					t.Fatal("install tuple not saved")
				}
				// A committed attempt cannot be replayed, even before the request bound is
				// exhausted. Its exact ID is already available through observational Status.
				code, _ = artifactRequest(t, h, method, attempt, "v1.2.4")
				if code != 409 || h.console.artifactAttempts != 1 {
					t.Fatal("committed staging attempt replayed")
				}
			})
		}
	}
}
func TestConsoleArtifactTupleAndReadFailuresFailClosed(t *testing.T) {
	for _, kind := range []string{"install", "update"} {
		t.Run(kind, func(t *testing.T) {
			h := fixture(t, func(o *Options, _ *dependencies, f *fakeNative) {
				if kind == "update" {
					o.Scope = Scope{Kind: "update", Operation: "fixture-update", Installation: "fixture-install", Actions: []string{"configure", "review", "apply", "status"}}
					f.result.OperationID = "fixture-update"
					f.result.OperationKind = "update"
					f.result.InstallationOperation = "fixture-install"
				}
			})
			a := artifact{"v1.2.3", strings.Repeat("a", 40), "/fixed/archive", "/fixed/bundle", strings.Repeat("b", 64)}
			h.console.artifact = a
			h.backend.draft = serverinstall.Draft{Release: a.Release, SourceCommit: a.Source, ArchivePath: a.Archive, BundlePath: a.Bundle}
			h.backend.updated = serverinstall.UpdateDraft{Release: a.Release, SourceCommit: a.Source, ArchivePath: a.Archive, BundlePath: a.Bundle}
			for _, field := range []string{"archive", "bundle", "release", "source"} {
				changed := a
				switch field {
				case "archive":
					changed.Archive += "x"
				case "bundle":
					changed.Bundle += "x"
				case "release":
					changed.Release += "x"
				case "source":
					changed.Source = strings.Repeat("c", 40)
				}
				h.console.artifact = changed
				if _, _, unsaved, err := h.console.artifactState(context.Background()); err != nil || !unsaved {
					t.Fatalf("changed %s accepted", field)
				}
			}
			h.console.artifact = a
			h.backend.loadError = errors.New("unavailable")
			if _, _, unsaved, err := h.console.artifactState(context.Background()); err == nil || !unsaved {
				t.Fatal("unavailable draft accepted")
			}
		})
	}
}
func TestConsoleArtifactAttemptAndResourceBound(t *testing.T) {
	h := fixture(t, func(_ *Options, d *dependencies, _ *fakeNative) { d.fetch = fakeStagedFetch(t) })
	h.login(t)
	code, _ := artifactRequest(t, h, "fetch", "invalid", "v1.2.4")
	if code != 400 || h.console.artifactAttempts != 0 {
		t.Fatal("invalid correlation consumed resources")
	}
	for _, id := range []string{"11111111-1111-4111-8111-111111111111", "22222222-2222-4222-8222-222222222222", "33333333-3333-4333-8333-333333333333"} {
		code, _ = artifactRequest(t, h, "fetch", id, "v0.0.0-invalid")
		if code != 503 {
			t.Fatalf("failed selection status %d", code)
		}
	}
	code, _ = artifactRequest(t, h, "fetch", "44444444-4444-4444-8444-444444444444", "v1.2.4")
	if code != 429 || h.console.artifactAttempts != 3 {
		t.Fatal("three-attempt bound changed")
	}
	_, id, _, _ := h.console.artifactState(context.Background())
	if id != "" {
		t.Fatal("failed attempt reported committed")
	}
}

func TestConsolePreviouslyCommittedStagingIDCannotBeReused(t *testing.T) {
	h := fixture(t, func(_ *Options, d *dependencies, _ *fakeNative) { d.fetch = fakeStagedFetch(t) })
	h.login(t)
	first := "11111111-1111-4111-8111-111111111111"
	second := "22222222-2222-4222-8222-222222222222"
	for _, id := range []string{first, second} {
		code, _ := artifactRequest(t, h, "fetch", id, "v1.2.4")
		if code != 200 {
			t.Fatalf("stage status %d", code)
		}
	}
	code, _ := artifactRequest(t, h, "fetch", first, "v1.2.5")
	if code != 409 || h.console.artifactAttempts != 2 || h.console.artifactAttempt != second {
		t.Fatal("prior committed ID reused or current selection changed")
	}
}
