// SPDX-License-Identifier: MPL-2.0

package installer

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"os/exec"
	"runtime"
	"sort"
	"strconv"
	"strings"
)

type CapabilityState string

const (
	StateAvailable        CapabilityState = "available"
	StateProvisionable    CapabilityState = "provisionable"
	StateOperatorRequired CapabilityState = "operator-required"
	StateUnsupported      CapabilityState = "unsupported"
)

type PortState string

const (
	PortUnknown PortState = "unknown"
	PortFree    PortState = "free"
	PortInUse   PortState = "in-use"
)

type Platform struct {
	OS               string `json:"os"`
	Arch             string `json:"arch"`
	Distribution     string `json:"distribution,omitempty"`
	VersionID        string `json:"version_id,omitempty"`
	DistributionLike string `json:"distribution_like,omitempty"`
	Privileged       bool   `json:"privileged"`
}

type Capability struct {
	ID       string          `json:"id"`
	Required bool            `json:"required"`
	State    CapabilityState `json:"state"`
	Provider string          `json:"provider,omitempty"`
	Detail   string          `json:"detail"`
}

type Report struct {
	Platform     Platform     `json:"platform"`
	Capabilities []Capability `json:"capabilities"`
}

type ProvisioningSupport struct {
	PersistentService    string
	PostgreSQL           string
	PrivateStorage       string
	HTTPSIngress         string
	PublicTLSCertificate string
}

type Environment struct {
	OS                     string
	Arch                   string
	EUID                   int
	Distribution           string
	VersionID              string
	DistributionLike       string
	Commands               map[string]string
	Systemd                bool
	PersistentServiceReady bool
	PostgreSQLReady        bool
	PrivateStorageReady    bool
	HTTPSIngressReady      bool
	PublicTLSReady         bool
	HTTPSPort              PortState
}

func CurrentProvisioningSupport() ProvisioningSupport {
	// This binary currently exposes discovery/planning only. Distribution-specific
	// mutating adapters are added by the server/panel installer surfaces, and they
	// must opt into only the operations they can actually execute and verify.
	return ProvisioningSupport{}
}

func Preflight() Report {
	return Evaluate(Inspect(), CurrentProvisioningSupport())
}

func Inspect() Environment {
	env := Environment{
		OS:        runtime.GOOS,
		Arch:      runtime.GOARCH,
		EUID:      os.Geteuid(),
		Commands:  make(map[string]string),
		HTTPSPort: PortUnknown,
	}
	if data, err := os.ReadFile("/etc/os-release"); err == nil {
		values := parseOSRelease(string(data))
		env.Distribution = strings.ToLower(values["ID"])
		env.VersionID = values["VERSION_ID"]
		env.DistributionLike = strings.ToLower(values["ID_LIKE"])
	}
	for _, name := range []string{"apt-get", "dnf", "yum", "apk", "pacman", "systemctl", "psql", "pg_isready", "openssl"} {
		if path, err := exec.LookPath(name); err == nil {
			env.Commands[name] = path
		}
	}
	if info, err := os.Stat("/run/systemd/system"); err == nil && info.IsDir() && env.Commands["systemctl"] != "" {
		env.Systemd = true
	}
	env.HTTPSPort = inspectListeningPort(443)
	return env
}

