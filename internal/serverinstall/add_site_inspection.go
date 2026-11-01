// SPDX-License-Identifier: MPL-2.0
package serverinstall

import (
	"context"
	"crypto/x509"
	"encoding/pem"
	"net/url"
	"path/filepath"
	"strings"
	"time"
)

// AddSiteInspectionDraft contains only the prospective site-b credentials.
// It is never stored in a native installation or printed in an operation result.
// Admission does not implement or authorize a topology-changing Apply.
type AddSiteInspectionDraft struct {
	InstallationOperation string `json:"installation_operation"`
	SiteBOrigin           string `json:"site_b_origin"`
	SiteBDSN              string `json:"site_b_dsn"`
	SiteBLogin            string `json:"site_b_login"`
	SiteBPassword         string `json:"site_b_password"`
}

func (d AddSiteInspectionDraft) withInstallation(base Draft) Draft {
	result := base
	result.SiteBOrigin, result.SiteBDSN = d.SiteBOrigin, d.SiteBDSN
	result.SiteBLogin, result.SiteBPassword = d.SiteBLogin, d.SiteBPassword
	return result
}

// ValidateAddSiteInspection checks the same three-origin, account and physical
// isolation input contract as the initial two-site path. The existing bootstrap
// passwords are intentionally absent in an installed journal, so validation
// substitutes non-credential placeholders locally and never returns them.
func ValidateAddSiteInspection(base Draft, proposed AddSiteInspectionDraft) []Problem {
	var problems []Problem
	if !operationSyntax.MatchString(proposed.InstallationOperation) || proposed.InstallationOperation != base.OperationID {
		problems = append(problems, Problem{"installation_operation", "installation", "Choose the exact original native installation operation."})
	}
	if siteBRequested(base) {
		problems = append(problems, Problem{"site_b_origin", "already-declared", "The installation already declares site-b; inspect its existing site instead."})
		return problems
	}
	if proposed.SiteBOrigin == "" && proposed.SiteBDSN == "" && proposed.SiteBLogin == "" && proposed.SiteBPassword == "" {
		problems = append(problems, Problem{"site_b_origin", "missing-site", "Provide all four new site-b inputs."})
		return problems
	}
	candidate := proposed.withInstallation(base)
	if candidate.ControlPassword == "" {
		candidate.ControlPassword = strings.Repeat("x", 20)
	}
	if candidate.SitePassword == "" {
		candidate.SitePassword = strings.Repeat("x", 20)
	}
	return append(problems, ValidateDraft(candidate)...)
}

func tlsCoversAddSite(certPEM []byte, origin string, now time.Time) bool {
	u, err := url.Parse(origin)
	if err != nil || u == nil || u.Scheme != "https" || u.Hostname() == "" {
		return false
	}
	block, _ := pem.Decode(certPEM)
	if block == nil || block.Type != "CERTIFICATE" {
		return false
	}
	leaf, err := x509.ParseCertificate(block.Bytes)
	if err != nil || now.Before(leaf.NotBefore) || !now.Before(leaf.NotAfter) || leaf.VerifyHostname(u.Hostname()) != nil {
		return false
	}
	for _, usage := range leaf.ExtKeyUsage {
		if usage == x509.ExtKeyUsageAny || usage == x509.ExtKeyUsageServerAuth {
			return true
		}
	}
	return len(leaf.ExtKeyUsage) == 0
}

func addSiteInspectionResult(operation string) Result {
	return Result{OperationKind: "add-site-inspection", InstallationOperation: operation, OperationID: operation,
		State: "blocked", Code: "not-reviewed", Message: "Inspection only; no Add Site effect, review token, or Apply capability is available.",
		Actions: []string{"inspect"}}
}

// InspectAddSite is a bounded *non-applying* operator admission. It positively
// binds the running one-site native installation, its immutable original data
// and runtime, then probes the new three-way PostgreSQL role isolation and
// existing TLS coverage. In particular it never provisions a DB, bootstrap
// account, config change, restart or site directory.
func (m *Manager) InspectAddSite(ctx context.Context, proposed AddSiteInspectionDraft) (Result, error) {
	result := addSiteInspectionResult(proposed.InstallationOperation)
	if e := m.authorize(); e != nil {
		return result, e
	}
	if !operationSyntax.MatchString(proposed.InstallationOperation) {
		result.Code = "invalid-input"
		result.Problems = []Problem{{Field: "installation_operation", Code: "format", Message: "Use an exact existing native installation ID."}}
		return result, ErrInput
	}
	unlock, err := m.lock(ctx, false)
	if err != nil {
		return result, err
	}
	defer unlock()
	owner, err := readPrivate(filepath.Join(m.stateRoot, "installation"), m.ownerUID, 128)
	if err != nil || string(owner) != proposed.InstallationOperation+"\n" {
		result.Code = "installation-ownership"
		return result, ErrState
	}
	parent, err := m.load(proposed.InstallationOperation)
	if err != nil {
		result.Code = "installation-ownership"
		return result, err
	}
	if parent.State != "installed" || parent.Attempts["remove"] != "" {
		result.Code = "installation-not-active"
		return result, ErrState
	}
	result.AdminURL = parent.Draft.ControlOrigin + "/admin"
	result.Release = parent.Draft.Release
	if problems := ValidateAddSiteInspection(parent.Draft, proposed); len(problems) > 0 {
		result.Code, result.Problems = "invalid-input", problems
		return result, ErrInput
	}
	// Adding while a managed update is active or pending would invalidate its
	// frozen topology and recovery snapshots. Refuse even a positive inspection.
	pending, pe := m.updateMarker("pending-update")
	active, ae := m.updateMarker("active-update")
	if pe != nil || ae != nil || pending != "" || active != "" {
		result.Code = "update-ownership"
		return result, ErrState
	}
	if err = m.validateInstalled(ctx, parent); err != nil || m.updateUnitFileState(ctx) != "enabled" {
		result.Code = "installation-unhealthy"
		return result, ErrPrerequisite
	}
	cert, err := readPrivate(m.tlsSnapshotPath(parent, false), m.ownerUID, 1<<20)
	if err != nil || digest(cert) != parent.CertSHA256 || !tlsCoversAddSite(cert, proposed.SiteBOrigin, time.Now()) {
		result.Code = "tls-hostname-unavailable"
		result.Problems = []Problem{{Field: "site_b_origin", Code: "tls-hostname", Message: "The currently owned TLS certificate must already cover site-b; replacing certificates is not supported by this inspection."}}
		return result, ErrPrerequisite
	}
	candidate := proposed.withInstallation(parent.Draft)
	targets, err := databaseIdentities(ctx, candidate)
	if err != nil || len(targets) != 3 || targets["control"] != parent.DatabaseTargets["control"] ||
		targets["site-a"] != parent.DatabaseTargets["site-a"] || targets["site-b"] == "" {
		result.Code = "database-isolation-unverified"
		result.Problems = []Problem{{Field: "site_b_dsn", Code: "database-roles", Message: "Supply a third independently owned PostgreSQL 18 database with isolated credentials, no foreign CONNECT/role membership, and unchanged original database identities."}}
		return result, ErrPrerequisite
	}
	result.State, result.Code = "inspected", "prerequisites-verified"
	result.Message = "A currently healthy one-site installation, separate third PostgreSQL database/role and existing TLS hostname coverage were verified. No site was added. Applying a topology change remains unavailable."
	return result, nil
}
