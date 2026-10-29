// SPDX-License-Identifier: MPL-2.0

package releaseartifact

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/url"
	"regexp"
)

var runPathSyntax = regexp.MustCompile(`^/AChWorks/rixa/actions/runs/[1-9][0-9]*(/attempts/[1-9][0-9]*)?$`)

type certificateClaims struct {
	Issuer           string `json:"issuer"`
	SAN              string `json:"subjectAlternativeName"`
	SignerURI        string `json:"buildSignerURI"`
	SignerDigest     string `json:"buildSignerDigest"`
	Runner           string `json:"runnerEnvironment"`
	RepositoryURI    string `json:"sourceRepositoryURI"`
	RepositoryDigest string `json:"sourceRepositoryDigest"`
	RepositoryRef    string `json:"sourceRepositoryRef"`
	RepositoryID     string `json:"sourceRepositoryIdentifier"`
	OwnerURI         string `json:"sourceRepositoryOwnerURI"`
	OwnerID          string `json:"sourceRepositoryOwnerIdentifier"`
	ConfigURI        string `json:"buildConfigURI"`
	ConfigDigest     string `json:"buildConfigDigest"`
	Trigger          string `json:"buildTrigger"`
	RunURI           string `json:"runInvocationURI"`
}

type verifiedClaim struct {
	VerificationResult struct {
		Signature struct {
			Certificate certificateClaims `json:"certificate"`
		} `json:"signature"`
		Timestamps []json.RawMessage `json:"verifiedTimestamps"`
		Statement  struct {
			Type          string `json:"_type"`
			PredicateType string `json:"predicateType"`
			Subject       []struct {
				Name   string            `json:"name"`
				Digest map[string]string `json:"digest"`
			} `json:"subject"`
		} `json:"statement"`
	} `json:"verificationResult"`
}

// These values are read only from gh's cryptographically verified certificate
// result. Statement predicates are workflow-controlled and cannot establish
// repository ownership, source, trigger, runner or signer authority.
func checkPolicy(data []byte, request Request, digest string) (string, error) {
	if len(data) == 0 || len(data) > maxOutputBytes || uniqueJSON(data) != nil {
		return "", ErrPolicy
	}
	var claims []verifiedClaim
	if json.Unmarshal(data, &claims) != nil || len(claims) != 1 {
		return "", ErrPolicy
	}
	verified := claims[0].VerificationResult
	c := verified.Signature.Certificate
	ref := "refs/tags/" + request.ReleaseTag
	workflowURI := "https://github.com/" + Workflow + "@" + ref
	if c.Issuer != "https://token.actions.githubusercontent.com" || c.SAN != workflowURI || c.SignerURI != workflowURI || c.SignerDigest != request.SourceCommit || c.ConfigURI != workflowURI || c.ConfigDigest != request.SourceCommit || c.Runner != "github-hosted" || c.Trigger != "push" {
		return "", ErrPolicy
	}
	if c.RepositoryURI != "https://github.com/"+Repository || c.OwnerURI != "https://github.com/AChWorks" || c.RepositoryID != "1403734531" || c.OwnerID != "80599060" || c.RepositoryDigest != request.SourceCommit || c.RepositoryRef != ref {
		return "", ErrPolicy
	}
	if len(verified.Timestamps) == 0 || len(verified.Timestamps) > 16 || verified.Statement.Type != "https://in-toto.io/Statement/v1" || verified.Statement.PredicateType != "https://slsa.dev/provenance/v1" || len(verified.Statement.Subject) != 1 {
		return "", ErrPolicy
	}
	subject := verified.Statement.Subject[0]
	if subject.Name != "rixa-"+request.ReleaseTag+"-linux-amd64.tar.gz" || len(subject.Digest) != 1 || subject.Digest["sha256"] != digest {
		return "", ErrPolicy
	}
	u, err := url.Parse(c.RunURI)
	if err != nil || u.Scheme != "https" || u.Host != "github.com" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.RawPath != "" || !runPathSyntax.MatchString(u.Path) {
		return "", ErrPolicy
	}
	return c.RunURI, nil
}

// Reject ambiguous duplicate JSON properties and excessive nesting before policy
// decoding. Unrelated authenticated output fields can evolve without widening
// the finite explicit claims accepted above.
func uniqueJSON(data []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	var value func(int) error
	value = func(depth int) error {
		if depth > 32 {
			return ErrPolicy
		}
		token, err := decoder.Token()
		if err != nil {
			return err
		}
		if delimiter, ok := token.(json.Delim); ok {
			switch delimiter {
			case '{':
				seen := map[string]bool{}
				for decoder.More() {
					key, err := decoder.Token()
					if err != nil {
						return err
					}
					name, ok := key.(string)
					if !ok || seen[name] {
						return ErrPolicy
					}
					seen[name] = true
					if err = value(depth + 1); err != nil {
						return err
					}
				}
				end, err := decoder.Token()
				if err != nil || end != json.Delim('}') {
					return ErrPolicy
				}
			case '[':
				for decoder.More() {
					if err := value(depth + 1); err != nil {
						return err
					}
				}
				end, err := decoder.Token()
				if err != nil || end != json.Delim(']') {
					return ErrPolicy
				}
			default:
				return ErrPolicy
			}
		}
		return nil
	}
	if err := value(0); err != nil {
		return err
	}
	if _, err := decoder.Token(); !errors.Is(err, io.EOF) {
		return ErrPolicy
	}
	return nil
}
