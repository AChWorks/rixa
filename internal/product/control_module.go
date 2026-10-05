// SPDX-License-Identifier: MPL-2.0

package product

import (
	"context"
	"sync"

	"github.com/AChWorks/achrix"
)

type controlModule struct {
	mu    sync.Mutex
	state string
}

func newControlModule() *controlModule {
	return &controlModule{state: "new"}
}

func (m *controlModule) Descriptor() achrix.Descriptor {
	return achrix.Descriptor{
		ID:      "rixa.control",
		Version: Version,
		Provides: []achrix.Capability{
			{ID: CapabilityControlInventoryRead, Version: 1},
			{ID: CapabilityControlSiteManage, Version: 1},
		},
		Requires: []achrix.Capability{{ID: "achrix.authorization", Version: 2}},
	}
}

func (m *controlModule) Start(ctx context.Context) error {
	if err := lifecycleContext(ctx); err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.state != "new" {
		return achrix.ErrNotReady
	}
	m.state = "ready"
	return ctx.Err()
}

func (m *controlModule) Ready(ctx context.Context) error {
	if err := lifecycleContext(ctx); err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.state != "ready" {
		return achrix.ErrNotReady
	}
	return ctx.Err()
}

func (m *controlModule) Stop(ctx context.Context) error {
	m.mu.Lock()
	m.state = "stopped"
	m.mu.Unlock()
	return ctx.Err()
}

func lifecycleContext(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if _, ok := ctx.Deadline(); !ok {
		return ErrConfiguration
	}
	return nil
}
