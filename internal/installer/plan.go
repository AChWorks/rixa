// SPDX-License-Identifier: MPL-2.0

package installer

import "sort"

type PlanStatus string

const (
	PlanReady       PlanStatus = "ready"
	PlanNeedsAction PlanStatus = "operator-action-required"
	PlanUnsupported PlanStatus = "unsupported"
)

type PlanAction struct {
	ID         string `json:"id"`
	Capability string `json:"capability"`
	Provider   string `json:"provider,omitempty"`
}

type Plan struct {
	Status       PlanStatus   `json:"status"`
	Actions      []PlanAction `json:"actions,omitempty"`
	Requirements []Capability `json:"requirements,omitempty"`
}

type Assessment struct {
	Report Report `json:"report"`
	Plan   Plan   `json:"plan"`
}

func Assess() Assessment {
	report := Preflight()
	return Assessment{Report: report, Plan: BuildPlan(report)}
}

// BuildPlan converts read-only discovery into a deterministic mutation plan.
// It never invents a provisioning mechanism: only capabilities explicitly
// classified as provisionable become installer actions. Operator-required and
// unsupported capabilities remain blockers for the calling installation surface.
func BuildPlan(report Report) Plan {
	plan := Plan{Status: PlanReady}
	for _, capability := range report.Capabilities {
		if !capability.Required {
			continue
		}
		switch capability.State {
		case StateProvisionable:
			plan.Actions = append(plan.Actions, PlanAction{
				ID:         "provision." + capability.ID,
				Capability: capability.ID,
				Provider:   capability.Provider,
			})
		case StateOperatorRequired:
			plan.Requirements = append(plan.Requirements, capability)
			if plan.Status != PlanUnsupported {
				plan.Status = PlanNeedsAction
			}
		case StateUnsupported:
			plan.Requirements = append(plan.Requirements, capability)
			plan.Status = PlanUnsupported
		}
	}
	sort.Slice(plan.Actions, func(i, j int) bool { return plan.Actions[i].ID < plan.Actions[j].ID })
	sort.Slice(plan.Requirements, func(i, j int) bool { return plan.Requirements[i].ID < plan.Requirements[j].ID })
	return plan
}
