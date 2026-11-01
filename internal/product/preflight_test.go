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

// SafeToRetry only confirms that no data was sent, not recoverability.
type replaySafePreflightError struct{ cause error }

func (e replaySafePreflightError) Error() string     { return e.cause.Error() }
func (e replaySafePreflightError) Unwrap() error     { return e.cause }
func (e replaySafePreflightError) SafeToRetry() bool { return true }

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
		{"dns-unwrapped-cancel", &net.DNSError{UnwrapErr: context.Canceled, Err: secret, Name: "private.host"}, context.Canceled, ""},
		{"dns-unwrapped-deadline", &net.DNSError{UnwrapErr: context.DeadlineExceeded, Err: secret, Name: "private.host"}, ErrUnavailable, ""},
		{"dns-temporary-unwrapped-cancel", &net.DNSError{IsTemporary: true, UnwrapErr: context.Canceled, Err: secret}, context.Canceled, ""},
		{"dns-timeout-without-inner", &net.DNSError{IsTimeout: true, Err: secret}, ErrUnavailable, ""},
		{"dns-notfound-with-cancel", &net.DNSError{IsNotFound: true, UnwrapErr: context.Canceled, Err: secret}, ErrDatabaseAdmission, "host resolution"},
		{"dns-temporary-nested-unknown-and-timeout", &net.DNSError{IsTemporary: true, UnwrapErr: errors.Join(errors.New(secret), context.DeadlineExceeded), Err: secret}, ErrDatabaseAdmission, "host resolution"},
		{"dns-temporary-nested-pg-permission", &net.DNSError{IsTemporary: true, UnwrapErr: &pgconn.PgError{Code: "42501", Message: secret}, Err: secret}, ErrDatabaseAdmission, "permission"},
		{"dns-temporary-nested-tls", &net.DNSError{IsTemporary: true, UnwrapErr: x509.UnknownAuthorityError{}, Err: secret}, ErrDatabaseAdmission, "TLS"},
		{"dns-op-wrapped-cancel", &net.OpError{Op: "dial", Net: "tcp", Err: &net.DNSError{UnwrapErr: context.Canceled, Err: secret}}, context.Canceled, ""},
		{"dns-op-wrapped-temporary", &net.OpError{Op: "dial", Net: "tcp", Err: &net.DNSError{IsTemporary: true, Err: secret}}, ErrUnavailable, ""},
		{"dns-op-wrapped-notfound", &net.OpError{Op: "dial", Net: "tcp", Err: &net.DNSError{IsNotFound: true, Err: secret}}, ErrDatabaseAdmission, "host resolution"},
		{"dns-op-wrapped-unknown-and-timeout", &net.OpError{Op: "dial", Net: "tcp", Err: &net.DNSError{IsTemporary: true, UnwrapErr: errors.Join(errors.New(secret), context.DeadlineExceeded), Err: secret}}, ErrDatabaseAdmission, "host resolution"},
		{"dns-op-wrapped-permanent-tls", &net.OpError{Op: "dial", Net: "tcp", Err: &net.DNSError{IsTemporary: true, UnwrapErr: x509.UnknownAuthorityError{}, Err: secret}}, ErrDatabaseAdmission, "TLS"},
		{"pg-starting-57P03", &pgconn.PgError{Code: "57P03", Message: secret}, ErrUnavailable, ""},
		{"pg-connection-08006", &pgconn.PgError{Code: "08006", Message: secret}, ErrUnavailable, ""},
		{"pg-busy-53300", &pgconn.PgError{Code: "53300", Message: secret}, ErrUnavailable, ""},
		{"connection-refused", &net.OpError{Op: "dial", Net: "tcp", Err: syscall.ECONNREFUSED}, ErrUnavailable, ""},
		{"net-op-eacces", &net.OpError{Op: "dial", Net: "tcp", Err: syscall.EACCES}, ErrDatabaseAdmission, "network"},
		{"net-op-eperm", &net.OpError{Op: "dial", Net: "tcp", Err: syscall.EPERM}, ErrDatabaseAdmission, "network"},
		{"net-op-unclassified", &net.OpError{Op: "dial", Net: "tcp", Err: errors.New(secret)}, ErrDatabaseAdmission, "network"},
		{"net-op-unclassified-eio", &net.OpError{Op: "dial", Net: "tcp", Err: syscall.EIO}, ErrDatabaseAdmission, "network"},
		{"net-op-reset", &net.OpError{Op: "read", Net: "tcp", Err: syscall.ECONNRESET}, ErrUnavailable, ""},
		{"net-op-EOF", &net.OpError{Op: "read", Net: "tcp", Err: io.EOF}, ErrUnavailable, ""},
		{"net-op-unknown-joined-transient", errors.Join(&net.OpError{Op: "dial", Net: "tcp", Err: errors.New(secret)}, syscall.ECONNREFUSED), ErrDatabaseAdmission, "network"},
		{"net-op-unknown-joined-deadline", errors.Join(&net.OpError{Op: "dial", Net: "tcp", Err: errors.New(secret)}, context.DeadlineExceeded), ErrDatabaseAdmission, "network"},
		{"net-op-unknown-inner-join-refused", &net.OpError{Op: "dial", Net: "tcp", Err: errors.Join(errors.New(secret), syscall.ECONNREFUSED)}, ErrDatabaseAdmission, "network"},
		{"net-op-permission-inner-join-refused", &net.OpError{Op: "dial", Net: "tcp", Err: errors.Join(syscall.EACCES, syscall.ECONNREFUSED)}, ErrDatabaseAdmission, "network"},
		{"pg-starting-joined-unknown-transport", errors.Join(&pgconn.PgError{Code: "57P03"}, &net.OpError{Op: "dial", Net: "tcp", Err: errors.New(secret)}), ErrDatabaseAdmission, "network"},
		{"temporary-dns-joined-unknown-transport", errors.Join(&net.DNSError{IsTemporary: true}, &net.OpError{Op: "dial", Net: "tcp", Err: errors.New(secret)}), ErrDatabaseAdmission, "network"},
		{"net-op-known-temporary-dns", &net.OpError{Op: "dial", Net: "tcp", Err: &net.DNSError{IsTemporary: true}}, ErrUnavailable, ""},
		{"net-op-known-timeout", &net.OpError{Op: "read", Net: "tcp", Err: context.DeadlineExceeded}, ErrUnavailable, ""},
		{"joined-known-transients", errors.Join(&net.OpError{Op: "dial", Net: "tcp", Err: syscall.ECONNREFUSED}, context.DeadlineExceeded), ErrUnavailable, ""},
		{"net-op-permission-joined-timeout", errors.Join(&net.OpError{Op: "dial", Net: "tcp", Err: syscall.EACCES}, context.DeadlineExceeded), ErrDatabaseAdmission, "network"},
		{"safe-to-replay-eacces", replaySafePreflightError{cause: &net.OpError{Op: "dial", Net: "tcp", Err: syscall.EACCES}}, ErrDatabaseAdmission, "network"},
		{"safe-to-replay-unknown", replaySafePreflightError{cause: &net.OpError{Op: "dial", Net: "tcp", Err: errors.New(secret)}}, ErrDatabaseAdmission, "network"},
		{"safe-to-replay-refused", replaySafePreflightError{cause: &net.OpError{Op: "dial", Net: "tcp", Err: syscall.ECONNREFUSED}}, ErrUnavailable, ""},
		{"pg-auth-joined-transient", errors.Join(&pgconn.PgError{Code: "28P01", Message: secret}, syscall.ECONNREFUSED), ErrDatabaseAdmission, "authentication"},
		{"pg-auth-joined-cancel", errors.Join(&pgconn.PgError{Code: "28P01", Message: secret}, context.Canceled), ErrDatabaseAdmission, "authentication"},
		{"tls-joined-cancel", errors.Join(x509.UnknownAuthorityError{}, context.Canceled), ErrDatabaseAdmission, "TLS"},
		{"net-op-context-canceled", &net.OpError{Op: "dial", Net: "tcp", Err: context.Canceled}, context.Canceled, ""},
		{"tls-joined-transient", errors.Join(x509.UnknownAuthorityError{}, syscall.ECONNREFUSED), ErrDatabaseAdmission, "TLS"},
		{"reset", fmt.Errorf("socket closed: %w", syscall.ECONNRESET), ErrUnavailable, ""},
		{"network-unreachable", fmt.Errorf("socket: %w", syscall.ENETUNREACH), ErrUnavailable, ""},
		{"EOF", io.EOF, ErrUnavailable, ""},
		{"probe-timeout", context.DeadlineExceeded, ErrUnavailable, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if strings.HasPrefix(tc.name, "safe-to-replay") && !pgconn.SafeToRetry(tc.cause) {
				t.Fatal("fixture must satisfy pgconn.SafeToRetry")
			}
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

func TestNetworkWrapperPreservesSpecificPermanentCause(t *testing.T) {
	for _, tc := range []struct {
		name, category string
		cause          error
	}{
		{"TLS", "TLS", x509.UnknownAuthorityError{}},
		{"permission", "permission", &pgconn.PgError{Code: "42501"}},
		{"DNS", "host resolution", &net.DNSError{IsNotFound: true}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := safePreflightError(context.Background(), &net.OpError{Op: "dial", Net: "tcp", Err: tc.cause})
			if !errors.Is(err, ErrDatabaseAdmission) || !strings.Contains(err.Error(), tc.category) {
				t.Fatalf("lost permanent admission category %s: %v", tc.category, err)
			}
		})
	}
}

