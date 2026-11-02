// SPDX-License-Identifier: MPL-2.0
package serverinstall

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"net"
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

// readBoundTLSFiles accepts only the exact private regular certificate and key
// owned by the expected account. This is an observation, not permission to
// replace or repair TLS material. The leaf and key must form a valid pair.
func readBoundTLSFiles(certPath, keyPath string, owner int, certHash, keyHash string) ([]byte, []byte, error) {
	if filepath.Dir(certPath) != filepath.Dir(keyPath) || privateDir(filepath.Dir(certPath), owner, false) != nil {
		return nil, nil, ErrState
	}
	cert, certErr := readPrivate(certPath, owner, 1<<20)
	key, keyErr := readPrivate(keyPath, owner, 1<<20)
	if certErr != nil || keyErr != nil || digest(cert) != certHash || digest(key) != keyHash {
		return nil, nil, ErrState
	}
	if _, err := tls.X509KeyPair(cert, key); err != nil {
		return nil, nil, ErrState
	}
	return cert, key, nil
}

// proveServedSiteBCertificate performs a genuine, bounded TLS handshake on the
// already owned local listener, with SNI for the proposed site-b. It requires
// ordinary verified TLS server-auth AND the exact originally pinned leaf, not
// merely a different leaf signed by the same trusted CA. No HTTP request or
// application operation is sent to the unconfigured site.
func proveServedSiteBCertificate(ctx context.Context, endpoint, origin string, pinnedCert []byte) error {
	u, err := url.Parse(origin)
	if err != nil || u == nil || u.Scheme != "https" || u.Hostname() == "" {
		return ErrPrerequisite
	}
	block, _ := pem.Decode(pinnedCert)
	pool := x509.NewCertPool()
	if block == nil || block.Type != "CERTIFICATE" || !pool.AppendCertsFromPEM(pinnedCert) {
		return ErrPrerequisite
	}
	probeCtx, cancel := context.WithTimeout(ctx, 7*time.Second)
	defer cancel()
	dialer := &tls.Dialer{NetDialer: &net.Dialer{Timeout: 3 * time.Second}, Config: &tls.Config{
		RootCAs: pool, MinVersion: tls.VersionTLS12, ServerName: u.Hostname(),
	}}
	conn, err := dialer.DialContext(probeCtx, "tcp", endpoint)
	if err != nil {
		return ErrPrerequisite
	}
	defer conn.Close()
	tlsConn, ok := conn.(*tls.Conn)
	if !ok {
		return ErrPrerequisite
	}
	state := tlsConn.ConnectionState()
	if len(state.PeerCertificates) == 0 || !bytes.Equal(state.PeerCertificates[0].Raw, block.Bytes) {
		return ErrPrerequisite
	}
	return nil
}

// verifyCurrentSiteBTLS binds original immutable snapshots to both current
// private runtime files and the actual currently served TLS leaf. No changing
// TLS files, starting a service or provisioning site-b is allowed.
func (m *Manager) verifyCurrentSiteBTLS(ctx context.Context, j *journal, origin string) error {
	originalCert, originalKey, err := readBoundTLSFiles(
		m.tlsSnapshotPath(j, false), m.tlsSnapshotPath(j, true),
		m.ownerUID, j.CertSHA256, j.KeySHA256,
	)
	if err != nil {
		return ErrPrerequisite
	}
	currentCert, currentKey, err := readBoundTLSFiles(
		runtimeRoot+"/tls/cert.pem", runtimeRoot+"/tls/key.pem",
		int(j.UID), j.CertSHA256, j.KeySHA256,
	)
	if err != nil || !bytes.Equal(currentCert, originalCert) || !bytes.Equal(currentKey, originalKey) ||
		!tlsCoversAddSite(currentCert, origin, time.Now()) {
		return ErrPrerequisite
	}
	endpoint, err := localNativeEndpoint(j.Draft.Listen)
	if err != nil {
		return ErrPrerequisite
	}
	return proveServedSiteBCertificate(ctx, endpoint, origin, currentCert)
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
	// A pending update is always an incompatible in-flight topology owner.
	// A COMPLETED active update may be inspected only with the full immutable
	// original/update lineage, frozen manager/helper, current config snapshot
	// and actual loaded service positively verified. No update is resumed here.
	pending, pe := m.updateMarker("pending-update")
	active, ae := m.updateMarker("active-update")
	if pe != nil || ae != nil || pending != "" {
		result.Code = "update-ownership"
		return result, ErrState
	}
	current := parent
	if active != "" {
		current, err = m.completedAddSiteUpdate(ctx, parent, active)
		if err != nil {
			result.Code = "update-ownership"
			return result, ErrState
		}
		result.Release, result.ActiveRelease = current.Draft.Release, current.Draft.Release
	}
	// Bind to the frozen installed unit receipt rather than requiring this
	// newer inspector to generate identical systemd bytes. Update/Remove still
	// require exact supported templates for every effect they authorize.
	originalManager := filepath.Join(managerRoot, parent.ManagerSHA256)
	if !trustedExecutable(originalManager) {
		result.Code = "installation-unhealthy"
		return result, ErrPrerequisite
	}
	originalHash, managerError := hashFile(originalManager)
	if managerError != nil || originalHash != parent.ManagerSHA256 ||
		m.checkRecordedService(current) != nil ||
		m.updateUnitFileState(ctx) != "enabled" {
		result.Code = "installation-unhealthy"
		return result, ErrPrerequisite
	}
	if active == "" {
		if m.validateInstalledApplication(ctx, current) != nil {
			result.Code = "installation-unhealthy"
			return result, ErrPrerequisite
		}
	} else if m.loadedUpdateService(ctx, current, true) != nil ||
		m.validateInstalledHTTPS(ctx, current) != nil {
		// A compatible Update snapshot is already byte-pinned above. It can
		// retain supported presentation settings, so do not require original
		// generated config bytes on the updated-only inspection path.
		result.Code = "installation-unhealthy"
		return result, ErrPrerequisite
	}
	if m.verifyCurrentSiteBTLS(ctx, parent, proposed.SiteBOrigin) != nil {
		result.Code = "tls-runtime-unverified"
		result.Problems = []Problem{{Field: "site_b_origin", Code: "tls-binding", Message: "The installed TLS certificate/key and currently served leaf must match the original owned evidence and cover site-b. Inspection cannot replace TLS material."}}
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
	result.Message = "A currently healthy one-site installation (including an exactly bound completed update, if present), separate third PostgreSQL database/role and existing TLS hostname coverage were verified. No site was added. Applying a topology change remains unavailable."
	return result, nil
}
