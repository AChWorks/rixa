// SPDX-License-Identifier: MPL-2.0

package installer

import (
	"fmt"
	"regexp"
	"sort"
)

type PlanStatus string

const (
	PlanReady       PlanStatus = "ready"
	PlanNeedsAction PlanStatus = "operator-action-required"
	PlanUnsupported PlanStatus = "unsupported"
)

var planIdentifierSyntax = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,127}$`)

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
// It never invents a provisioning mechanism: only required capabilities
// explicitly classified as provisionable with a named adapter become actions.
// Malformed/duplicate required capability evidence fails closed.
func BuildPlan(report Report) Plan {
	plan := Plan{Status: PlanReady}
	seen := make(map[string]struct{}, len(report.Capabilities))
	for _, capability := range report.Capabilities {
		if !capability.Required {
			continue
		}
		if !planIdentifierSyntax.MatchString(capability.ID) {
			plan.reject(capability, "invalid required capability identifier")
			continue
		}
		if _, duplicate := seen[capability.ID]; duplicate {
			plan.reject(capability, "duplicate required capability evidence")
			continue
		}
		seen[capability.ID] = struct{}{}

		switch capability.State {
		case StateAvailable:
		case StateProvisionable:
			if !planIdentifierSyntax.MatchString(capability.Provider) {
				plan.reject(capability, "provisionable capability is missing a valid adapter provider")
				continue
			}
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
		default:
			plan.reject(capability, "unknown required capability state")
		}
	}
	sort.Slice(plan.Actions, func(i, j int) bool { return plan.Actions[i].ID < plan.Actions[j].ID })
	sort.Slice(plan.Requirements, func(i, j int) bool { return plan.Requirements[i].ID < plan.Requirements[j].ID })
	return plan
}

func (p *Plan) reject(capability Capability, reason string) {
	capability.State = StateUnsupported
	capability.Provider = ""
	capability.Detail = fmt.Sprintf("%s: %s", reason, capability.ID)
	p.Requirements = append(p.Requirements, capability)
	p.Status = PlanUnsupported
}
