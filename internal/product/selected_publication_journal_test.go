// SPDX-License-Identifier: MPL-2.0

package product

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

// This independent journal gate is deliberately strict: a successful browser
// return or an empty finish.request alone is insufficient evidence. The ordered
// log must describe one exact publish/withdraw journey with no new-ID replay.
const selectedPublicationBasePath = "/admin/sites/site/site-a/content"

var selectedPublicationChecks = []string{
	"real-control-login-and-cookie-isolation",
	"en-ltr-390-keyboard",
	"reviewed-intent-is-not-publication",
	"confirmed-site-wide-apply-visible-on-real-https",
	"fa-rtl-wide",
	"clear-intent-does-not-auto-withdraw",
	"lost-ack-actual-commit-inspect-same-operation",
	"site-b-preserved-and-no-js-errors",
}

var selectedPublicationSteps = []string{
	"mutation", "mutation", "mutation", "mutation",
	"effect_receipt_before_lost_ack", "browser_request_failed",
	"uncertain_result_observed", "complete",
}

type selectedPublicationJournalEntry struct {
	Type                string   `json:"type"`
	SiteID              string   `json:"site_id"`
	OperationID         string   `json:"operation_id,omitempty"`
	Path                string   `json:"path,omitempty"`
	Payload             string   `json:"payload,omitempty"`
	Status              int      `json:"status,omitempty"`
	Generation          string   `json:"generation,omitempty"`
	Error               string   `json:"error,omitempty"`
	Checks              []string `json:"checks,omitempty"`
	PublishedGeneration string   `json:"published_generation,omitempty"`
	WithdrawnGeneration string   `json:"withdrawn_generation,omitempty"`
}

func readSelectedPublicationJournal(path string) ([]string, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return nil, fmt.Errorf("journal open: %w", err)
	}
	if !info.Mode().IsRegular() || info.Mode().Perm() != 0o600 || info.Size() <= 0 || info.Size() > 1<<20 {
		return nil, fmt.Errorf("journal must be a nonempty ordinary 0600 file of at most 1MiB")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("journal read: %w", err)
	}
	return validateSelectedPublicationJournal(data)
}

func validateSelectedPublicationJournal(data []byte) ([]string, error) {
	if len(data) == 0 || len(data) > 1<<20 || !bytes.HasSuffix(data, []byte("\n")) {
		return nil, fmt.Errorf("journal absent, oversized, or truncated")
	}
	scanner := bufio.NewScanner(io.LimitReader(bytes.NewReader(data), 1<<20))
	scanner.Buffer(make([]byte, 4096), 64<<10)
	identities := make([]string, 0, 4)
	seen := make(map[string]string, 4)
	var selectedID, selectedRevision, selectedRoute, firstApplyGeneration, finalGeneration string
	step := 0
	for scanner.Scan() {
		if step >= len(selectedPublicationSteps) {
			return nil, fmt.Errorf("journal contains an unexpected action after completion")
		}
		var event selectedPublicationJournalEntry
		if err := json.Unmarshal(scanner.Bytes(), &event); err != nil {
			return nil, fmt.Errorf("journal event %d invalid JSON: %w", step+1, err)
		}
		if event.SiteID != "site-a" || event.Type != selectedPublicationSteps[step] {
			return nil, fmt.Errorf("journal action %d has unexpected type or site: %s", step+1, event.Type)
		}
		switch step {
		case 0, 1, 2, 3:
			path := []string{"/publication-intent", "/publish", "/publication-intent-clear", "/publish"}[step]
			if event.Path != selectedPublicationBasePath+path || !validOperationID(event.OperationID) ||
				event.Status != 0 || event.Generation != "" || event.Error != "" {
				return nil, fmt.Errorf("mutation %d has invalid path, operation, or metadata", step+1)
			}
			var payload map[string]string
			if err := json.Unmarshal([]byte(event.Payload), &payload); err != nil ||
				payload["operation_id"] != event.OperationID || !validEditorialID(payload["id"]) {
				return nil, fmt.Errorf("mutation %d has invalid exact payload", step+1)
			}
			if _, exists := seen[event.OperationID]; exists {
				return nil, fmt.Errorf("unexpected operation identity replay in this four-action journey")
			}
			seen[event.OperationID] = event.Payload
			identities = append(identities, event.OperationID)
			if step == 0 {
				selectedID = payload["id"]
				selectedRevision = payload["revision"]
				if selectedRevision == "" || payload["expected_head"] != selectedRevision {
					return nil, fmt.Errorf("selected publication intent revision invalid")
				}
			} else if payload["id"] != selectedID {
				return nil, fmt.Errorf("publication mutations target different content IDs")
			}
			if step == 1 {
				if payload["expected_intent_revision"] != selectedRevision || payload["route"] == "" {
					return nil, fmt.Errorf("first publication mutation lacks selected intent/route")
				}
				selectedRoute = payload["route"]
			}
			if step == 3 {
				if payload["expected_intent_revision"] != "0" || payload["route"] != selectedRoute {
					return nil, fmt.Errorf("withdrawal mutation lacks cleared intent or matching route")
				}
				firstApplyGeneration = payload["expected_generation"]
			}
		case 4:
			if event.OperationID != identities[3] || event.Status != 200 ||
				!validEditorialID(event.Generation) || event.Path != "" || event.Payload != "" {
				return nil, fmt.Errorf("actual committed 200 receipt is missing or mismatched")
			}
			finalGeneration = event.Generation
		case 5:
			if event.OperationID != identities[3] || event.Error == "" || len(event.Error) > 512 ||
				event.Path != "" || event.Payload != "" {
				return nil, fmt.Errorf("exact withdrawal browser failure evidence missing")
			}
		case 6:
			if event.OperationID != identities[3] || event.Path != "" || event.Payload != "" {
				return nil, fmt.Errorf("uncertain-result UI observation missing or mismatched")
			}
		case 7:
			if event.OperationID != identities[3] || !reflect.DeepEqual(event.Checks, selectedPublicationChecks) ||
				!validEditorialID(event.PublishedGeneration) || event.PublishedGeneration != firstApplyGeneration ||
				event.WithdrawnGeneration != finalGeneration ||
				event.PublishedGeneration == event.WithdrawnGeneration {
				return nil, fmt.Errorf("terminal completion missing exact checks, operation or generation binding")
			}
		}
		step++
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("journal scan: %w", err)
	}
	if step != len(selectedPublicationSteps) || len(identities) != 4 {
		return nil, fmt.Errorf("incomplete publication journey: %d of %d actions", step, len(selectedPublicationSteps))
	}
	return identities, nil
}

