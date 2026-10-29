// SPDX-License-Identifier: MPL-2.0

package releaseartifact

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// This opt-in proof uses the authenticated official cli/cli release recorded in
// #29. It tests the real isolated helper transport, not a fabricated signature.
// The production Rixa policy remains fixed; only this test selects cli/cli.
func TestGenuineHelperTransportAndForeignPolicy(t *testing.T) {
	root := os.Getenv("RIXA_ATTESTATION_PROOF_DIR")
	if root == "" {
		t.Skip("set RIXA_ATTESTATION_PROOF_DIR to the verified upstream fixture directory")
	}
	helper, err := findHelper()
	if err != nil {
		t.Fatal("trusted supported helper is required for this explicit proof")
	}
	const source = "63837fb778d6a39f02be0ce76ff89f2bc8d74bdc"
	const digest = "4c806978ffce1cec70853302a56d70ac6da4f19bae7adca4e1a8ab00633dd91d"
	archive := filepath.Join(root, "gh_2.103.3_linux_amd64.tar.gz")
	bundle := filepath.Join(root, "gh.bundle.json")
	body, err := os.ReadFile(archive)
	if err != nil || digestOf(body) != digest {
		t.Fatal("upstream fixture digest does not match the recorded official release")
	}
	args := []string{"attestation", "verify", archive, "--bundle", bundle,
		"--repo", "cli/cli", "--source-ref", "refs/heads/trunk", "--source-digest", source,
		"--signer-workflow", "cli/cli/.github/workflows/deployment.yml", "--signer-digest", source,
		"--deny-self-hosted-runners", "--predicate-type", "https://slsa.dev/provenance/v1",
		"--cert-oidc-issuer", "https://token.actions.githubusercontent.com", "--hostname", "github.com", "--format", "json"}
	ctx, cancel := context.WithTimeout(context.Background(), verificationTimeout)
	defer cancel()
	out, detail, err := runHelper(ctx, helper, args, t.TempDir())
	if err != nil {
		t.Fatalf("genuine upstream verification through the isolated runner failed: %v; %s", err, detail)
	}
	var claims []verifiedClaim
	if json.Unmarshal(out, &claims) != nil || len(claims) != 1 {
		t.Fatal("actual helper returned an unexpected verified claim shape")
	}
	v := claims[0].VerificationResult
	c := v.Signature.Certificate
	if c.RepositoryURI != "https://github.com/cli/cli" || c.RepositoryID != "212613049" || c.OwnerID != "59704711" || c.RepositoryDigest != source || c.RepositoryRef != "refs/heads/trunk" || c.Runner != "github-hosted" || len(v.Timestamps) == 0 {
		t.Fatal("authenticated upstream identity/digest was not retained")
	}
	// The upstream workflow signs several architectures together; the Rixa
	// publisher intentionally signs one archive and keeps its stricter policy.
	matches := 0
	for _, subject := range v.Statement.Subject {
		if subject.Name == "gh_2.103.3_linux_amd64.tar.gz" && subject.Digest["sha256"] == digest {
			matches++
		}
	}
	if matches != 1 {
		t.Fatal("the authenticated upstream statement does not bind this exact archive")
	}
	if _, err := checkPolicy(out, Request{ReleaseTag: "v2.103.3", SourceCommit: source}, digest); !errors.Is(err, ErrPolicy) {
		t.Fatal("genuine foreign producer passed the fixed Rixa policy")
	}
}
