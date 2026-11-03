// SPDX-License-Identifier: MPL-2.0
package serverinstall

import (
	"bytes"
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
)

// Interrupted stages retain their first positively authenticated executor's
// stage receipts while a newer executor has only an unacknowledged intent.
func interruptedAddSiteAt(t *testing.T, stage string) *addSiteJournal {
	t.Helper()
	j := applyingAddSiteFixture()
	j.ExecutorAcknowledged = true
	j.ExecutorVerifiedGeneration = 1
	j.Attempts["executor"] = "verified"
	j.Stage, j.State, j.Code = stage, "reconciliation-required", "stage-outcome-unknown"
	found := false
	for _, s := range addSiteStages {
		if s == stage {
			found = true
			j.Attempts[s] = "attempted"
			break
		}
		j.Attempts[s] = "verified"
	}
	if !found {
		t.Fatalf("unknown test stage %s", stage)
	}
	if j.Attempts["bootstrap-site-b"] == "verified" {
		j.SiteBPrincipal = strings.Repeat("A", 26)
	}
	if !validAddSiteJournal(j) {
		t.Fatalf("invalid interrupted %s fixture", stage)
	}
	return j
}

func TestAddSiteResumePreservesPriorStagesAcrossVerifiedExecutorGenerations(t *testing.T) {
	for _, stage := range []string{"prepare", "migrate", "bootstrap-site-b", "activate", "start", "commit"} {
		t.Run(stage, func(t *testing.T) {
			m := testManager(t)
			j := interruptedAddSiteAt(t, stage)
			prior := make(map[string]string, len(j.Attempts))
			for k, v := range j.Attempts {
				prior[k] = v
			}
			if err := m.saveAddSite(j); err != nil {
				t.Fatal(err)
			}
			launches := 0
			m.run = func(_ context.Context, name string, args ...string) ([]byte, error) {
				launches++
				if name != "/usr/bin/systemd-run" ||
					!strings.Contains(strings.Join(args, " "), "rixa-add-site-"+j.Draft.OperationID+"-2") {
					t.Fatalf("unexpected executor launch: %s %q", name, args)
				}
				observed, err := m.loadAddSite(j.Draft.OperationID)
				if err != nil || observed.ExecutorGeneration != 2 ||
					observed.ExecutorVerifiedGeneration != 1 || observed.ExecutorAcknowledged ||
					observed.State != "applying" ||
					observed.Attempts["executor"] != "attempted" ||
					observed.Stage != stage {
					t.Fatalf("requeue did not persist prior intent before launch: %+v %v", observed, err)
				}
				for s, v := range prior {
					if s != "executor" && observed.Attempts[s] != v {
						t.Fatalf("prior stage receipt %s mutated: %s vs %s", s, observed.Attempts[s], v)
					}
				}
				return nil, nil
			}
			r, err := m.queueAddSite(context.Background(), j)
			if err != nil || r.State != "applying" || launches != 1 {
				t.Fatalf("legitimate second-generation queue rejected: %+v %v", r, err)
			}
			queued, err := m.loadAddSite(j.Draft.OperationID)
			if err != nil || !queued.ExecutorAcknowledged ||
				queued.ExecutorVerifiedGeneration != 1 ||
				queued.Attempts["executor"] != "attempted" {
				t.Fatalf("parent falsely verified new child, or journal invalid: %+v %v", queued, err)
			}
			if !addSiteExecutorAdmission(queued) {
				t.Fatal("authenticated new child cannot enter")
			}
			if !acknowledgeAddSiteExecutor(queued) || queued.ExecutorVerifiedGeneration != 2 ||
				queued.Attempts["executor"] != "verified" ||
				queued.Stage != stage {
				t.Fatal("verified new generation did not bind the retained stage")
			}
			if err := m.saveAddSite(queued); err != nil {
				t.Fatalf("self-observed ack not durable: %v", err)
			}
			final, err := m.loadAddSite(j.Draft.OperationID)
			if err != nil || !reflect.DeepEqual(final, queued) || launches != 1 {
				t.Fatalf("continuation lost existing stage or replayed launch: %+v %v", final, err)
			}
		})
	}
}