func publicationJournalExample(t *testing.T) []selectedPublicationJournalEntry {
	t.Helper()
	id := strings.Repeat("A", 26)
	published := strings.Repeat("B", 26)
	withdrawn := strings.Repeat("C", 26)
	ids := []string{
		"11111111-1111-4111-8111-111111111111",
		"22222222-2222-4222-8222-222222222222",
		"33333333-3333-4333-8333-333333333333",
		"44444444-4444-4444-8444-444444444444",
	}
	payloads := []map[string]string{
		{"operation_id": ids[0], "id": id, "expected_head": "56", "expected_publication_version": "1", "revision": "56"},
		{"operation_id": ids[1], "id": id, "expected_head": "56", "expected_publication_version": "2", "expected_intent_revision": "56", "expected_generation": published, "route": "/posts/control-browser-proof/"},
		{"operation_id": ids[2], "id": id, "expected_head": "56", "expected_publication_version": "2", "revision": "56"},
		{"operation_id": ids[3], "id": id, "expected_head": "56", "expected_publication_version": "3", "expected_intent_revision": "0", "expected_generation": published, "route": "/posts/control-browser-proof/"},
	}
	paths := []string{"/publication-intent", "/publish", "/publication-intent-clear", "/publish"}
	events := make([]selectedPublicationJournalEntry, 0, 8)
	for i, input := range payloads {
		raw, err := json.Marshal(input)
		if err != nil {
			t.Fatal(err)
		}
		events = append(events, selectedPublicationJournalEntry{
			Type: "mutation", SiteID: "site-a", OperationID: ids[i],
			Path: selectedPublicationBasePath + paths[i], Payload: string(raw),
		})
	}
	events = append(events,
		selectedPublicationJournalEntry{Type: "effect_receipt_before_lost_ack", SiteID: "site-a", OperationID: ids[3], Status: 200, Generation: withdrawn},
		selectedPublicationJournalEntry{Type: "browser_request_failed", SiteID: "site-a", OperationID: ids[3], Error: "net::ERR_FAILED"},
		selectedPublicationJournalEntry{Type: "uncertain_result_observed", SiteID: "site-a", OperationID: ids[3]},
		selectedPublicationJournalEntry{Type: "complete", SiteID: "site-a", OperationID: ids[3], Checks: append([]string(nil), selectedPublicationChecks...), PublishedGeneration: published, WithdrawnGeneration: withdrawn},
	)
	return events
}