func Evaluate(env Environment, support ProvisioningSupport) Report {
	report := Report{
		Platform: Platform{
			OS:               env.OS,
			Arch:             env.Arch,
			Distribution:     env.Distribution,
			VersionID:        env.VersionID,
			DistributionLike: env.DistributionLike,
			Privileged:       env.EUID == 0,
		},
	}

	if env.OS == "linux" && env.Arch == "amd64" {
		report.add("runtime.platform", true, StateAvailable, "linux-amd64", "Published Rixa runtime target is supported.")
	} else {
		report.add("runtime.platform", true, StateUnsupported, "", fmt.Sprintf("Current target is %s/%s; the published installation profile currently requires linux/amd64.", env.OS, env.Arch))
	}

	if env.OS == "linux" && env.Arch == "amd64" {
		report.add("runtime.direct-tls", false, StateAvailable, "rixa", "Rixa can terminate browser TLS directly while preserving exact Host, Origin and client socket semantics.")
	} else {
		report.add("runtime.direct-tls", false, StateUnsupported, "", "Direct-TLS hosting requires a supported published Rixa runtime target.")
	}

	if env.EUID == 0 {
		report.add("host.privilege", false, StateAvailable, "root", "Privileged provisioning is permitted by the current process.")
	} else {
		report.add("host.privilege", false, StateOperatorRequired, "", "Root privilege is unavailable; a hosting administrator must provide protected paths/services or run the privileged server installer.")
	}

	packageName, packageSupported := packageManager(env)
	switch {
	case packageName != "" && packageSupported:
		report.add("host.package-manager", false, StateAvailable, packageName, "Debian-family package management was detected; adapter support is declared separately.")
	case packageName != "":
		report.add("host.package-manager", false, StateOperatorRequired, packageName, "A package manager is present, but automated provisioning for this distribution is not yet a supported Rixa path.")
	default:
		report.add("host.package-manager", false, StateOperatorRequired, "", "No supported package manager was detected; required packages must be supplied by the host/operator.")
	}

	if env.PersistentServiceReady {
		report.add("service.persistence", true, StateAvailable, "existing", "A persistent restartable Rixa service has already been verified.")
	} else if support.PersistentService != "" {
		report.add("service.persistence", true, StateProvisionable, support.PersistentService, "The selected installer adapter declares a supported mechanism to create and verify a persistent Rixa service.")
	} else {
		detail := "Provide a persistent restartable Rixa process through the hosting environment."
		if env.Systemd {
			detail = "systemd is present, but this installer surface has not declared a supported Rixa service adapter; use a suitable installer adapter or have the operator provide the service."
		}
		report.add("service.persistence", true, StateOperatorRequired, "", detail)
	}

	if env.PostgreSQLReady {
		report.add("database.postgresql", true, StateAvailable, "existing", "The selected PostgreSQL endpoint and credentials have been verified for this installation.")
	} else if support.PostgreSQL != "" {
		report.add("database.postgresql", true, StateProvisionable, support.PostgreSQL, "The selected installer adapter declares a supported mechanism to provision and verify PostgreSQL, or reuse a supplied compatible endpoint.")
	} else {
		detail := "Provide a compatible reachable PostgreSQL database/credentials or use a supported privileged installer path that can provision it."
		if env.Commands["psql"] != "" && env.Commands["pg_isready"] != "" {
			detail = "PostgreSQL client/readiness tooling is present, but the installation still needs a verified endpoint, database and credentials."
		}
		report.add("database.postgresql", true, StateOperatorRequired, "", detail)
	}

	if env.PrivateStorageReady {
		report.add("storage.private", true, StateAvailable, "existing", "Protected non-public Rixa configuration, secret and data roots have been verified.")
	} else if support.PrivateStorage != "" {
		report.add("storage.private", true, StateProvisionable, support.PrivateStorage, "The selected installer adapter declares a supported mechanism to provision and verify protected Rixa configuration, secret and data roots.")
	} else {
		report.add("storage.private", true, StateOperatorRequired, "", "Provide private non-public configuration/data paths with suitable ownership and permissions; the current installer cannot create them automatically here.")
	}

	if env.HTTPSIngressReady {
		report.add("ingress.https", true, StateAvailable, "existing", "A supported public HTTPS ingress path to Rixa has been verified.")
	} else if support.HTTPSIngress != "" {
		detail := "The selected installer adapter declares a supported mechanism to configure and verify public HTTPS ingress."
		if env.HTTPSPort == PortInUse {
			detail = "TCP/443 already has a listener; the selected adapter declares a supported mechanism to integrate with and verify that existing ingress without blindly replacing it."
		}
		report.add("ingress.https", true, StateProvisionable, support.HTTPSIngress, detail)
	} else {
		switch env.HTTPSPort {
		case PortFree:
			report.add("ingress.https", true, StateOperatorRequired, "", "TCP/443 is free, but this installer surface has not declared a supported ingress adapter.")
		case PortInUse:
			report.add("ingress.https", true, StateOperatorRequired, "existing-listener", "TCP/443 already has a listener. Preserve it; a separately supported panel/front-proxy integration is required instead of overwriting it.")
		default:
			report.add("ingress.https", true, StateOperatorRequired, "", "Could not establish TCP/443 ownership from the host; the operator must provide a supported public HTTPS ingress path.")
		}
	}

	if env.PublicTLSReady {
		report.add("tls.public-certificate", true, StateAvailable, "existing", "Certificate/key material for every configured public hostname has been verified.")
	} else if support.PublicTLSCertificate != "" {
		report.add("tls.public-certificate", true, StateProvisionable, support.PublicTLSCertificate, "The selected installer adapter declares a supported mechanism to provision and verify certificate material after validated domain/DNS input.")
	} else {
		report.add("tls.public-certificate", true, StateOperatorRequired, "", "Provide validated domain/certificate material or use a supported installer adapter that provisions it; preflight never fabricates or weakens TLS.")
	}

	sort.Slice(report.Capabilities, func(i, j int) bool {
		return report.Capabilities[i].ID < report.Capabilities[j].ID
	})
	return report
}