// Gen2 can have an acknowledged, quiescent unit but no self-verified child.
// Gen3 must remain possible while preserving Gen1's positive stage receipts.
func TestAddSiteResumeRepeatedPreAdmissionInterruption(t *testing.T) {
	m := testManager(t)
	j := interruptedAddSiteAt(t, "migrate")
	if err := m.saveAddSite(j); err != nil {
		t.Fatal(err)
	}
	launched := 0
	m.run = func(context.Context, string, ...string) ([]byte, error) { launched++; return nil, nil }
	for generation := 2; generation <= 4; generation++ {
		// A prior acknowledgement alone never establishes the new child's
		// identity, but an independently quiesced unit makes requeue legal.
		r, err := m.queueAddSite(context.Background(), j)
		if err != nil || r.State != "applying" {
			t.Fatalf("gen%d rejected: %v", generation, err)
		}
		recovered, err := m.loadAddSite(j.Draft.OperationID)
		if err != nil || recovered.ExecutorGeneration != generation ||
			recovered.ExecutorVerifiedGeneration != 1 ||
			recovered.Attempts["migrate"] != "attempted" ||
			recovered.Attempts["prepare"] != "verified" ||
			recovered.Attempts["executor"] != "attempted" ||
			!recovered.ExecutorAcknowledged {
			t.Fatalf("gen%d lost older stage ownership: %+v %v", generation, recovered, err)
		}
		j = recovered
	}
	if launched != 3 {
		t.Fatal("unexpected number of verified dispatch attempts")
	}
	if !acknowledgeAddSiteExecutor(j) || j.ExecutorVerifiedGeneration != 4 {
		t.Fatal("final self-authenticated child cannot adopt retained stage")
	}
	if err := m.saveAddSite(j); err != nil {
		t.Fatal(err)
	}
}

func TestAddSiteLostParentAcknowledgementAllowsOnlyAuthenticatedChild(t *testing.T) {
	m := testManager(t)
	j := reviewedAddSiteFixture()
	j.State, j.Stage, j.Code = "applying", "executor", "queued"
	j.ReviewToken = ""
	j.Frozen = addSiteDraftDigest(j.Draft)
	j.ManagerSHA256 = j.ReviewedManagerSHA256
	launches := 0
	m.run = func(_ context.Context, name string, args ...string) ([]byte, error) {
		if name != "/usr/bin/systemd-run" {
			t.Fatalf("unexpected host command %s", name)
		}
		launches++
		b, err := m.loadAddSite(j.Draft.OperationID)
		if err != nil || b.State != "applying" || b.ExecutorGeneration != 1 ||
			b.ExecutorVerifiedGeneration != 0 || b.ExecutorAcknowledged ||
			b.Attempts["executor"] != "attempted" {
			t.Fatalf("lost-ack child not durably attempted before dispatch: %+v %v", b, err)
		}
		return nil, ErrPrerequisite // systemd might have launched successfully
	}
	// The real Apply holds the lifecycle lock until the parent records the
	// unknown response. The child may acquire the lock only afterward.
	unlock, err := m.lock(context.Background(), false)
	if err != nil {
		t.Fatal(err)
	}
	result, runErr := m.queueAddSite(context.Background(), j)
	unlock()
	if !errors.Is(runErr, ErrState) || result.Code != "executor-acknowledgement-unknown" || launches != 1 {
		t.Fatalf("lost acknowledgement was assumed successful: %+v %v", result, runErr)
	}
	childLock, err := m.lock(context.Background(), false)
	if err != nil {
		t.Fatalf("child cannot acquire released parent lock: %v", err)
	}
	defer childLock()
	pending, err := m.loadAddSite(j.Draft.OperationID)
	if err != nil || pending.State != "reconciliation-required" || !addSiteExecutorAdmission(pending) {
		t.Fatalf("authentic executor incorrectly denied after parent lost ack: %+v %v", pending, err)
	}
	corrupt := *pending
	corrupt.Code = "stage-outcome-unknown"
	if addSiteExecutorAdmission(&corrupt) || acknowledgeAddSiteExecutor(&corrupt) {
		t.Fatal("unrelated reconciliation accepted as an unacknowledged launch")
	}
	corrupt = *pending
	corrupt.ExecutorVerifiedGeneration = corrupt.ExecutorGeneration
	if addSiteExecutorAdmission(&corrupt) {
		t.Fatal("already-verified generation was admitted as an unobserved child")
	}
	if !acknowledgeAddSiteExecutor(pending) {
		t.Fatal("exact positively observed child could not acknowledge itself")
	}
	if err = m.saveAddSite(pending); err != nil {
		t.Fatal(err)
	}
	recovered, err := m.loadAddSite(j.Draft.OperationID)
	if err != nil || recovered.State != "applying" ||
		recovered.ExecutorVerifiedGeneration != 1 ||
		!recovered.ExecutorAcknowledged || recovered.Attempts["executor"] != "verified" ||
		launches != 1 {
		t.Fatalf("no durable child acknowledgement or repeated launch: %+v %v", recovered, err)
	}
}

