// SPDX-License-Identifier: MPL-2.0
package product

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/AChWorks/achrix"
	"github.com/AChWorks/achrix/bridge"
	"github.com/AChWorks/achrix/oauth"
)

func remoteLogRecords(t *testing.T, b *bytes.Buffer) []map[string]any {
	t.Helper()
	var records []map[string]any
	for _, line := range bytes.Split(bytes.TrimSpace(b.Bytes()), []byte("\n")) {
		if len(line) == 0 {
			continue
		}
		var item map[string]any
		if err := json.Unmarshal(line, &item); err != nil {
			t.Fatalf("malformed slog event: %v", err)
		}
		records = append(records, item)
	}
	return records
}

func remoteDiagnosticForTest(phase string, dispatched bool, result bridge.Outcome, code, ref string, cause error) bridge.Diagnostic {
	return bridge.Diagnostic{
		Request: bridge.Request{
			ID: "req-123", Caller: bridge.Caller{
				Principal: achrix.Principal("account-a"),
				ClientID:  "approved-client", ConnectionID: "grant-123",
			},
			Channel: "mcp", Operation: remoteSaveTool,
		},
		Phase: phase, Dispatched: dispatched, Outcome: result,
		Code: code, Reference: ref, Cause: cause,
	}
}

func TestRemoteBridgeDiagnosticsAtInfoLevel(t *testing.T) {
	cases := []bridge.Diagnostic{
		remoteDiagnosticForTest("complete", true, bridge.Succeeded, "", "operation-one", nil),
		remoteDiagnosticForTest("admit", false, bridge.NotExecuted, "denied", "", errors.New("policy denied")),
		remoteDiagnosticForTest("invoke", true, bridge.NotCommitted, "stale_revision", "", errors.New("revision mismatch")),
		remoteDiagnosticForTest("invoke", true, bridge.Unknown, "unknown_write", "operation-two", errors.New("backend unavailable")),
		remoteDiagnosticForTest("delivery", true, bridge.Succeeded, "delivery_failed", "operation-three", errors.New("write acknowledged but delivery failed")),
	}
	var buffer bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&buffer, &slog.HandlerOptions{Level: slog.LevelInfo}))
	for _, d := range cases {
		logRemoteBridgeDiagnostic(logger, "site-a", "https://site-a.example/mcp", d)
	}
	records := remoteLogRecords(t, &buffer)
	if len(records) != len(cases) {
		t.Fatalf("lost INFO diagnostics: count=%d want=%d", len(records), len(cases))
	}
	for i, d := range cases {
		m := records[i]
		expected := map[string]any{
			"level": "INFO", "site": "site-a", "resource": "https://site-a.example/mcp",
			"request_id": d.Request.ID, "user": string(d.Request.Caller.Principal),
			"client": d.Request.Caller.ClientID, "connection": d.Request.Caller.ConnectionID,
			"channel": d.Request.Channel, "operation": d.Request.Operation,
			"phase": d.Phase, "dispatched": d.Dispatched, "outcome": string(d.Outcome),
			"code": d.Code, "reference": d.Reference,
		}
		for key, value := range expected {
			if m[key] != value {
				t.Errorf("event %d %s: got=%v want=%v", i, key, m[key], value)
			}
		}
		if d.Cause != nil && m["cause_type"] == nil {
			t.Errorf("event %d lost cause type", i)
		}
		if m["cause_detail"] != nil {
			t.Errorf("event %d exposed debug details at INFO", i)
		}
	}
}

func TestRemoteProtectedDiagnosticCause(t *testing.T) {
	credential := "example-" + strings.Repeat("x", 12)
	privateText := "private-" + strings.Repeat("z", 12) + " with spaces"
	cause := fmt.Errorf("dial tcp db.example:5432: connection refused; "+
		"dsn=postgres://operator:%s@db.example/site; "+
		"Authorization: Bearer %s; body_html=<p>%s</p>", credential, credential, privateText)
	var buf bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
	d := remoteDiagnosticForTest("invoke", true, bridge.Unknown, "unknown_write", "operation-42", cause)
	logRemoteBridgeDiagnostic(logger, "site-a", "https://site-a.example/mcp", d)
	records := remoteLogRecords(t, &buf)
	if len(records) != 2 || records[0]["level"] != "INFO" || records[1]["level"] != "DEBUG" {
		t.Fatalf("expected structured INFO and protected DEBUG, got %v", records)
	}
	detail, ok := records[1]["cause_detail"].(string)
	if !ok || !strings.Contains(detail, "db.example:5432: connection refused") ||
		!strings.Contains(detail, "[redacted]") {
		t.Fatalf("missing actionable protected cause: %v", records[1])
	}
	if strings.Contains(buf.String(), credential) || strings.Contains(buf.String(), privateText) {
		t.Fatal("confidential diagnostic input escaped targeted scrubbing")
	}
	if records[1]["reference"] != "operation-42" || records[1]["dispatched"] != true {
		t.Fatal("protected diagnostic lost original recovery correlation")
	}
	if detail := remoteProtectedCause(errors.New("content: required field is missing")); !strings.Contains(detail, "required field is missing") {
		t.Fatal("a non-payload diagnostic lost useful field error context")
	}
	if detail := remoteProtectedCause(errors.New(strings.Repeat("line of context ", 200))); len(detail) > 1024 {
		t.Fatal("unbounded protected diagnostic")
	}
}

func TestRemoteOAuthDiagnosticsAtInfoAndDebug(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
	logRemoteOAuthDiagnostic(logger, "site-a", "https://site-a.example/oauth",
		"oauth-request-1", "consent_policy", "unavailable",
		errors.New("upstream verification: connection refused"))
	records := remoteLogRecords(t, &buf)
	if len(records) != 2 || records[0]["level"] != "INFO" ||
		records[0]["request_id"] != "oauth-request-1" ||
		records[0]["phase"] != "consent_policy" || records[0]["outcome"] != "unavailable" ||
		records[1]["level"] != "DEBUG" ||
		!strings.Contains(records[1]["cause_detail"].(string), "connection refused") {
		t.Fatalf("OAuth failure was uncorrelated or hidden: %v", records)
	}
}

func TestRemoteConsentDisclosesEnrolledClientCapability(t *testing.T) {
	view := oauth.ConsentView{
		Subject: "site-user-a", ClientID: "enrolled",
		ClientName:  "Client <img src=x>",
		FormAction:  "/oauth/authorize",
		Transaction: "one-time-tx", CSRF: "one-time-csrf",
	}
	cases := []struct {
		name                string
		canWrite            bool
		expected, forbidden string
	}{
		{"read only", false, "(read-only)", "can create and save private drafts"},
		{"draft writer", true, "can create and save private drafts", "(read-only)"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w := httptest.NewRecorder()
			renderRixaOAuthConsent(w, view, tc.canWrite)
			body := w.Body.String()
			for _, required := range []string{
				tc.expected,
				"does not grant publishing or Theme modification",
				`name="transaction" value="one-time-tx"`,
				`name="csrf" value="one-time-csrf"`,
				"Client &lt;img src=x&gt;",
			} {
				if !strings.Contains(body, required) {
					t.Errorf("required consent disclosure absent: %s", required)
				}
			}
			if strings.Contains(body, tc.forbidden) || strings.Contains(body, "<img") {
				t.Error("consent disclosed incorrect scope or did not escape client name")
			}
		})
	}
}
