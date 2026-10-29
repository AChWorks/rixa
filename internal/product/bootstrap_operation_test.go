// SPDX-License-Identifier: MPL-2.0

package product

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/AChWorks/achrix"
	"github.com/AChWorks/achrix/identity"
)

type bootstrapOperationAccountProbe struct {
	account                              identity.Account
	createErr                            error
	loginErr                             error
	logoutErr                            error
	principal                            achrix.Principal
	createCalls, loginCalls, logoutCalls int
	afterCreate                          func()
	invalidCreate                        bool
	requireObservationDeadline           bool
	observationDeadlineSeen              bool
}

func (p *bootstrapOperationAccountProbe) LookupAccount(ctx context.Context, _ achrix.Principal, _ string) (identity.Account, error) {
	if p.requireObservationDeadline && p.createCalls > 0 {
		deadline, ok := ctx.Deadline()
		if !ok || time.Until(deadline) > time.Second {
			return identity.Account{}, errors.New("missing bounded reconciliation deadline")
		}
		p.observationDeadlineSeen = true
	}
	if p.account.ID == "" {
		return identity.Account{}, identity.ErrNotFound
	}
	return p.account, nil
}
func (p *bootstrapOperationAccountProbe) CreateAccount(context.Context, achrix.Principal, string, string) (identity.Account, error) {
	p.createCalls++
	p.account = identity.Account{ID: testControlAdmin, Login: "initial-admin", Enabled: true, Revision: 1}
	if p.invalidCreate {
		p.account.Enabled = false
	}
	if p.afterCreate != nil {
		p.afterCreate()
	}
	return p.account, p.createErr
}
func (p *bootstrapOperationAccountProbe) Login(context.Context, string, string, string) (identity.Session, error) {
	p.loginCalls++
	principal := p.principal
	if principal == "" {
		principal = achrix.Principal(p.account.ID)
	}
	return identity.Session{Principal: principal, Token: "private-session-token", CSRF: "private-csrf"}, p.loginErr
}
func (p *bootstrapOperationAccountProbe) Logout(ctx context.Context, token string) error {
	p.logoutCalls++
	if ctx.Err() != nil || token != "private-session-token" {
		return identity.ErrInvalid
	}
	return p.logoutErr
}
func bootstrapOperationFixture(t *testing.T) BootstrapOperationRequest {
	t.Helper()
	s := setupFixture(t)
	return BootstrapOperationRequest{OperationID: "bootstrap-test", StatePath: filepath.Join(filepath.Dir(s.TargetPath), "bootstrap.json"), Configuration: s.Configuration, Scope: "control", Login: "initial-admin", Password: "private-test-password-value"}
}
func bootstrapOperationTestProbe(context.Context, Config, string, func(string) (string, bool)) (string, string, error) {
	return "postgres://operator:private-dsn-password@localhost/target", "1:100", nil
}
func bootstrapOperationTestFactory(p bootstrapOperationAccounts) bootstrapOperationFactory {
	return func(context.Context, Config, string, string, *slog.Logger) (bootstrapOperationAccounts, func() error, error) {
		return p, func() error { return nil }, nil
	}
}
func runBootstrapOperationTest(r BootstrapOperationRequest, apply bool, p bootstrapOperationAccounts) (BootstrapOperationResult, error) {
	return bootstrapOperation(context.Background(), r, nil, nil, apply, bootstrapOperationTestProbe, bootstrapOperationTestFactory(p))
}
func seedBootstrapAttempt(t *testing.T, r BootstrapOperationRequest) {
	t.Helper()
	p := &bootstrapOperationAccountProbe{afterCreate: func() { panic("simulated process loss after commit") }}
	func() {
		defer func() {
			if recover() == nil {
				t.Fatal("fixture did not interrupt")
			}
		}()
		_, _ = runBootstrapOperationTest(r, true, p)
	}()
	d, err := openSetupDirectory(r.StatePath)
	if err != nil {
		t.Fatal(err)
	}
	defer d.close()
	j, err := loadBootstrapOperation(d, r.StatePath)
	if err != nil || j == nil || j.State != BootstrapAttempted || !j.BaselineAbsent {
		t.Fatalf("attempt was not durable: %+v %v", j, err)
	}
}
func TestBootstrapOperationCreatesOnceAndInspectsWithoutPassword(t *testing.T) {
	r := bootstrapOperationFixture(t)
	p := &bootstrapOperationAccountProbe{}
	got, err := runBootstrapOperationTest(r, true, p)
	if err != nil || got.State != BootstrapVerified || !got.Created || got.Principal != testControlAdmin {
		t.Fatalf("create: %+v %v", got, err)
	}
	r.Password = ""
	for _, apply := range []bool{false, true} {
		got, err = runBootstrapOperationTest(r, apply, p)
		if err != nil || got.State != BootstrapVerified || !got.Reconciled || got.Created {
			t.Fatalf("retained result: %+v %v", got, err)
		}
	}
	if p.createCalls != 1 || p.loginCalls != 0 || p.logoutCalls != 0 {
		t.Fatalf("unexpected replay/authentication: %+v", p)
	}
	data, err := os.ReadFile(r.StatePath)
	if err != nil {
		t.Fatal(err)
	}
	receipt, _ := json.Marshal(got)
	for _, s := range []string{"private-test-password-value", "private-dsn-password", "private-session-token", "private-csrf", string(r.Configuration)} {
		if strings.Contains(string(data), s) || strings.Contains(string(receipt), s) {
			t.Fatal("sensitive input leaked into journal/result")
		}
	}
	if strings.Contains(string(receipt), r.StatePath) || strings.Contains(string(receipt), r.Login) {
		t.Fatal("private location/login leaked into public receipt")
	}
	info, _ := os.Stat(r.StatePath)
	if info.Mode().Perm() != 0600 {
		t.Fatal("journal permissions")
	}
}
func TestBootstrapOperationFreshExistingLoginIsConflictEvenWithCorrectPassword(t *testing.T) {
	r := bootstrapOperationFixture(t)
	p := &bootstrapOperationAccountProbe{account: identity.Account{ID: testControlAdmin, Login: r.Login, Enabled: true, Revision: 1}}
	for i := 0; i < 2; i++ {
		got, err := runBootstrapOperationTest(r, true, p)
		if !errors.Is(err, identity.ErrConflict) || got.State != BootstrapConflict || got.Principal != "" {
			t.Fatalf("existing adopted: %+v %v", got, err)
		}
	}
	if p.createCalls+p.loginCalls+p.logoutCalls != 0 {
		t.Fatal("existing login triggered a write or authentication")
	}
}
func TestBootstrapOperationReconcilesLostCreateAcknowledgementWithPasswordProof(t *testing.T) {
	r := bootstrapOperationFixture(t)
	p := &bootstrapOperationAccountProbe{createErr: identity.ErrUnavailable}
	got, err := runBootstrapOperationTest(r, true, p)
	if err != nil || got.State != BootstrapVerified || !got.Reconciled || got.Created {
		t.Fatalf("unknown create: %+v %v", got, err)
	}
	if p.createCalls != 1 || p.loginCalls != 1 || p.logoutCalls != 1 {
		t.Fatalf("proof path: %+v", p)
	}
}
func TestBootstrapOperationResumeRequiresProofAndNeverRecreatesAbsentAttempt(t *testing.T) {
	r := bootstrapOperationFixture(t)
	seedBootstrapAttempt(t, r)
	p := &bootstrapOperationAccountProbe{}
	got, err := runBootstrapOperationTest(r, true, p)
	if !errors.Is(err, ErrBootstrapReconciliation) || got.Code != "attempted-account-absent" || p.createCalls != 0 {
		t.Fatalf("absent replayed: %+v %v", got, err)
	}
	p.account = identity.Account{ID: testControlAdmin, Login: r.Login, Enabled: true, Revision: 1}
	got, err = runBootstrapOperationTest(r, false, p)
	if !errors.Is(err, ErrBootstrapReconciliation) || got.Code != "account-needs-authentication" || p.loginCalls != 0 {
		t.Fatalf("inspection mutated: %+v %v", got, err)
	}
	r.Password = ""
	got, err = runBootstrapOperationTest(r, true, p)
	if !errors.Is(err, ErrBootstrapReconciliation) || got.Code != "password-required" || p.loginCalls != 0 {
		t.Fatalf("missing proof accepted: %+v %v", got, err)
	}
	r.Password = "private-test-password-value"
	got, err = runBootstrapOperationTest(r, true, p)
	if err != nil || got.State != BootstrapVerified || !got.Reconciled || p.createCalls != 0 || p.loginCalls != 1 || p.logoutCalls != 1 {
		t.Fatalf("resume: %+v %v", got, err)
	}
}
func TestBootstrapOperationAuthenticationFailureAndUncertainty(t *testing.T) {
	for _, tc := range []struct {
		name                string
		loginErr, logoutErr error
		principal           achrix.Principal
		unknown             bool
	}{
		{"wrong-password", identity.ErrAuthentication, nil, "", false},
		{"principal-mismatch", nil, nil, achrix.Principal(testSiteAAdmin), false},
		{"login-unknown", identity.ErrUnavailable, nil, "", true},
		{"logout-unknown", nil, identity.ErrUnavailable, "", true},
		{"logout-denied", nil, achrix.ErrDenied, "", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := bootstrapOperationFixture(t)
			seedBootstrapAttempt(t, r)
			p := &bootstrapOperationAccountProbe{account: identity.Account{ID: testControlAdmin, Login: r.Login, Enabled: true, Revision: 1}, loginErr: tc.loginErr, logoutErr: tc.logoutErr, principal: tc.principal}
			got, err := runBootstrapOperationTest(r, true, p)
			if !errors.Is(err, ErrBootstrapReconciliation) || got.Principal != "" || got.State != BootstrapReconciliation {
				t.Fatalf("invalid proof accepted: %+v %v", got, err)
			}
			if tc.loginErr == nil && p.logoutCalls != 1 {
				t.Fatal("issued session not cleaned up")
			}
			p.loginErr, p.logoutErr, p.principal = nil, nil, ""
			got, err = runBootstrapOperationTest(r, true, p)
			if tc.unknown {
				if !errors.Is(err, ErrBootstrapReconciliation) || got.Code != "authentication-attempt-unknown" || p.loginCalls != 1 {
					t.Fatalf("uncertain auth replayed: %+v %v", got, err)
				}
			} else if err != nil || got.State != BootstrapVerified || p.loginCalls != 2 {
				t.Fatalf("corrected proof: %+v %v", got, err)
			}
			if p.createCalls != 0 {
				t.Fatal("authentication recovery replayed create")
			}
		})
	}
}
func TestBootstrapOperationExactBindingsAndVerifiedDrift(t *testing.T) {
	r := bootstrapOperationFixture(t)
	p := &bootstrapOperationAccountProbe{}
	if _, err := runBootstrapOperationTest(r, true, p); err != nil {
		t.Fatal(err)
	}
	for _, change := range []func(*BootstrapOperationRequest){
		func(x *BootstrapOperationRequest) { x.OperationID = "different" },
		func(x *BootstrapOperationRequest) { x.Login = "another-admin" },
		func(x *BootstrapOperationRequest) { x.Scope = "site:site-a" },
		func(x *BootstrapOperationRequest) {
			x.Configuration = append(append([]byte(nil), x.Configuration...), ' ')
		},
	} {
		x := r
		change(&x)
		got, err := runBootstrapOperationTest(x, true, p)
		if !errors.Is(err, ErrBootstrapOperationStale) || got.Principal != "" {
			t.Fatalf("binding drift accepted: %+v %v", got, err)
		}
	}
	probe := func(context.Context, Config, string, func(string) (string, bool)) (string, string, error) {
		return "", "1:200", nil
	}
	got, err := bootstrapOperation(context.Background(), r, nil, nil, true, probe, bootstrapOperationTestFactory(p))
	if !errors.Is(err, ErrBootstrapOperationStale) || got.Code != "stale-database" {
		t.Fatalf("physical drift: %+v %v", got, err)
	}
	probe = func(context.Context, Config, string, func(string) (string, bool)) (string, string, error) {
		return "", "", ErrUnavailable
	}
	got, err = bootstrapOperation(context.Background(), r, nil, nil, true, probe, bootstrapOperationTestFactory(p))
	if !errors.Is(err, ErrUnavailable) || got.Code != "observation-unavailable" {
		t.Fatalf("outage relabeled drift: %+v %v", got, err)
	}
	p.account.ID = testSiteAAdmin
	got, err = runBootstrapOperationTest(r, true, p)
	if !errors.Is(err, ErrBootstrapReconciliation) || got.Code != "verified-account-drift" || p.createCalls != 1 {
		t.Fatalf("verified drift accepted/recreated: %+v %v", got, err)
	}
	p.account = identity.Account{}
	got, err = runBootstrapOperationTest(r, true, p)
	if !errors.Is(err, ErrBootstrapReconciliation) || got.Code != "verified-account-missing" || p.createCalls != 1 {
		t.Fatalf("verified account recreated: %+v %v", got, err)
	}
}
func TestBootstrapOperationRejectsUnsafeStateAndSharedParentConflict(t *testing.T) {
	t.Run("shared-lock", func(t *testing.T) {
		r := bootstrapOperationFixture(t)
		d, err := openSetupDirectory(r.StatePath)
		if err != nil {
			t.Fatal(err)
		}
		defer d.close()
		unlock, err := d.lock()
		if err != nil {
			t.Fatal(err)
		}
		defer unlock()
		r.StatePath = filepath.Join(filepath.Dir(r.StatePath), "different-bootstrap.json")
		got, err := runBootstrapOperationTest(r, true, &bootstrapOperationAccountProbe{})
		if !errors.Is(err, ErrSetupBusy) || got.Code != "busy" {
			t.Fatalf("parent lock bypassed: %+v %v", got, err)
		}
	})
	for _, kind := range []string{"symlink", "hardlink", "wide-permission", "malformed", "proof-tamper", "binary-tamper"} {
		t.Run(kind, func(t *testing.T) {
			r := bootstrapOperationFixture(t)
			p := &bootstrapOperationAccountProbe{}
			if _, err := runBootstrapOperationTest(r, true, p); err != nil {
				t.Fatal(err)
			}
			switch kind {
			case "symlink":
				_ = os.Rename(r.StatePath, r.StatePath+".real")
				_ = os.Symlink(r.StatePath+".real", r.StatePath)
			case "hardlink":
				_ = os.Link(r.StatePath, r.StatePath+".link")
			case "wide-permission":
				_ = os.Chmod(r.StatePath, 0644)
			case "malformed":
				_ = os.WriteFile(r.StatePath, []byte("{}"), 0600)
			case "proof-tamper", "binary-tamper":
				data, _ := os.ReadFile(r.StatePath)
				var j bootstrapOperationJournal
				_ = json.Unmarshal(data, &j)
				if kind == "proof-tamper" {
					j.ProofAttempted = true
				} else {
					j.CandidateSHA256 = strings.Repeat("0", 64)
				}
				data, _ = json.Marshal(j)
				_ = os.WriteFile(r.StatePath, data, 0600)
			}
			got, err := runBootstrapOperationTest(r, true, p)
			if err == nil || got.Principal != "" || p.createCalls != 1 {
				t.Fatalf("unsafe retained state accepted: %+v %v", got, err)
			}
		})
	}
}

