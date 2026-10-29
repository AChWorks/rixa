// SPDX-License-Identifier: MPL-2.0

// Package releaseartifact verifies a Rixa archive's publisher and exact source.
// It never extracts or executes the archive. A result identifies verified bytes,
// not authority to install them; a later consumer must retain/recheck that digest
// under its own protected staging and effect boundary.
package releaseartifact

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

const (
	Repository                = "AChWorks/rixa"
	Workflow                  = Repository + "/.github/workflows/release.yml"
	MinimumGHVersion          = "2.103.3"
	MaxArchiveBytes     int64 = 256 << 20
	MaxBundleBytes      int64 = 8 << 20
	maxOutputBytes            = 4 << 20
	maxDiagnosticBytes        = 32 << 10
	verificationTimeout       = 45 * time.Second
)

var (
	ErrInput        = errors.New("invalid artifact verification input")
	ErrAttestation  = errors.New("an offline publisher attestation bundle is required")
	ErrHelper       = errors.New("a trusted supported GitHub CLI verifier is required")
	ErrVerification = errors.New("artifact publisher verification failed")
	ErrPolicy       = errors.New("verified provenance does not match the Rixa release policy")
	ErrChanged      = errors.New("verification input changed; inspect and verify the intended bytes again")
	ErrCanceled     = errors.New("artifact verification was canceled or exceeded its deadline")
	tagSyntax       = regexp.MustCompile(`^v[0-9]+\.[0-9]+\.[0-9]+([.-][0-9A-Za-z][0-9A-Za-z.-]*)?$`)
	shaSyntax       = regexp.MustCompile(`^[0-9a-f]{40}$`)
	digestSyntax    = regexp.MustCompile(`^[0-9a-f]{64}$`)
)

type Request struct {
	ArchivePath    string
	BundlePath     string
	ReleaseTag     string
	SourceCommit   string
	ExpectedSHA256 string
}

// Result contains non-secret identity and useful bounded verifier diagnostics.
// Identity fields describe requested policy; only Status=verified establishes
// their authenticated match. Expected source/tag/digest are preconditions, not values
// inferred from an unsigned checksum, untrusted archive or statement predicate.
type Result struct {
	Schema       string `json:"schema"`
	Status       string `json:"status"`
	Code         string `json:"code"`
	Field        string `json:"field,omitempty"`
	Detail       string `json:"detail,omitempty"`
	SHA256       string `json:"sha256,omitempty"`
	ReleaseTag   string `json:"release,omitempty"`
	SourceCommit string `json:"source_commit,omitempty"`
	SourceRef    string `json:"source_ref,omitempty"`
	Repository   string `json:"repository,omitempty"`
	Workflow     string `json:"workflow,omitempty"`
	Runner       string `json:"runner,omitempty"`
	RunURL       string `json:"run_url,omitempty"`
}

type helperRunner func(context.Context, string, []string, string) ([]byte, string, error)

func Verify(parent context.Context, request Request) (Result, error) {
	return verify(parent, request, findHelper, runHelper)
}

