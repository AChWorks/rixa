// SPDX-License-Identifier: MPL-2.0

package product

import (
	"errors"
	"testing"
)

func TestResourceBudgetSeparatesCeilingWarmReserveAndComposition(t *testing.T) {
	config := Config{
		Resources: ResourceConfig{
			Identity: ModuleResourceConfig{MaxConns: 2, MaxOperations: 6},
			Audit: ModuleResourceConfig{MaxConns: 2, MaxOperations: 6},
			Media: ModuleResourceConfig{MaxConns: 2, MaxOperations: 4},
			EditorialMaxOperations: 2,
		},
		Sites: []SiteConfig{
			{ID: "a", PublicRoot: "/private/a"},
			{ID: "b", PublicRoot: "/private/b"},
			{ID: "disabled", Disabled: true, PublicRoot: "/private/disabled"},
		},
	}
	budget, err := config.ResourceBudget(2)
	if err != nil {
		t.Fatal(err)
	}
	if budget.ActiveSites != 2 || budget.PublicationSites != 2 {
		t.Fatalf("site counts=%#v", budget)
	}
	if budget.ControlDBMaxConnections != 4 || budget.SiteDBMaxConnections != 8 {
		t.Fatalf("component DB ceilings=%#v", budget)
	}
	if budget.AChrixPoolMaxConnectionsPerReplica != 16 ||
		budget.EditorialMaxConnectionsPerReplica != 4 ||
		budget.RuntimeDBMaxConnectionsPerReplica != 20 ||
		budget.RuntimeDBMaxConnectionsAggregate != 40 {
		t.Fatalf("runtime DB budget=%#v", budget)
	}
	if budget.RuntimeDBWarmReservePerReplica != 0 || budget.RuntimeDBWarmReserveAggregate != 0 {
		t.Fatalf("warm reserve must stay distinct from maxima: %#v", budget)
	}
	if budget.AChrixMaxOperationsPerReplica != 44 ||
		budget.AChrixMaxOperationsAggregate != 88 ||
		budget.IdentityHashMaxPerReplica != 6 ||
		budget.IdentityHashMaxAggregate != 12 ||
		budget.MediaExpensiveMaxPerReplica != 4 ||
		budget.MediaExpensiveMaxAggregate != 8 ||
		budget.EditorialMaxOperationsPerReplica != 4 ||
		budget.EditorialMaxOperationsAggregate != 8 ||
		budget.PublicReadMaxPerReplica != 64 ||
		budget.PublicReadMaxAggregate != 128 ||
		budget.PublicationApplyMaxPerReplica != 2 ||
		budget.PublicationApplyMaxAggregate != 4 {
		t.Fatalf("operation/admission budget=%#v", budget)
	}
}

func TestResourceBudgetPreservesAChrixDefaultsAndRejectsInvalidOverrides(t *testing.T) {
	config := Config{Sites: []SiteConfig{
		{ID: "a", PublicRoot: "/private/a"},
		{ID: "b", PublicRoot: "/private/b"},
	}}
	budget, err := config.ResourceBudget(1)
	if err != nil {
		t.Fatal(err)
	}
	if budget.AChrixPoolMaxConnectionsPerReplica != 32 ||
		budget.EditorialMaxConnectionsPerReplica != 8 ||
		budget.RuntimeDBMaxConnectionsPerReplica != 40 ||
		budget.AChrixMaxOperationsPerReplica != 104 {
		t.Fatalf("default budget drifted: %#v", budget)
	}

	for name, resources := range map[string]ResourceConfig{
		"negative connections": {Identity: ModuleResourceConfig{MaxConns: -1}},
		"identity nested minimum": {Identity: ModuleResourceConfig{MaxOperations: 1}},
		"audit nested minimum": {Audit: ModuleResourceConfig{MaxOperations: 1}},
		"negative media operations": {Media: ModuleResourceConfig{MaxOperations: -1}},
		"negative editorial operations": {EditorialMaxOperations: -1},
	} {
		t.Run(name, func(t *testing.T) {
			if err := resources.validate(); !errors.Is(err, ErrConfiguration) {
				t.Fatalf("invalid resources accepted: %v", err)
			}
		})
	}

	store, err := newEditorialStoreWithLimit("postgres://example.invalid/db", 7)
	if err != nil {
		t.Fatal(err)
	}
	if cap(store.slots) != 7 {
		t.Fatalf("editorial limit=%d want=7", cap(store.slots))
	}
}