func TestBootstrapOperationInputAndNoEffectInspection(t *testing.T) {
	r := bootstrapOperationFixture(t)
	r.Password = string([]byte{0xff})
	p := &bootstrapOperationAccountProbe{}
	called := false
	probe := func(context.Context, Config, string, func(string) (string, bool)) (string, string, error) {
		called = true
		return "", "", nil
	}
	got, err := bootstrapOperation(context.Background(), r, nil, nil, false, probe, bootstrapOperationTestFactory(p))
	if err != nil || got.State != BootstrapPlanned || called {
		t.Fatalf("fresh inspection performed effect/probe: %+v %v", got, err)
	}
	if _, err = os.Stat(r.StatePath); !os.IsNotExist(err) {
		t.Fatal("inspection created journal")
	}
	r.Password = "private-test-password-value"
	for _, change := range []func(*BootstrapOperationRequest){
		func(x *BootstrapOperationRequest) { x.Scope = "site:missing" },
		func(x *BootstrapOperationRequest) { x.Password = "too-short" },
		func(x *BootstrapOperationRequest) { x.Login = "Upper Case" },
		func(x *BootstrapOperationRequest) { x.Password = strings.Repeat("a", 1025) },
	} {
		x := r
		change(&x)
		got, err = runBootstrapOperationTest(x, true, p)
		if !errors.Is(err, ErrBootstrapOperationInput) || p.createCalls != 0 {
			t.Fatalf("invalid input reached effect: %+v %v", got, err)
		}
	}
}
func TestBootstrapOperationCanceledCreationDoesNotBeginProof(t *testing.T) {
	r := bootstrapOperationFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	p := &bootstrapOperationAccountProbe{createErr: context.Canceled, afterCreate: cancel}
	got, err := bootstrapOperation(ctx, r, nil, nil, true, bootstrapOperationTestProbe, bootstrapOperationTestFactory(p))
	if !errors.Is(err, ErrBootstrapReconciliation) || got.Code != "account-needs-authentication" || p.loginCalls != 0 {
		t.Fatalf("canceled flow started proof: %+v %v", got, err)
	}
	p.afterCreate = nil
	got, err = runBootstrapOperationTest(r, true, p)
	if err != nil || got.State != BootstrapVerified || p.createCalls != 1 || p.loginCalls != 1 {
		t.Fatalf("canceled proof resume: %+v %v", got, err)
	}
}
func TestBootstrapOperationDirectoryIdentityAndShutdownEvidence(t *testing.T) {
	r := bootstrapOperationFixture(t)
	p := &bootstrapOperationAccountProbe{}
	factory := func(context.Context, Config, string, string, *slog.Logger) (bootstrapOperationAccounts, func() error, error) {
		return p, func() error { return errors.New("shutdown unavailable") }, nil
	}
	got, err := bootstrapOperation(context.Background(), r, nil, nil, true, bootstrapOperationTestProbe, factory)
	if !errors.Is(err, ErrUnavailable) || got.State != BootstrapVerified || got.Code != "shutdown-unavailable" || got.Principal != testControlAdmin {
		t.Fatalf("shutdown evidence lost: %+v %v", got, err)
	}
	got, err = runBootstrapOperationTest(r, true, p)
	if err != nil || got.State != BootstrapVerified || p.createCalls != 1 {
		t.Fatalf("verified shutdown recovery replayed create: %+v %v", got, err)
	}
	original, _ := os.ReadFile(r.StatePath)
	parent := filepath.Dir(r.StatePath)
	if err = os.Rename(parent, parent+"-old"); err != nil {
		t.Fatal(err)
	}
	if err = os.Mkdir(parent, 0700); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(r.StatePath, original, 0600); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(parent + "-old") })
	got, err = runBootstrapOperationTest(r, true, p)
	if !errors.Is(err, ErrBootstrapOperationStale) || got.Code != "stale-binding" || p.createCalls != 1 {
		t.Fatalf("replaced directory accepted: %+v %v", got, err)
	}
}

