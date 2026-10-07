// SPDX-License-Identifier: MPL-2.0

package installer

import "testing"

func TestBuildPlanNeverInventsProvisioning(t *testing.T) {
	report := Evaluate(Environment{
		OS: "linux", Arch: "amd64", EUID: 0,
		Distribution: "ubuntu",
		Commands:     map[string]string{"apt-get": "/usr/bin/apt-get"},
		HTTPSPort:    PortFree,
	}, ProvisioningSupport{})
	plan := BuildPlan(report)
	if plan.Status != PlanNeedsAction {
		t.Fatalf("status=%s want=%s", plan.Status, PlanNeedsAction)
	}
	if len(plan.Actions) != 0 {
		t.Fatalf("discovery-only core invented provisioning actions: %#v", plan.Actions)
	}
	if len(plan.Requirements) == 0 {
		t.Fatal("missing operator requirements")
	}
}

func TestBuildPlanUsesOnlyExplicitAdapterSupport(t *testing.T) {
	report := Evaluate(Environment{
		OS: "linux", Arch: "amd64", EUID: 0,
		Distribution: "ubuntu",
		Commands:     map[string]string{"apt-get": "/usr/bin/apt-get", "systemctl": "/usr/bin/systemctl"},
		Systemd:      true,
		HTTPSPort:    PortFree,
	}, ProvisioningSupport{
		PostgreSQL:        "apt-postgresql",
		PersistentService: "systemd",
		PrivateStorage:    "filesystem",
		HTTPSIngress:      "direct-tls",
	})
	plan := BuildPlan(report)
	want := map[string]bool{
		"provision.database.postgresql": true,
		"provision.ingress.https":       true,
		"provision.service.persistence": true,
		"provision.storage.private":     true,
	}
	for _, action := range plan.Actions {
		delete(want, action.ID)
	}
	if len(want) != 0 {
		t.Fatalf("missing explicit adapter actions: %#v; got=%#v", want, plan.Actions)
	}
	if plan.Status != PlanNeedsAction {
		t.Fatalf("TLS certificate should remain an operator requirement, status=%s", plan.Status)
	}
}

func TestBuildPlanUnsupportedDominatesOperatorRequirements(t *testing.T) {
	report := Evaluate(Environment{OS: "linux", Arch: "arm64", EUID: 1000, Commands: map[string]string{}}, ProvisioningSupport{})
	plan := BuildPlan(report)
	if plan.Status != PlanUnsupported {
		t.Fatalf("status=%s want=%s", plan.Status, PlanUnsupported)
	}
	if len(plan.Requirements) == 0 {
		t.Fatal("unsupported plan lost requirement evidence")
	}
}

func TestBuildPlanAllowsConstrainedHostWhenRequiredCapabilitiesAreAlreadyReady(t *testing.T) {
	report := Evaluate(Environment{
		OS:                     "linux",
		Arch:                   "amd64",
		EUID:                   1000,
		Distribution:           "custom-host",
		Commands:               map[string]string{},
		PersistentServiceReady: true,
		PostgreSQLReady:        true,
		PrivateStorageReady:    true,
		HTTPSIngressReady:      true,
		PublicTLSReady:         true,
		HTTPSPort:              PortInUse,
	}, ProvisioningSupport{})
	plan := BuildPlan(report)
	if plan.Status != PlanReady {
		t.Fatalf("provider-satisfied constrained host status=%s requirements=%#v", plan.Status, plan.Requirements)
	}
	if len(plan.Actions) != 0 || len(plan.Requirements) != 0 {
		t.Fatalf("ready constrained host produced work: actions=%#v requirements=%#v", plan.Actions, plan.Requirements)
	}
}

func TestBuildPlanRejectsMalformedAdapterEvidence(t *testing.T) {
	for name, capability := range map[string]Capability{
		"unknown-state":    {ID: "database.postgresql", Required: true, State: CapabilityState("mystery")},
		"missing-provider": {ID: "database.postgresql", Required: true, State: StateProvisionable},
		"invalid-id":       {ID: "database postgresql", Required: true, State: StateAvailable},
	} {
		t.Run(name, func(t *testing.T) {
			plan := BuildPlan(Report{Capabilities: []Capability{capability}})
			if plan.Status != PlanUnsupported || len(plan.Requirements) != 1 {
				t.Fatalf("malformed evidence did not fail closed: %#v", plan)
			}
		})
	}
}

func TestBuildPlanRejectsDuplicateRequiredEvidence(t *testing.T) {
	plan := BuildPlan(Report{Capabilities: []Capability{
		{ID: "storage.private", Required: true, State: StateAvailable},
		{ID: "storage.private", Required: true, State: StateAvailable},
	}})
	if plan.Status != PlanUnsupported || len(plan.Requirements) != 1 {
		t.Fatalf("duplicate required evidence did not fail closed: %#v", plan)
	}
}