func (r *Report) add(id string, required bool, state CapabilityState, provider, detail string) {
	r.Capabilities = append(r.Capabilities, Capability{ID: id, Required: required, State: state, Provider: provider, Detail: detail})
}

func (r Report) Capability(id string) (Capability, bool) {
	for _, capability := range r.Capabilities {
		if capability.ID == id {
			return capability, true
		}
	}
	return Capability{}, false
}

func WriteText(w io.Writer, report Report) error {
	if w == nil {
		return fmt.Errorf("installer: nil output")
	}
	if _, err := fmt.Fprintf(w, "Rixa installation preflight\nplatform: %s/%s", report.Platform.OS, report.Platform.Arch); err != nil {
		return err
	}
	if report.Platform.Distribution != "" {
		if _, err := fmt.Fprintf(w, " (%s %s)", report.Platform.Distribution, report.Platform.VersionID); err != nil {
			return err
		}
	}
	if _, err := fmt.Fprintln(w); err != nil {
		return err
	}
	for _, capability := range report.Capabilities {
		provider := ""
		if capability.Provider != "" {
			provider = " [" + capability.Provider + "]"
		}
		kind := "context"
		if capability.Required {
			kind = "required"
		}
		if _, err := fmt.Fprintf(w, "%-28s %-8s %-18s%s %s\n", capability.ID, kind, capability.State, provider, capability.Detail); err != nil {
			return err
		}
	}
	plan := BuildPlan(report)
	_, err := fmt.Fprintf(w, "installation-status: %s\n", plan.Status)
	return err
}

func packageManager(env Environment) (string, bool) {
	if env.Commands["apt-get"] != "" {
		return "apt-get", debianLike(env)
	}
	for _, name := range []string{"dnf", "yum", "apk", "pacman"} {
		if env.Commands[name] != "" {
			return name, false
		}
	}
	return "", false
}

func debianLike(env Environment) bool {
	if env.Distribution == "debian" || env.Distribution == "ubuntu" {
		return true
	}
	for _, value := range strings.Fields(env.DistributionLike) {
		if value == "debian" || value == "ubuntu" {
			return true
		}
	}
	return false
}

func parseOSRelease(data string) map[string]string {
	result := make(map[string]string)
	scanner := bufio.NewScanner(strings.NewReader(data))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, value, ok := strings.Cut(line, "=")
		if !ok || key == "" {
			continue
		}
		value = strings.TrimSpace(value)
		if len(value) >= 2 && ((value[0] == '"' && value[len(value)-1] == '"') || (value[0] == '\'' && value[len(value)-1] == '\'')) {
			if unquoted, err := strconv.Unquote(value); err == nil {
				value = unquoted
			} else {
				value = strings.Trim(value, "\"'")
			}
		}
		result[key] = value
	}
	return result
}

func inspectListeningPort(port uint16) PortState {
	known := false
	for _, path := range []string{"/proc/net/tcp", "/proc/net/tcp6"} {
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		known = true
		if procNetHasListener(string(data), port) {
			return PortInUse
		}
	}
	if known {
		return PortFree
	}
	return PortUnknown
}

func procNetHasListener(data string, port uint16) bool {
	scanner := bufio.NewScanner(strings.NewReader(data))
	first := true
	for scanner.Scan() {
		if first {
			first = false
			continue
		}
		fields := strings.Fields(scanner.Text())
		if len(fields) < 4 || fields[3] != "0A" {
			continue
		}
		local := fields[1]
		index := strings.LastIndexByte(local, ':')
		if index < 0 || index+1 >= len(local) {
			continue
		}
		value, err := strconv.ParseUint(local[index+1:], 16, 16)
		if err == nil && uint16(value) == port {
			return true
		}
	}
	return false
}
