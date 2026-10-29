// SPDX-License-Identifier: MPL-2.0

package releaseartifact

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"syscall"
	"time"
)

const (
	releaseAPI             = "https://api.github.com/repos/" + Repository
	fetchTimeout           = 3 * time.Minute
	maxMetadataBytes int64 = 2 << 20
)

var (
	ErrFetch          = errors.New("Rixa artifact fetch failed")
	ErrNoRelease      = errors.New("no published Rixa release is available for the selected channel")
	ErrReleaseChanged = errors.New("selected release or tag changed during fetch")
	requestIDSyntax   = regexp.MustCompile(`^[A-Za-z0-9:_-]{1,128}$`)
)

type FetchRequest struct {
	OutputDir  string
	ReleaseTag string
	// Channel is stable (the default) or development (published prereleases only).
	// An exact ReleaseTag and a Channel cannot be supplied together.
	Channel string
}

// FetchResult describes a verified download, never permission to execute/install.
// A failed fetch may leave partial, unverified files in its new private directory.
// Existing output directories are never adopted, emptied or overwritten.
type FetchResult struct {
	Schema       string  `json:"schema"`
	Status       string  `json:"status"`
	Code         string  `json:"code"`
	Field        string  `json:"field,omitempty"`
	Detail       string  `json:"detail,omitempty"`
	Channel      string  `json:"channel,omitempty"`
	ReleaseTag   string  `json:"release,omitempty"`
	SourceCommit string  `json:"source_commit,omitempty"`
	ArchivePath  string  `json:"archive,omitempty"`
	ChecksumPath string  `json:"checksum,omitempty"`
	BundlePath   string  `json:"bundle,omitempty"`
	Verification *Result `json:"verification,omitempty"`
}

type releaseAsset struct {
	ID          int64  `json:"id"`
	Name        string `json:"name"`
	Size        int64  `json:"size"`
	State       string `json:"state"`
	Digest      string `json:"digest"`
	DownloadURL string `json:"browser_download_url"`
}
type publishedRelease struct {
	ID          int64          `json:"id"`
	Tag         string         `json:"tag_name"`
	Draft       bool           `json:"draft"`
	Prerelease  bool           `json:"prerelease"`
	PublishedAt string         `json:"published_at"`
	Assets      []releaseAsset `json:"assets"`
}
type tagObject struct {
	SHA  string `json:"sha"`
	Type string `json:"type"`
}
type gitReference struct {
	Ref    string    `json:"ref"`
	SHA    string    `json:"sha"`
	Object tagObject `json:"object"`
}

// Fetch uses only fixed public GitHub endpoints and the existing Verify policy.
// It does not extract or execute payload bytes and does not read ambient tokens.
func Fetch(ctx context.Context, request FetchRequest) (FetchResult, error) {
	client := &http.Client{Timeout: 90 * time.Second, CheckRedirect: fetchRedirect}
	return fetch(ctx, request, client, Verify)
}

func fetchRedirect(request *http.Request, via []*http.Request) error {
	if len(via) >= 3 || len(via) == 0 || via[0].URL.Hostname() != "github.com" || !assetTransportURL(request.URL) {
		return errors.New("release download redirect is not supported")
	}
	return nil
}
func assetTransportURL(u *url.URL) bool {
	return u != nil && u.Scheme == "https" && u.User == nil && (u.Port() == "" || u.Port() == "443") &&
		u.Fragment == "" && (u.Hostname() == "github.com" || u.Hostname() == "release-assets.githubusercontent.com" || u.Hostname() == "objects.githubusercontent.com")
}

