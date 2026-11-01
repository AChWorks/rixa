// SPDX-License-Identifier: MPL-2.0

package product

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"net"
	"strings"
	"syscall"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestDatabasePreflightRejectsUnsafeRemoteBeforeConnect(t *testing.T) {
	tests := []struct {
		name string
		dsn  string
	}{
		{
			name: "remote plaintext",
			dsn:  "postgres://user:password@db.example.com/app?sslmode=disable",
		},
		{
			name: "remote unverified TLS",
			dsn:  "postgres://user:password@db.example.com/app?sslmode=require",
		},
		{
			name: "oversized DSN",
			dsn:  strings.Repeat("x", 4097),
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			calls := 0
			_, err := inspectDatabaseTargetWithConnector(context.Background(), test.dsn, func(context.Context, *pgx.ConnConfig) (*pgx.Conn, error) {
				calls++
				return nil, errors.New("connector should not run")
			})
			if !errors.Is(err, ErrConfiguration) {
				t.Fatalf("unsafe DSN error=%v want ErrConfiguration", err)
			}
			if calls != 0 {
				t.Fatalf("unsafe DSN reached connector %d time(s)", calls)
			}
		})
	}
}

func TestDatabasePreflightRejectsUnsafeRemoteFallbackBeforeConnect(t *testing.T) {
	dsn := "host=localhost,db.example.com port=5432,5432 user=user password=password dbname=app sslmode=disable"
	parsed, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatalf("fixture parse: %v", err)
	}
	if parsed.ConnConfig.Host != "localhost" || len(parsed.ConnConfig.Fallbacks) == 0 || parsed.ConnConfig.Fallbacks[0].Host != "db.example.com" {
		t.Fatalf("fixture did not produce local primary plus remote fallback: host=%q fallbacks=%#v", parsed.ConnConfig.Host, parsed.ConnConfig.Fallbacks)
	}

	calls := 0
	_, err = inspectDatabaseTargetWithConnector(context.Background(), dsn, func(context.Context, *pgx.ConnConfig) (*pgx.Conn, error) {
		calls++
		return nil, errors.New("connector should not run")
	})
	if !errors.Is(err, ErrConfiguration) {
		t.Fatalf("unsafe remote fallback error=%v want ErrConfiguration", err)
	}
	if calls != 0 {
		t.Fatalf("unsafe remote fallback reached connector %d time(s)", calls)
	}
}

func TestDatabasePreflightAdmitsLocalAndVerifiedRemoteToProbe(t *testing.T) {
	tests := []struct {
		name string
		dsn  string
	}{
		{
			name: "loopback development",
			dsn:  "postgres://user:password@127.0.0.1:5432/app?sslmode=disable",
		},
		{
			name: "verified remote",
			dsn:  "postgres://user:password@db.example.com:5432/app?sslmode=verify-full",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			calls := 0
			_, err := inspectDatabaseTargetWithConnector(context.Background(), test.dsn, func(context.Context, *pgx.ConnConfig) (*pgx.Conn, error) {
				calls++
				return nil, &net.OpError{Op: "dial", Net: "tcp", Err: syscall.ECONNREFUSED}
			})
			if !errors.Is(err, ErrUnavailable) {
				t.Fatalf("safe DSN probe error=%v want ErrUnavailable from synthetic connector failure", err)
			}
			if calls != 1 {
				t.Fatalf("safe DSN connector calls=%d want=1", calls)
			}
		})
	}
}

