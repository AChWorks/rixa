// SPDX-License-Identifier: MPL-2.0

package serverinstall

import (
	"context"
	"github.com/AChWorks/rixa/internal/releaseartifact"
	"github.com/AChWorks/rixa/internal/releasepackage"
)

const bootstrapSchema = "rixa.native-bootstrap.v1"

type BootstrapOperation struct {
	OperationKind         string `json:"operation_kind"`
	OperationID           string `json:"operation_id"`
	InstallationOperation string `json:"installation_operation,omitempty"`
	State                 string `json:"state"`
	Release               string `json:"release,omitempty"`
	ManagerPath           string `json:"manager,omitempty"`
	ManagerSHA256         string `json:"manager_sha256,omitempty"`
	HelperPath            string `json:"-"`
	Message               string `json:"message,omitempty"`
}
type BootstrapDiscovery struct {
	Schema                string               `json:"schema"`
	State                 string               `json:"state"`
	Code                  string               `json:"code"`
	Message               string               `json:"message"`
	InstallationOperation string               `json:"installation_operation,omitempty"`
	SelectedOperation     string               `json:"selected_operation,omitempty"`
	Postgres              []PostgresDiscovery  `json:"postgres_operations,omitempty"`
	Operations            []BootstrapOperation `json:"operations,omitempty"`
	Actions               []string             `json:"actions"`
}
type BootstrapRequest struct {
	Channel           string
	ReleaseTag        string
	OutputDir         string
	OperationID       string
	Update            bool
	PostgresOperation string
}
type BootstrapRoute struct {
	ManagerPath           string   `json:"manager"`
	ManagerSHA256         string   `json:"manager_sha256"`
	OperationKind         string   `json:"operation_kind"`
	OperationID           string   `json:"operation_id"`
	InstallationOperation string   `json:"installation_operation,omitempty"`
	Release               string   `json:"release,omitempty"`
	Arguments             []string `json:"arguments"`
	HelperPath            string   `json:"-"`
	Plain                 bool     `json:"plain,omitempty"`
	NoColor               bool     `json:"no_color,omitempty"`
}
type BootstrapPrepared struct {
	Schema       string         `json:"schema"`
	State        string         `json:"state"`
	Code         string         `json:"code"`
	Message      string         `json:"message"`
	Route        BootstrapRoute `json:"route"`
	ArchivePath  string         `json:"archive,omitempty"`
	BundlePath   string         `json:"bundle,omitempty"`
	SourceCommit string         `json:"source_commit,omitempty"`
}
type BootstrapFacade interface {
	DiscoverBootstrap(context.Context) (BootstrapDiscovery, error)
	PrepareBootstrap(context.Context, BootstrapRequest) (BootstrapPrepared, error)
	OpenBootstrap(context.Context, string) (BootstrapRoute, error)
}

// All override seams below are private unit-test dependencies. Public methods
// always use protected native paths and the real shared verifier/parser.
type bootstrapBackend struct {
	m           *Manager
	managerRoot string
	cacheRoot   string
	footprint   []string
	clean       func(context.Context) error
	current     func() (string, string, error)
	fetch       func(context.Context, releaseartifact.FetchRequest) (releaseartifact.FetchResult, error)
	verify      func(context.Context, releaseartifact.Request) (releaseartifact.Result, error)
	extract     func(string, string, releasepackage.Expected) (releasepackage.Result, error)
	postgres    func(context.Context) ([]PostgresDiscovery, error)
	credentials func(context.Context, string) (PostgresCredentials, error)
}

func nativeBootstrap(m *Manager) *bootstrapBackend {
	b := &bootstrapBackend{m: m, managerRoot: managerRoot, cacheRoot: "/opt/rixa-bootstrap", footprint: []string{runtimeRoot, "/var/lib/rixa", releaseRoot, unitPath, unitPath + ".d", "/run/systemd/system/rixa.service", "/run/systemd/system/rixa.service.d", "/etc/rixa/service.env"}, fetch: releaseartifact.Fetch, verify: releaseartifact.Verify, extract: releasepackage.Extract}
	b.clean = b.cleanHost
	b.current = bootstrapCurrentImage
	b.postgres = m.DiscoverPostgres
	b.credentials = m.LoadPostgresCredentials
	return b
}
func (m *Manager) DiscoverBootstrap(ctx context.Context) (BootstrapDiscovery, error) {
	return nativeBootstrap(m).discover(ctx)
}
func (m *Manager) OpenBootstrap(ctx context.Context, id string) (BootstrapRoute, error) {
	return nativeBootstrap(m).open(ctx, id)
}
func (m *Manager) PrepareBootstrap(ctx context.Context, r BootstrapRequest) (BootstrapPrepared, error) {
	return nativeBootstrap(m).prepare(ctx, r)
}
