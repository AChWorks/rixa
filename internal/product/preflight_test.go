// SPDX-License-Identifier: MPL-2.0

package product

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
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
				return nil, errors.New("synthetic dial failure")
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
