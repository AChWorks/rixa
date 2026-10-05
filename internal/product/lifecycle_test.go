// SPDX-License-Identifier: MPL-2.0

package product

import (
	"context"
	"errors"
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
