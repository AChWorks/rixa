// SPDX-License-Identifier: MPL-2.0
package serverinstall

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestReviewedEvidenceEmptyTransportPersistence(t *testing.T) {
	for key := range transportKeys {
		t.Setenv(key, "")
	}
	if got := transportEnvironment(); got != nil {
		t.Fatal("empty transport was not normalized")
	}
	m := testManager(t)
	j := reviewedJournal(validDraft())
	// Reproduce the earlier non-nil empty map omitted by JSON.
	j.TransportEnv = map[string]string{}
	if e := m.save(j); e != nil {
		t.Fatal(e)
	}
	loaded, e := m.load(j.Draft.OperationID)
	if e != nil {
		t.Fatal(e)
	}
	if loaded.TransportEnv != nil {
		t.Fatal("fixture did not reproduce the omitted map")
	}
	before := captureReviewedEvidence(loaded)
	loaded.TransportEnv = transportEnvironment()
	if p := changedReviewedEvidence(before, loaded); len(p) != 0 {
		t.Fatalf("unchanged empty transport rejected after persistence: %+v", p)
	}
	loaded.TransportEnv = map[string]string{}
	if p := changedReviewedEvidence(before, loaded); len(p) != 0 {
		t.Fatalf("nil and empty transport compared differently: %+v", p)
	}
	if loaded.State != "ready" || loaded.ReviewToken != j.ReviewToken {
		t.Fatal("unchanged evidence invalidated review")
	}
}

func TestReviewedEvidenceDriftInvalidatesPersistedConfirmation(t *testing.T) {
	cases := []struct {
		name, field string
		change      func(*journal)
	}{
		{"archive", "archive", func(j *journal) { j.ArchiveSHA256 = strings.Repeat("f", 64) }},
		{"certificate", "tls_cert", func(j *journal) { j.CertSHA256 = strings.Repeat("f", 64) }},
		{"key", "tls_key", func(j *journal) { j.KeySHA256 = strings.Repeat("f", 64) }},
		{"database", "control_dsn", func(j *journal) { j.DatabaseTargets["site-a"] = "1:4" }},
		{"helper", "archive", func(j *journal) { j.HelperSHA256 = strings.Repeat("f", 64) }},
		{"transport", "archive", func(j *journal) {
			j.TransportEnv = map[string]string{"HTTPS_PROXY": "https://private-user:private-password@proxy.test"}
		}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			m := testManager(t)
			j := reviewedJournal(validDraft())
			before := captureReviewedEvidence(j)
			token := j.ReviewToken
			c.change(j)
			p := changedReviewedEvidence(before, j)
			if len(p) != 1 || p[0].Field != c.field || p[0].Code != "evidence-changed" {
				t.Fatalf("wrong drift guidance: %+v", p)
			}
			r, e := m.rejectReview(j, "review-evidence-changed", p)
			if !errors.Is(e, ErrPrerequisite) || r.State != "blocked" || r.Stage != "review" || r.Code != "review-evidence-changed" || r.ReviewToken != "" || slices.Contains(r.Actions, "apply") || !slices.Contains(r.Actions, "review") {
				t.Fatalf("drift remained applicable: %+v %v", r, e)
			}
			b, _ := json.Marshal(r)
			if strings.Contains(string(b), "private-password") || strings.Contains(string(b), "private-user") {
				t.Fatal("private transport appeared in receipt")
			}
			loaded, e := m.load(j.Draft.OperationID)
			if e != nil || loaded.State != "blocked" || loaded.Code != "review-evidence-changed" || !strings.Contains(loaded.Message, c.field) || loaded.ReviewToken != "" || loaded.Frozen != "" || len(loaded.Attempts) != 0 || loaded.ExecutorGeneration != 0 {
				t.Fatalf("drift was not durably pre-effect: %+v %v", result(j), e)
			}
			if _, e := os.Stat(filepath.Join(m.stateRoot, "installation")); !os.IsNotExist(e) {
				t.Fatal("review drift wrote installation marker")
			}
			// The earlier explicit confirmation cannot be reused after rejection.
			r, e = m.Apply(context.Background(), j.Draft.OperationID, token)
			if !errors.Is(e, ErrState) || r.Code != "review-stale" {
				t.Fatalf("rejected token reused: %+v %v", r, e)
			}
		})
	}
}
