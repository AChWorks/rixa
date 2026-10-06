// SPDX-License-Identifier: MPL-2.0

package product

import "fmt"

const (
	defaultModuleMaxConnections      int64 = 4
	defaultIdentityMaxOperations     int64 = 16
	defaultAuditMaxOperations        int64 = 16
	defaultMediaMaxOperations        int64 = 4
	defaultEditorialMaxOperations          = 4
	maxConfiguredResourceOperations       = int(^uint32(0) >> 1)
)

func (c ResourceConfig) validate() error {
	if err := validateModuleResources(c.Identity, 2); err != nil {
		return err
	}
	if err := validateModuleResources(c.Audit, 2); err != nil {
		return err
	}
	if err := validateModuleResources(c.Media, 1); err != nil {
		return err
	}
	if c.EditorialMaxOperations < 0 || c.EditorialMaxOperations > maxConfiguredResourceOperations {
		return ErrConfiguration
	}
	return nil
}

func validateModuleResources(c ModuleResourceConfig, minOperations int) error {
	if c.MaxConns < 0 || c.MaxOperations < 0 || c.MaxOperations > maxConfiguredResourceOperations {
		return ErrConfiguration
	}
	if c.MaxOperations > 0 && c.MaxOperations < minOperations {
		return ErrConfiguration
	}
	return nil
}

func (c ResourceConfig) editorialMaxOperations() int {
	if c.EditorialMaxOperations == 0 {
		return defaultEditorialMaxOperations
	}
	return c.EditorialMaxOperations
}

func configuredConnections(value int32) int64 {
	if value == 0 {
		return defaultModuleMaxConnections
	}
	return int64(value)
}

func configuredOperations(value int, fallback int64) int64 {
	if value == 0 {
		return fallback
	}
	return int64(value)
}

// ResourceBudget is a configuration ceiling, not current demand or a capacity
// claim. Warm reserve is zero because the selected AChrix pools use MinConns=0
// and Rixa editorial connections are opened only while admitted work is active.
type ResourceBudget struct {
	Replicas                           int   `json:"replicas"`
	ActiveSites                        int   `json:"active_sites"`
	PublicationSites                   int   `json:"publication_sites"`
	ControlDBMaxConnections            int64 `json:"control_db_max_connections"`
	SiteDBMaxConnections               int64 `json:"site_db_max_connections"`
	AChrixPoolMaxConnectionsPerReplica int64 `json:"achrix_pool_max_connections_per_replica"`
	EditorialMaxConnectionsPerReplica  int64 `json:"editorial_max_connections_per_replica"`
	RuntimeDBMaxConnectionsPerReplica  int64 `json:"runtime_db_max_connections_per_replica"`
	RuntimeDBWarmReservePerReplica     int64 `json:"runtime_db_warm_reserve_per_replica"`
	RuntimeDBMaxConnectionsAggregate   int64 `json:"runtime_db_max_connections_aggregate"`
	RuntimeDBWarmReserveAggregate      int64 `json:"runtime_db_warm_reserve_aggregate"`
	AChrixMaxOperationsPerReplica      int64 `json:"achrix_max_operations_per_replica"`
	EditorialMaxOperationsPerReplica   int64 `json:"editorial_max_operations_per_replica"`
	PublicReadMaxPerReplica            int64 `json:"public_read_max_per_replica"`
	PublicationApplyMaxPerReplica      int64 `json:"publication_apply_max_per_replica"`
}

// ResourceBudget returns the finite configured ceiling for the declared
// composition. Replicas is a planning multiplier only; Rixa does not create or
// coordinate replicas itself.
func (c Config) ResourceBudget(replicas int) (ResourceBudget, error) {
	if replicas < 1 || replicas > maxConfiguredResourceOperations {
		return ResourceBudget{}, fmt.Errorf("%w: replicas", ErrConfiguration)
	}
	if err := c.Resources.validate(); err != nil {
		return ResourceBudget{}, fmt.Errorf("%w: resources", ErrConfiguration)
	}

	activeSites := 0
	publicationSites := 0
	for _, site := range c.Sites {
		if site.Disabled {
			continue
		}
		activeSites++
		if site.PublicRoot != "" {
			publicationSites++
		}
	}

	identityConns := configuredConnections(c.Resources.Identity.MaxConns)
	auditConns := configuredConnections(c.Resources.Audit.MaxConns)
	mediaConns := configuredConnections(c.Resources.Media.MaxConns)
	editorialOps := int64(c.Resources.editorialMaxOperations())

	controlDB, ok := checkedAdd(identityConns, auditConns)
	if !ok {
		return ResourceBudget{}, ErrConfiguration
	}
	siteDB, ok := checkedAdd(identityConns, auditConns, mediaConns, editorialOps)
	if !ok {
		return ResourceBudget{}, ErrConfiguration
	}
	achrixPools, ok := checkedAdd(controlDB, int64(activeSites)*(identityConns+auditConns+mediaConns))
	if !ok {
		return ResourceBudget{}, ErrConfiguration
	}
	editorialTotal, ok := checkedMul(int64(activeSites), editorialOps)
	if !ok {
		return ResourceBudget{}, ErrConfiguration
	}
	runtimeMax, ok := checkedAdd(achrixPools, editorialTotal)
	if !ok {
		return ResourceBudget{}, ErrConfiguration
	}

	identityOps := configuredOperations(c.Resources.Identity.MaxOperations, defaultIdentityMaxOperations)
	auditOps := configuredOperations(c.Resources.Audit.MaxOperations, defaultAuditMaxOperations)
	mediaOps := configuredOperations(c.Resources.Media.MaxOperations, defaultMediaMaxOperations)
	achrixOps, ok := checkedAdd(identityOps, auditOps, int64(activeSites)*(identityOps+auditOps+mediaOps))
	if !ok {
		return ResourceBudget{}, ErrConfiguration
	}
	publicReads, ok := checkedMul(int64(publicationSites), int64(publicReadConcurrency))
	if !ok {
		return ResourceBudget{}, ErrConfiguration
	}
	publicationApplies := int64(publicationSites)
	aggregateMax, ok := checkedMul(runtimeMax, int64(replicas))
	if !ok {
		return ResourceBudget{}, ErrConfiguration
	}

	return ResourceBudget{
		Replicas: replicas, ActiveSites: activeSites, PublicationSites: publicationSites,
		ControlDBMaxConnections: controlDB, SiteDBMaxConnections: siteDB,
		AChrixPoolMaxConnectionsPerReplica: achrixPools,
		EditorialMaxConnectionsPerReplica: editorialTotal,
		RuntimeDBMaxConnectionsPerReplica: runtimeMax,
		RuntimeDBWarmReservePerReplica: 0,
		RuntimeDBMaxConnectionsAggregate: aggregateMax,
		RuntimeDBWarmReserveAggregate: 0,
		AChrixMaxOperationsPerReplica: achrixOps,
		EditorialMaxOperationsPerReplica: editorialTotal,
		PublicReadMaxPerReplica: publicReads,
		PublicationApplyMaxPerReplica: publicationApplies,
	}, nil
}

func checkedAdd(values ...int64) (int64, bool) {
	const maxInt64 = int64(^uint64(0) >> 1)
	var total int64
	for _, value := range values {
		if value < 0 || total > maxInt64-value {
			return 0, false
		}
		total += value
	}
	return total, true
}

func checkedMul(a, b int64) (int64, bool) {
	const maxInt64 = int64(^uint64(0) >> 1)
	if a < 0 || b < 0 || a != 0 && b > maxInt64/a {
		return 0, false
	}
	return a * b, true
}