func TestAddSiteResumeEmptyFirstGenerationWithVerifiedParentAck(t *testing.T) {
	m := testManager(t)
	j := reviewedAddSiteFixture()
	j.State, j.Stage, j.Code = "applying", "executor", "queued"
	j.ReviewToken = ""
	j.Frozen = addSiteDraftDigest(j.Draft)
	j.ManagerSHA256 = j.ReviewedManagerSHA256
	count := 0
	m.run = func(context.Context, string, ...string) ([]byte, error) { count++; return nil, nil }
	if _, err := m.queueAddSite(context.Background(), j); err != nil {
		t.Fatal(err)
	}
	previous, err := m.loadAddSite(j.Draft.OperationID)
	if err != nil || previous.ExecutorVerifiedGeneration != 0 || !previous.ExecutorAcknowledged {
		t.Fatalf("bad initial queue: %v", err)
	}
	if _, err = m.queueAddSite(context.Background(), previous); err != nil {
		t.Fatalf("quiesced acknowledged but stage-free executor cannot retry: %v", err)
	}
	second, err := m.loadAddSite(j.Draft.OperationID)
	if err != nil || second.ExecutorGeneration != 2 || second.ExecutorVerifiedGeneration != 0 ||
		second.Attempts["executor"] != "attempted" || count != 2 {
		t.Fatalf("unverified stage-free executor invented stage ownership: %+v %v", second, err)
	}
}

func TestAddSiteJournalRejectsForgedExecutorGenerationReceipts(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(*addSiteJournal)
	}{
		{"verified-current-without-proof", func(j *addSiteJournal) { j.ExecutorVerifiedGeneration = 0 }},
		{"attempted-current-already-verified", func(j *addSiteJournal) { j.Attempts["executor"] = "attempted" }},
		{"stages-with-unverified-parent", func(j *addSiteJournal) {
			j.ExecutorGeneration = 2
			j.ExecutorVerifiedGeneration = 0
			j.Attempts["executor"] = "attempted"
			j.ExecutorAcknowledged = false
		}},
		{"forged-future-generation", func(j *addSiteJournal) { j.ExecutorVerifiedGeneration = 3 }},
		{"verified-with-old-generation-receipt", func(j *addSiteJournal) {
			j.ExecutorGeneration = 2
			j.Attempts["executor"] = "verified"
			j.ExecutorVerifiedGeneration = 1
		}},
		{"verified-without-acknowledgement", func(j *addSiteJournal) { j.ExecutorAcknowledged = false }},
		{"unverified-all-generations-with-stages", func(j *addSiteJournal) {
			j.ExecutorGeneration = 3
			j.ExecutorVerifiedGeneration = 0
			j.Attempts["executor"] = "attempted"
			j.ExecutorAcknowledged = false
		}},
		{"terminal-with-unverified-generation", func(j *addSiteJournal) {
			for _, stage := range addSiteStages {
				j.Attempts[stage] = "verified"
			}
			j.Stage = "commit"
			j.Draft.SiteBPassword = ""
			j.State = "installed"
			j.ExecutorGeneration = 2
			j.ExecutorVerifiedGeneration = 1
			j.Attempts["executor"] = "attempted"
			j.ExecutorAcknowledged = false
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			j := interruptedAddSiteAt(t, "activate")
			if tc.name == "terminal-with-unverified-generation" {
				j.SiteBPrincipal = strings.Repeat("A", 26)
			}
			tc.mutate(j)
			if validAddSiteJournal(j) {
				t.Fatal("forged executor generation receipt accepted")
			}
		})
	}
}