func verify(parent context.Context, request Request, discover func() (string, error), run helperRunner) (Result, error) {
	result := Result{Schema: "rixa.artifact-verification.v1", Status: "not-verified", Code: "invalid-input"}
	if len(request.ReleaseTag) > 128 || !tagSyntax.MatchString(request.ReleaseTag) {
		result.Field, result.Detail = "release", "Select one exact versioned Rixa release tag."
		return result, ErrInput
	}
	if !shaSyntax.MatchString(request.SourceCommit) {
		result.Field, result.Detail = "source_commit", "Select the full lowercase 40-character source commit independently of the archive."
		return result, ErrInput
	}
	if request.ExpectedSHA256 != "" && !digestSyntax.MatchString(request.ExpectedSHA256) {
		result.Field, result.Detail = "sha256", "The optional expected SHA-256 must contain 64 lowercase hexadecimal characters."
		return result, ErrInput
	}
	result.ReleaseTag, result.SourceCommit = request.ReleaseTag, request.SourceCommit
	result.SourceRef = "refs/tags/" + request.ReleaseTag
	result.Repository, result.Workflow, result.Runner = Repository, Workflow, "github-hosted"
	if parent.Err() != nil {
		result.Code = "canceled"
		return result, ErrCanceled
	}
	ctx, cancel := context.WithTimeout(parent, verificationTimeout)
	defer cancel()
	dir, err := os.MkdirTemp("", "rixa-artifact-verify-")
	if err != nil {
		result.Code = "private-storage-unavailable"
		return result, ErrInput
	}
	defer os.RemoveAll(dir)
	dir, err = filepath.Abs(dir)
	if err != nil {
		result.Code = "private-storage-unsafe"
		return result, ErrInput
	}
	if !trustedParents(dir) {
		result.Code = "private-storage-unsafe"
		return result, ErrInput
	}
	archive, err := snapshotInput(request.ArchivePath, filepath.Join(dir, "archive"), MaxArchiveBytes)
	if err != nil {
		result.Field, result.Detail = "archive", "Provide a readable regular archive of at most 256 MiB; symlinks and special files are not supported."
		return result, ErrInput
	}
	defer archive.original.Close()
	result.SHA256 = archive.digest
	bundle, err := snapshotInput(request.BundlePath, filepath.Join(dir, "bundle.json"), MaxBundleBytes)
	if err != nil {
		result.Field, result.Detail = "bundle", "Provide the adjacent single JSON .sigstore.json provenance bundle of at most 8 MiB; a checksum alone does not authenticate the publisher."
		if errors.Is(err, os.ErrNotExist) || request.BundlePath == "" {
			result.Code = "attestation-required"
			return result, ErrAttestation
		}
		return result, ErrInput
	}
	defer bundle.original.Close()
	data, err := os.ReadFile(bundle.snapshot)
	if err != nil || len(data) == 0 || data[0] != '{' && !strings.HasPrefix(strings.TrimSpace(string(data)), "{") || !json.Valid(data) || uniqueJSON(data) != nil {
		result.Field, result.Detail = "bundle", "The provenance bundle must be one valid unambiguous JSON object."
		return result, ErrInput
	}
	if request.ExpectedSHA256 != "" && request.ExpectedSHA256 != archive.digest {
		result.Code = "digest-mismatch"
		result.Field = "archive"
		return result, ErrVerification
	}
	helper, err := discover()
	if err != nil {
		result.Code = "helper-required"
		result.Detail = "Install a trusted official gh >= " + MinimumGHVersion + " supporting attestation verify, source digest/ref and offline bundles."
		return result, ErrHelper
	}
	ref := "refs/tags/" + request.ReleaseTag
	args := []string{"attestation", "verify", archive.snapshot, "--bundle", bundle.snapshot, "--repo", Repository, "--signer-workflow", Workflow, "--source-ref", ref, "--source-digest", request.SourceCommit, "--signer-digest", request.SourceCommit, "--deny-self-hosted-runners", "--predicate-type", "https://slsa.dev/provenance/v1", "--cert-oidc-issuer", "https://token.actions.githubusercontent.com", "--hostname", "github.com", "--format", "json"}
	output, detail, err := run(ctx, helper, args, dir)
	result.Detail = detail
	if ctx.Err() != nil {
		result.Code = "canceled"
		return result, ErrCanceled
	}
	if err != nil {
		if errors.Is(err, ErrHelper) {
			result.Code = "helper-required"
			return result, ErrHelper
		}
		result.Code = "verification-failed"
		return result, ErrVerification
	}
	if !archive.unchanged() || !bundle.unchanged() {
		result.Code = "input-changed"
		return result, ErrChanged
	}
	claim, err := checkPolicy(output, request, archive.digest)
	if err != nil {
		result.Code = "policy-mismatch"
		result.Detail = "Rixa release policy rejected the authenticated certificate or payload identity. " + result.Detail
		return result, ErrPolicy
	}
	result.Status, result.Code = "verified", "ok"
	result.ReleaseTag, result.SourceCommit = request.ReleaseTag, request.SourceCommit
	result.Repository, result.Workflow, result.Runner = Repository, Workflow, "github-hosted"
	result.RunURL = claim
	return result, nil
}

type inputSnapshot struct {
	original *os.File
	path     string
	info     os.FileInfo
	snapshot string
	digest   string
	maximum  int64
}

func snapshotInput(path, snapshot string, maximum int64) (*inputSnapshot, error) {
	original, err := openRegular(path, maximum)
	if err != nil {
		return nil, err
	}
	info, err := original.Stat()
	if err != nil {
		original.Close()
		return nil, err
	}
	copy, err := os.OpenFile(snapshot, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		original.Close()
		return nil, err
	}
	hash := sha256.New()
	written, copyErr := io.Copy(io.MultiWriter(copy, hash), io.LimitReader(original, maximum+1))
	closeErr := copy.Close()
	if copyErr != nil || closeErr != nil || written == 0 || written > maximum {
		original.Close()
		return nil, ErrInput
	}
	return &inputSnapshot{original: original, path: path, info: info, snapshot: snapshot, digest: hex.EncodeToString(hash.Sum(nil)), maximum: maximum}, nil
}
func (s *inputSnapshot) unchanged() bool {
	current, err := os.Lstat(s.path)
	if err != nil || !current.Mode().IsRegular() || !os.SameFile(s.info, current) {
		return false
	}
	if _, err = s.original.Seek(0, io.SeekStart); err != nil {
		return false
	}
	hash := sha256.New()
	n, err := io.Copy(hash, io.LimitReader(s.original, s.maximum+1))
	if err != nil || n < 1 || n > s.maximum || hex.EncodeToString(hash.Sum(nil)) != s.digest {
		return false
	}
	current, err = os.Lstat(s.path)
	after, statErr := s.original.Stat()
	return err == nil && statErr == nil && current.Mode().IsRegular() && os.SameFile(s.info, current) && after.Size() == s.info.Size() && after.ModTime().Equal(s.info.ModTime())
}
