// SPDX-License-Identifier: MPL-2.0
package serverinstall

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// Exercise the same durable postflight transition used by the real executor.
// Only host observations are replaced: unit tests must not touch /etc,
// systemd, PostgreSQL or a privileged manager.
func startedAddSitePostflight(t *testing.T, m *Manager, managerBytes []byte) (*addSiteJournal, *journal, *journal) {
	t.Helper()
	j := interruptedAddSiteAt(t, "start")
	j.State, j.Code, j.Message = "applying", "stage-verified", "start verified."
	j.Attempts["start"] = "verified"
	j.ManagerSHA256 = digest(managerBytes)
	j.ReviewedManagerSHA256 = j.ManagerSHA256
	if err := m.saveAddSite(j); err != nil {
		t.Fatal(err)
	}
	return j, &journal{}, &journal{}
}

func TestAddSitePostflightObservationalFailuresKeepRealJournalResumable(t *testing.T) {
	// The final client construction occurs after readiness, but still before
	// durable session intent. It must not strand an otherwise healthy start.
	for _, failAt := range []string{
		"database-identity", "configuration", "service-owner", "tls-sni", "client-setup",
	} {
		t.Run(failAt, func(t *testing.T) {
			m := testManager(t)
			managerBytes := []byte("exact-frozen-postflight-test-manager")
			j, parent, current := startedAddSitePostflight(t, m, managerBytes)
			preparationChecks, loginAttempts := 0, 0
			checks := []string{"database-identity", "configuration", "service-owner", "tls-sni", "client-setup"}
			prepare := func(_ context.Context, observed *addSiteJournal, _, _ *journal) (addSiteSessionAttempt, func(), error) {
				for _, check := range checks {
					preparationChecks++
					if observed.Attempts["postflight"] != "" {
						t.Fatal("session intent was persisted during read-only preparation")
					}
					if check == failAt {
						return nil, nil, fmt.Errorf("%w: simulated %s drift", ErrPrerequisite, check)
					}
				}
				return func(context.Context) error {
					loginAttempts++
					return nil
				}, nil, nil
			}
			r, err := m.runAddSitePostflight(context.Background(), j, parent, current, prepare)
			if !errors.Is(err, ErrState) || r.Code != "postflight-readiness-required" ||
				loginAttempts != 0 || preparationChecks == 0 {
				t.Fatalf("pre-session admission was not fail-closed and recoverable: %+v %v calls=%d", r, err, loginAttempts)
			}
			stored, err := m.loadAddSite(j.Draft.OperationID)
			if err != nil || !validAddSiteJournal(stored) || stored.State != "reconciliation-required" ||
				stored.Stage != "start" || stored.Attempts["start"] != "verified" ||
				stored.Attempts["postflight"] != "" || stored.Attempts["commit"] != "" ||
				stored.ExecutorVerifiedGeneration != 1 || addSiteRecoveryServiceRequirement(stored) != "owned-running" {
				t.Fatalf("failed observation persisted a session attempt: %+v %v", stored, err)
			}

			// Use the actual Resume locking/journal/queue transition with
			// positive simulated host/fencing observations, as these are
			// privileged and separately require physical acceptance.
			launches := 0
			m.run = func(_ context.Context, executable string, args ...string) ([]byte, error) {
				launches++
				if executable != "/usr/bin/systemd-run" ||
					!strings.Contains(strings.Join(args, " "), j.Draft.OperationID+"-2") {
					t.Fatalf("unexpected Resume effect: %s %q", executable, args)
				}
				persisted, e := m.loadAddSite(j.Draft.OperationID)
				if e != nil || persisted.ExecutorGeneration != 2 ||
					persisted.ExecutorVerifiedGeneration != 1 ||
					persisted.Attempts["postflight"] != "" ||
					persisted.Attempts["start"] != "verified" {
					t.Fatalf("Resume failed to durably carry pre-session evidence: %+v %v", persisted, e)
				}
				return nil, nil
			}
			r, err = m.resumeAddSiteWithObservations(context.Background(), j.Draft.OperationID,
				func() ([]byte, error) { return managerBytes, nil },
				func(_ context.Context, observed *addSiteJournal) bool {
					return observed.ExecutorGeneration == 1 &&
						observed.ExecutorVerifiedGeneration == 1 &&
						observed.State == "reconciliation-required" &&
						observed.Attempts["postflight"] == "" &&
						observed.Attempts["start"] == "verified"
				})
			if err != nil || r.State != "applying" || launches != 1 || loginAttempts != 0 {
				t.Fatalf("fenced Resume transition did not requeue once: %+v %v launches=%d", r, err, launches)
			}

			// The positively authenticated second generation may finish the
			// first attempted session only after a fresh read-only admission.
			queued, err := m.loadAddSite(j.Draft.OperationID)
			if err != nil || !acknowledgeAddSiteExecutor(queued) {
				t.Fatalf("second executor cannot claim retained stages: %+v %v", queued, err)
			}
			if err := m.saveAddSite(queued); err != nil {
				t.Fatal(err)
			}
			r, err = m.runAddSitePostflight(context.Background(), queued, parent, current,
				func(_ context.Context, observed *addSiteJournal, _, _ *journal) (addSiteSessionAttempt, func(), error) {
					if observed.Attempts["postflight"] != "" {
						t.Fatal("new generation inherited a fabricated login intent")
					}
					return func(context.Context) error {
						persisted, e := m.loadAddSite(j.Draft.OperationID)
						if e != nil || persisted.Stage != "postflight" ||
							persisted.Attempts["postflight"] != "attempted" {
							t.Fatalf("session dispatched without durable intent: %+v %v", persisted, e)
						}
						loginAttempts++
						return nil
					}, nil, nil
				})
			if err != nil || loginAttempts != 1 || r.Stage != "postflight" || r.State != "applying" {
				t.Fatalf("verified resumed generation did not finish postflight: %+v %v", r, err)
			}
			stored, err = m.loadAddSite(j.Draft.OperationID)
			if err != nil || stored.Attempts["postflight"] != "verified" ||
				stored.ExecutorVerifiedGeneration != 2 || stored.Attempts["commit"] != "" {
				t.Fatalf("completed session receipt invalid: %+v %v", stored, err)
			}
		})
	}
}

