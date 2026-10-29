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
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/AChWorks/achrix/installation"
)

func setupFixture(t *testing.T) SetupRequest {
	t.Helper()
	root := t.TempDir()
	if err := os.Chmod(root, 0o700); err != nil {
		t.Fatal(err)
	}
	data := []byte(`{"listen":"127.0.0.1:19443","tls":{"cert_file":"/private/cert.pem","key_file":"/private/key.pem"},"control":{"origin":"https://control.example.test","database_env":"SETUP_CONTROL","admin_principal":"AAAAAAAAAAAAAAAAAAAAAAAAAA"},"sites":[{"id":"site-a","origin":"https://site-a.example.test","database_env":"SETUP_SITE_A","media_root":"/private/media-a","admin_principal":"BBBBBBBBBBBBBBBBBBBBBBBBBB"}]}`)
	return SetupRequest{OperationID: "setup-test", TargetPath: filepath.Join(root, "rixa.json"), Configuration: data}
}
func satisfiedSetupProbe(context.Context, Config, func(string) (string, bool)) ([]installation.Observation, []string) {
	out := []installation.Observation{}
	for _, id := range []string{"runtime-platform", "runtime-tls", "database-secrets", "database-targets", "private-storage"} {
		out = append(out, installation.Observation{RequirementID: id, State: installation.Satisfied})
	}
	return out, []string{"1:100", "1:101"}
}
func setupRequestJournal(t *testing.T, r SetupRequest, state SetupState) *setupJournal {
	t.Helper()
	result, err := assessSetup(context.Background(), r, nil, satisfiedSetupProbe)
	if err != nil {
		t.Fatal(err)
	}
	d, err := openSetupDirectory(r.TargetPath)
	if err != nil {
		t.Fatal(err)
	}
	defer d.close()
	j := &setupJournal{Schema: setupJournalSchema, OperationID: r.OperationID, TargetSHA256: targetSetupDigest(r.TargetPath, d), Configuration: append([]byte(nil), r.Configuration...), ConfigurationSHA256: setupDigest(r.Configuration), DatabaseTargets: []string{"1:100", "1:101"}, Binding: result.Plan.Binding(), PlanID: result.Plan.ID(), State: state, Progress: installation.Progress{PlanID: result.Plan.ID()}}
	if state != SetupPlanned {
		a := installation.Attempted
		if state == SetupVerified {
			a = installation.Verified
		}
		j.Progress.Attempts = []installation.Attempt{{RequirementID: "configuration-target", State: a}}
	}
	if err = d.save(r.TargetPath, j); err != nil {
		t.Fatal(err)
	}
	return j
}
func TestSetupPersistsExactConfigurationAndReentry(t *testing.T) {
	r := setupFixture(t)
	assessed, err := assessSetup(context.Background(), r, nil, satisfiedSetupProbe)
	if err != nil || assessed.Plan.Status() != installation.ChangesRequired || len(assessed.Plan.Actions()) != 1 {
		t.Fatalf("assessment: %+v, %v", assessed, err)
	}
	entries, _ := os.ReadDir(filepath.Dir(r.TargetPath))
	if len(entries) != 0 {
		t.Fatal("read-only assessment created state")
	}
	applied, err := applySetup(context.Background(), r, nil, satisfiedSetupProbe, nil)
	if err != nil || applied.State != SetupVerified || applied.Plan.Status() != installation.Ready {
		t.Fatalf("apply: %+v, %v", applied, err)
	}
	data, err := os.ReadFile(r.TargetPath)
	if err != nil || !bytes.Equal(data, r.Configuration) {
		t.Fatal("configuration was not preserved exactly")
	}
	info, _ := os.Stat(r.TargetPath)
	if info.Mode().Perm() != 0o600 {
		t.Fatal("configuration permissions")
	}
	again, err := applySetup(context.Background(), r, nil, satisfiedSetupProbe, nil)
	after, _ := os.Stat(r.TargetPath)
	if err != nil || again.State != SetupVerified || !os.SameFile(info, after) {
		t.Fatal("re-entry recreated configuration")
	}
	observed, err := InspectSetup(r.TargetPath, r.OperationID)
	if err != nil || observed.State != SetupVerified {
		t.Fatal("passive inspect failed")
	}
}
func TestSetupLostAcknowledgementVerifiesWithoutReplay(t *testing.T) {
	r := setupFixture(t)
	_, err := applySetup(context.Background(), r, nil, satisfiedSetupProbe, func() error { return errors.New("synthetic lost acknowledgement") })
	if !errors.Is(err, ErrSetupState) {
		t.Fatal("fixture did not interrupt")
	}
	before, _ := os.Stat(r.TargetPath)
	inspected, err := InspectSetup(r.TargetPath, r.OperationID)
	if err != nil || inspected.State != SetupReconciliation {
		t.Fatal("uncertain attempt was hidden")
	}
	result, err := applySetup(context.Background(), r, nil, satisfiedSetupProbe, nil)
	after, _ := os.Stat(r.TargetPath)
	if err != nil || result.State != SetupVerified || !result.Reconciled || !os.SameFile(before, after) {
		t.Fatalf("reconciliation: %+v %v", result, err)
	}
	d, _ := openSetupDirectory(r.TargetPath)
	defer d.close()
	j, _ := d.load(r.TargetPath)
	if j.Created {
		t.Fatal("lost acknowledgement must not fabricate creation ownership")
	}
}
func TestSetupMissingAttemptAndVerificationDriftNeverReplay(t *testing.T) {
	for _, state := range []SetupState{SetupAttempted, SetupVerified} {
		t.Run(string(state), func(t *testing.T) {
			r := setupFixture(t)
			setupRequestJournal(t, r, state)
			result, err := applySetup(context.Background(), r, nil, satisfiedSetupProbe, nil)
			if err != nil || result.State != SetupReconciliation || result.Plan.Status() != installation.ReconciliationRequired || len(result.Plan.Actions()) != 0 {
				t.Fatalf("unsafe replay proposal: %+v %v", result, err)
			}
			if _, err = os.Stat(r.TargetPath); !errors.Is(err, os.ErrNotExist) {
				t.Fatal("missing prior effect was replayed")
			}
		})
	}
}
func TestSetupRetainsUnrelatedExistingFile(t *testing.T) {
	r := setupFixture(t)
	original := []byte("unrelated retained material")
	if err := os.WriteFile(r.TargetPath, original, 0o600); err != nil {
		t.Fatal(err)
	}
	result, err := applySetup(context.Background(), r, nil, satisfiedSetupProbe, nil)
	if err != nil || result.Plan.Status() != installation.UnsupportedPlan || len(result.Plan.Actions()) != 0 {
		t.Fatalf("foreign target: %+v %v", result, err)
	}
	actual, _ := os.ReadFile(r.TargetPath)
	if !bytes.Equal(actual, original) {
		t.Fatal("unrelated file changed")
	}
	d, _ := openSetupDirectory(r.TargetPath)
	defer d.close()
	j, err := d.load(r.TargetPath)
	if err != nil || j != nil {
		t.Fatal("blocked operation created a journal")
	}
}
func TestSetupExistingExactFileIsReusedWithoutCreationOwnership(t *testing.T) {
	r := setupFixture(t)
	if err := os.WriteFile(r.TargetPath, r.Configuration, 0o600); err != nil {
		t.Fatal(err)
	}
	original, _ := os.Stat(r.TargetPath)
	result, err := applySetup(context.Background(), r, nil, satisfiedSetupProbe, nil)
	after, _ := os.Stat(r.TargetPath)
	if err != nil || result.State != SetupVerified || !os.SameFile(original, after) {
		t.Fatal("existing target replaced")
	}
	d, _ := openSetupDirectory(r.TargetPath)
	defer d.close()
	j, _ := d.load(r.TargetPath)
	if j.Created {
		t.Fatal("reuse claimed creation ownership")
	}
}
func TestSetupBindingChangesRetainPriorState(t *testing.T) {
	r := setupFixture(t)
	setupRequestJournal(t, r, SetupAttempted)
	original, _ := os.ReadFile(filepath.Join(filepath.Dir(r.TargetPath), setupJournalName(r.TargetPath)))
	for _, change := range []func(SetupRequest) SetupRequest{
		func(v SetupRequest) SetupRequest { v.OperationID = "different-operation"; return v },
		func(v SetupRequest) SetupRequest {
			v.Configuration = append(append([]byte(nil), v.Configuration...), byte(' '))
			return v
		},
	} {
		changed := change(r)
		_, err := applySetup(context.Background(), changed, nil, satisfiedSetupProbe, nil)
		if !errors.Is(err, ErrSetupStale) {
			t.Fatalf("changed binding: %v", err)
		}
	}
	actual, _ := os.ReadFile(filepath.Join(filepath.Dir(r.TargetPath), setupJournalName(r.TargetPath)))
	if !bytes.Equal(original, actual) {
		t.Fatal("stale request changed prior progress")
	}
}
func TestSetupChangedCandidateOrDatabaseIdentityFailsClosed(t *testing.T) {
	for _, kind := range []string{"candidate", "database"} {
		t.Run(kind, func(t *testing.T) {
			r := setupFixture(t)
			j := setupRequestJournal(t, r, SetupAttempted)
			probe := satisfiedSetupProbe
			if kind == "candidate" {
				j.Binding.CandidateSHA256 = strings.Repeat("b", 64)
				d, _ := openSetupDirectory(r.TargetPath)
				if err := d.save(r.TargetPath, j); err != nil {
					t.Fatal(err)
				}
				d.close()
			} else {
				probe = func(ctx context.Context, c Config, l func(string) (string, bool)) ([]installation.Observation, []string) {
					o, _ := satisfiedSetupProbe(ctx, c, l)
					return o, []string{"2:100", "2:101"}
				}
			}
			_, err := applySetup(context.Background(), r, nil, probe, nil)
			if !errors.Is(err, ErrSetupStale) {
				t.Fatalf("identity drift: %v", err)
			}
			if _, err = os.Stat(r.TargetPath); !errors.Is(err, os.ErrNotExist) {
				t.Fatal("identity drift executed an effect")
			}
		})
	}
}
func TestSetupUnknownPrerequisiteBlocksAllEffects(t *testing.T) {
	r := setupFixture(t)
	probe := func(ctx context.Context, c Config, l func(string) (string, bool)) ([]installation.Observation, []string) {
		o, id := satisfiedSetupProbe(ctx, c, l)
		o[3].State = installation.Unknown
		return o, id
	}
	result, err := applySetup(context.Background(), r, nil, probe, nil)
	if err != nil || result.Plan.Status() != installation.OperatorActionRequired || len(result.Plan.Actions()) != 0 {
		t.Fatalf("unknown preflight: %+v %v", result, err)
	}
	if _, err = os.Stat(r.TargetPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("partial preflight executed configuration")
	}
}
func TestSetupRejectsUnsafePathsEntriesAndJournal(t *testing.T) {
	for _, kind := range []string{"parent-mode", "parent-alias", "target-symlink", "target-hardlink", "target-mode", "lock-symlink", "journal-symlink", "journal-corrupt", "journal-shape", "journal-large", "fifo"} {
		t.Run(kind, func(t *testing.T) {
			r := setupFixture(t)
			parent := filepath.Dir(r.TargetPath)
			switch kind {
			case "parent-mode":
				os.Chmod(parent, 0o755)
			case "parent-alias":
				alias := filepath.Join(t.TempDir(), "alias")
				os.Symlink(parent, alias)
				r.TargetPath = filepath.Join(alias, "rixa.json")
			case "target-symlink":
				os.Symlink("retained-target", r.TargetPath)
			case "target-hardlink":
				source := filepath.Join(parent, "retained")
				os.WriteFile(source, r.Configuration, 0o600)
				os.Link(source, r.TargetPath)
			case "target-mode":
				os.WriteFile(r.TargetPath, r.Configuration, 0o644)
			case "lock-symlink":
				os.Symlink("retained-lock", filepath.Join(parent, ".rixa-management.lock"))
			case "journal-symlink":
				os.Symlink("retained-journal", filepath.Join(parent, setupJournalName(r.TargetPath)))
			case "journal-corrupt":
				os.WriteFile(filepath.Join(parent, setupJournalName(r.TargetPath)), []byte(`{"unexpected":true}`), 0o600)
			case "journal-shape":
				j := setupRequestJournal(t, r, SetupVerified)
				j.Progress.Attempts = nil
				d, _ := openSetupDirectory(r.TargetPath)
				d.save(r.TargetPath, j)
				d.close()
			case "journal-large":
				os.WriteFile(filepath.Join(parent, setupJournalName(r.TargetPath)), bytes.Repeat([]byte("x"), 2<<20+1), 0o600)
			case "fifo":
				if err := exec.Command("mkfifo", r.TargetPath).Run(); err != nil {
					t.Fatal(err)
				}
			}
			result, err := applySetup(context.Background(), r, nil, satisfiedSetupProbe, nil)
			if err == nil && (result.Plan == nil || len(result.Plan.Actions()) != 0 || result.Plan.Status() == installation.Ready) {
				t.Fatalf("unsafe entry accepted: %+v", result)
			}
		})
	}
}
func TestSetupAtomicCreateNeverReplacesLateEntry(t *testing.T) {
	r := setupFixture(t)
	d, err := openSetupDirectory(r.TargetPath)
	if err != nil {
		t.Fatal(err)
	}
	defer d.close()
	original := []byte("late entry")
	os.WriteFile(r.TargetPath, original, 0o600)
	if !errors.Is(d.createConfig(r.TargetPath, r.Configuration), ErrSetupState) {
		t.Fatal("atomic create overwrote a late entry")
	}
	actual, _ := os.ReadFile(r.TargetPath)
	if !bytes.Equal(original, actual) {
		t.Fatal("late entry was altered")
	}
}
func TestSetupLockSubprocess(t *testing.T) {
	target := os.Getenv("RIXA_SETUP_LOCK_FIXTURE")
	if target == "" {
		return
	}
	d, err := openSetupDirectory(target)
	if err != nil {
		os.Exit(2)
	}
	defer d.close()
	unlock, err := d.lock()
	if err != nil {
		os.Exit(3)
	}
	defer unlock()
	fmt.Println("locked")
	_, _ = io.Copy(io.Discard, os.Stdin)
}
func TestSetupSerializesDifferentProcessesAndOperationIDs(t *testing.T) {
	r := setupFixture(t)
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(exe, "-test.run=^TestSetupLockSubprocess$")
	cmd.Env = append(os.Environ(), "RIXA_SETUP_LOCK_FIXTURE="+r.TargetPath)
	stdin, _ := cmd.StdinPipe()
	stdout, _ := cmd.StdoutPipe()
	if err = cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		stdin.Close()
		if err := cmd.Wait(); err != nil {
			t.Errorf("lock subprocess: %v", err)
		}
	})
	line, err := bufio.NewReader(stdout).ReadString('\n')
	if err != nil || line != "locked\n" {
		t.Fatal("child did not acquire lock")
	}
	r.OperationID = "another-operation"
	r.TargetPath = filepath.Join(filepath.Dir(r.TargetPath), "another-config.json")
	result, err := applySetup(context.Background(), r, nil, satisfiedSetupProbe, nil)
	if !errors.Is(err, ErrSetupBusy) || result.Code != "busy" {
		t.Fatalf("cross-process conflict: %+v %v", result, err)
	}
}
func TestSetupPublicResultsDoNotExposePrivateValues(t *testing.T) {
	r := setupFixture(t)
	secret := "postgres://private-user:secret-canary@127.0.0.1:1/private-db?sslmode=disable"
	result, err := AssessSetup(context.Background(), r, func(string) (string, bool) { return secret, true })
	if err != nil {
		t.Fatal(err)
	}
	encoded, _ := json.Marshal(result)
	for _, value := range []string{secret, "secret-canary", "private-user", "private-db", r.TargetPath, "/private/cert.pem", "/private/media-a"} {
		if bytes.Contains(encoded, []byte(value)) {
			t.Fatal("private value in public result")
		}
	}
	if len(result.Plan.Actions()) != 0 {
		t.Fatal("actual unavailable prerequisites offered effects")
	}
	invalid := r
	invalid.Configuration = []byte(secret)
	_, err = ApplySetup(context.Background(), invalid, nil)
	if err == nil || strings.Contains(err.Error(), "secret-canary") {
		t.Fatal("input error leaked raw data")
	}
}

