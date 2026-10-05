// SPDX-License-Identifier: MPL-2.0

package product

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/AChWorks/achrix"
	"github.com/AChWorks/achrix/identity"
)

type bootstrapAppProbe struct {
	startErr    error
	readyErr    error
	shutdownErr error
	shutdowns   int
}

func (p *bootstrapAppProbe) Start(context.Context) error { return p.startErr }
func (p *bootstrapAppProbe) Ready(context.Context) error { return p.readyErr }
func (p *bootstrapAppProbe) Shutdown(context.Context) error {
	p.shutdowns++
	return p.shutdownErr
}

type bootstrapAccountProbe struct {
	createAccount identity.Account
	createErr     error
	lookupAccount identity.Account
	lookupErr     error
	createCalls   int
	lookupCalls   int
}

func (p *bootstrapAccountProbe) CreateAccount(context.Context, achrix.Principal, string, string) (identity.Account, error) {
	p.createCalls++
	return p.createAccount, p.createErr
}

func (p *bootstrapAccountProbe) LookupAccount(context.Context, achrix.Principal, string) (identity.Account, error) {
	p.lookupCalls++
	return p.lookupAccount, p.lookupErr
}

func bootstrapTestConfig() Config {
	return Config{StartupTimeout: time.Second, ShutdownTimeout: time.Second}
}

func TestBootstrapAdminPreservesDeterministicConflict(t *testing.T) {
	app := &bootstrapAppProbe{}
	accounts := &bootstrapAccountProbe{
		createErr:     identity.ErrConflict,
		lookupAccount: identity.Account{ID: testControlAdmin, Login: "admin", Enabled: true, Revision: 1},
	}
	_, err := bootstrapAdmin(context.Background(), bootstrapTestConfig(), "admin", "not-used", app, accounts)
	if !errors.Is(err, identity.ErrConflict) {
		t.Fatalf("conflict was not preserved: %v", err)
	}
	if accounts.lookupCalls != 0 {
		t.Fatalf("deterministic conflict triggered reconciliation: lookups=%d", accounts.lookupCalls)
	}
	if app.shutdowns != 1 {
		t.Fatalf("bootstrap application shutdowns=%d want=1", app.shutdowns)
	}
}

func TestBootstrapAdminReconcilesOnlyAmbiguousCreateOutcome(t *testing.T) {
	account := identity.Account{ID: testControlAdmin, Login: "admin", Enabled: true, Revision: 1}
	app := &bootstrapAppProbe{}
	accounts := &bootstrapAccountProbe{
		createErr:     identity.ErrUnavailable,
		lookupAccount: account,
	}
	result, err := bootstrapAdmin(context.Background(), bootstrapTestConfig(), "admin", "not-used", app, accounts)
	if err != nil {
		t.Fatal(err)
	}
	if !result.Reconciled || result.Created || result.Account != account {
		t.Fatalf("unexpected reconciliation result: %#v", result)
	}
	if accounts.lookupCalls != 1 {
		t.Fatalf("ambiguous create reconciliation lookups=%d want=1", accounts.lookupCalls)
	}
}

func TestBootstrapAdminPropagatesShutdownFailure(t *testing.T) {
	cleanupErr := errors.New("fixture shutdown failure")
	account := identity.Account{ID: testControlAdmin, Login: "admin", Enabled: true, Revision: 1}
	app := &bootstrapAppProbe{shutdownErr: cleanupErr}
	accounts := &bootstrapAccountProbe{createAccount: account}
	result, err := bootstrapAdmin(context.Background(), bootstrapTestConfig(), "admin", "not-used", app, accounts)
	if !errors.Is(err, cleanupErr) {
		t.Fatalf("shutdown error discarded: %v", err)
	}
	if !result.Created || result.Account != account {
		t.Fatalf("successful create evidence was lost: %#v", result)
	}
}
