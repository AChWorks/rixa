// SPDX-License-Identifier: MPL-2.0
package product

import (
	"context"
	"errors"
	"net/url"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

// PostgreSQL 18 admission evidence: real scram/password rejection, actual
// pg_control_system privilege denial, and recovery after a bounded grant.
// The only schema/role effects are scoped to an isolated disposable CI fixture.
func TestPreflightRealPostgreSQLAdmissionAndPhysicalIdentity(t *testing.T) {
	adminDSN := os.Getenv("RIXA_TEST_POSTGRES_DSN")
	if adminDSN == "" {
		if os.Getenv("CI") != "" {
			t.Fatal("RIXA_TEST_POSTGRES_DSN is required in CI")
		}
		t.Skip("set RIXA_TEST_POSTGRES_DSN for disposable real PostgreSQL admission proof")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	admin, err := pgx.Connect(ctx, adminDSN)
	if err != nil {
		t.Fatal("disposable PostgreSQL test admin connection unavailable")
	}
	defer admin.Close(context.Background())
	suffix := strconv.FormatInt(time.Now().UnixNano(), 36)
	dbName, roleName := "rixa_probe_"+suffix, "rixa_probe_role_"+suffix
	dbIdent, roleIdent := pgx.Identifier{dbName}.Sanitize(), pgx.Identifier{roleName}.Sanitize()
	const roleSecret = "temporary-test-probe-only"
	if _, err = admin.Exec(ctx, "CREATE ROLE "+roleIdent+" LOGIN PASSWORD '"+roleSecret+"'"); err != nil {
		t.Fatal("create disposable admission login failed")
	}
	t.Cleanup(func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		conn, e := pgx.Connect(cleanup, adminDSN)
		if e != nil {
			t.Error("cannot connect to clean disposable admission fixture")
			return
		}
		defer conn.Close(context.Background())
		if _, e = conn.Exec(cleanup, "DROP DATABASE IF EXISTS "+dbIdent+" WITH (FORCE)"); e != nil {
			t.Error("cannot drop disposable database")
		}
		if _, e = conn.Exec(cleanup, "DROP ROLE IF EXISTS "+roleIdent); e != nil {
			t.Error("cannot drop disposable role")
		}
	})
	if _, err = admin.Exec(ctx, "CREATE DATABASE "+dbIdent+" OWNER "+roleIdent+" TEMPLATE template0"); err != nil {
		t.Fatal("create isolated admission database failed")
	}
	u, err := url.Parse(adminDSN)
	if err != nil {
		t.Fatal("test PostgreSQL DSN must be URL")
	}
	u.Path = "/" + dbName
	u.User = url.UserPassword(roleName, roleSecret)
	validRoleDSN := u.String()
	// PostgreSQL 18's default PUBLIC execute grant on this catalog function
	// can admit an ordinary login. Force a known permission-denied fixture
	// only inside the freshly created, test-owned database before probing.
	dbAdminDSN := *u
	adminURL, parseErr := url.Parse(adminDSN)
	if parseErr != nil {
		t.Fatal("test admin DSN invalid")
	}
	dbAdminDSN.User = adminURL.User
	withinDB, e := pgx.Connect(ctx, dbAdminDSN.String())
	if e != nil {
		t.Fatal("disposable PostgreSQL database admin connection unavailable")
	}
	defer withinDB.Close(context.Background())
	if _, e = withinDB.Exec(ctx, "REVOKE EXECUTE ON FUNCTION pg_catalog.pg_control_system() FROM PUBLIC"); e != nil {
		t.Fatal("cannot restrict disposable physical identity observation fixture")
	}
	badPassword := *u
	badPassword.User = url.UserPassword(roleName, "deliberately-incorrect-test-password")
	_, err = inspectDatabaseTarget(ctx, badPassword.String())
	if !errors.Is(err, ErrDatabaseAdmission) || errors.Is(err, ErrUnavailable) || !strings.Contains(err.Error(), "authentication") || strings.Contains(err.Error(), roleSecret) {
		t.Fatalf("real PostgreSQL invalid password incorrectly classified: %v", err)
	}
	if err = validateDatabaseTargets(ctx, []ResolvedDatabase{{Kind: DatabaseControl, ID: "control", DSN: badPassword.String()}}); err == nil ||
		!errors.Is(err, ErrDatabaseAdmission) || !strings.Contains(err.Error(), "database target control: ") || strings.Contains(err.Error(), roleSecret) {
		t.Fatalf("admission target context or redaction lost: %v", err)
	}
	_, err = inspectDatabaseTarget(ctx, validRoleDSN)
	if !errors.Is(err, ErrDatabaseAdmission) || errors.Is(err, ErrUnavailable) || !strings.Contains(err.Error(), "permission") || strings.Contains(err.Error(), roleSecret) {
		t.Fatalf("real PostgreSQL pg_control_system denial incorrectly classified: %v", err)
	}
	if _, e = withinDB.Exec(ctx, "GRANT EXECUTE ON FUNCTION pg_catalog.pg_control_system() TO "+roleIdent); e != nil {
		t.Fatal("grant disposable physical identity observation failed")
	}
	identity, e := inspectDatabaseTarget(ctx, validRoleDSN)
	if e != nil || identity.systemID == "" || identity.databaseOID == "" {
		t.Fatalf("real granted physical identity query failed: %v", e)
	}
	if _, e = withinDB.Exec(ctx, "REVOKE EXECUTE ON FUNCTION pg_catalog.pg_control_system() FROM "+roleIdent); e != nil {
		t.Fatal("revoke disposable observation permission failed")
	}
	_, err = inspectDatabaseTarget(ctx, validRoleDSN)
	if !errors.Is(err, ErrDatabaseAdmission) || !strings.Contains(err.Error(), "permission") {
		t.Fatalf("real PostgreSQL revoked identity privilege became retryable: %v", err)
	}
}