func encodePublicationJournal(t *testing.T, entries []selectedPublicationJournalEntry) []byte {
	t.Helper()
	var buf bytes.Buffer
	for _, entry := range entries {
		if err := json.NewEncoder(&buf).Encode(entry); err != nil {
			t.Fatal(err)
		}
	}
	return buf.Bytes()
}

func TestSelectedPublicationJournalRejectsIncompleteEvidence(t *testing.T) {
	good := publicationJournalExample(t)
	if ids, err := validateSelectedPublicationJournal(encodePublicationJournal(t, good)); err != nil || len(ids) != 4 {
		t.Fatalf("valid exact fixture journal rejected: ids=%d err=%v", len(ids), err)
	}
	tests := []struct {
		name string
		edit func([]selectedPublicationJournalEntry) []selectedPublicationJournalEntry
	}{
		{"missing_browser_check", func(e []selectedPublicationJournalEntry) []selectedPublicationJournalEntry {
			e[7].Checks = e[7].Checks[:7]
			return e
		}},
		{"duplicate_browser_check", func(e []selectedPublicationJournalEntry) []selectedPublicationJournalEntry {
			e[7].Checks[7] = e[7].Checks[6]
			return e
		}},
		{"extra_fresh_operation", func(e []selectedPublicationJournalEntry) []selectedPublicationJournalEntry {
			extra := e[3]
			extra.OperationID = "55555555-5555-4555-8555-555555555555"
			extra.Payload = strings.ReplaceAll(extra.Payload, e[3].OperationID, extra.OperationID)
			return append(e[:4:4], append([]selectedPublicationJournalEntry{extra}, e[4:]...)...)
		}},
		{"missing_receipt", func(e []selectedPublicationJournalEntry) []selectedPublicationJournalEntry {
			return append(e[:4:4], e[5:]...)
		}},
		{"mismatched_receipt", func(e []selectedPublicationJournalEntry) []selectedPublicationJournalEntry {
			e[4].OperationID = e[2].OperationID
			return e
		}},
		{"bad_receipt_status", func(e []selectedPublicationJournalEntry) []selectedPublicationJournalEntry {
			e[4].Status = 202
			return e
		}},
		{"missing_request_failure", func(e []selectedPublicationJournalEntry) []selectedPublicationJournalEntry {
			return append(e[:5:5], e[6:]...)
		}},
		{"missing_uncertain_UI", func(e []selectedPublicationJournalEntry) []selectedPublicationJournalEntry {
			return append(e[:6:6], e[7:]...)
		}},
		{"unexpected_action", func(e []selectedPublicationJournalEntry) []selectedPublicationJournalEntry {
			e[2].Path = selectedPublicationBasePath + "/publish"
			return e
		}},
		{"missing_completion", func(e []selectedPublicationJournalEntry) []selectedPublicationJournalEntry { return e[:7] }},
		{"duplicate_completion", func(e []selectedPublicationJournalEntry) []selectedPublicationJournalEntry { return append(e, e[7]) }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			input := publicationJournalExample(t)
			if _, err := validateSelectedPublicationJournal(encodePublicationJournal(t, tt.edit(input))); err == nil {
				t.Fatal("invalid publication journey was accepted")
			}
		})
	}
	t.Run("truncated_json", func(t *testing.T) {
		b := encodePublicationJournal(t, good)
		if _, err := validateSelectedPublicationJournal(b[:len(b)-1]); err == nil {
			t.Fatal("truncated journal was accepted")
		}
	})
}

func TestSelectedPublicationFinishMarkerIsRequired(t *testing.T) {
	root := t.TempDir()
	marker := filepath.Join(root, "finish.request")
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if err := awaitBrowserMarker(ctx, marker); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("missing browser completion marker was accepted or not diagnosed: %v", err)
	}
	if err := os.WriteFile(marker, []byte("not empty"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := awaitBrowserMarker(context.Background(), marker); err == nil {
		t.Fatal("nonempty completion marker was accepted")
	}
	if err := os.WriteFile(marker, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := awaitBrowserMarker(context.Background(), marker); err != nil {
		t.Fatalf("empty ordinary marker was rejected: %v", err)
	}
}