func TestAddSitePostflightUnknownSessionCannotBeReplayed(t *testing.T) {
	for _, tc := range []struct {
		name       string
		issueLogin bool
	}{
		{"interruption-at-session-dispatch", false},
		{"actual-http-login-request", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := testManager(t)
			managerBytes := []byte("postflight-session-test-manager")
			j, parent, current := startedAddSitePostflight(t, m, managerBytes)
			requests, preparationCalls := 0, 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/auth/login" {
					t.Errorf("unexpected endpoint %s", r.URL.Path)
				}
				requests++
				persisted, err := m.loadAddSite(j.Draft.OperationID)
				if err != nil || persisted.Attempts["postflight"] != "attempted" ||
					persisted.Stage != "postflight" {
					t.Errorf("HTTP login sent before session intent was durable: %+v %v", persisted, err)
				}
				w.WriteHeader(http.StatusServiceUnavailable)
			}))
			defer server.Close()
			client := server.Client()
			preparation := func(context.Context, *addSiteJournal, *journal, *journal) (addSiteSessionAttempt, func(), error) {
				preparationCalls++
				return func(ctx context.Context) error {
					persisted, err := m.loadAddSite(j.Draft.OperationID)
					if err != nil || persisted.Attempts["postflight"] != "attempted" {
						t.Fatalf("dispatch lacks durable receipt: %+v %v", persisted, err)
					}
					if !tc.issueLogin {
						return context.Canceled // uncertain crash at the boundary
					}
					return verifyLogin(ctx, client, server.URL,
						"private-login", "private-password", "fixture-principal", "/admin/content/")
				}, client.CloseIdleConnections, nil
			}
			r, err := m.runAddSitePostflight(context.Background(), j, parent, current, preparation)
			if !errors.Is(err, ErrState) || r.Code != "stage-outcome-unknown" ||
				preparationCalls != 1 || requests != 0 && !tc.issueLogin ||
				tc.issueLogin && requests != 1 {
				t.Fatalf("unknown session outcome misclassified: %+v %v requests=%d", r, err, requests)
			}
			unknown, err := m.loadAddSite(j.Draft.OperationID)
			if err != nil || !validAddSiteJournal(unknown) ||
				unknown.Attempts["postflight"] != "attempted" ||
				unknown.State != "reconciliation-required" {
				t.Fatalf("unknown effect not preserved: %+v %v", unknown, err)
			}
			if _, err := m.runAddSitePostflight(context.Background(), unknown, parent, current, preparation); !errors.Is(err, ErrState) {
				t.Fatalf("replayed unknown login: %v", err)
			}
			resumed, err := m.resumeAddSiteWithObservations(context.Background(), j.Draft.OperationID,
				func() ([]byte, error) { return managerBytes, nil },
				func(_ context.Context, state *addSiteJournal) bool {
					return state.Attempts["postflight"] == "" // unchanged production non-replay gate
				})
			if !errors.Is(err, ErrState) || resumed.State != "reconciliation-required" ||
				preparationCalls != 1 || requests != 0 && !tc.issueLogin ||
				tc.issueLogin && requests != 1 {
				t.Fatalf("uncertain login was requeued or retried: %+v %v requests=%d", resumed, err, requests)
			}
		})
	}
}

