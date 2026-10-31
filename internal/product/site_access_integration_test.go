// SPDX-License-Identifier: MPL-2.0

package product

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/AChWorks/achrix"
	"github.com/AChWorks/achrix/identity"
	"github.com/jackc/pgx/v5"
)

// Real PostgreSQL, real independent Identity account status, real AChrix
// authorization and the same EditorialService used by the browser.
func TestSiteUserGrantsRealAccountsAndSiteIsolation(t *testing.T) {
	adminDSN := os.Getenv("RIXA_TEST_POSTGRES_DSN")
	if adminDSN == "" {
		if os.Getenv("CI") != "" {
			t.Fatal("RIXA_TEST_POSTGRES_DSN is required in CI")
		}
		t.Skip("set RIXA_TEST_POSTGRES_DSN for real site-grant proof")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	dsns := createTestDatabases(t, ctx, adminDSN)
	dir := t.TempDir()
	mediaA, mediaB := filepath.Join(dir, "media-a"), filepath.Join(dir, "media-b")
	publicA, publicB := filepath.Join(dir, "public-a"), filepath.Join(dir, "public-b")
	for _, root := range []string{mediaA, mediaB, publicA, publicB} {
		if err := os.Mkdir(root, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	cert, key, _ := writeTestCertificate(t, dir, []string{"control.grant.test", "a.grant.test", "b.grant.test"})
	config := Config{
		Listen:   "127.0.0.1:19443",
		TLS:      TLSConfig{CertFile: cert, KeyFile: key},
		Language: "en", StartupTimeout: 15 * time.Second, ShutdownTimeout: 10 * time.Second,
		Control: ControlConfig{Origin: "https://control.grant.test:19443", DatabaseEnv: "RIXA_TEST_CONTROL"},
		Sites: []SiteConfig{
			{ID: "site-a", Origin: "https://a.grant.test:19443", DatabaseEnv: "RIXA_TEST_A", MediaRoot: mediaA, PublicRoot: publicA, PublicPolicy: testPublicPolicy()},
			{ID: "site-b", Origin: "https://b.grant.test:19443", DatabaseEnv: "RIXA_TEST_B", MediaRoot: mediaB, PublicRoot: publicB, PublicPolicy: testPublicPolicy()},
		},
	}
	env := mapLookup(map[string]string{
		"RIXA_TEST_CONTROL": dsns["control"], "RIXA_TEST_A": dsns["a"], "RIXA_TEST_B": dsns["b"],
	})
	if err := Migrate(ctx, config, env); err != nil {
		t.Fatal(err)
	}
	controlBootstrap, err := BootstrapAdmin(ctx, config, "control", "control-admin", testControlPassword, env, nil)
	if err != nil {
		t.Fatal(err)
	}
	aBootstrap, err := BootstrapAdmin(ctx, config, "site:site-a", "site-a-admin", testSiteAPassword, env, nil)
	if err != nil {
		t.Fatal(err)
	}
	bBootstrap, err := BootstrapAdmin(ctx, config, "site:site-b", "site-b-admin", testSiteBPassword, env, nil)
	if err != nil {
		t.Fatal(err)
	}
	config.Control.AdminPrincipal = controlBootstrap.Account.ID
	config.Sites[0].AdminPrincipal = aBootstrap.Account.ID
	config.Sites[1].AdminPrincipal = bBootstrap.Account.ID
	resolved, err := config.ResolveRuntime(ctx, env)
	if err != nil {
		t.Fatal(err)
	}
	runtime, err := BuildRuntime(resolved, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := runtime.Start(ctx); err != nil {
		t.Fatal(err)
	}
	defer func() {
		stopCtx, stopCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer stopCancel()
		if err := runtime.Shutdown(stopCtx); err != nil {
			t.Errorf("shutdown: %v", err)
		}
	}()
	a, b := runtime.Sites["site-a"], runtime.Sites["site-b"]
	siteAdmin, foreignAdmin := a.adminPrincipal, b.adminPrincipal
	reader, err := a.Identity.CreateAccount(ctx, siteAdmin, "reader", "Test-Reader-Password-2026!")
	if err != nil {
		t.Fatal(err)
	}
	drafter, err := a.Identity.CreateAccount(ctx, siteAdmin, "drafter", "Test-Drafter-Password-2026!")
	if err != nil {
		t.Fatal(err)
	}
	foreign, err := b.Identity.CreateAccount(ctx, foreignAdmin, "foreign", "Test-Foreign-Password-2026!")
	if err != nil {
		t.Fatal(err)
	}

	if _, err := a.Editorial.List(ctx, achrix.Principal(reader.ID)); !errors.Is(err, achrix.ErrDenied) {
		t.Fatalf("ungranted member admitted: %v", err)
	}
	if _, err := a.Access.Grant(ctx, siteAdmin, reader.ID, CapabilityContentRead); !errors.Is(err, ErrSiteGrantNotFound) {
		t.Fatalf("absent grant = %v", err)
	}
	if _, err := a.Access.Set(ctx, achrix.Principal(reader.ID), reader.ID, CapabilityContentRead, true, 0); !errors.Is(err, achrix.ErrDenied) {
		t.Fatalf("ordinary user managed grants: %v", err)
	}
	if _, err := a.Access.Set(ctx, foreignAdmin, reader.ID, CapabilityContentRead, true, 0); !errors.Is(err, achrix.ErrDenied) {
		t.Fatalf("foreign site administrator managed grants: %v", err)
	}
	if _, err := a.Access.Set(ctx, siteAdmin, "invalid-id", CapabilityContentRead, true, 0); !errors.Is(err, ErrSiteGrantInvalid) {
		t.Fatalf("malformed account admitted: %v", err)
	}
	if _, err := a.Access.Set(ctx, siteAdmin, foreign.ID, CapabilityContentRead, true, 0); !errors.Is(err, ErrSiteGrantInvalid) {
		t.Fatalf("foreign account admitted to site A: %v", err)
	}

	readGrant, err := a.Access.Set(ctx, siteAdmin, reader.ID, CapabilityContentRead, true, 0)
	if err != nil || readGrant.Revision != 1 {
		t.Fatalf("reader grant = %#v / %v", readGrant, err)
	}
	if _, err := a.Access.Set(ctx, siteAdmin, reader.ID, CapabilityContentRead, true, 0); !errors.Is(err, ErrSiteGrantConflict) {
		t.Fatalf("duplicate or unknown CAS created grant: %v", err)
	}
	if _, err := a.Access.Set(ctx, siteAdmin, reader.ID, CapabilityContentRead, false, 0); !errors.Is(err, ErrSiteGrantConflict) {
		t.Fatalf("stale create should conflict: %v", err)
	}
	if _, err := a.Access.Set(ctx, siteAdmin, drafter.ID, CapabilityContentRead, true, 0); err != nil {
		t.Fatal(err)
	}
	if _, err := a.Access.Set(ctx, siteAdmin, drafter.ID, CapabilityContentEdit, true, 0); err != nil {
		t.Fatal(err)
	}
	if _, err := b.Access.Set(ctx, foreignAdmin, foreign.ID, CapabilityContentRead, true, 0); err != nil {
		t.Fatal(err)
	}

	draft, err := a.Editorial.Create(ctx, achrix.Principal(drafter.ID), newOperationID(), ContentPost, "Private draft", "<p>Version one</p>")
	if err != nil {
		t.Fatalf("drafter create: %v", err)
	}
	if _, err := a.Editorial.Head(ctx, achrix.Principal(reader.ID), draft.ID); err != nil {
		t.Fatalf("reader cannot read allowed site draft: %v", err)
	}
	if _, err := a.Editorial.Create(ctx, achrix.Principal(reader.ID), newOperationID(), ContentPost, "Bad", "<p>Disallowed</p>"); !errors.Is(err, achrix.ErrDenied) {
		t.Fatalf("reader created content: %v", err)
	}
	if _, err := a.Editorial.Save(ctx, achrix.Principal(reader.ID), newOperationID(), draft.ID, 1, "Bad", "<p>Overwrite</p>"); !errors.Is(err, achrix.ErrDenied) {
		t.Fatalf("reader saved draft: %v", err)
	}
	if _, err := a.Editorial.Operation(ctx, achrix.Principal(drafter.ID), newOperationID()); !errors.Is(err, achrix.ErrDenied) {
		t.Fatalf("member can inspect unscoped operations: %v", err)
	}
	if _, err := a.Editorial.SetPublicationIntent(ctx, achrix.Principal(drafter.ID), newOperationID(), draft.ID, 1, 1, 1); !errors.Is(err, achrix.ErrDenied) {
		t.Fatalf("drafter changed publication intent: %v", err)
	}
	if _, err := b.Editorial.Head(ctx, achrix.Principal(drafter.ID), draft.ID); !errors.Is(err, achrix.ErrDenied) {
		t.Fatalf("site A drafter entered site B: %v", err)
	}
	if _, err := a.Editorial.Head(ctx, achrix.Principal(foreign.ID), draft.ID); !errors.Is(err, achrix.ErrDenied) {
		t.Fatalf("site B member entered site A: %v", err)
	}
	saved, err := a.Editorial.Save(ctx, achrix.Principal(drafter.ID), newOperationID(), draft.ID, 1, "Version two", "<p>New</p>")
	if err != nil || saved.Revision != 2 {
		t.Fatalf("authorized save: %#v / %v", saved, err)
	}

	editGrant, err := a.Access.Grant(ctx, siteAdmin, drafter.ID, CapabilityContentEdit)
	if err != nil {
		t.Fatal(err)
	}
	editGrant, err = a.Access.Set(ctx, siteAdmin, drafter.ID, CapabilityContentEdit, false, editGrant.Revision)
	if err != nil || editGrant.Revision != 2 {
		t.Fatalf("revoke: %#v / %v", editGrant, err)
	}
	if _, err := a.Editorial.Save(ctx, achrix.Principal(drafter.ID), newOperationID(), draft.ID, 2, "Bad", "<p>Disallowed</p>"); !errors.Is(err, achrix.ErrDenied) {
		t.Fatalf("revoked editor can write: %v", err)
	}
	if _, err := a.Editorial.Head(ctx, achrix.Principal(drafter.ID), draft.ID); err != nil {
		t.Fatalf("revoked edit unexpectedly revoked read: %v", err)
	}
	if _, err := a.Access.Set(ctx, siteAdmin, drafter.ID, CapabilityContentEdit, true, 1); !errors.Is(err, ErrSiteGrantConflict) {
		t.Fatalf("stale revision reinstated edit: %v", err)
	}

	before, err := a.Identity.Account(ctx, siteAdmin, reader.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := a.Identity.SetEnabled(ctx, siteAdmin, reader.ID, before.Revision, false); err != nil {
		t.Fatal(err)
	}
	if _, err := a.Editorial.Head(ctx, achrix.Principal(reader.ID), draft.ID); !errors.Is(err, achrix.ErrDenied) {
		t.Fatalf("disabled identity can read via retained grant: %v", err)
	}
	current, err := a.Identity.Account(ctx, siteAdmin, reader.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := a.Identity.SetEnabled(ctx, siteAdmin, reader.ID, current.Revision, true); err != nil {
		t.Fatal(err)
	}
	if _, err := a.Editorial.Head(ctx, achrix.Principal(reader.ID), draft.ID); err != nil {
		t.Fatalf("re-enabled account lost retained membership: %v", err)
	}
	readGrant, err = a.Access.Set(ctx, siteAdmin, reader.ID, CapabilityContentRead, false, readGrant.Revision)
	if err != nil || readGrant.Revision != 2 {
		t.Fatalf("reader revoke: %#v / %v", readGrant, err)
	}
	if _, err := a.Editorial.Head(ctx, achrix.Principal(reader.ID), draft.ID); !errors.Is(err, achrix.ErrDenied) {
		t.Fatalf("revoked reader still reads: %v", err)
	}
	conn, err := pgx.Connect(ctx, dsns["a"])
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close(context.Background())
	var count int
	if err := conn.QueryRow(ctx, "SELECT count(*) FROM rixa.site_grant_history WHERE account_id=$1 AND permission=$2", reader.ID, CapabilityContentRead).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 2 {
		t.Fatalf("reader grant audit history = %d, want 2", count)
	}
	if _, err := a.Access.Set(ctx, siteAdmin, reader.ID, "rixa.publication.apply", true, 0); !errors.Is(err, ErrSiteGrantInvalid) {
		t.Fatalf("unscoped grant accepted: %v", err)
	}
	if _, err := a.Identity.Account(ctx, siteAdmin, reader.ID); errors.Is(err, identity.ErrNotFound) {
		t.Fatal("grant update changed Identity account")
	}
}