// Drive representative *wrapped driver errors* through the actual read-only
// connection preflight. This must never print the driver error, since
// pgconn.ConnectError can include the username/database and secret DSN fields.
func TestDatabasePreflightClassifiesSanitizedConnectionErrors(t *testing.T) {
	const secret = "postgres://private-role:very-secret@127.0.0.1:5432/private"
	const dsn = "postgres://role:lab-password@127.0.0.1:5432/app?sslmode=disable"
	cases := []struct {
		name     string
		cause    error
		sentinel error
		category string
	}{
		{"password-28P01", &pgconn.PgError{Code: "28P01", Message: secret}, ErrDatabaseAdmission, "authentication"},
		{"auth-28000", &pgconn.PgError{Code: "28000", Message: secret}, ErrDatabaseAdmission, "authentication"},
		{"privilege-42501", &pgconn.PgError{Code: "42501", Message: secret}, ErrDatabaseAdmission, "permission"},
		{"missing-database-3D000", &pgconn.PgError{Code: "3D000", Message: secret}, ErrDatabaseAdmission, "rejected"},
		{"unknown-postgresql-XX000", &pgconn.PgError{Code: "XX000", Message: secret}, ErrDatabaseAdmission, "rejected"},
		{"x509-unknown-ca", &tls.CertificateVerificationError{Err: x509.UnknownAuthorityError{}}, ErrDatabaseAdmission, "TLS"},
		{"x509-hostname", x509.HostnameError{}, ErrDatabaseAdmission, "TLS"},
		{"x509-expired", x509.CertificateInvalidError{Reason: x509.Expired}, ErrDatabaseAdmission, "TLS"},
		{"invalid-tls-wire", tls.RecordHeaderError{}, ErrDatabaseAdmission, "TLS"},
		{"permanent-unrecognized", errors.New(secret), ErrDatabaseAdmission, "rejected"},
		{"dns-permanent", &net.DNSError{Err: secret, Name: "private.host", IsNotFound: true}, ErrDatabaseAdmission, "host resolution"},
		{"dns-temporary", &net.DNSError{Err: secret, Name: "private.host", IsTemporary: true}, ErrUnavailable, ""},
		{"pg-starting-57P03", &pgconn.PgError{Code: "57P03", Message: secret}, ErrUnavailable, ""},
		{"pg-connection-08006", &pgconn.PgError{Code: "08006", Message: secret}, ErrUnavailable, ""},
		{"pg-busy-53300", &pgconn.PgError{Code: "53300", Message: secret}, ErrUnavailable, ""},
		{"connection-refused", &net.OpError{Op: "dial", Net: "tcp", Err: syscall.ECONNREFUSED}, ErrUnavailable, ""},
		{"reset", fmt.Errorf("socket closed: %w", syscall.ECONNRESET), ErrUnavailable, ""},
		{"network-unreachable", fmt.Errorf("socket: %w", syscall.ENETUNREACH), ErrUnavailable, ""},
		{"EOF", io.EOF, ErrUnavailable, ""},
		{"probe-timeout", context.DeadlineExceeded, ErrUnavailable, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			_, err := inspectDatabaseTargetWithConnector(context.Background(), dsn, func(context.Context, *pgx.ConnConfig) (*pgx.Conn, error) {
				calls++
				return nil, fmt.Errorf("nested driver %s: %w", secret, tc.cause)
			})
			if calls != 1 || !errors.Is(err, tc.sentinel) || strings.Contains(err.Error(), secret) || tc.category != "" && !strings.Contains(err.Error(), tc.category) {
				t.Fatalf("unsafe classification or retry: calls=%d err=%v expected=%v", calls, err, tc.sentinel)
			}
			if tc.sentinel == ErrDatabaseAdmission && errors.Is(err, ErrUnavailable) {
				t.Fatalf("permanent admission accidentally marked retryable: %v", err)
			}
		})
	}
}

func TestDatabasePreflightCanceledAndMissingConnectionFailSafely(t *testing.T) {
	dsn := "postgres://role:password@127.0.0.1:5432/app?sslmode=disable"
	_, err := inspectDatabaseTargetWithConnector(context.Background(), dsn, func(context.Context, *pgx.ConnConfig) (*pgx.Conn, error) { return nil, nil })
	if !errors.Is(err, ErrDatabaseAdmission) || errors.Is(err, ErrUnavailable) {
		t.Fatalf("nil connection accepted: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	_, err = inspectDatabaseTargetWithConnector(ctx, dsn, func(context.Context, *pgx.ConnConfig) (*pgx.Conn, error) { cancel(); return nil, syscall.ECONNREFUSED })
	if !errors.Is(err, context.Canceled) || errors.Is(err, ErrUnavailable) {
		t.Fatalf("parent cancellation became retryable: %v", err)
	}
}
