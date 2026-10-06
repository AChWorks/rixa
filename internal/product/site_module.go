// SPDX-License-Identifier: MPL-2.0

package product

import (
	"context"
	"sync"

	"github.com/AChWorks/achrix"
)

// siteModule declares the Rixa-owned site capabilities to the AChrix
// Application graph. Domain persistence and behavior stay in EditorialService;
// this component exists so product capabilities participate in the same normal
// composition/lifecycle/authorization boundary as Foundation capabilities.
type siteModule struct {
	mu    sync.Mutex
	state string
}

func newSiteModule() *siteModule {
	return &siteModule{state: "new"}
}

func (m *siteModule) Descriptor() achrix.Descriptor {
	return achrix.Descriptor{
		ID:      "rixa.site",
		Version: Version,
		Provides: []achrix.Capability{
			{ID: CapabilityContentList, Version: 1},
			{ID: CapabilityContentRead, Version: 1},
			{ID: CapabilityContentEdit, Version: 1},
			{ID: CapabilityContentPreview, Version: 1},
			{ID: CapabilityContentPublishIntent, Version: 1},
			{ID: CapabilityAppearanceRead, Version: 1},
			{ID: CapabilityAppearanceEdit, Version: 1},
			{ID: CapabilityOperationRead, Version: 1},
		},
		Requires: []achrix.Capability{{ID: "achrix.authorization", Version: 2}},
	}
}

func (m *siteModule) Start(ctx context.Context) error {
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

func (m *siteModule) Ready(ctx context.Context) error {
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

func (m *siteModule) Stop(ctx context.Context) error {
	m.mu.Lock()
	m.state = "stopped"
	m.mu.Unlock()
	return ctx.Err()
}
