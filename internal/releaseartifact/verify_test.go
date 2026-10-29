// SPDX-License-Identifier: MPL-2.0

package releaseartifact

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

func verificationFixture(t *testing.T) (Request, []byte) {
	t.Helper()
	dir := t.TempDir()
	archive, bundle := filepath.Join(dir, "download.tar.gz"), filepath.Join(dir, "attestation.json")
	body := []byte("bounded artifact bytes; no extraction or execution")
	if err := os.WriteFile(archive, body, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(bundle, []byte(`{"mediaType":"test fixture; not a cryptographic attestation"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	return Request{ArchivePath: archive, BundlePath: bundle, ReleaseTag: "v0.2.0", SourceCommit: strings.Repeat("a", 40)}, body
}
func digestOf(data []byte) string { d := sha256.Sum256(data); return hex.EncodeToString(d[:]) }

// This is a policy fixture for claims returned by a trusted verifier, never a
// forged signature or evidence that cryptographic verification succeeded.
func claimFixture(request Request, digest string) map[string]any {
	uri := "https://github.com/" + Workflow + "@refs/tags/" + request.ReleaseTag
	cert := map[string]any{"issuer": "https://token.actions.githubusercontent.com", "subjectAlternativeName": uri, "buildSignerURI": uri, "buildSignerDigest": request.SourceCommit, "runnerEnvironment": "github-hosted", "sourceRepositoryURI": "https://github.com/" + Repository, "sourceRepositoryDigest": request.SourceCommit, "sourceRepositoryRef": "refs/tags/" + request.ReleaseTag, "sourceRepositoryIdentifier": "1403734531", "sourceRepositoryOwnerURI": "https://github.com/AChWorks", "sourceRepositoryOwnerIdentifier": "80599060", "buildConfigURI": uri, "buildConfigDigest": request.SourceCommit, "buildTrigger": "push", "runInvocationURI": "https://github.com/AChWorks/rixa/actions/runs/123/attempts/1"}
	return map[string]any{"verificationResult": map[string]any{"signature": map[string]any{"certificate": cert}, "verifiedTimestamps": []any{map[string]any{"type": "Tlog", "timestamp": "2026-10-29T00:00:00Z"}}, "statement": map[string]any{"_type": "https://in-toto.io/Statement/v1", "predicateType": "https://slsa.dev/provenance/v1", "subject": []any{map[string]any{"name": "rixa-" + request.ReleaseTag + "-linux-amd64.tar.gz", "digest": map[string]any{"sha256": digest}}}}}}
}
func claimJSON(t *testing.T, c map[string]any) []byte {
	t.Helper()
	b, err := json.Marshal([]any{c})
	if err != nil {
		t.Fatal(err)
	}
	return b
}
func certOf(c map[string]any) map[string]any {
	return c["verificationResult"].(map[string]any)["signature"].(map[string]any)["certificate"].(map[string]any)
}
func statementOf(c map[string]any) map[string]any {
	return c["verificationResult"].(map[string]any)["statement"].(map[string]any)
}

func TestVerifiedClaimPolicy(t *testing.T) {
	r, body := verificationFixture(t)
	digest := digestOf(body)
	if _, err := checkPolicy(claimJSON(t, claimFixture(r, digest)), r, digest); err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{"issuer", "subjectAlternativeName", "buildSignerURI", "buildSignerDigest", "runnerEnvironment", "sourceRepositoryURI", "sourceRepositoryDigest", "sourceRepositoryRef", "sourceRepositoryIdentifier", "sourceRepositoryOwnerURI", "sourceRepositoryOwnerIdentifier", "buildConfigURI", "buildConfigDigest", "buildTrigger", "runInvocationURI"} {
		t.Run(field, func(t *testing.T) {
			claim := claimFixture(r, digest)
			certOf(claim)[field] = "unexpected"
			if _, err := checkPolicy(claimJSON(t, claim), r, digest); !errors.Is(err, ErrPolicy) {
				t.Fatal("mismatched authenticated certificate claim accepted")
			}
		})
	}
	for _, field := range []string{"_type", "predicateType"} {
		t.Run(field, func(t *testing.T) {
			claim := claimFixture(r, digest)
			statementOf(claim)[field] = "unexpected"
			if _, err := checkPolicy(claimJSON(t, claim), r, digest); !errors.Is(err, ErrPolicy) {
				t.Fatal("mismatched statement contract accepted")
			}
		})
	}
	for _, name := range []string{"different.tar.gz", "rixa-v0.2.0-linux-arm64.tar.gz", "rixa-v0.1.0-linux-amd64.tar.gz"} {
		t.Run(name, func(t *testing.T) {
			claim := claimFixture(r, digest)
			statementOf(claim)["subject"].([]any)[0].(map[string]any)["name"] = name
			if _, err := checkPolicy(claimJSON(t, claim), r, digest); !errors.Is(err, ErrPolicy) {
				t.Fatal("wrong payload kind accepted")
			}
		})
	}
	for _, mutate := range []func(map[string]any){
		func(c map[string]any) { statementOf(c)["subject"] = []any{} },
		func(c map[string]any) {
			s := statementOf(c)["subject"].([]any)
			statementOf(c)["subject"] = append(s, s[0])
		},
		func(c map[string]any) {
			statementOf(c)["subject"].([]any)[0].(map[string]any)["digest"] = map[string]any{"sha256": strings.Repeat("b", 64)}
		},
		func(c map[string]any) {
			statementOf(c)["subject"].([]any)[0].(map[string]any)["digest"] = map[string]any{"sha256": digest, "sha512": "extra"}
		},
		func(c map[string]any) { c["verificationResult"].(map[string]any)["verifiedTimestamps"] = []any{} },
	} {
		claim := claimFixture(r, digest)
		mutate(claim)
		if _, err := checkPolicy(claimJSON(t, claim), r, digest); !errors.Is(err, ErrPolicy) {
			t.Fatal("incomplete/mismatched verified statement accepted")
		}
	}
	for _, uri := range []string{"https://github.com/AChWorks/rixa/actions/runs/not-a-run", "https://github.com/AChWorks/rixa/actions/runs/123/../../elsewhere", "https://github.com/AChWorks/rixa/actions/runs/123?query=unexpected"} {
		claim := claimFixture(r, digest)
		certOf(claim)["runInvocationURI"] = uri
		if _, err := checkPolicy(claimJSON(t, claim), r, digest); !errors.Is(err, ErrPolicy) {
			t.Fatal("malformed authenticated run context accepted")
		}
	}
	// A workflow-controlled predicate cannot substitute for authenticated claims.
	claim := claimFixture(r, digest)
	delete(certOf(claim), "sourceRepositoryIdentifier")
	statementOf(claim)["predicate"] = map[string]any{"sourceRepositoryIdentifier": "1403734531"}
	if _, err := checkPolicy(claimJSON(t, claim), r, digest); !errors.Is(err, ErrPolicy) {
		t.Fatal("unsigned-predicate policy substitution accepted")
	}
}

func TestPolicyRejectsAmbiguousOrMalformedOutput(t *testing.T) {
	r, body := verificationFixture(t)
	digest := digestOf(body)
	valid := claimJSON(t, claimFixture(r, digest))
	for _, data := range [][]byte{nil, []byte(`[]`), []byte(`{}`), []byte(`null`), append(append([]byte{}, valid...), valid...), []byte(`[{"verificationResult":{},"verificationResult":{}}]`), []byte(strings.Repeat("[", 35) + strings.Repeat("]", 35)), []byte(strings.Repeat(" ", maxOutputBytes+1))} {
		if _, err := checkPolicy(data, r, digest); !errors.Is(err, ErrPolicy) {
			t.Fatal("ambiguous/malformed output accepted")
		}
	}
}

func TestVerifySnapshotsInputsAndEnforcesPolicyArguments(t *testing.T) {
	r, body := verificationFixture(t)
	digest := digestOf(body)
	var snapshots []string
	run := func(_ context.Context, helper string, args []string, dir string) ([]byte, string, error) {
		if helper != "trusted-test-helper" {
			t.Fatal("wrong helper")
		}
		archive, bundle := args[2], args[4]
		snapshots = []string{archive, bundle}
		if archive == r.ArchivePath || bundle == r.BundlePath {
			t.Fatal("helper consumed mutable original path")
		}
		copied, err := os.ReadFile(archive)
		if err != nil || string(copied) != string(body) {
			t.Fatal("incorrect private archive snapshot")
		}
		if info, err := os.Stat(dir); err != nil || info.Mode().Perm() != 0o700 {
			t.Fatal("unsafe verifier staging")
		}
		for _, pair := range [][2]string{{"--repo", Repository}, {"--signer-workflow", Workflow}, {"--source-ref", "refs/tags/" + r.ReleaseTag}, {"--source-digest", r.SourceCommit}, {"--signer-digest", r.SourceCommit}, {"--cert-oidc-issuer", "https://token.actions.githubusercontent.com"}, {"--hostname", "github.com"}, {"--format", "json"}} {
			found := false
			for i := 0; i+1 < len(args); i++ {
				if args[i] == pair[0] && args[i+1] == pair[1] {
					found = true
				}
			}
			if !found {
				t.Fatalf("missing verification policy flag %s", pair[0])
			}
		}
		if !strings.Contains(strings.Join(args, " "), "--deny-self-hosted-runners") {
			t.Fatal("self-hosted signer accepted")
		}
		return claimJSON(t, claimFixture(r, digest)), "public diagnostic", nil
	}
	result, err := verify(context.Background(), r, func() (string, error) { return "trusted-test-helper", nil }, run)
	if err != nil || result.Schema != "rixa.artifact-verification.v1" || result.SourceRef != "refs/tags/"+r.ReleaseTag || result.Status != "verified" || result.SHA256 != digest || result.SourceCommit != r.SourceCommit {
		t.Fatalf("policy result mapping: %+v %v", result, err)
	}
	for _, path := range snapshots {
		if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
			t.Fatal("private temporary snapshot retained")
		}
	}
}

func TestVerifyDetectsReplacementAndMutation(t *testing.T) {
	for _, kind := range []string{"replace-archive", "mutate-archive", "mutate-bundle"} {
		t.Run(kind, func(t *testing.T) {
			r, body := verificationFixture(t)
			run := func(context.Context, string, []string, string) ([]byte, string, error) {
				switch kind {
				case "replace-archive":
					_ = os.Rename(r.ArchivePath, r.ArchivePath+".old")
					_ = os.WriteFile(r.ArchivePath, body, 0o600)
				case "mutate-archive":
					_ = os.WriteFile(r.ArchivePath, []byte("changed"), 0o600)
				case "mutate-bundle":
					_ = os.WriteFile(r.BundlePath, []byte(`{"changed":true}`), 0o600)
				}
				return claimJSON(t, claimFixture(r, digestOf(body))), "", nil
			}
			result, err := verify(context.Background(), r, func() (string, error) { return "test", nil }, run)
			if !errors.Is(err, ErrChanged) || result.Status == "verified" {
				t.Fatal("mutable verification input accepted")
			}
		})
	}
}

func TestVerifyMissingMalformedAndUnsafeInputsNeverCallsHelper(t *testing.T) {
	for _, kind := range []string{"missing-bundle", "empty-bundle", "bad-bundle", "multiple-bundles", "bad-release", "bad-source", "wrong-digest", "archive-symlink", "archive-fifo", "bundle-large"} {
		t.Run(kind, func(t *testing.T) {
			r, _ := verificationFixture(t)
			switch kind {
			case "missing-bundle":
				_ = os.Remove(r.BundlePath)
			case "empty-bundle":
				_ = os.WriteFile(r.BundlePath, nil, 0o600)
			case "bad-bundle":
				_ = os.WriteFile(r.BundlePath, []byte(`{"invalid"`), 0o600)
			case "multiple-bundles":
				_ = os.WriteFile(r.BundlePath, []byte(`{} {}`), 0o600)
			case "bad-release":
				r.ReleaseTag = "refs/heads/main"
			case "bad-source":
				r.SourceCommit = "not-a-source"
			case "wrong-digest":
				r.ExpectedSHA256 = strings.Repeat("b", 64)
			case "archive-symlink":
				_ = os.Rename(r.ArchivePath, r.ArchivePath+".real")
				_ = os.Symlink(r.ArchivePath+".real", r.ArchivePath)
			case "archive-fifo":
				_ = os.Remove(r.ArchivePath)
				if err := syscall.Mkfifo(r.ArchivePath, 0o600); err != nil {
					t.Fatal(err)
				}
			case "bundle-large":
				f, err := os.OpenFile(r.BundlePath, os.O_WRONLY, 0)
				if err != nil {
					t.Fatal(err)
				}
				err = f.Truncate(MaxBundleBytes + 1)
				f.Close()
				if err != nil {
					t.Fatal(err)
				}
			}
			called := false
			result, err := verify(context.Background(), r, func() (string, error) { called = true; return "", ErrHelper }, nil)
			if err == nil || result.Status == "verified" || called {
				t.Fatalf("invalid input reached verifier: %+v %v called=%v", result, err, called)
			}
		})
	}
}

func TestVerifierFailureCancellationAndMalformedSuccess(t *testing.T) {
	for _, kind := range []string{"helper-missing", "helper-failed", "malformed-success", "empty-success", "wrong-repository"} {
		t.Run(kind, func(t *testing.T) {
			r, body := verificationFixture(t)
			discover := func() (string, error) {
				if kind == "helper-missing" {
					return "", ErrHelper
				}
				return "test", nil
			}
			run := func(context.Context, string, []string, string) ([]byte, string, error) {
				switch kind {
				case "helper-failed":
					return nil, "useful public cryptographic failure", errors.New("exit1")
				case "malformed-success":
					return []byte("not-json"), "", nil
				case "empty-success":
					return []byte(`[]`), "", nil
				default:
					c := claimFixture(r, digestOf(body))
					certOf(c)["sourceRepositoryIdentifier"] = "different"
					return claimJSON(t, c), "", nil
				}
			}
			result, err := verify(context.Background(), r, discover, run)
			if err == nil || result.Status == "verified" {
				t.Fatal("helper failure or unbound output accepted")
			}
			if kind == "helper-failed" && !strings.Contains(result.Detail, "useful public cryptographic failure") {
				t.Fatal("useful diagnostics discarded")
			}
		})
	}
	r, _ := verificationFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	result, err := Verify(ctx, r)
	if !errors.Is(err, ErrCanceled) || result.Status == "verified" {
		t.Fatal("canceled verification accepted")
	}
}

func TestHelperVersionAndCredentialIsolation(t *testing.T) {
	for text, want := range map[string]bool{"gh version 2.103.3 (2026-10-27)": true, "gh version 2.104.0": true, "gh version 2.103.2": false, "gh version 2.46.0": false, "gh version 3.0.0": false, "gh version 2.103.3-custom": false, "pretend gh version 2.103.3": false} {
		if supportedVersion([]byte(text)) != want {
			t.Fatalf("helper version boundary %q", text)
		}
	}
	t.Setenv("GH_TOKEN", "private-token-canary")
	t.Setenv("GITHUB_TOKEN", "other-private-token-canary")
	t.Setenv("HTTPS_PROXY", "https://proxy-user:private-proxy-password@proxy.example.test")
	env, secrets := helperEnvironment("/private/verify")
	for _, entry := range env {
		if strings.HasPrefix(entry, "GH_TOKEN=") || strings.HasPrefix(entry, "GITHUB_TOKEN=") {
			t.Fatal("ambient token passed to offline verifier")
		}
	}
	got := cleanDiagnostic("network private-proxy-password /private/verify/archive\x1b[31m", "/private/verify", secrets)
	if strings.Contains(got, "private-proxy-password") || strings.Contains(got, "/private/verify") || strings.ContainsRune(got, '\x1b') {
		t.Fatal("sensitive/control diagnostics leaked")
	}
	if !strings.Contains(got, "network") {
		t.Fatal("useful diagnostics suppressed")
	}
}

func TestTrustedHelperRejectsWritableSearchPath(t *testing.T) {
	dir := t.TempDir()
	if err := os.Chmod(dir, 0o777); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "gh"), []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir)
	if _, err := findHelper(); !errors.Is(err, ErrHelper) {
		t.Fatal("writable helper parent accepted")
	}
}

func TestBoundedVerifierOutputProcess(t *testing.T) {
	if mode := os.Getenv("RIXA_VERIFIER_OUTPUT_CHILD"); mode != "" {
		stream := os.Stdout
		if mode == "stderr" {
			stream = os.Stderr
			_, _ = stream.WriteString("bounded-diagnostic-start\n")
		}
		block := []byte(strings.Repeat("x", 8192))
		for {
			if _, err := stream.Write(block); err != nil {
				os.Exit(0)
			}
		}
	}
	for _, mode := range []string{"stdout", "stderr"} {
		t.Run(mode, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			_, detail, err := helperCommand(ctx, os.Args[0], []string{"-test.run=^TestBoundedVerifierOutputProcess$"}, []string{"RIXA_VERIFIER_OUTPUT_CHILD=" + mode}, t.TempDir())
			if !errors.Is(err, ErrVerification) || !strings.Contains(detail, "exceeded") {
				t.Fatalf("unbounded child output not stopped: %v %q", err, detail)
			}
			if mode == "stderr" && !strings.Contains(detail, "bounded-diagnostic-start") {
				t.Fatal("useful bounded diagnostics discarded")
			}
		})
	}
}

func TestLiveVerifierRejectsRealForeignPublisher(t *testing.T) {
	archive, bundle := os.Getenv("RIXA_ARTIFACT_PROOF_ARCHIVE"), os.Getenv("RIXA_ARTIFACT_PROOF_BUNDLE")
	if archive == "" || bundle == "" {
		t.Skip("set explicit real public artifact/bundle paths for non-login cryptographic rejection proof")
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	result, err := Verify(ctx, Request{ArchivePath: archive, BundlePath: bundle, ReleaseTag: "v2.103.3", SourceCommit: "63837fb778d6a39f02be0ce76ff89f2bc8d74bdc", ExpectedSHA256: "4c806978ffce1cec70853302a56d70ac6da4f19bae7adca4e1a8ab00633dd91d"})
	if !errors.Is(err, ErrVerification) || result.Status == "verified" || result.Code != "verification-failed" {
		t.Fatalf("real foreign publisher: %+v %v", result, err)
	}
	if !strings.Contains(result.Detail, "issuer") && !strings.Contains(result.Detail, "SourceRepository") && !strings.Contains(result.Detail, "policy") {
		t.Fatalf("real policy rejection diagnostic absent: %+v", result)
	}
}
