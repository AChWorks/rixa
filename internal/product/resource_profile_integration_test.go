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
	"os"
	"path/filepath"
	goruntime "runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/AChWorks/achrix/identity"
	"github.com/AChWorks/achrix/media"
	"github.com/jackc/pgx/v5"
)

type databaseDemandObservation struct {
	Total  int
	Active int
}

func TestRuntimeResourceBoundProfile(t *testing.T) {
	if os.Getenv("RIXA_RESOURCE_OBSERVATION") != "1" {
		t.Skip("set RIXA_RESOURCE_OBSERVATION=1 for the real PostgreSQL resource observation")
	}
	adminDSN := os.Getenv("RIXA_TEST_POSTGRES_DSN")
	if adminDSN == "" {
		t.Fatal("RIXA_TEST_POSTGRES_DSN is required")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
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
	cert, key, _ := writeTestCertificate(t, dir, []string{
		"control.resource.test", "a.resource.test", "b.resource.test", "disabled.resource.test",
	})
	config := Config{
		Listen: "127.0.0.1:20443",
		TLS: TLSConfig{CertFile: cert, KeyFile: key},
		Language: "en",
		StartupTimeout: 15 * time.Second,
		ShutdownTimeout: 10 * time.Second,
		Resources: ResourceConfig{
			Identity: ModuleResourceConfig{MaxConns: 6, MaxOperations: 2},
			Audit: ModuleResourceConfig{MaxConns: 6, MaxOperations: 2},
			Media: ModuleResourceConfig{MaxConns: 6, MaxOperations: 6},
			EditorialMaxOperations: 2,
		},
		Control: ControlConfig{Origin: "https://control.resource.test:20443", DatabaseEnv: "RIXA_RESOURCE_CONTROL"},
		Sites: []SiteConfig{
			{ID: "site-a", Origin: "https://a.resource.test:20443", DatabaseEnv: "RIXA_RESOURCE_A", MediaRoot: mediaA, PublicRoot: publicA, PublicPolicy: testPublicPolicy()},
			{ID: "site-b", Origin: "https://b.resource.test:20443", DatabaseEnv: "RIXA_RESOURCE_B", MediaRoot: mediaB, PublicRoot: publicB, PublicPolicy: testPublicPolicy()},
			{ID: "disabled", Origin: "https://disabled.resource.test:20443", DatabaseEnv: "RIXA_RESOURCE_DISABLED", MediaRoot: filepath.Join(dir, "disabled-media"), Disabled: true},
		},
	}
	if err := config.validateStructure(); err != nil {
		t.Fatal(err)
	}
	budget, err := config.ResourceBudget(1)
	if err != nil {
		t.Fatal(err)
	}
	if budget.RuntimeDBMaxConnectionsPerReplica != 52 ||
		budget.RuntimeDBWarmReservePerReplica != 0 ||
		budget.AChrixMaxOperationsPerReplica != 24 ||
		budget.IdentityHashMaxPerReplica != 6 ||
		budget.MediaExpensiveMaxPerReplica != 4 ||
		budget.PublicReadMaxPerReplica != 64 {
		t.Fatalf("unexpected resource budget: %#v", budget)
	}

	secrets := map[string]string{
		"RIXA_RESOURCE_CONTROL": dsns["control"],
		"RIXA_RESOURCE_A": dsns["a"],
		"RIXA_RESOURCE_B": dsns["b"],
	}
	getenv := mapLookup(secrets)
	if err = Migrate(ctx, config, getenv); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	controlBootstrap, err := BootstrapAdmin(ctx, config, "control", "resource-control", "Resource-Control-Password-2026!", getenv, nil)
	if err != nil {
		t.Fatalf("bootstrap control: %v", err)
	}
	aBootstrap, err := BootstrapAdmin(ctx, config, "site:site-a", "resource-a", "Resource-A-Password-2026!", getenv, nil)
	if err != nil {
		t.Fatalf("bootstrap site A: %v", err)
	}
	bBootstrap, err := BootstrapAdmin(ctx, config, "site:site-b", "resource-b", "Resource-B-Password-2026!", getenv, nil)
	if err != nil {
		t.Fatalf("bootstrap site B: %v", err)
	}
	config.Control.AdminPrincipal = controlBootstrap.Account.ID
	config.Sites[0].AdminPrincipal = aBootstrap.Account.ID
	config.Sites[1].AdminPrincipal = bBootstrap.Account.ID

	resolved, err := config.ResolveRuntime(ctx, getenv)
	if err != nil {
		t.Fatal(err)
	}
	runtime, err := BuildRuntime(resolved, nil)
	if err != nil {
		t.Fatal(err)
	}
	if runtime.Sites["disabled"] != nil {
		t.Fatal("disabled site acquired runtime resources")
	}
	before := observeDatabaseDemand(t, ctx, adminDSN, dsns)
	if before.Total != 0 || before.Active != 0 {
		t.Fatalf("constructor/bootstrap left runtime database demand: %#v", before)
	}
	if err = runtime.Start(ctx); err != nil {
		t.Fatal(err)
	}
	started := observeDatabaseDemand(t, ctx, adminDSN, dsns)
	if started.Total <= 0 || int64(started.Total) >= budget.RuntimeDBMaxConnectionsPerReplica {
		t.Fatalf("startup demand=%#v configured ceiling=%d", started, budget.RuntimeDBMaxConnectionsPerReplica)
	}

	if _, err = runtime.ControlService().ApplySitePublication(ctx, runtime.Control.AdminPrincipal, "site-b", PublicationRequest{
		OperationID: newOperationID(),
	}); err != nil {
		t.Fatalf("publish site B mixed-load fixture: %v", err)
	}

	aAdmin := runtime.Sites["site-a"].adminPrincipal
	bAdmin := runtime.Sites["site-b"].adminPrincipal
	accountA, err := runtime.Sites["site-a"].Identity.CreateAccount(ctx, aAdmin, "resource-member-a", "Resource-Member-A-Password-2026!")
	if err != nil {
		t.Fatal(err)
	}
	accountB, err := runtime.Sites["site-b"].Identity.CreateAccount(ctx, bAdmin, "resource-member-b", "Resource-Member-B-Password-2026!")
	if err != nil {
		t.Fatal(err)
	}

	asset, err := runtime.Sites["site-a"].Media.Create(ctx, aAdmin, "resource-profile.png", bytes.NewReader(testPNG(t)))
	if err != nil {
		t.Fatalf("create resource-profile image: %v", err)
	}
	releaseExpensive := make(chan struct{})
	var releaseExpensiveOnce sync.Once
	releaseExpensiveWork := func() { releaseExpensiveOnce.Do(func() { close(releaseExpensive) }) }
	defer releaseExpensiveWork()
	writers := []*blockingResourceWriter{
		{entered: make(chan struct{}), release: releaseExpensive},
		{entered: make(chan struct{}), release: releaseExpensive},
	}
	expensiveResults := make(chan error, len(writers))
	for _, writer := range writers {
		writer := writer
		go func() {
			_, prepareErr := runtime.Sites["site-a"].Media.PreparePublicImage(ctx, aAdmin, media.PreparePublicImageRequest{
				AssetID: asset.ID, ExpectedRevision: asset.Revision,
			}, writer)
			expensiveResults <- prepareErr
		}()
	}
	for _, writer := range writers {
		select {
		case <-writer.entered:
		case <-time.After(2 * time.Second):
			t.Fatal("media expensive operation did not reach output writer")
		}
	}
	thirdPrepareCtx, thirdPrepareCancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	_, thirdPrepareErr := runtime.Sites["site-a"].Media.PreparePublicImage(thirdPrepareCtx, aAdmin, media.PreparePublicImageRequest{
		AssetID: asset.ID, ExpectedRevision: asset.Revision,
	}, &bytes.Buffer{})
	thirdPrepareCancel()
	if !errors.Is(thirdPrepareErr, media.ErrLimited) {
		t.Fatalf("third media expensive operation was not bounded by the fixed decoder lane: %v", thirdPrepareErr)
	}
	releaseExpensiveWork()
	for range writers {
		if prepareErr := <-expensiveResults; prepareErr != nil {
			t.Fatalf("admitted media expensive operation failed: %v", prepareErr)
		}
	}

	locker, err := pgx.Connect(ctx, dsns["a"])
	if err != nil {
		t.Fatal(err)
	}
	tx, err := locker.Begin(ctx)
	if err != nil {
		_ = locker.Close(context.Background())
		t.Fatal(err)
	}
	if _, err = tx.Exec(ctx, "LOCK TABLE identity.accounts IN ACCESS EXCLUSIVE MODE"); err != nil {
		_ = tx.Rollback(context.Background())
		_ = locker.Close(context.Background())
		t.Fatal(err)
	}

	startReads := make(chan struct{})
	results := make(chan error, 2)
	var wg sync.WaitGroup
	for range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-startReads
			readCtx, readCancel := context.WithTimeout(context.Background(), 1500*time.Millisecond)
			defer readCancel()
			_, readErr := runtime.Sites["site-a"].Identity.Account(readCtx, aAdmin, accountA.ID)
			results <- readErr
		}()
	}
	close(startReads)
	blocked := waitForActiveDatabaseDemand(t, ctx, adminDSN, dsns["a"], 2)
	if int64(blocked.Total) > budget.SiteDBMaxConnections+1 {
		t.Fatalf("site A exceeded runtime DB ceiling plus one test locker: observation=%#v site_ceiling=%d", blocked, budget.SiteDBMaxConnections)
	}

	limitedCtx, limitedCancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	_, limitedErr := runtime.Sites["site-a"].Identity.Account(limitedCtx, aAdmin, accountA.ID)
	limitedCancel()
	if !errors.Is(limitedErr, identity.ErrLimited) {
		t.Fatalf("saturated site A did not fail fast with identity.ErrLimited: %v", limitedErr)
	}

	const publicBurst = 8
	publicStart := make(chan struct{})
	publicResults := make(chan error, publicBurst)
	for range publicBurst {
		go func() {
			<-publicStart
			req := httptest.NewRequest(http.MethodGet, "https://b.resource.test:20443/", nil)
			req.Host = "b.resource.test:20443"
			req.TLS = &tls.ConnectionState{}
			rec := httptest.NewRecorder()
			runtime.Handler.ServeHTTP(rec, req)
			if rec.Code != http.StatusOK {
				publicResults <- fmt.Errorf("site B public status=%d", rec.Code)
				return
			}
			if !strings.Contains(rec.Body.String(), "No published posts.") {
				publicResults <- fmt.Errorf("site B public response missing published empty-state")
				return
			}
			publicResults <- nil
		}()
	}
	close(publicStart)
	for range publicBurst {
		if publicErr := <-publicResults; publicErr != nil {
			t.Fatal(publicErr)
		}
	}
	mixed := observeDatabaseDemand(t, ctx, adminDSN, dsns)
	if mixed.Active != blocked.Active {
		t.Fatalf("static public burst changed active database demand: before=%#v after=%#v", blocked, mixed)
	}

	otherCtx, otherCancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	other, otherErr := runtime.Sites["site-b"].Identity.Account(otherCtx, bAdmin, accountB.ID)
	otherCancel()
	if otherErr != nil || other.ID != accountB.ID {
		t.Fatalf("site B stalled behind saturated site A: account=%#v err=%v", other, otherErr)
	}

	if err = tx.Rollback(ctx); err != nil {
		_ = locker.Close(context.Background())
		t.Fatal(err)
	}
	if err = locker.Close(ctx); err != nil {
		t.Fatal(err)
	}
	wg.Wait()
	close(results)
	for readErr := range results {
		if readErr != nil {
			t.Fatalf("admitted site A read did not complete after releasing lock: %v", readErr)
		}
	}
	reclaimed := observeDatabaseDemand(t, ctx, adminDSN, dsns)
	if reclaimed.Active != 0 {
		t.Fatalf("post-burst active DB demand was not reclaimed: %#v", reclaimed)
	}
	reuseCtx, reuseCancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	_, reuseErr := runtime.Sites["site-a"].Identity.Account(reuseCtx, aAdmin, accountA.ID)
	reuseCancel()
	if reuseErr != nil {
		t.Fatalf("released admission was not reusable: %v", reuseErr)
	}

	fmt.Printf("RIXA_RESOURCE_OBSERVATION runner_goos=%s runner_goarch=%s runner_cpus=%d runner_mem_total_kib=%s active_sites=%d publication_sites=%d db_ceiling=%d warm_reserve=%d identity_hash_ceiling=%d media_expensive_ceiling=%d constructor_total=%d startup_total=%d saturation_site_a_total=%d saturation_site_a_active=%d mixed_public_reads=%d mixed_active=%d post_burst_total=%d post_burst_active=%d\n",
		goruntime.GOOS, goruntime.GOARCH, goruntime.NumCPU(), runnerMemoryTotalKiB(),
		budget.ActiveSites, budget.PublicationSites, budget.RuntimeDBMaxConnectionsPerReplica,
		budget.RuntimeDBWarmReservePerReplica, budget.IdentityHashMaxPerReplica, budget.MediaExpensiveMaxPerReplica,
		before.Total, started.Total, blocked.Total, blocked.Active, publicBurst, mixed.Active,
		reclaimed.Total, reclaimed.Active)

	stopCtx, stopCancel := context.WithTimeout(context.Background(), 10*time.Second)
	if err = runtime.Shutdown(stopCtx); err != nil {
		stopCancel()
		t.Fatal(err)
	}
	stopCancel()
	stopped := observeDatabaseDemand(t, ctx, adminDSN, dsns)
	if stopped.Total != 0 || stopped.Active != 0 {
		t.Fatalf("shutdown left database demand: %#v", stopped)
	}
}