func fetch(parent context.Context, request FetchRequest, client *http.Client, verifyArtifact func(context.Context, Request) (Result, error)) (result FetchResult, err error) {
	result = FetchResult{Schema: "rixa.artifact-fetch.v1", Status: "not-verified", Code: "invalid-input"}
	fail := func(code, field, detail string, cause error) (FetchResult, error) {
		result.Code, result.Field, result.Detail = code, field, detail
		return result, cause
	}
	if request.ReleaseTag != "" && (request.Channel != "" || len(request.ReleaseTag) > 128 || !tagSyntax.MatchString(request.ReleaseTag)) {
		return fail("invalid-input", "release", "Use one exact versioned tag, or a channel, not both.", ErrInput)
	}
	if request.ReleaseTag == "" && request.Channel == "" {
		request.Channel = "stable"
	}
	if request.Channel != "" && request.Channel != "stable" && request.Channel != "development" {
		return fail("invalid-input", "channel", "Channel must be stable or development.", ErrInput)
	}
	result.Channel = request.Channel
	if request.OutputDir == "" || len(request.OutputDir) > 4096 || !filepath.IsAbs(request.OutputDir) || filepath.Clean(request.OutputDir) != request.OutputDir || request.OutputDir == "/" || strings.ContainsAny(request.OutputDir, "\x00\r\n") {
		return fail("invalid-input", "output", "Provide a new absolute directory inside an existing trusted private parent.", ErrInput)
	}
	parentDir := filepath.Dir(request.OutputDir)
	canonical, pathErr := filepath.EvalSymlinks(parentDir)
	if pathErr != nil || canonical != parentDir || !trustedParents(parentDir) {
		return fail("unsafe-output", "output", "Output parents must be real directories owned by this user or root and protected from other writers.", ErrInput)
	}
	if parent.Err() != nil {
		return fail("canceled", "", "Fetch was canceled.", ErrCanceled)
	}
	ctx, cancel := context.WithTimeout(parent, fetchTimeout)
	defer cancel()
	if err = checkRepository(ctx, client); err != nil {
		return fail(fetchErrorCode(ctx, "metadata-unavailable"), "release", err.Error(), err)
	}
	release, err := selectRelease(ctx, client, request)
	if err != nil {
		return fail(fetchErrorCode(ctx, "release-unavailable"), "release", err.Error(), err)
	}
	assets, err := checkedAssets(release)
	if err != nil {
		return fail("invalid-release", "release", err.Error(), err)
	}
	source, frozenRef, err := resolveTag(ctx, client, release.Tag)
	if err != nil {
		return fail(fetchErrorCode(ctx, "tag-unavailable"), "release", err.Error(), err)
	}
	result.ReleaseTag, result.SourceCommit = release.Tag, source
	if err = os.Mkdir(request.OutputDir, 0o700); err != nil {
		return fail("output-unavailable", "output", "Output must not already exist; choose a new directory. Existing files were not modified.", ErrInput)
	}
	dirInfo, err := os.Lstat(request.OutputDir)
	if err != nil || !dirInfo.IsDir() || dirInfo.Mode().Perm() != 0o700 || !trustedOwner(dirInfo) {
		return fail("unsafe-output", "output", "The newly created private directory is unsafe; inspect it before continuing.", ErrInput)
	}
	root, err := os.OpenRoot(request.OutputDir)
	if err != nil {
		return fail("output-unavailable", "output", "The private output directory cannot be opened.", ErrInput)
	}
	defer root.Close()
	for _, asset := range assets {
		if err = downloadAsset(ctx, client, root, asset); err != nil {
			return fail(fetchErrorCode(ctx, "download-failed"), "assets", err.Error()+" Partial files are unverified; use a new directory for another fetch.", err)
		}
	}
	archiveName := "rixa-" + release.Tag + "-linux-amd64.tar.gz"
	checksumFile, _, err := openFetchedAsset(root, assets[1])
	var checksum []byte
	if err == nil {
		checksum, err = io.ReadAll(io.LimitReader(checksumFile, assets[1].Size+1))
		err = errors.Join(err, checksumFile.Close())
	}
	if err != nil || string(checksum) != strings.TrimPrefix(assets[0].Digest, "sha256:")+"  "+archiveName+"\n" {
		return fail("checksum-mismatch", "checksum", "The adjacent checksum must describe exactly the selected archive digest and filename.", ErrFetch)
	}
	archivePath := filepath.Join(request.OutputDir, archiveName)
	bundlePath := archivePath + ".sigstore.json"
	verification, verifyErr := verifyArtifact(ctx, Request{ArchivePath: archivePath, BundlePath: bundlePath, ReleaseTag: release.Tag, SourceCommit: source, ExpectedSHA256: strings.TrimPrefix(assets[0].Digest, "sha256:")})
	if verifyErr != nil {
		result.Verification = &verification
		return fail("verification-failed", "verification", "The downloaded publisher evidence was rejected; inspect the verification result.", verifyErr)
	}
	if verification.Status != "verified" || verification.Code != "ok" || verification.ReleaseTag != release.Tag || verification.SourceCommit != source || verification.SHA256 != strings.TrimPrefix(assets[0].Digest, "sha256:") {
		return fail("verification-failed", "verification", "The verifier did not return the exact selected archive/source receipt.", ErrVerification)
	}
	var current publishedRelease
	if err = fetchJSON(ctx, client, releaseAPI+"/releases/tags/"+url.PathEscape(release.Tag), &current); err != nil {
		return fail(fetchErrorCode(ctx, "recheck-unavailable"), "release", err.Error(), err)
	}
	currentAssets, assetErr := checkedAssets(current)
	currentSource, currentRef, tagErr := resolveTag(ctx, client, release.Tag)
	if assetErr != nil || tagErr != nil || !sameRelease(release, current) || !slices.Equal(assets, currentAssets) || currentSource != source || currentRef != frozenRef {
		return fail("release-changed", "release", "Release/tag evidence changed or could not be reconciled; do not use these downloads as the selected candidate.", ErrReleaseChanged)
	}
	if err = checkRepository(ctx, client); err != nil {
		return fail(fetchErrorCode(ctx, "recheck-unavailable"), "release", err.Error(), err)
	}
	// Recheck protected output identity and bytes after network observations.
	currentInfo, statErr := os.Lstat(request.OutputDir)
	if statErr != nil || !os.SameFile(dirInfo, currentInfo) || currentInfo.Mode().Perm() != 0o700 {
		return fail("output-changed", "output", "Output identity changed; verify the intended bytes again.", ErrChanged)
	}
	for _, asset := range assets {
		f, info, readErr := openFetchedAsset(root, asset)
		if readErr != nil {
			return fail("output-changed", "assets", "Downloaded files changed after verification.", ErrChanged)
		}
		hash := sha256.New()
		n, readErr := io.Copy(hash, io.LimitReader(f, asset.Size+1))
		closeErr := f.Close()
		currentFile, statErr := root.Lstat(asset.Name)
		if statErr != nil || !safeFetchedInfo(currentFile, asset.Size) || !os.SameFile(info, currentFile) || readErr != nil || closeErr != nil || n != asset.Size || "sha256:"+hex.EncodeToString(hash.Sum(nil)) != asset.Digest {
			return fail("output-changed", "assets", "Downloaded files changed after verification.", ErrChanged)
		}
	}
	result.Status, result.Code = "verified", "ok"
	result.ArchivePath, result.BundlePath, result.ChecksumPath = archivePath, bundlePath, archivePath+".sha256"
	result.Verification = &verification
	return result, nil
}
func fetchErrorCode(ctx context.Context, fallback string) string {
	if ctx.Err() != nil {
		return "canceled"
	}
	return fallback
}