func TestSetupRetainedUnavailableDatabaseBlocksWithoutInventingDrift(t *testing.T) {
	for _, state := range []SetupState{SetupAttempted, SetupVerified} {
		t.Run(string(state), func(t *testing.T) {
			r := setupFixture(t)
			if state == SetupVerified {
				if err := os.WriteFile(r.TargetPath, r.Configuration, 0o600); err != nil {
					t.Fatal(err)
				}
			}
			setupRequestJournal(t, r, state)
			before, _ := os.ReadFile(filepath.Join(filepath.Dir(r.TargetPath), setupJournalName(r.TargetPath)))
			unavailable := func(ctx context.Context, c Config, l func(string) (string, bool)) ([]installation.Observation, []string) {
				observations, identities := satisfiedSetupProbe(ctx, c, l)
				observations[3].State = installation.Unknown
				identities[1] = ""
				return observations, identities
			}
			result, err := applySetup(context.Background(), r, nil, unavailable, nil)
			expected := installation.OperatorActionRequired
			if state == SetupAttempted {
				expected = installation.ReconciliationRequired
			}
			if err != nil || result.Plan.Status() != expected || len(result.Plan.Actions()) != 0 || result.Code == "stale-binding" {
				t.Fatalf("inconclusive evidence fabricated drift: %+v %v", result, err)
			}
			after, _ := os.ReadFile(filepath.Join(filepath.Dir(r.TargetPath), setupJournalName(r.TargetPath)))
			if !bytes.Equal(before, after) {
				t.Fatal("blocked operation changed retained state")
			}
			if state == SetupAttempted {
				if _, err = os.Stat(r.TargetPath); !errors.Is(err, os.ErrNotExist) {
					t.Fatal("unknown evidence replayed configuration")
				}
			} else {
				recovered, err := applySetup(context.Background(), r, nil, satisfiedSetupProbe, nil)
				if err != nil || recovered.Plan.Status() != installation.Ready {
					t.Fatal("restored database evidence did not recover original binding")
				}
			}
		})
	}
}
func TestSetupKnownDatabaseDriftStillRejectsDuringPartialOutage(t *testing.T) {
	r := setupFixture(t)
	setupRequestJournal(t, r, SetupAttempted)
	partial := func(ctx context.Context, c Config, l func(string) (string, bool)) ([]installation.Observation, []string) {
		observations, identities := satisfiedSetupProbe(ctx, c, l)
		observations[3].State = installation.Unknown
		identities[0] = "9:999"
		identities[1] = ""
		return observations, identities
	}
	_, err := applySetup(context.Background(), r, nil, partial, nil)
	if !errors.Is(err, ErrSetupStale) {
		t.Fatalf("known target drift hidden by another unavailable target: %v", err)
	}
}