func TestDatabasePreflightContextTerminationDoesNotMaskPermanentAdmission(t *testing.T) {
	const secret = "postgres://private-role:private-password@127.0.0.1:5432/private"
	const dsn = "postgres://role:fixture-password@127.0.0.1:5432/app?sslmode=disable"
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	expired, stopDeadline := context.WithTimeout(context.Background(), 0)
	defer stopDeadline()
	tests := []struct {
		name     string
		ctx      context.Context
		cause    error
		want     error
		category string
	}{
		{"canceled-auth", canceled, errors.Join(&pgconn.PgError{Code: "28P01", Message: secret}, context.Canceled), ErrDatabaseAdmission, "authentication"},
		{"canceled-permission", canceled, errors.Join(&pgconn.PgError{Code: "42501", Message: secret}, context.Canceled), ErrDatabaseAdmission, "permission"},
		{"canceled-tls", canceled, errors.Join(x509.UnknownAuthorityError{}, context.Canceled), ErrDatabaseAdmission, "TLS"},
		{"expired-auth", expired, &pgconn.PgError{Code: "28P01", Message: secret}, ErrDatabaseAdmission, "authentication"},
		{"expired-permission", expired, &pgconn.PgError{Code: "42501", Message: secret}, ErrDatabaseAdmission, "permission"},
		{"expired-tls", expired, x509.UnknownAuthorityError{}, ErrDatabaseAdmission, "TLS"},
		{"canceled-network-permission", canceled, &net.OpError{Op: "dial", Net: "tcp", Err: syscall.EACCES}, ErrDatabaseAdmission, "network"},
		{"canceled-nested-dns-permission", canceled, &net.DNSError{IsTemporary: true, UnwrapErr: &pgconn.PgError{Code: "42501", Message: secret}}, ErrDatabaseAdmission, "permission"},
		{"expired-nested-dns-tls", expired, &net.DNSError{IsTimeout: true, UnwrapErr: x509.UnknownAuthorityError{}}, ErrDatabaseAdmission, "TLS"},
		{"canceled-dns-notfound", canceled, &net.DNSError{IsNotFound: true, Err: secret}, ErrDatabaseAdmission, "host resolution"},
		{"canceled-nested-dns-cancellation", canceled, &net.DNSError{UnwrapErr: context.Canceled}, context.Canceled, ""},
		{"canceled-dns-temporary", canceled, &net.DNSError{IsTemporary: true}, context.Canceled, ""},
		{"expired-nested-dns-unknown", expired, &net.DNSError{IsTemporary: true, UnwrapErr: errors.New(secret)}, context.DeadlineExceeded, ""},
		{"canceled-transient", canceled, &net.OpError{Op: "dial", Net: "tcp", Err: syscall.ECONNREFUSED}, context.Canceled, ""},
		{"canceled-unknown", canceled, &net.OpError{Op: "dial", Net: "tcp", Err: errors.New(secret)}, context.Canceled, ""},
		{"expired-transient", expired, context.DeadlineExceeded, context.DeadlineExceeded, ""},
		{"expired-unknown", expired, errors.New(secret), context.DeadlineExceeded, ""},
		{"canceled-missing-connection", canceled, nil, context.Canceled, ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			attempts := 0
			_, err := inspectDatabaseTargetWithConnector(tc.ctx, dsn, func(context.Context, *pgx.ConnConfig) (*pgx.Conn, error) {
				attempts++
				return nil, tc.cause
			})
			if attempts != 1 || !errors.Is(err, tc.want) {
				t.Fatalf("preflight attempts=%d result=%v expected=%v", attempts, err, tc.want)
			}
			if tc.category != "" && !strings.Contains(err.Error(), tc.category) {
				t.Fatalf("permanent classification lost category %q: %v", tc.category, err)
			}
			if strings.Contains(err.Error(), secret) || tc.want == ErrDatabaseAdmission &&
				(errors.Is(err, ErrUnavailable) || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)) {
				t.Fatalf("unsafe or retryable classification: %v", err)
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
