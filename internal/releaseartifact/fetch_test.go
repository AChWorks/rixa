// SPDX-License-Identifier: MPL-2.0

package releaseartifact

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

type fetchRoundTripper func(*http.Request) (*http.Response, error)

func (f fetchRoundTripper) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

type fetchFixture struct {
	t           *testing.T
	release     publishedRelease
	repository  map[string]any
	ref         gitReference
	tags        map[string]gitReference
	bodies      map[string][]byte
	override    func(*http.Request) (*http.Response, error)
	calls       []string
	verifyCalls int
}

func newFetchFixture(t *testing.T) *fetchFixture {
	t.Helper()
	f := &fetchFixture{t: t, repository: map[string]any{"id": int64(1403734531), "full_name": Repository, "owner": map[string]any{"id": int64(80599060), "login": "AChWorks"}}, tags: map[string]gitReference{}, bodies: map[string][]byte{}}
	f.release = publishedRelease{ID: 123, Tag: "v1.2.3", PublishedAt: "2026-10-29T12:00:00Z"}
	f.ref = gitReference{Ref: "refs/tags/v1.2.3", Object: tagObject{SHA: strings.Repeat("a", 40), Type: "commit"}}
	name := "rixa-v1.2.3-linux-amd64.tar.gz"
	archive := []byte("opaque archive; do not execute or extract")
	sum := sha256.Sum256(archive)
	f.addAsset(name, archive)
	f.addAsset(name+".sha256", []byte(hex.EncodeToString(sum[:])+"  "+name+"\n"))
	f.addAsset(name+".sigstore.json", []byte(`{"test":"policy boundary mocked, not genuine signing"}`))
	return f
}
func (f *fetchFixture) addAsset(name string, data []byte) {
	sum := sha256.Sum256(data)
	address := "https://github.com/" + Repository + "/releases/download/" + f.release.Tag + "/" + name
	f.release.Assets = append(f.release.Assets, releaseAsset{ID: int64(len(f.release.Assets) + 1), Name: name, Size: int64(len(data)), State: "uploaded", Digest: "sha256:" + hex.EncodeToString(sum[:]), DownloadURL: address})
	f.bodies[address] = data
}
func (f *fetchFixture) response(data []byte) *http.Response {
	return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(bytes.NewReader(data)), ContentLength: int64(len(data))}
}
func (f *fetchFixture) client() *http.Client {
	return &http.Client{CheckRedirect: fetchRedirect, Transport: fetchRoundTripper(func(r *http.Request) (*http.Response, error) {
		f.calls = append(f.calls, r.URL.String())
		if r.Header.Get("Authorization") != "" || r.URL.User != nil {
			f.t.Fatal("ambient credentials entered public fetch")
		}
		if r.Method != "GET" {
			f.t.Fatal("fetch attempted remote mutation")
		}
		if f.override != nil {
			if response, err := f.override(r); response != nil || err != nil {
				return response, err
			}
		}
		var value any
		switch r.URL.String() {
		case releaseAPI:
			value = f.repository
		case releaseAPI + "/releases/latest", releaseAPI + "/releases/tags/v1.2.3":
			value = f.release
		case releaseAPI + "/releases?per_page=20":
			value = []publishedRelease{f.release}
		case releaseAPI + "/git/ref/tags/v1.2.3":
			value = f.ref
		default:
			if body, ok := f.bodies[r.URL.String()]; ok {
				return f.response(body), nil
			}
			if tag, ok := f.tags[strings.TrimPrefix(r.URL.String(), releaseAPI+"/git/tags/")]; ok {
				value = tag
			} else {
				return &http.Response{StatusCode: 404, Header: make(http.Header), Body: io.NopCloser(strings.NewReader("not found"))}, nil
			}
		}
		data, err := json.Marshal(value)
		if err != nil {
			f.t.Fatal(err)
		}
		return f.response(data), nil
	})}
}
func (f *fetchFixture) verify(ctx context.Context, r Request) (Result, error) {
	f.verifyCalls++
	if r.ReleaseTag != "v1.2.3" || r.SourceCommit != strings.Repeat("a", 40) || r.ExpectedSHA256 != strings.TrimPrefix(f.release.Assets[0].Digest, "sha256:") {
		f.t.Fatalf("unbound verification: %#v", r)
	}
	if _, err := os.Stat(r.ArchivePath); err != nil {
		f.t.Fatal(err)
	}
	if _, err := os.Stat(r.BundlePath); err != nil {
		f.t.Fatal(err)
	}
	return Result{Schema: "rixa.artifact-verification.v1", Status: "verified", Code: "ok", ReleaseTag: r.ReleaseTag, SourceCommit: r.SourceCommit, SHA256: r.ExpectedSHA256}, nil
}
func privateFetchOutput(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.Chmod(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	return filepath.Join(dir, "new-download")
}

func TestFetchBindsExactAssetsSourceAndPrivateOutput(t *testing.T) {
	f := newFetchFixture(t)
	output := privateFetchOutput(t)
	result, err := fetch(context.Background(), FetchRequest{OutputDir: output, ReleaseTag: "v1.2.3"}, f.client(), f.verify)
	if err != nil || result.Status != "verified" || result.Code != "ok" || result.Verification == nil || f.verifyCalls != 1 {
		t.Fatalf("fetch: %#v / %v", result, err)
	}
	if result.ArchivePath != filepath.Join(output, f.release.Assets[0].Name) || result.SourceCommit != strings.Repeat("a", 40) {
		t.Fatal("result did not preserve selected identity")
	}
	entries, err := os.ReadDir(output)
	if err != nil || len(entries) != 3 {
		t.Fatalf("output: %v %v", entries, err)
	}
	info, _ := os.Stat(output)
	if info.Mode().Perm() != 0o700 {
		t.Fatal("directory is not private")
	}
	for _, entry := range entries {
		info, _ := entry.Info()
		if info.Mode().Perm() != 0o600 || !info.Mode().IsRegular() {
			t.Fatal("unsafe artifact mode")
		}
	}
}

func TestFetchStableDoesNotFallBackToPrerelease(t *testing.T) {
	f := newFetchFixture(t)
	f.release.Prerelease = true
	result, err := fetch(context.Background(), FetchRequest{OutputDir: privateFetchOutput(t)}, f.client(), f.verify)
	if !errors.Is(err, ErrNoRelease) || result.Status == "verified" || f.verifyCalls != 0 {
		t.Fatalf("prerelease used as stable: %#v %v", result, err)
	}
}
func TestFetchExplicitDevelopmentAndAnnotatedTag(t *testing.T) {
	f := newFetchFixture(t)
	f.release.Prerelease = true
	refSHA := strings.Repeat("b", 40)
	f.ref.Object = tagObject{SHA: refSHA, Type: "tag"}
	f.tags[refSHA] = gitReference{SHA: refSHA, Object: tagObject{SHA: strings.Repeat("a", 40), Type: "commit"}}
	result, err := fetch(context.Background(), FetchRequest{OutputDir: privateFetchOutput(t), Channel: "development"}, f.client(), f.verify)
	if err != nil || result.Status != "verified" || result.Channel != "development" {
		t.Fatalf("development annotated tag: %#v %v", result, err)
	}
}
func TestFetchRejectsUnsafeReleaseMetadata(t *testing.T) {
	tests := []struct {
		name   string
		change func(*fetchFixture)
		code   string
	}{
		{"repository identity", func(f *fetchFixture) { f.repository["id"] = 1 }, "metadata-unavailable"},
		{"draft", func(f *fetchFixture) { f.release.Draft = true }, "invalid-release"},
		{"missing bundle", func(f *fetchFixture) { f.release.Assets = f.release.Assets[:2] }, "invalid-release"},
		{"duplicate asset", func(f *fetchFixture) { f.release.Assets[1] = f.release.Assets[0] }, "invalid-release"},
		{"asset identity", func(f *fetchFixture) { f.release.Assets[1].ID = f.release.Assets[0].ID }, "invalid-release"},
		{"asset url", func(f *fetchFixture) { f.release.Assets[0].DownloadURL = "https://attacker.invalid/archive" }, "invalid-release"},
		{"asset digest", func(f *fetchFixture) { f.release.Assets[0].Digest = "sha256:bad" }, "invalid-release"},
		{"asset state", func(f *fetchFixture) { f.release.Assets[0].State = "new" }, "invalid-release"},
		{"asset size", func(f *fetchFixture) { f.release.Assets[0].Size = MaxArchiveBytes + 1 }, "invalid-release"},
		{"tag mismatch", func(f *fetchFixture) { f.ref.Ref = "refs/heads/main" }, "tag-unavailable"},
		{"tag cycle", func(f *fetchFixture) {
			f.ref.Object.Type = "tag"
			f.tags[f.ref.Object.SHA] = gitReference{SHA: f.ref.Object.SHA, Object: f.ref.Object}
		}, "tag-unavailable"},
		{"target commitish ignored", func(f *fetchFixture) { f.ref.Object.Type = "tree" }, "tag-unavailable"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			f := newFetchFixture(t)
			test.change(f)
			output := privateFetchOutput(t)
			result, err := fetch(context.Background(), FetchRequest{OutputDir: output, ReleaseTag: "v1.2.3"}, f.client(), f.verify)
			if err == nil || result.Code != test.code || result.Status == "verified" || f.verifyCalls != 0 {
				t.Fatalf("unsafe metadata accepted: %#v %v", result, err)
			}
			if _, err := os.Stat(output); !errors.Is(err, os.ErrNotExist) {
				t.Fatal("metadata rejection created output")
			}
		})
	}
}
func TestFetchDoesNotAdoptExistingOutput(t *testing.T) {
	f := newFetchFixture(t)
	output := privateFetchOutput(t)
	if err := os.Mkdir(output, 0o700); err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(output, "keep")
	if err := os.WriteFile(marker, []byte("preserve"), 0o600); err != nil {
		t.Fatal(err)
	}
	result, err := fetch(context.Background(), FetchRequest{OutputDir: output, ReleaseTag: "v1.2.3"}, f.client(), f.verify)
	data, _ := os.ReadFile(marker)
	if err == nil || result.Code != "output-unavailable" || string(data) != "preserve" || f.verifyCalls != 0 {
		t.Fatalf("existing output changed: %#v %v", result, err)
	}
}
func TestFetchRejectsSymlinkAndWritableParents(t *testing.T) {
	for _, kind := range []string{"symlink", "writable"} {
		t.Run(kind, func(t *testing.T) {
			f := newFetchFixture(t)
			parent := filepath.Dir(privateFetchOutput(t))
			output := filepath.Join(parent, "child")
			if kind == "symlink" {
				link := parent + "-link"
				if err := os.Symlink(parent, link); err != nil {
					t.Fatal(err)
				}
				defer os.Remove(link)
				output = filepath.Join(link, "child")
			} else {
				if err := os.Chmod(parent, 0o777); err != nil {
					t.Fatal(err)
				}
				defer os.Chmod(parent, 0o700)
			}
			result, err := fetch(context.Background(), FetchRequest{OutputDir: output, ReleaseTag: "v1.2.3"}, f.client(), f.verify)
			if err == nil || result.Code != "unsafe-output" || len(f.calls) != 0 {
				t.Fatalf("unsafe path: %#v %v", result, err)
			}
		})
	}
}
func TestFetchRejectsChangedOrCorruptDownloads(t *testing.T) {
	for _, kind := range []string{"corrupt", "short", "long", "checksum", "verify", "metadata drift", "ref drift", "output drift"} {
		t.Run(kind, func(t *testing.T) {
			f := newFetchFixture(t)
			output := privateFetchOutput(t)
			verifier := f.verify
			switch kind {
			case "corrupt":
				f.bodies[f.release.Assets[0].DownloadURL] = bytes.Repeat([]byte("x"), int(f.release.Assets[0].Size))
			case "short":
				f.bodies[f.release.Assets[0].DownloadURL] = []byte("short")
			case "long":
				f.bodies[f.release.Assets[0].DownloadURL] = bytes.Repeat([]byte("x"), int(f.release.Assets[0].Size+1))
			case "checksum":
				asset := &f.release.Assets[1]
				data := []byte("invalid checksum")
				sum := sha256.Sum256(data)
				asset.Size = int64(len(data))
				asset.Digest = "sha256:" + hex.EncodeToString(sum[:])
				f.bodies[asset.DownloadURL] = data
			case "verify":
				verifier = func(context.Context, Request) (Result, error) {
					return Result{Status: "not-verified", Code: "policy-mismatch"}, ErrPolicy
				}
			case "metadata drift":
				verifier = func(c context.Context, r Request) (Result, error) {
					result, err := f.verify(c, r)
					f.release.ID++
					return result, err
				}
			case "ref drift":
				verifier = func(c context.Context, r Request) (Result, error) {
					result, err := f.verify(c, r)
					f.ref.Object.SHA = strings.Repeat("c", 40)
					return result, err
				}
			case "output drift":
				verifier = func(c context.Context, r Request) (Result, error) {
					result, err := f.verify(c, r)
					err2 := os.WriteFile(r.ArchivePath, []byte("replaced"), 0o600)
					if err2 != nil {
						t.Fatal(err2)
					}
					return result, err
				}
			}
			result, err := fetch(context.Background(), FetchRequest{OutputDir: output, ReleaseTag: "v1.2.3"}, f.client(), verifier)
			if err == nil || result.Status == "verified" || result.ArchivePath != "" {
				t.Fatalf("invalid download accepted: %#v %v", result, err)
			}
			if _, err := os.Stat(output); err != nil {
				t.Fatal("partial failure directory truth was lost")
			}
		})
	}
}
func TestFetchMetadataBoundsAndSafeDiagnostics(t *testing.T) {
	f := newFetchFixture(t)
	f.override = func(r *http.Request) (*http.Response, error) {
		if r.URL.String() != releaseAPI {
			return nil, nil
		}
		return &http.Response{StatusCode: 403, Header: http.Header{"X-Github-Request-Id": []string{"SAFE:123"}}, Body: io.NopCloser(strings.NewReader("secret diagnostic should never be reflected"))}, nil
	}
	result, err := fetch(context.Background(), FetchRequest{OutputDir: privateFetchOutput(t)}, f.client(), f.verify)
	if err == nil || !strings.Contains(result.Detail, "HTTP403") || !strings.Contains(result.Detail, "SAFE:123") || strings.Contains(result.Detail, "secret") {
		t.Fatalf("diagnostic lost/unsafe: %#v %v", result, err)
	}
	for _, data := range []string{`{"id":1,"id":1403734531}`, strings.Repeat("x", int(maxMetadataBytes+1))} {
		f := newFetchFixture(t)
		f.override = func(r *http.Request) (*http.Response, error) { return f.response([]byte(data)), nil }
		result, err := fetch(context.Background(), FetchRequest{OutputDir: privateFetchOutput(t)}, f.client(), f.verify)
		if err == nil || result.Status == "verified" {
			t.Fatal("ambiguous/oversize metadata accepted")
		}
	}
}
func TestFetchRedirectPolicy(t *testing.T) {
	start, _ := http.NewRequest("GET", "https://github.com/AChWorks/rixa/releases/download/v1.2.3/archive", nil)
	for _, address := range []string{"https://release-assets.githubusercontent.com/public?signature=value", "https://objects.githubusercontent.com/asset"} {
		req, _ := http.NewRequest("GET", address, nil)
		if err := fetchRedirect(req, []*http.Request{start}); err != nil {
			t.Fatalf("official asset redirect rejected: %v", err)
		}
	}
	for _, address := range []string{"http://release-assets.githubusercontent.com/asset", "https://attacker.invalid/asset", "https://user:password@github.com/asset", "https://github.com:444/asset", "https://github.com/asset#fragment"} {
		u, _ := url.Parse(address)
		if assetTransportURL(u) {
			t.Fatalf("unsafe redirect allowed: %s", address)
		}
	}
	api, _ := http.NewRequest("GET", releaseAPI, nil)
	next, _ := http.NewRequest("GET", "https://github.com/asset", nil)
	if fetchRedirect(next, []*http.Request{api}) == nil || fetchRedirect(next, []*http.Request{start, start, start}) == nil {
		t.Fatal("API/loop redirect accepted")
	}
}
func TestFetchCanceledAndInvalidSelection(t *testing.T) {
	f := newFetchFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	result, err := fetch(ctx, FetchRequest{OutputDir: privateFetchOutput(t)}, f.client(), f.verify)
	if !errors.Is(err, ErrCanceled) || result.Code != "canceled" || len(f.calls) != 0 {
		t.Fatal("canceled fetch made network calls")
	}
	for _, request := range []FetchRequest{{OutputDir: privateFetchOutput(t), ReleaseTag: "v1.2.3", Channel: "stable"}, {OutputDir: privateFetchOutput(t), Channel: "nightly"}, {OutputDir: "relative"}, {OutputDir: privateFetchOutput(t), ReleaseTag: "../bad"}} {
		result, err := fetch(context.Background(), request, f.client(), f.verify)
		if err == nil || result.Code != "invalid-input" {
			t.Fatalf("invalid selection accepted: %#v %v", request, err)
		}
	}
}
func TestFetchDevelopmentIsExplicitAndFinite(t *testing.T) {
	f := newFetchFixture(t)
	result, err := fetch(context.Background(), FetchRequest{OutputDir: privateFetchOutput(t), Channel: "development"}, f.client(), f.verify)
	if !errors.Is(err, ErrNoRelease) || result.Status == "verified" {
		t.Fatal("development silently selected stable")
	}
	f = newFetchFixture(t)
	f.release.Prerelease = true
	before := f.release
	result, err = fetch(context.Background(), FetchRequest{OutputDir: privateFetchOutput(t), Channel: "development"}, f.client(), f.verify)
	if err != nil || !reflect.DeepEqual(before, f.release) || result.Status != "verified" {
		t.Fatalf("explicit development failed: %#v %v", result, err)
	}
}