func TestAddSiteRecoveryStartRequiresPositiveOwnedLiveService(t *testing.T) {
	j := interruptedAddSiteAt(t, "start")
	if got := addSiteRecoveryServiceRequirement(j); got != "owned-running" {
		t.Fatalf("unknown start incorrectly requires a disabled service: %s", got)
	}
	candidate, env := []byte("two-site-config"), []byte("two-site-env")
	for _, tc := range []struct {
		name                         string
		config, environment          []byte
		state                        string
		unitOwned, loadedOwned, want bool
	}{
		{"owned-running", candidate, env, "enabled", true, true, true},
		{"service-inactive", candidate, env, "disabled", true, false, false},
		{"foreign-loaded-executable", candidate, env, "enabled", true, false, false},
		{"unowned-unit", candidate, env, "enabled", false, true, false},
		{"different-config", []byte("operator-edited"), env, "enabled", true, true, false},
		{"different-env", candidate, []byte("changed-env"), "enabled", true, true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			accepted := addSiteRunningObservationAccepted(tc.config, tc.environment, candidate, env, tc.state, tc.unitOwned, tc.loadedOwned)
			if accepted != tc.want {
				t.Fatalf("unexpected start admission=%t want=%t", accepted, tc.want)
			}
		})
	}
	delete(j.Attempts, "start")
	j.Stage = "check"
	if addSiteRecoveryServiceRequirement(j) != "quiesced" {
		t.Fatal("pre-start stage did not retain quiescence requirement")
	}
	delete(j.Attempts, "stop")
	j.Stage = "bootstrap-site-b"
	if addSiteRecoveryServiceRequirement(j) != "original-running" {
		t.Fatal("pre-stop stage no longer requires original healthy service")
	}
}

func TestAddSiteExecutorFenceRejectsUnknownGenerationDespiteOldProof(t *testing.T) {
	m := testManager(t)
	j := interruptedAddSiteAt(t, "migrate")
	j.ExecutorGeneration = 2
	j.Attempts["executor"] = "attempted"
	j.ExecutorAcknowledged = false
	if !validAddSiteJournal(j) {
		t.Fatal("valid previous-stage receipt rejected before fence")
	}
	calls := 0
	m.run = func(_ context.Context, name string, args ...string) ([]byte, error) {
		calls++
		if name != "/usr/bin/systemctl" || !strings.Contains(strings.Join(args, " "), addSiteExecutorName(j)) {
			t.Fatalf("unexpected observation %s %q", name, args)
		}
		return []byte("LoadState=not-found\nActiveState=inactive\nMainPID=0\nControlGroup=\n"), ErrPrerequisite
	}
	if m.addSiteExecutorFenced(context.Background(), j) {
		t.Fatal("previous generation proof falsely acknowledged a new unknown launch")
	}
	if calls != 0 {
		t.Fatal("unknown child was even probed as an authorized resumed generation")
	}
	j.ExecutorAcknowledged = true // positive parent response
	if !m.addSiteExecutorFenced(context.Background(), j) || calls != 1 {
		t.Fatal("positively acknowledged quiescent unit was not accepted")
	}
	j.ExecutorAcknowledged = false
	j.ExecutorVerifiedGeneration = 2
	j.Attempts["executor"] = "verified"
	if !m.addSiteExecutorFenced(context.Background(), j) || calls != 2 {
		t.Fatal("positively self-verified quiescent unit was not accepted")
	}
}

func TestAddSiteResumeNeverReplaysUnknownPostflight(t *testing.T) {
	j := interruptedAddSiteAt(t, "postflight")
	if !validAddSiteJournal(j) {
		t.Fatal("bad postflight fixture")
	}
	if j.Attempts["postflight"] != "attempted" {
		t.Fatal("uncertain login/logout receipt did not survive")
	}
	// Explicitly denied by addSiteResumable before any queue, even when
	// independently observed service could be healthy.
	if !bytes.Equal([]byte(addSiteRecoveryServiceRequirement(j)), []byte("owned-running")) {
		t.Fatal("unknown postflight should be after a verified start")
	}
}