func TestAddSitePostflightRejectsUnverifiedGenerationBeforeAnyObservation(t *testing.T) {
	m := testManager(t)
	managerBytes := []byte("postflight-unverified-executor")
	j, parent, current := startedAddSitePostflight(t, m, managerBytes)
	j.ExecutorGeneration = 2
	j.ExecutorAcknowledged = true
	j.Attempts["executor"] = "attempted"
	if err := m.saveAddSite(j); err != nil {
		t.Fatal(err)
	}
	calls := 0
	r, err := m.runAddSitePostflight(context.Background(), j, parent, current,
		func(context.Context, *addSiteJournal, *journal, *journal) (addSiteSessionAttempt, func(), error) {
			calls++
			return func(context.Context) error {
				t.Fatal("unverified executor reached administrator session")
				return nil
			}, nil, nil
		})
	if !errors.Is(err, ErrState) || calls != 0 || r.Stage != "start" {
		t.Fatalf("unknown executor entered postflight: %+v %v calls=%d", r, err, calls)
	}
	stored, err := m.loadAddSite(j.Draft.OperationID)
	if err != nil || stored.Stage != "start" || stored.Attempts["postflight"] != "" ||
		stored.ExecutorGeneration != 2 || stored.ExecutorVerifiedGeneration != 1 {
		t.Fatalf("rejected executor mutated journal: %+v %v", stored, err)
	}
}

func TestAddSitePostflightCancelledAfterReadinessBeforeSessionIntent(t *testing.T) {
	m := testManager(t)
	j, parent, current := startedAddSitePostflight(t, m, []byte("cancelled-after-readiness-manager"))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	sessions, cleaned := 0, 0
	result, err := m.runAddSitePostflight(ctx, j, parent, current,
		func(context.Context, *addSiteJournal, *journal, *journal) (addSiteSessionAttempt, func(), error) {
			// All read-only observations succeeded, but the executor's
			// context was canceled before the durable session boundary.
			cancel()
			return func(context.Context) error {
				sessions++
				return nil
			}, func() { cleaned++ }, nil
		})
	if !errors.Is(err, ErrState) || result.Code != "postflight-readiness-required" ||
		sessions != 0 || cleaned != 1 {
		t.Fatalf("cancelled pre-session preparation was replayable or leaked a client: %+v %v", result, err)
	}
	persisted, err := m.loadAddSite(j.Draft.OperationID)
	if err != nil || persisted.State != "reconciliation-required" ||
		persisted.Stage != "start" || persisted.Attempts["postflight"] != "" ||
		persisted.Attempts["start"] != "verified" {
		t.Fatalf("cancellation wrote a non-replayable session receipt: %+v %v", persisted, err)
	}
}
