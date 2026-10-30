// SPDX-License-Identifier: MPL-2.0
package serverinstall

import (
	"context"
	"fmt"
	"io"
	"log/slog"

	"github.com/AChWorks/achrix/installation"
)

// PostgresDraft explicitly requests the clean dedicated native prerequisite
// profile. Compatible supplied databases use the existing native DSN path.
type PostgresDraft struct {
	OperationID   string `json:"operation_id"`
	Port          int    `json:"port"`
	DedicatedHost bool   `json:"dedicated_host"`
}
type PostgresPackage struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}
type PostgresPlan struct {
	Profile        string             `json:"profile"`
	Cluster        string             `json:"cluster"`
	Port           int                `json:"port"`
	Packages       []PostgresPackage  `json:"packages"`
	CommonPackages []PostgresPackage  `json:"common_packages"`
	Effects        []string           `json:"effects"`
	Retained       []string           `json:"retained"`
	Assessment     *installation.Plan `json:"assessment,omitempty"`
}

// PostgresResult is public and intentionally contains no credential source.
type PostgresResult struct {
	Result
	Plan *PostgresPlan `json:"postgres_plan,omitempty"`
}
type PostgresDiscovery struct {
	OperationID   string   `json:"operation_id"`
	State         string   `json:"state"`
	Stage         string   `json:"stage,omitempty"`
	Code          string   `json:"code"`
	Cluster       string   `json:"cluster"`
	Port          int      `json:"port"`
	ManagerSHA256 string   `json:"manager_sha256,omitempty"`
	ManagerPath   string   `json:"manager_path,omitempty"`
	Actions       []string `json:"actions"`
}

// PostgresCredentials is a private in-process handoff into the protected native
// installation draft. It must never be a public CLI JSON verb or presentation.
type PostgresCredentials struct {
	ControlDSN string `json:"-"`
	SiteDSN    string `json:"-"`
}

func (PostgresCredentials) Format(s fmt.State, _ rune) {
	_, _ = io.WriteString(s, "serverinstall.PostgresCredentials{protected:[redacted]}")
}
func (PostgresCredentials) LogValue() slog.Value {
	return slog.StringValue("[protected PostgreSQL credentials]")
}

type PostgresFacade interface {
	SavePostgresDraft(context.Context, PostgresDraft) (PostgresResult, error)
	LoadPostgresDraft(context.Context, string) (PostgresDraft, error)
	ReviewPostgres(context.Context, string) (PostgresResult, error)
	ApplyPostgres(context.Context, string, string) (PostgresResult, error)
	ResumePostgres(context.Context, string) (PostgresResult, error)
	PostgresStatus(context.Context, string) (PostgresResult, error)
	DiscoverPostgres(context.Context) ([]PostgresDiscovery, error)
	LoadPostgresCredentials(context.Context, string) (PostgresCredentials, error)
}

func ValidatePostgresDraft(d PostgresDraft) []Problem {
	var p []Problem
	if !operationSyntax.MatchString(d.OperationID) {
		p = append(p, Problem{"operation_id", "format", "Use 1–48 lowercase letters, numbers or hyphens, beginning with a letter or number."})
	}
	if d.Port < 1024 || d.Port > 65535 {
		p = append(p, Problem{"port", "port", "Choose an explicitly unused unprivileged TCP port from 1024 to 65535."})
	}
	if !d.DedicatedHost {
		p = append(p, Problem{"dedicated_host", "dedicated-host-required", "Explicitly confirm this is a dedicated clean supported host. Existing or shared PostgreSQL is supplied through the normal database input instead."})
	}
	return p
}