func observeDatabaseDemand(t *testing.T, ctx context.Context, adminDSN string, dsns map[string]string) databaseDemandObservation {
	t.Helper()
	admin, err := pgx.Connect(ctx, adminDSN)
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close(context.Background())
	var result databaseDemandObservation
	for _, key := range []string{"control", "a", "b"} {
		cfg, parseErr := pgx.ParseConfig(dsns[key])
		if parseErr != nil {
			t.Fatal(parseErr)
		}
		var total, active int
		if err = admin.QueryRow(ctx, `
			SELECT count(*)::int,
			       count(*) FILTER (WHERE state='active')::int
			FROM pg_stat_activity
			WHERE datname=$1
		`, cfg.Database).Scan(&total, &active); err != nil {
			t.Fatal(err)
		}
		result.Total += total
		result.Active += active
	}
	return result
}

func observeOneDatabaseDemand(t *testing.T, ctx context.Context, adminDSN, dsn string) databaseDemandObservation {
	t.Helper()
	admin, err := pgx.Connect(ctx, adminDSN)
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close(context.Background())
	cfg, err := pgx.ParseConfig(dsn)
	if err != nil {
		t.Fatal(err)
	}
	var result databaseDemandObservation
	if err = admin.QueryRow(ctx, `
		SELECT count(*)::int,
		       count(*) FILTER (WHERE state='active')::int
		FROM pg_stat_activity
		WHERE datname=$1
	`, cfg.Database).Scan(&result.Total, &result.Active); err != nil {
		t.Fatal(err)
	}
	return result
}

func waitForActiveDatabaseDemand(t *testing.T, ctx context.Context, adminDSN, dsn string, want int) databaseDemandObservation {
	t.Helper()
	deadline := time.Now().Add(500 * time.Millisecond)
	var last databaseDemandObservation
	for {
		last = observeOneDatabaseDemand(t, ctx, adminDSN, dsn)
		if last.Active >= want {
			return last
		}
		if time.Now().After(deadline) {
			t.Fatalf("database demand did not reach %d active sessions: %#v", want, last)
		}
		time.Sleep(10 * time.Millisecond)
	}
}


func runnerMemoryTotalKiB() string {
	data, err := os.ReadFile("/proc/meminfo")
	if err != nil {
		return "unknown"
	}
	for _, line := range strings.Split(string(data), "\n") {
		if !strings.HasPrefix(line, "MemTotal:") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) >= 2 {
			return fields[1]
		}
	}
	return "unknown"
}


type blockingResourceWriter struct {
	entered chan struct{}
	release <-chan struct{}
	once    sync.Once
}

func (w *blockingResourceWriter) Write(p []byte) (int, error) {
	w.once.Do(func() { close(w.entered) })
	<-w.release
	return len(p), nil
}