func sameRelease(a, b publishedRelease) bool {
	return a.ID == b.ID && a.Tag == b.Tag && a.Draft == b.Draft && a.Prerelease == b.Prerelease && a.PublishedAt == b.PublishedAt
}
func checkRepository(ctx context.Context, client *http.Client) error {
	var repository struct {
		ID    int64  `json:"id"`
		Name  string `json:"full_name"`
		Owner struct {
			ID    int64  `json:"id"`
			Login string `json:"login"`
		} `json:"owner"`
	}
	if err := fetchJSON(ctx, client, releaseAPI, &repository); err != nil {
		return err
	}
	if repository.ID != 1403734531 || repository.Name != Repository || repository.Owner.ID != 80599060 || repository.Owner.Login != "AChWorks" {
		return fmt.Errorf("%w: official repository identity does not match", ErrFetch)
	}
	return nil
}
func selectRelease(ctx context.Context, client *http.Client, request FetchRequest) (publishedRelease, error) {
	var release publishedRelease
	path := "/releases/latest"
	if request.ReleaseTag != "" {
		path = "/releases/tags/" + url.PathEscape(request.ReleaseTag)
	}
	if request.Channel == "development" {
		var releases []publishedRelease
		if err := fetchJSON(ctx, client, releaseAPI+"/releases?per_page=20", &releases); err != nil {
			return release, err
		}
		if len(releases) > 20 {
			return release, fmt.Errorf("%w: release list exceeded its bound", ErrFetch)
		}
		for _, candidate := range releases {
			if candidate.Draft || !candidate.Prerelease {
				continue
			}
			at, err := time.Parse(time.RFC3339, candidate.PublishedAt)
			if err != nil {
				return release, fmt.Errorf("%w: invalid publication time", ErrFetch)
			}
			selected, _ := time.Parse(time.RFC3339, release.PublishedAt)
			if release.ID == 0 || at.After(selected) {
				release = candidate
			}
		}
		if release.ID == 0 {
			return release, ErrNoRelease
		}
		return release, nil
	}
	if err := fetchJSON(ctx, client, releaseAPI+path, &release); err != nil {
		return release, err
	}
	if request.ReleaseTag != "" && release.Tag != request.ReleaseTag || request.Channel == "stable" && release.Prerelease {
		return release, ErrNoRelease
	}
	return release, nil
}
func checkedAssets(release publishedRelease) ([]releaseAsset, error) {
	if release.ID <= 0 || release.Draft || !tagSyntax.MatchString(release.Tag) || len(release.Tag) > 128 || len(release.Assets) != 3 {
		return nil, fmt.Errorf("%w: expected one published versioned release with exactly archive, checksum and attestation assets", ErrFetch)
	}
	if _, err := time.Parse(time.RFC3339, release.PublishedAt); err != nil {
		return nil, fmt.Errorf("%w: release publication time is unavailable", ErrFetch)
	}
	name := "rixa-" + release.Tag + "-linux-amd64.tar.gz"
	expected := []string{name, name + ".sha256", name + ".sigstore.json"}
	maximum := []int64{MaxArchiveBytes, 4096, MaxBundleBytes}
	out := make([]releaseAsset, 3)
	ids := map[int64]bool{}
	for _, asset := range release.Assets {
		index := -1
		for i, n := range expected {
			if asset.Name == n {
				index = i
			}
		}
		if index < 0 || out[index].ID != 0 || asset.ID <= 0 || ids[asset.ID] || asset.State != "uploaded" || asset.Size < 1 || asset.Size > maximum[index] || !strings.HasPrefix(asset.Digest, "sha256:") || !digestSyntax.MatchString(strings.TrimPrefix(asset.Digest, "sha256:")) || asset.DownloadURL != "https://github.com/"+Repository+"/releases/download/"+release.Tag+"/"+asset.Name {
			return nil, fmt.Errorf("%w: release assets have missing, ambiguous or inconsistent names, identity, sizes, digests or URLs", ErrFetch)
		}
		out[index] = asset
		ids[asset.ID] = true
	}
	return out, nil
}
func resolveTag(ctx context.Context, client *http.Client, tag string) (source, refSHA string, err error) {
	var ref gitReference
	if err = fetchJSON(ctx, client, releaseAPI+"/git/ref/tags/"+url.PathEscape(tag), &ref); err != nil {
		return
	}
	if ref.Ref != "refs/tags/"+tag || !shaSyntax.MatchString(ref.Object.SHA) {
		err = fmt.Errorf("%w: invalid exact tag reference", ErrFetch)
		return
	}
	refSHA = ref.Object.SHA
	object := ref.Object
	seen := map[string]bool{}
	for depth := 0; depth < 5; depth++ {
		if !shaSyntax.MatchString(object.SHA) || seen[object.SHA] {
			break
		}
		seen[object.SHA] = true
		if object.Type == "commit" {
			return object.SHA, refSHA, nil
		}
		if object.Type != "tag" {
			break
		}
		var annotated gitReference
		if err = fetchJSON(ctx, client, releaseAPI+"/git/tags/"+object.SHA, &annotated); err != nil {
			return
		}
		if annotated.SHA != object.SHA {
			break
		}
		object = annotated.Object
	}
	return "", "", fmt.Errorf("%w: tag must resolve to one commit within four annotated tags", ErrFetch)
}
func fetchJSON(ctx context.Context, client *http.Client, address string, target any) error {
	response, err := fetchResponse(ctx, client, address, true)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	data, err := io.ReadAll(io.LimitReader(response.Body, maxMetadataBytes+1))
	if err != nil || len(data) == 0 || int64(len(data)) > maxMetadataBytes || uniqueJSON(data) != nil || json.Unmarshal(data, target) != nil {
		return fmt.Errorf("%w: GitHub metadata was unavailable, oversized or ambiguous", ErrFetch)
	}
	return nil
}
func fetchResponse(ctx context.Context, client *http.Client, address string, metadata bool) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, address, nil)
	if err != nil {
		return nil, ErrFetch
	}
	req.Header.Set("User-Agent", "Rixa-release-fetch")
	if metadata {
		req.Header.Set("Accept", "application/vnd.github+json")
		req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	}
	response, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("%w: GitHub transport unavailable; inspect connectivity, proxy and CA configuration", ErrFetch)
	}
	if response.StatusCode != http.StatusOK {
		response.Body.Close()
		requestID := response.Header.Get("X-GitHub-Request-Id")
		if !requestIDSyntax.MatchString(requestID) {
			requestID = "unavailable"
		}
		if response.StatusCode == http.StatusNotFound {
			return nil, fmt.Errorf("%w: GitHub HTTP404 (request %s); selected published channel/tag/evidence was not found", ErrNoRelease, requestID)
		}
		return nil, fmt.Errorf("%w: GitHub HTTP%d (request %s)", ErrFetch, response.StatusCode, requestID)
	}
	return response, nil
}
func downloadAsset(ctx context.Context, client *http.Client, root *os.Root, asset releaseAsset) error {
	response, err := fetchResponse(ctx, client, asset.DownloadURL, false)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.ContentLength > 0 && response.ContentLength != asset.Size {
		return fmt.Errorf("%w: download size differs from frozen metadata", ErrFetch)
	}
	output, err := root.OpenFile(asset.Name+".partial", os.O_WRONLY|os.O_CREATE|os.O_EXCL|syscall.O_NOFOLLOW, 0o600)
	if err != nil {
		return fmt.Errorf("%w: private asset output unavailable", ErrFetch)
	}
	hash := sha256.New()
	n, readErr := io.Copy(io.MultiWriter(output, hash), io.LimitReader(response.Body, asset.Size+1))
	syncErr := output.Sync()
	closeErr := output.Close()
	if readErr != nil || syncErr != nil || closeErr != nil || n != asset.Size || "sha256:"+hex.EncodeToString(hash.Sum(nil)) != asset.Digest {
		return fmt.Errorf("%w: asset bytes do not match the frozen remote size/digest", ErrFetch)
	}
	// Link creates the final name exclusively; an unexpected name is never
	// overwritten. On failure the private partial remains explicitly unverified.
	if err = root.Link(asset.Name+".partial", asset.Name); err != nil {
		return fmt.Errorf("%w: private asset finalization refused an unavailable or existing final name", ErrFetch)
	}
	if err = root.Remove(asset.Name + ".partial"); err != nil {
		return fmt.Errorf("%w: private partial finalization is incomplete", ErrFetch)
	}
	return nil
}

// A pathname inside the owned output can still have changed since download.
// Open without following links or waiting for a FIFO, then reject unsafe metadata
// before any read. Callers retain the frozen size bound for the actual bytes.
func openFetchedAsset(root *os.Root, asset releaseAsset) (*os.File, os.FileInfo, error) {
	f, err := root.OpenFile(asset.Name, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, nil, err
	}
	info, err := f.Stat()
	if err != nil || !safeFetchedInfo(info, asset.Size) {
		f.Close()
		return nil, nil, ErrChanged
	}
	return f, info, nil
}
func safeFetchedInfo(info os.FileInfo, size int64) bool {
	return info != nil && info.Mode().IsRegular() && info.Mode().Perm() == 0o600 &&
		info.Mode()&(os.ModeSetuid|os.ModeSetgid|os.ModeSticky) == 0 && trustedOwner(info) && info.Size() == size
}
