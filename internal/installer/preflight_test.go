// SPDX-License-Identifier: MPL-2.0

package installer

import (
	"bytes"
	"strings"
	"testing"
)

func TestEvaluatePrivilegedDebianServer(t *testing.T) {
	env := Environment{
		OS:           "linux",
		Arch:         "amd64",
		EUID:         0,
		Distribution: "ubuntu",
		VersionID:    "24.04",
		Commands: map[string]string{
			"apt-get":   "/usr/bin/apt-get",
			"systemctl": "/usr/bin/systemctl",
		},
		Systemd:   true,
		HTTPSPort: PortFree,
	}
	report := Evaluate(env, ProvisioningSupport{DirectTLSIngress: true})
	assertCapability(t, report, "runtime.platform", StateAvailable)
	assertCapability(t, report, "host.privilege", StateAvailable)
	assertCapability(t, report, "host.package-manager", StateAvailable)
	assertCapability(t, report, "service.persistence", StateOperatorRequired)
	assertCapability(t, report, "database.postgresql", StateOperatorRequired)
	assertCapability(t, report, "storage.private", StateOperatorRequired)
	assertCapability(t, report, "ingress.https", StateProvisionable)
	assertCapability(t, report, "tls.public-certificate", StateOperatorRequired)
}

func TestEvaluateConstrainedHostIsActionable(t *testing.T) {
	env := Environment{
		OS:           "linux",
		Arch:         "amd64",
		EUID:         1000,
		Distribution: "almalinux",
		VersionID:    "10",
		Commands: map[string]string{
			"dnf":        "/usr/bin/dnf",
			"psql":       "/usr/bin/psql",
			"pg_isready": "/usr/bin/pg_isready",
		},
		HTTPSPort: PortInUse,
	}
	report := Evaluate(env, ProvisioningSupport{})
	assertCapability(t, report, "runtime.platform", StateAvailable)
	assertCapability(t, report, "host.privilege", StateOperatorRequired)
	assertCapability(t, report, "host.package-manager", StateOperatorRequired)
	assertCapability(t, report, "service.persistence", StateOperatorRequired)
	assertCapability(t, report, "database.postgresql", StateOperatorRequired)
	assertCapability(t, report, "storage.private", StateOperatorRequired)
	ingress := assertCapability(t, report, "ingress.https", StateOperatorRequired)
	if !strings.Contains(ingress.Detail, "already has a listener") {
		t.Fatalf("ingress detail is not actionable: %q", ingress.Detail)
	}
}

func TestEvaluateUnsupportedPlatformFailsClosed(t *testing.T) {
	report := Evaluate(Environment{OS: "linux", Arch: "arm64", EUID: 0, Commands: map[string]string{}}, ProvisioningSupport{})
	capability := assertCapability(t, report, "runtime.platform", StateUnsupported)
	if !strings.Contains(capability.Detail, "linux/amd64") {
		t.Fatalf("unsupported platform detail=%q", capability.Detail)
	}
}

func TestParseOSRelease(t *testing.T) {
	values := parseOSRelease("ID=ubuntu\nVERSION_ID=\"24.04\"\nID_LIKE=\"debian\"\n")
	if values["ID"] != "ubuntu" || values["VERSION_ID"] != "24.04" || values["ID_LIKE"] != "debian" {
		t.Fatalf("unexpected os-release parse: %#v", values)
	}
}

func TestProcNetHasListener(t *testing.T) {
	const table = "  sl  local_address rem_address   st\n   0: 00000000:01BB 00000000:0000 0A\n   1: 0100007F:20FB 00000000:0000 0A\n"
	if !procNetHasListener(table, 443) {
		t.Fatal("443 listener was not detected")
	}
	if procNetHasListener(table, 8444) {
		t.Fatal("unexpected listener detected")
	}
}

func TestWriteTextIncludesStatesAndDetails(t *testing.T) {
	report := Evaluate(Environment{
		OS: "linux", Arch: "amd64", EUID: 1000, Commands: map[string]string{}, HTTPSPort: PortUnknown,
	}, ProvisioningSupport{})
	var output bytes.Buffer
	if err := WriteText(&output, report); err != nil {
		t.Fatal(err)
	}
	text := output.String()
	for _, expected := range []string{"Rixa installation preflight", "runtime.platform", "operator-required", "PostgreSQL"} {
		if !strings.Contains(text, expected) {
			t.Fatalf("output missing %q: %s", expected, text)
		}
	}
}

func assertCapability(t *testing.T, report Report, id string, state CapabilityState) Capability {
	t.Helper()
	capability, ok := report.Capability(id)
	if !ok {
		t.Fatalf("capability %s missing", id)
	}
	if capability.State != state {
		t.Fatalf("capability %s state=%s want=%s detail=%s", id, capability.State, state, capability.Detail)
	}
	return capability
}