// This exercises the actual Resume admission/serialization/queue implementation,
// with only the immutable-manager/host-health probes substituted by bounded
// positive fixture observations (those probes need root-owned /etc and /opt).
func TestAddSiteResumeEntryPointDurablyRequeuesInterruptedStages(t *testing.T) {
	for _, stage := range []string{"prepare", "migrate", "bootstrap-site-b", "activate", "commit"} {
		t.Run(stage, func(t *testing.T) {
			m := testManager(t)
			j := interruptedAddSiteAt(t, stage)
			managerBytes := []byte("test-only-frozen-manager-image")
			j.ManagerSHA256 = digest(managerBytes)
			j.ReviewedManagerSHA256 = j.ManagerSHA256
			if err := m.saveAddSite(j); err != nil {
				t.Fatal(err)
			}
			admissions, launches := 0, 0
			m.run = func(_ context.Context, executable string, args ...string) ([]byte, error) {
				launches++
				if executable != "/usr/bin/systemd-run" ||
					!strings.Contains(strings.Join(args, " "), "rixa-add-site-"+j.Draft.OperationID+"-2") {
					t.Fatalf("resume dispatched unexpected command %s %q", executable, args)
				}
				persisted, err := m.loadAddSite(j.Draft.OperationID)
				if err != nil || persisted.ExecutorGeneration != 2 ||
					persisted.ExecutorVerifiedGeneration != 1 ||
					persisted.Attempts["executor"] != "attempted" ||
					persisted.Attempts[stage] != "attempted" || persisted.ExecutorAcknowledged {
					t.Fatalf("resume effect before durable intent: %+v %v", persisted, err)
				}
				// Resume still holds the native lifecycle lock during dispatch.
				if release, err := m.lock(context.Background(), false); !errors.Is(err, ErrBusy) {
					if err == nil {
						release()
					}
					t.Fatalf("resume did not serialize executor dispatch: %v", err)
				}
				return nil, nil
			}
			r, err := m.resumeAddSiteWithObservations(context.Background(), j.Draft.OperationID,
				func() ([]byte, error) { return managerBytes, nil },
				func(_ context.Context, observed *addSiteJournal) bool {
					admissions++
					return validAddSiteJournal(observed) &&
						observed.ExecutorGeneration == 1 &&
						observed.ExecutorVerifiedGeneration == 1 &&
						observed.State == "reconciliation-required"
				})
			if err != nil || r.State != "applying" || admissions != 1 || launches != 1 {
				t.Fatalf("Resume entrypoint did not queue exactly once: %+v %v", r, err)
			}
			persisted, err := m.loadAddSite(j.Draft.OperationID)
			if err != nil || persisted.ExecutorGeneration != 2 ||
				persisted.ExecutorVerifiedGeneration != 1 || !persisted.ExecutorAcknowledged ||
				persisted.Attempts["executor"] != "attempted" ||
				persisted.Attempts[stage] != "attempted" {
				t.Fatalf("Resume entrypoint lost protected stage: %+v %v", persisted, err)
			}
			r, err = m.resumeAddSiteWithObservations(context.Background(), j.Draft.OperationID,
				func() ([]byte, error) { return []byte("different-image"), nil },
				func(context.Context, *addSiteJournal) bool {
					t.Fatal("must not observe host after manager drift")
					return true
				})
			if !errors.Is(err, ErrManagerChanged) || launches != 1 || r.Code != "manager-changed" {
				t.Fatalf("stale manager could requeue: %+v %v", r, err)
			}
			r, err = m.resumeAddSiteWithObservations(context.Background(), j.Draft.OperationID,
				func() ([]byte, error) { return managerBytes, nil },
				func(context.Context, *addSiteJournal) bool { return false })
			if !errors.Is(err, ErrState) || launches != 1 {
				t.Fatalf("unverified host conditions could requeue: %+v %v", r, err)
			}
		})
	}
}