func TestBootstrapOperationDeterministicCreateConflictIsNeverAdopted(t *testing.T) {
	r := bootstrapOperationFixture(t)
	p := &bootstrapOperationAccountProbe{createErr: identity.ErrConflict}
	for i := 0; i < 2; i++ {
		got, err := runBootstrapOperationTest(r, true, p)
		if !errors.Is(err, identity.ErrConflict) || got.State != BootstrapConflict || got.Principal != "" {
			t.Fatalf("conflict adopted: %+v %v", got, err)
		}
	}
	if p.createCalls != 1 || p.loginCalls != 0 || p.logoutCalls != 0 {
		t.Fatalf("conflict replayed/proved: %+v", p)
	}
}

func TestBootstrapOperationRequestDiagnosticFormattingIsPrivate(t *testing.T) {
	r := bootstrapOperationFixture(t)
	var log bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&log, nil))
	logger.Info("bootstrap request", "request", r)
	for _, rendered := range []string{fmt.Sprint(r), fmt.Sprintf("%+v", r), fmt.Sprintf("%#v", r), log.String()} {
		for _, secret := range []string{r.Password, r.StatePath, string(r.Configuration)} {
			if strings.Contains(rendered, secret) {
				t.Fatal("protected request leaked through diagnostic formatting")
			}
		}
	}
}

func TestBootstrapOperationInvalidCreateMetadataRequiresReconciliation(t *testing.T) {
	r := bootstrapOperationFixture(t)
	p := &bootstrapOperationAccountProbe{invalidCreate: true}
	got, err := runBootstrapOperationTest(r, true, p)
	if !errors.Is(err, ErrBootstrapReconciliation) || got.State != BootstrapReconciliation || got.Code != "invalid-account-result" || got.Principal != "" {
		t.Fatalf("malformed account result: %+v %v", got, err)
	}
}

func TestBootstrapOperationUnknownCreateObservationHasIndependentDeadline(t *testing.T) {
	r := bootstrapOperationFixture(t)
	p := &bootstrapOperationAccountProbe{createErr: identity.ErrUnavailable, requireObservationDeadline: true}
	got, err := runBootstrapOperationTest(r, true, p)
	if err != nil || got.State != BootstrapVerified || !p.observationDeadlineSeen {
		t.Fatalf("unbounded reconciliation observation: %+v %v", got, err)
	}
}
