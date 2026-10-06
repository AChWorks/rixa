// SPDX-License-Identifier: MPL-2.0

package product

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/AChWorks/achrix"
)

type lifecycleProbe struct {
	mu      sync.Mutex
	id      string
	fail    bool
	started bool
	stopped bool
}

func (m *lifecycleProbe) Descriptor() achrix.Descriptor {
	return achrix.Descriptor{ID: m.id, Version: "1"}
}

func (m *lifecycleProbe) Start(ctx context.Context) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	m.started = true
	if m.fail {
		return errors.New("probe start failed")
	}
	return nil
}

func (m *lifecycleProbe) Ready(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if !m.started || m.stopped {
		return achrix.ErrNotReady
	}
	return nil
}

func (m *lifecycleProbe) Stop(ctx context.Context) error {
	m.mu.Lock()
	m.stopped = true
	m.mu.Unlock()
	return ctx.Err()
}

func (m *lifecycleProbe) state() (started, stopped bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.started, m.stopped
}

func TestRuntimeStartFailureCleansEarlierApplications(t *testing.T) {
	firstModule := &lifecycleProbe{id: "rixa.probe.first"}
	secondModule := &lifecycleProbe{id: "rixa.probe.second", fail: true}
	policy := achrix.PolicyFunc(func(context.Context, achrix.Principal, string, string) error { return achrix.ErrDenied })

	first, err := achrix.New(
		achrix.Config{StartupTimeout: time.Second, ShutdownTimeout: time.Second},
		policy,
		firstModule,
	)
	if err != nil {
		t.Fatal(err)
	}
	second, err := achrix.New(
		achrix.Config{StartupTimeout: time.Second, ShutdownTimeout: time.Second},
		policy,
		secondModule,
	)
	if err != nil {
		t.Fatal(err)
	}

	runtime := &Runtime{
		Config:       RuntimeConfig{Config: Config{StartupTimeout: time.Second, ShutdownTimeout: time.Second}},
		applications: []*achrix.Application{first, second},
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err = runtime.Start(ctx); err == nil {
		t.Fatal("expected startup failure")
	}
	started, stopped := firstModule.state()
	if !started || !stopped {
		t.Fatalf("earlier application not cleaned: started=%v stopped=%v", started, stopped)
	}
	_, secondStopped := secondModule.state()
	if !secondStopped {
		t.Fatal("failing application did not clean its partial start")
	}
	if err = first.Ready(ctx); !errors.Is(err, achrix.ErrNotReady) {
		t.Fatalf("cleaned application remained ready: %v", err)
	}
}


func TestRuntimeSiteReadinessFailureRollsBackEarlierPublication(t *testing.T) {
	adminDSN := os.Getenv("RIXA_TEST_POSTGRES_DSN")
	if adminDSN == "" {
		if os.Getenv("CI") != "" {
			t.Fatal("RIXA_TEST_POSTGRES_DSN is required in CI")
		}
		t.Skip("set RIXA_TEST_POSTGRES_DSN to run site-readiness rollback proof")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	dsns := createTestDatabases(t, ctx, adminDSN)
	if err := migrateEditorial(ctx, dsns["a"], "site-a"); err != nil {
		t.Fatalf("migrate site A editorial state: %v", err)
	}
	if err := migrateEditorial(ctx, dsns["b"], "site-b"); err != nil {
		t.Fatalf("migrate site B editorial state: %v", err)
	}

	base := t.TempDir()
	publicA := filepath.Join(base, "public-a")
	publicB := filepath.Join(base, "public-b")
	if err := os.Mkdir(publicA, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(publicB, 0o755); err != nil {
		t.Fatal(err)
	}

	policy := achrix.PolicyFunc(func(context.Context, achrix.Principal, string, string) error { return achrix.ErrDenied })
	firstModule := &lifecycleProbe{id: "rixa.probe.site-a"}
	secondModule := &lifecycleProbe{id: "rixa.probe.site-b"}
	firstApp, err := achrix.New(
		achrix.Config{StartupTimeout: time.Second, ShutdownTimeout: time.Second},
		policy,
		firstModule,
	)
	if err != nil {
		t.Fatal(err)
	}
	secondApp, err := achrix.New(
		achrix.Config{StartupTimeout: time.Second, ShutdownTimeout: time.Second},
		policy,
		secondModule,
	)
	if err != nil {
		t.Fatal(err)
	}

	newEditorial := func(dsn string) *EditorialService {
		return &EditorialService{store: &editorialStore{
			dsn: dsn,
			slots: make(chan struct{}, defaultEditorialMaxOperations),
		}}
	}
	newPublication := func(siteID, root string) *PublicationService {
		lifecycleCtx, lifecycleCancel := context.WithCancel(context.Background())
		t.Cleanup(lifecycleCancel)
		return &PublicationService{
			siteID: siteID,
			root: root,
			slots: make(chan struct{}, publicReadConcurrency),
			applyGate: make(chan struct{}, 1),
			rootSync: syncDir,
			lifecycleCtx: lifecycleCtx,
			lifecycleCancel: lifecycleCancel,
		}
	}

	siteA := &SiteRuntime{
		ID: "site-a",
		App: firstApp,
		Editorial: newEditorial(dsns["a"]),
		Publication: newPublication("site-a", publicA),
	}
	siteB := &SiteRuntime{
		ID: "site-b",
		App: secondApp,
		Editorial: newEditorial(dsns["b"]),
		Publication: newPublication("site-b", publicB),
	}
	runtime := &Runtime{
		Config: RuntimeConfig{Config: Config{
			StartupTimeout: time.Second,
			ShutdownTimeout: time.Second,
		}},
		Sites: map[string]*SiteRuntime{
			"site-b": siteB,
			"site-a": siteA,
		},
		applications: []*achrix.Application{firstApp, secondApp},
	}

	startCtx, startCancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer startCancel()
	if err = runtime.Start(startCtx); err == nil {
		t.Fatal("expected site-b publication readiness failure")
	}
	if siteA.Publication.ready.Load() {
		t.Fatal("earlier site publication remained ready after later site readiness failure")
	}
	if siteB.Publication.ready.Load() {
		t.Fatal("failing site publication became ready")
	}
	if err = firstApp.Ready(context.Background()); !errors.Is(err, achrix.ErrNotReady) {
		t.Fatalf("site-a Application remained ready after rollback: %v", err)
	}
	if err = secondApp.Ready(context.Background()); !errors.Is(err, achrix.ErrNotReady) {
		t.Fatalf("site-b Application remained ready after rollback: %v", err)
	}
	_, firstStopped := firstModule.state()
	_, secondStopped := secondModule.state()
	if !firstStopped || !secondStopped {
		t.Fatalf("dependent Applications were not cleaned: siteA=%v siteB=%v", firstStopped, secondStopped)
	}
}

func TestRuntimeShutdownDrainFailurePreservesApplicationsUntilRetry(t *testing.T) {
	module := &lifecycleProbe{id: "rixa.probe.shutdown-drain"}
	policy := achrix.PolicyFunc(func(context.Context, achrix.Principal, string, string) error { return achrix.ErrDenied })
	app, err := achrix.New(
		achrix.Config{StartupTimeout: time.Second, ShutdownTimeout: time.Second},
		policy,
		module,
	)
	if err != nil {
		t.Fatal(err)
	}
	setupCtx, setupCancel := context.WithTimeout(context.Background(), time.Second)
	if err = app.Start(setupCtx); err != nil {
		setupCancel()
		t.Fatal(err)
	}
	if err = app.Ready(setupCtx); err != nil {
		setupCancel()
		t.Fatal(err)
	}
	setupCancel()

	lifecycleCtx, lifecycleCancel := context.WithCancel(context.Background())
	defer lifecycleCancel()
	publication := &PublicationService{
		applyGate: make(chan struct{}, 1),
		lifecycleCtx: lifecycleCtx,
		lifecycleCancel: lifecycleCancel,
	}
	publication.ready.Store(true)
	publication.applyGate <- struct{}{}

	runtime := &Runtime{
		Config: RuntimeConfig{Config: Config{ShutdownTimeout: time.Second}},
		Sites: map[string]*SiteRuntime{
			"site-a": {ID: "site-a", App: app, Publication: publication},
		},
		applications: []*achrix.Application{app},
		started: true,
	}

	timeoutCtx, timeoutCancel := context.WithTimeout(context.Background(), 25*time.Millisecond)
	err = runtime.Shutdown(timeoutCtx)
	timeoutCancel()
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("expected bounded publication drain timeout, got %v", err)
	}
	if publication.ready.Load() {
		t.Fatal("publication remained ready after shutdown began")
	}
	started, stopped := module.state()
	if !started || stopped {
		t.Fatalf("dependent Application was torn down during unresolved publication drain: started=%v stopped=%v", started, stopped)
	}
	readyCtx, readyCancel := context.WithTimeout(context.Background(), time.Second)
	if err = app.Ready(readyCtx); err != nil {
		readyCancel()
		t.Fatalf("dependent Application lost readiness before publication drain resolved: %v", err)
	}
	readyCancel()

	<-publication.applyGate
	cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), time.Second)
	defer cleanupCancel()
	if err = runtime.Shutdown(cleanupCtx); err != nil {
		t.Fatalf("supported cleanup retry failed: %v", err)
	}
	_, stopped = module.state()
	if !stopped {
		t.Fatal("dependent Application was not stopped after publication drain resolved")
	}
	if runtime.started {
		t.Fatal("runtime remained marked started after successful cleanup retry")
	}
}
