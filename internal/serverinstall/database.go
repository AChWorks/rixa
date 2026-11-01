// SPDX-License-Identifier: MPL-2.0
package serverinstall

import (
	"context"
	"fmt"
	"strconv"
	"time"

	"github.com/AChWorks/rixa/internal/product"
	"github.com/jackc/pgx/v5"
)

// Role isolation is intentionally a prerequisite failure, never a leaked DSN
// or catalog error. Its identity allows Review to give actionable guidance.
var errNativeRoleIsolation = fmt.Errorf("%w: independent PostgreSQL roles and privileges required", ErrPrerequisite)

// Retain physical server/database identities, not just mutable DSN addresses.
// Product.DatabaseTargets first enforces its existing safe TLS/isolation rules.
func databaseIdentities(parent context.Context, d Draft) (map[string]string, error) {
	c, e := product.ParseConfig(configBytes(d, "", ""))
	if e != nil {
		return nil, ErrInput
	}
	targets, e := c.DatabaseTargets(parent, databaseLookup(d))
	if e != nil {
		return nil, ErrPrerequisite
	}
	identities := map[string]string{}
	for _, target := range targets {
		ctx, cancel := context.WithTimeout(parent, 3*time.Second)
		cfg, e := pgx.ParseConfig(target.DSN)
		if e != nil {
			cancel()
			return nil, ErrInput
		}
		cfg.ConnectTimeout = time.Second
		conn, e := pgx.ConnectConfig(ctx, cfg)
		if e != nil {
			cancel()
			return nil, ErrPrerequisite
		}
		var version int
		var server, database string
		e = conn.QueryRow(ctx, "SELECT current_setting('server_version_num')::int, c.system_identifier::text, d.oid::text FROM pg_control_system() AS c JOIN pg_database AS d ON d.datname=current_database()").Scan(&version, &server, &database)
		closeCtx, closeCancel := context.WithTimeout(context.WithoutCancel(parent), time.Second)
		ce := conn.Close(closeCtx)
		closeCancel()
		cancel()
		if e != nil || ce != nil || version/10000 != 18 || server == "" || database == "" {
			return nil, ErrPrerequisite
		}
		identities[target.ID] = server + ":" + database
	}
	if len(identities) != databaseTargetCount(d) {
		return nil, ErrInput
	}
	if siteBRequested(d) {
		// Source-level optional two-site acceptance additionally requires three
		// least-privileged, distinct PostgreSQL owners without cross-database
		// CONNECT or role membership. The established single-site admission is
		// not broadened or otherwise changed.
		if err := validateNativeSiteDatabaseRoles(parent, targets, identities); err != nil {
			return nil, errNativeRoleIsolation
		}
	}
	return identities, nil
}

// nativeDatabaseRole is a bounded read-only catalog observation. DB ownership
// is necessary for the product's native schema creation/migrations; no grant,
// revocation, role creation or other database effect is performed by Review.
type nativeDatabaseRole struct {
	roleOID     uint32
	roleName    string
	databaseOID uint32
	serverID    string
}

// validateNativeSiteDatabaseRoles is deliberately limited to the optional
// Control + site-a + site-b native profile. It does not change the one-site
// installation contract, the generic Rixa multi-site runtime or provisioning.
func validateNativeSiteDatabaseRoles(parent context.Context, targets []product.ResolvedDatabase, identities map[string]string) error {
	if len(targets) != 3 || len(identities) != 3 {
		return ErrPrerequisite
	}
	roles := make(map[string]nativeDatabaseRole, len(targets))
	names := map[string]bool{}
	for _, target := range targets {
		ctx, cancel := context.WithTimeout(parent, 3*time.Second)
		cfg, err := pgx.ParseConfig(target.DSN)
		if err != nil {
			cancel()
			return ErrPrerequisite
		}
		cfg.ConnectTimeout = time.Second
		conn, err := pgx.ConnectConfig(ctx, cfg)
		if err != nil {
			cancel()
			return ErrPrerequisite
		}
		var roleOID, dbOID, ownerOID uint32
		var roleName string
		var sameLogin, canLogin, superuser, createDB, createRole, replication, bypassRLS bool
		var memberships int
		err = conn.QueryRow(ctx, `SELECT r.oid, r.rolname, current_user = session_user,
                   r.rolcanlogin, r.rolsuper, r.rolcreatedb, r.rolcreaterole,
                   r.rolreplication, r.rolbypassrls, d.oid, d.datdba,
                   (SELECT count(*) FROM pg_catalog.pg_auth_members m WHERE m.member = r.oid)
            FROM pg_catalog.pg_roles r
            JOIN pg_catalog.pg_database d ON d.datname = current_database()
            WHERE r.rolname = session_user`).Scan(
			&roleOID, &roleName, &sameLogin, &canLogin,
			&superuser, &createDB, &createRole, &replication, &bypassRLS, &dbOID, &ownerOID, &memberships,
		)
		closeCtx, closeCancel := context.WithTimeout(context.WithoutCancel(parent), time.Second)
		closed := conn.Close(closeCtx)
		closeCancel()
		cancel()
		if err != nil || closed != nil || roleOID == 0 || dbOID == 0 ||
			roleOID != ownerOID || !sameLogin || !canLogin || superuser ||
			createDB || createRole || replication || bypassRLS || memberships != 0 || roleName == "" ||
			names[roleName] {
			return ErrPrerequisite
		}
		names[roleName] = true
		observed := identities[target.ID]
		colon := -1
		for i := len(observed) - 1; i >= 0; i-- {
			if observed[i] == ':' {
				colon = i
				break
			}
		}
		if colon < 1 {
			return ErrPrerequisite
		}
		oid, e := strconv.ParseUint(observed[colon+1:], 10, 32)
		if e != nil || uint32(oid) != dbOID {
			return ErrPrerequisite
		}
		roles[target.ID] = nativeDatabaseRole{roleOID: roleOID, roleName: roleName, databaseOID: dbOID, serverID: observed[:colon]}
	}

	// Compare effective access by role OID (includes PUBLIC/INHERIT), plus
	// role membership (which could enable SET ROLE despite NOINHERIT).
	// Different PostgreSQL clusters are physically isolated by system ID.
	for _, target := range targets {
		current := roles[target.ID]
		ctx, cancel := context.WithTimeout(parent, 3*time.Second)
		cfg, err := pgx.ParseConfig(target.DSN)
		if err != nil {
			cancel()
			return ErrPrerequisite
		}
		cfg.ConnectTimeout = time.Second
		conn, err := pgx.ConnectConfig(ctx, cfg)
		if err != nil {
			cancel()
			return ErrPrerequisite
		}
		valid := true
		for _, peer := range targets {
			if peer.ID == target.ID {
				continue
			}
			other := roles[peer.ID]
			if current.serverID != other.serverID {
				continue
			}
			var canConnect, isMember bool
			err = conn.QueryRow(ctx, `SELECT
                pg_catalog.has_database_privilege($1::oid, $2::oid, 'CONNECT'),
                pg_catalog.pg_has_role($1::oid, $3::oid, 'MEMBER')`,
				current.roleOID, other.databaseOID, other.roleOID).Scan(&canConnect, &isMember)
			if err != nil || canConnect || isMember {
				valid = false
				break
			}
		}
		closeCtx, closeCancel := context.WithTimeout(context.WithoutCancel(parent), time.Second)
		closed := conn.Close(closeCtx)
		closeCancel()
		cancel()
		if !valid || closed != nil {
			return ErrPrerequisite
		}
	}
	return nil
}
