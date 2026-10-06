// SPDX-License-Identifier: MPL-2.0

package product

import (
	"bytes"
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/AChWorks/achrix"
	"github.com/AChWorks/achrix/identity"
	"github.com/jackc/pgx/v5"
)

const (
	transferControlPassword = "Transfer-Control-Password-2026!"
	transferSiteAPassword   = "Transfer-Site-A-Password-2026!"
	transferSiteBPassword   = "Transfer-Site-B-Password-2026!"
	transferTargetPassword  = "Transfer-Target-Control-2026!"
)

func TestSiteTransferRoundTrip(t *testing.T) {
	adminDSN := os.Getenv("RIXA_TEST_POSTGRES_DSN")
	if adminDSN == "" {
		if os.Getenv("CI") != "" {
			t.Fatal("RIXA_TEST_POSTGRES_DSN is required in CI")
		}
		t.Skip("set RIXA_TEST_POSTGRES_DSN to run site transfer proof")
	}
	tooling := siteTransferTestTooling(t)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	dsns := createSiteTransferDatabases(t, ctx, adminDSN)
	base := t.TempDir()
	sourceMediaA := filepath.Join(base, "source-media-a")
	sourceMediaB := filepath.Join(base, "source-media-b")
	sourcePublicA := filepath.Join(base, "source-public-a")
	sourcePublicB := filepath.Join(base, "source-public-b")
	for _, root := range []string{sourceMediaA, sourceMediaB, sourcePublicA, sourcePublicB} {
		if err := os.Mkdir(root, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	cert, key, _ := writeTestCertificate(t, base, []string{
		"source-control.transfer.test",
		"target-control.transfer.test",
		"site-a.transfer.test",
		"site-b.transfer.test",
	})
	source := Config{
		Listen: "127.0.0.1:22443",
		TLS: TLSConfig{CertFile: cert, KeyFile: key},
		Language: "en",
		StartupTimeout: 15 * time.Second,
		ShutdownTimeout: 10 * time.Second,
		Control: ControlConfig{Origin: "https://source-control.transfer.test:22443", DatabaseEnv: "TRANSFER_SOURCE_CONTROL"},
		Sites: []SiteConfig{
			{ID: "site-a", Origin: "https://site-a.transfer.test:22443", DatabaseEnv: "TRANSFER_SOURCE_A", MediaRoot: sourceMediaA, PublicRoot: sourcePublicA, PublicPolicy: testPublicPolicy()},
			{ID: "site-b", Origin: "https://site-b.transfer.test:22443", DatabaseEnv: "TRANSFER_SOURCE_B", MediaRoot: sourceMediaB, PublicRoot: sourcePublicB, PublicPolicy: testPublicPolicy()},
		},
	}
	sourceEnv := mapLookup(map[string]string{
		"TRANSFER_SOURCE_CONTROL": dsns["source-control"],
		"TRANSFER_SOURCE_A": dsns["source-a"],
		"TRANSFER_SOURCE_B": dsns["source-b"],
	})
	if err := Migrate(ctx, source, sourceEnv); err != nil {
		t.Fatalf("source migrate: %v", err)
	}
	sourceControl, err := BootstrapAdmin(ctx, source, "control", "transfer-source-control", transferControlPassword, sourceEnv, nil)
	if err != nil {
		t.Fatalf("source control bootstrap: %v", err)
	}
	sourceA, err := BootstrapAdmin(ctx, source, "site:site-a", "transfer-site-a", transferSiteAPassword, sourceEnv, nil)
	if err != nil {
		t.Fatalf("source site A bootstrap: %v", err)
	}
	sourceB, err := BootstrapAdmin(ctx, source, "site:site-b", "transfer-site-b", transferSiteBPassword, sourceEnv, nil)
	if err != nil {
		t.Fatalf("source site B bootstrap: %v", err)
	}
	source.Control.AdminPrincipal = sourceControl.Account.ID
	source.Sites[0].AdminPrincipal = sourceA.Account.ID
	source.Sites[1].AdminPrincipal = sourceB.Account.ID

	resolved, err := source.ResolveRuntime(ctx, sourceEnv)
	if err != nil {
		t.Fatalf("source resolve: %v", err)
	}
	sourceRuntime, err := BuildRuntime(resolved, nil)
	if err != nil {
		t.Fatalf("source build: %v", err)
	}
	if err = sourceRuntime.Start(ctx); err != nil {
		t.Fatalf("source start: %v", err)
	}
	aAdmin := sourceRuntime.Sites["site-a"].adminPrincipal
	bAdmin := sourceRuntime.Sites["site-b"].adminPrincipal

	member, err := sourceRuntime.Sites["site-a"].Identity.CreateAccount(ctx, aAdmin, "transfer-member", "Transfer-Member-Password-2026!")
	if err != nil {
		t.Fatalf("source member: %v", err)
	}
	session, err := sourceRuntime.Sites["site-a"].Identity.Login(ctx, "transfer-site-a", transferSiteAPassword, "")
	if err != nil {
		t.Fatalf("source session: %v", err)
	}
	imageBytes := testPNG(t)
	assetA, err := sourceRuntime.Sites["site-a"].Media.Create(ctx, aAdmin, "transfer.png", bytes.NewReader(imageBytes))
	if err != nil {
		t.Fatalf("source Media A: %v", err)
	}
	assetB, err := sourceRuntime.Sites["site-b"].Media.Create(ctx, bAdmin, "site-b-only.png", bytes.NewReader(imageBytes))
	if err != nil {
		t.Fatalf("source Media B: %v", err)
	}

	postA, err := sourceRuntime.Sites["site-a"].Editorial.Create(ctx, aAdmin, newOperationID(), ContentPost, "Transferred post",
		"<p>site A transfer body</p><figure data-media-id=\""+assetA.ID+"\"><figcaption>transfer image</figcaption></figure>")
	if err != nil {
		t.Fatalf("source content A: %v", err)
	}
	if _, err = sourceRuntime.Sites["site-a"].Editorial.SetPublicationIntent(ctx, aAdmin, newOperationID(), postA.ID, 1, 1, 1); err != nil {
		t.Fatalf("source publication intent: %v", err)
	}
	appearance, err := sourceRuntime.Sites["site-a"].Editorial.Appearance(ctx, aAdmin)
	if err != nil {
		t.Fatal(err)
	}
	appearanceInput := appearance.input()
	appearanceInput.Theme = "dark"
	appearanceInput.FooterText = "transferred footer"
	if _, err = sourceRuntime.Sites["site-a"].Editorial.SaveAppearance(ctx, aAdmin, newOperationID(), appearance.Revision, appearanceInput); err != nil {
		t.Fatalf("source appearance: %v", err)
	}
	publication, err := sourceRuntime.Sites["site-a"].Publication.Apply(ctx, aAdmin, PublicationRequest{
		OperationID: newOperationID(), ContentID: postA.ID, Route: "/transferred-post",
	})
	if err != nil {
		t.Fatalf("source publication: %v", err)
	}
	if publication.Route != "/transferred-post" {
		t.Fatalf("source publication route=%q", publication.Route)
	}

	postB, err := sourceRuntime.Sites["site-b"].Editorial.Create(ctx, bAdmin, newOperationID(), ContentPost, "Site B only", "<p>must not transfer</p>")
	if err != nil {
		t.Fatalf("source content B: %v", err)
	}

	blockedCapture := filepath.Join(base, "capture-while-running")
	if _, err = CaptureSite(ctx, source, "site-a", blockedCapture, sourceEnv, tooling); !errors.Is(err, ErrSiteTransfer) {
		t.Fatalf("capture accepted a running source: %v", err)
	}

	for {
		result, reconcileErr := sourceRuntime.Sites["site-a"].Media.Reconcile(ctx, aAdmin, 40)
		if reconcileErr != nil {
			t.Fatalf("source reconcile: %v", reconcileErr)
		}
		if result.Processed == 0 && result.Busy == 0 {
			break
		}
	}
	stopCtx, stopCancel := context.WithTimeout(context.Background(), 10*time.Second)
	if err = sourceRuntime.Shutdown(stopCtx); err != nil {
		stopCancel()
		t.Fatalf("source shutdown: %v", err)
	}
	stopCancel()

	captureRoot := filepath.Join(base, "site-a-transfer")
	manifest, err := CaptureSite(ctx, source, "site-a", captureRoot, sourceEnv, tooling)
	if err != nil {
		t.Fatalf("capture: %v", err)
	}
	if manifest.Summary.SourceSessionRows < 1 || manifest.Summary.Accounts < 2 ||
		manifest.Summary.ContentItems < 1 || manifest.Summary.ReadyMedia != 1 ||
		manifest.Site.ID != "site-a" || manifest.Site.AdminPrincipal != string(aAdmin) {
		t.Fatalf("capture manifest summary=%#v site=%#v", manifest.Summary, manifest.Site)
	}
	if len(manifest.ReadyMedia) != 1 || manifest.ReadyMedia[0].ID != assetA.ID {
		t.Fatalf("capture Media=%#v", manifest.ReadyMedia)
	}
	for _, entry := range manifest.MediaEntries {
		if entry.Path == assetB.ID {
			t.Fatal("site B Media leaked into site A capture")
		}
	}

	targetParent := filepath.Join(base, "target")
	if err = os.Mkdir(targetParent, 0o700); err != nil {
		t.Fatal(err)
	}
	targetMedia := filepath.Join(targetParent, "media")
	targetPublic := filepath.Join(targetParent, "public")
	target := Config{
		Listen: "127.0.0.1:22444",
		TLS: TLSConfig{CertFile: cert, KeyFile: key},
		Language: "en",
		StartupTimeout: 15 * time.Second,
		ShutdownTimeout: 10 * time.Second,
		Control: ControlConfig{Origin: "https://target-control.transfer.test:22444", DatabaseEnv: "TRANSFER_TARGET_CONTROL"},
		Sites: []SiteConfig{{
			ID: "site-a", Origin: source.Sites[0].Origin, DatabaseEnv: "TRANSFER_TARGET_A",
			MediaRoot: targetMedia, PublicRoot: targetPublic, PublicPolicy: source.Sites[0].PublicPolicy,
			AdminPrincipal: source.Sites[0].AdminPrincipal,
		}},
	}
	targetEnv := mapLookup(map[string]string{
		"TRANSFER_TARGET_CONTROL": dsns["target-control"],
		"TRANSFER_TARGET_A": dsns["target-a"],
	})
	restoredManifest, err := RestoreSite(ctx, target, "site-a", captureRoot, targetEnv, tooling)
	if err != nil {
		t.Fatalf("restore: %v", err)
	}
	if restoredManifest.CapturedAt != manifest.CapturedAt {
		t.Fatalf("restore returned different manifest")
	}
	if _, err = os.Stat(targetMedia); err != nil {
		t.Fatalf("restored Media root: %v", err)
	}
	if _, err = os.Stat(targetPublic); err != nil {
		t.Fatalf("restored public root: %v", err)
	}

	targetSessionCount := queryTransferCount(t, ctx, dsns["target-a"], "SELECT count(*) FROM identity.sessions")
	if targetSessionCount != 0 {
		t.Fatalf("restored session rows=%d want=0", targetSessionCount)
	}
	if got := queryTransferCount(t, ctx, dsns["target-a"], "SELECT count(*) FROM rixa.content_items WHERE id=$1", postB.ID); got != 0 {
		t.Fatalf("site B content leaked into restored DB: %d", got)
	}
	if got := queryTransferCount(t, ctx, dsns["target-a"], "SELECT count(*) FROM media.assets WHERE id=$1", assetB.ID); got != 0 {
		t.Fatalf("site B Media metadata leaked into restored DB: %d", got)
	}

	if err = Migrate(ctx, target, targetEnv); err != nil {
		t.Fatalf("target migrate/check ledgers: %v", err)
	}
	targetControl, err := BootstrapAdmin(ctx, target, "control", "transfer-target-control", transferTargetPassword, targetEnv, nil)
	if err != nil {
		t.Fatalf("target control bootstrap: %v", err)
	}
	if targetControl.Account.ID == sourceControl.Account.ID {
		t.Fatal("target control administrator was not independently recreated")
	}
	target.Control.AdminPrincipal = targetControl.Account.ID
	targetResolved, err := target.ResolveRuntime(ctx, targetEnv)
	if err != nil {
		t.Fatalf("target resolve: %v", err)
	}
	targetRuntime, err := BuildRuntime(targetResolved, nil)
	if err != nil {
		t.Fatalf("target build: %v", err)
	}
	if err = targetRuntime.Start(ctx); err != nil {
		t.Fatalf("target start/readiness: %v", err)
	}
	defer func() {
		stop, cancelStop := context.WithTimeout(context.Background(), 10*time.Second)
		_ = targetRuntime.Shutdown(stop)
		cancelStop()
	}()

	if _, err = targetRuntime.Sites["site-a"].Identity.Authenticate(ctx, session.Token); !errors.Is(err, identity.ErrAuthentication) {
		t.Fatalf("source session survived restore: %v", err)
	}
	freshSession, err := targetRuntime.Sites["site-a"].Identity.Login(ctx, "transfer-site-a", transferSiteAPassword, "")
	if err != nil || freshSession.Principal != aAdmin {
		t.Fatalf("fresh restored login=%#v err=%v", freshSession, err)
	}
	gotMember, err := targetRuntime.Sites["site-a"].Identity.Account(ctx, aAdmin, member.ID)
	if err != nil || gotMember.Login != member.Login {
		t.Fatalf("restored member=%#v err=%v", gotMember, err)
	}
	var restoredBytes bytes.Buffer
	gotAsset, err := targetRuntime.Sites["site-a"].Media.Read(ctx, aAdmin, assetA.ID, &restoredBytes)
	if err != nil || gotAsset.Revision != assetA.Revision || !bytes.Equal(restoredBytes.Bytes(), imageBytes) {
		t.Fatalf("restored Media=%#v err=%v bytes=%d", gotAsset, err, restoredBytes.Len())
	}
	gotPost, err := targetRuntime.Sites["site-a"].Editorial.Head(ctx, aAdmin, postA.ID)
	if err != nil || gotPost.Title != postA.Title || gotPost.PublicationIntentRevision != 1 {
		t.Fatalf("restored content=%#v err=%v", gotPost, err)
	}
	gotAppearance, err := targetRuntime.Sites["site-a"].Editorial.Appearance(ctx, aAdmin)
	if err != nil || gotAppearance.Theme != "dark" || gotAppearance.FooterText != "transferred footer" {
		t.Fatalf("restored appearance=%#v err=%v", gotAppearance, err)
	}

	publicReq := httptest.NewRequest(http.MethodGet, source.Sites[0].Origin+"/transferred-post", nil)
	publicReq.Host = mustTransferURL(t, source.Sites[0].Origin).Host
	publicReq.TLS = &tls.ConnectionState{}
	publicRec := httptest.NewRecorder()
	targetRuntime.Handler.ServeHTTP(publicRec, publicReq)
	if publicRec.Code != http.StatusOK || !strings.Contains(publicRec.Body.String(), "Transferred post") {
		t.Fatalf("restored public response status=%d body=%s", publicRec.Code, publicRec.Body.String())
	}

	targetControlActor := targetRuntime.Control.AdminPrincipal
	if targetRuntime.Sites["site-a"].operator != operatorPrincipal(targetControlActor, "site-a") ||
		targetRuntime.Sites["site-a"].operator == operatorPrincipal(sourceControl.Account.ID, "site-a") {
		t.Fatal("target site operator was not remapped from the recreated control authority")
	}
	if _, err = targetRuntime.ControlService().CreateSiteAccount(ctx, targetControlActor, "site-a", "target-control-created", "Target-Control-Created-2026!"); err != nil {
		t.Fatalf("remapped target control authority failed: %v", err)
	}
	if _, err = targetRuntime.Sites["site-a"].Identity.Account(ctx, sourceControl.Account.ID, member.ID); !errors.Is(err, achrix.ErrDenied) {
		t.Fatalf("source control principal gained active target-site authority: %v", err)
	}

	dirtyParent := filepath.Join(base, "dirty-target")
	if err = os.Mkdir(dirtyParent, 0o700); err != nil {
		t.Fatal(err)
	}
	dirtyConn, err := pgx.Connect(ctx, dsns["dirty"])
	if err != nil {
		t.Fatal(err)
	}
	if _, err = dirtyConn.Exec(ctx, "CREATE SCHEMA rixa"); err != nil {
		_ = dirtyConn.Close(context.Background())
		t.Fatal(err)
	}
	_ = dirtyConn.Close(context.Background())
	dirtyConfig := target
	dirtyConfig.Sites = append([]SiteConfig(nil), target.Sites...)
	dirtyConfig.Sites[0].DatabaseEnv = "TRANSFER_DIRTY"
	dirtyConfig.Sites[0].MediaRoot = filepath.Join(dirtyParent, "media")
	dirtyConfig.Sites[0].PublicRoot = filepath.Join(dirtyParent, "public")
	dirtyEnv := mapLookup(map[string]string{
		"TRANSFER_TARGET_CONTROL": dsns["target-control"],
		"TRANSFER_DIRTY": dsns["dirty"],
	})
	if _, err = RestoreSite(ctx, dirtyConfig, "site-a", captureRoot, dirtyEnv, tooling); !errors.Is(err, ErrSiteTransfer) {
		t.Fatalf("restore accepted nonempty target database: %v", err)
	}
	if _, err = os.Lstat(dirtyConfig.Sites[0].MediaRoot); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("failed dirty-target restore created Media root: %v", err)
	}

	if err = os.WriteFile(filepath.Join(captureRoot, siteTransferCompleteFile), []byte("{\"manifest_sha256\":\""+strings.Repeat("0", 64)+"\"}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	tamperedParent := filepath.Join(base, "tampered-target")
	if err = os.Mkdir(tamperedParent, 0o700); err != nil {
		t.Fatal(err)
	}
	tamperedConfig := target
	tamperedConfig.Sites = append([]SiteConfig(nil), target.Sites...)
	tamperedConfig.Sites[0].DatabaseEnv = "TRANSFER_TAMPERED"
	tamperedConfig.Sites[0].MediaRoot = filepath.Join(tamperedParent, "media")
	tamperedConfig.Sites[0].PublicRoot = filepath.Join(tamperedParent, "public")
	tamperedEnv := mapLookup(map[string]string{
		"TRANSFER_TARGET_CONTROL": dsns["target-control"],
		"TRANSFER_TAMPERED": dsns["tampered"],
	})
	if _, err = RestoreSite(ctx, tamperedConfig, "site-a", captureRoot, tamperedEnv, tooling); !errors.Is(err, ErrSiteTransfer) {
		t.Fatalf("restore accepted tampered completion marker: %v", err)
	}
	if queryTransferCount(t, ctx, dsns["tampered"], "SELECT count(*) FROM pg_namespace WHERE nspname IN ('identity','audit','media','rixa')") != 0 {
		t.Fatal("tampered restore mutated target database")
	}

	fmt.Printf("RIXA_SITE_TRANSFER_OBSERVATION accounts=%d content_items=%d ready_media=%d source_sessions_excluded=%d media_entries=%d public_entries=%d target_control_remapped=true old_session_rejected=true\n",
		manifest.Summary.Accounts, manifest.Summary.ContentItems, manifest.Summary.ReadyMedia,
		manifest.Summary.SourceSessionRows, len(manifest.MediaEntries), len(manifest.PublicEntries))
}

func siteTransferTestTooling(t *testing.T) SiteTransferTooling {
	t.Helper()
	if dump, restore := os.Getenv("RIXA_PG_DUMP"), os.Getenv("RIXA_PG_RESTORE"); dump != "" && restore != "" {
		return SiteTransferTooling{PGDump: dump, PGRestore: restore}
	}
	if os.Getenv("CI") == "" {
		t.Skip("set RIXA_PG_DUMP and RIXA_PG_RESTORE outside CI")
	}
	docker, err := exec.LookPath("docker")
	if err != nil {
		t.Fatal("docker is required for PostgreSQL 18.6 transfer tooling in CI")
	}
	const image = "postgres:18.6@sha256:5a5a84b19854a9ffaa54082c166ff4ec27473a361e496e5ea167f298f2da9722"
	writeWrapper := func(name, tool string) string {
		path := filepath.Join(t.TempDir(), name)
		script := fmt.Sprintf("#!/bin/sh\nexec %q run --rm -i --network host -e PGHOST -e PGPORT -e PGUSER -e PGPASSWORD -e PGDATABASE -e PGSSLMODE -e PGCONNECT_TIMEOUT %q %s \"$@\"\n", docker, image, tool)
		if writeErr := os.WriteFile(path, []byte(script), 0o700); writeErr != nil {
			t.Fatal(writeErr)
		}
		return path
	}
	return SiteTransferTooling{
		PGDump: writeWrapper("pg-dump-18", "pg_dump"),
		PGRestore: writeWrapper("pg-restore-18", "pg_restore"),
	}
}

func createSiteTransferDatabases(t *testing.T, ctx context.Context, adminDSN string) map[string]string {
	t.Helper()
	admin, err := pgx.Connect(ctx, adminDSN)
	if err != nil {
		t.Fatalf("connect PostgreSQL: %v", err)
	}
	defer admin.Close(context.Background())
	suffix := strconv.FormatInt(time.Now().UnixNano(), 36)
	names := map[string]string{
		"source-control": "rixa_transfer_source_control_" + suffix,
		"source-a": "rixa_transfer_source_a_" + suffix,
		"source-b": "rixa_transfer_source_b_" + suffix,
		"target-control": "rixa_transfer_target_control_" + suffix,
		"target-a": "rixa_transfer_target_a_" + suffix,
		"dirty": "rixa_transfer_dirty_" + suffix,
		"tampered": "rixa_transfer_tampered_" + suffix,
	}
	for _, name := range names {
		if _, err = admin.Exec(ctx, "CREATE DATABASE "+name+" TEMPLATE template0 ENCODING 'UTF8'"); err != nil {
			t.Fatalf("create database %s: %v", name, err)
		}
	}
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cleanupCancel()
		conn, connectErr := pgx.Connect(cleanupCtx, adminDSN)
		if connectErr != nil {
			t.Logf("transfer cleanup connect: %v", connectErr)
			return
		}
		defer conn.Close(context.Background())
		for _, name := range names {
			if _, dropErr := conn.Exec(cleanupCtx, "DROP DATABASE "+name+" WITH (FORCE)"); dropErr != nil {
				t.Logf("drop %s: %v", name, dropErr)
			}
		}
	})
	result := make(map[string]string, len(names))
	for key, name := range names {
		result[key] = dsnDatabase(t, adminDSN, name)
	}
	return result
}

func queryTransferCount(t *testing.T, ctx context.Context, dsn, query string, args ...any) int64 {
	t.Helper()
	conn, err := pgx.Connect(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close(context.Background())
	var count int64
	if err = conn.QueryRow(ctx, query, args...).Scan(&count); err != nil {
		t.Fatal(err)
	}
	return count
}

func mustTransferURL(t *testing.T, raw string) *url.URL {
	t.Helper()
	value, err := url.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	return value
}
