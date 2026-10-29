// SPDX-License-Identifier: MPL-2.0

package product

import (
	"context"
	"errors"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/AChWorks/achrix"
)

func TestServeUnreadyPostgreSQLNeverBindsDirectTLS(t *testing.T) {
	adminDSN := os.Getenv("RIXA_TEST_POSTGRES_DSN")
	if adminDSN == "" {
		if os.Getenv("CI") != "" {
			t.Fatal("RIXA_TEST_POSTGRES_DSN is required in CI")
		}
		t.Skip("set RIXA_TEST_POSTGRES_DSN for the real PostgreSQL ingress admission proof")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	dsns := createTestDatabases(t, ctx, adminDSN)
	c := directTLSConfig(t)
	port := strconv.Itoa(freePort(t))
	c.Listen = net.JoinHostPort("0.0.0.0", port)
	c.Control.Origin = "https://control.rixa.test:" + port
	c.Sites[0].Origin = "https://site.rixa.test:" + port
	c.Sites[0].MediaRoot = filepath.Join(t.TempDir(), "media")
	if err := os.Mkdir(c.Sites[0].MediaRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	c.StartupTimeout, c.ShutdownTimeout = time.Second, time.Second
	resolved, err := c.ResolveRuntime(ctx, mapLookup(map[string]string{"RIXA_CONTROL": dsns["control"], "RIXA_SITE": dsns["a"]}))
	if err != nil {
		t.Fatalf("real target preflight: %v", err)
	}
	runtime, err := BuildRuntime(resolved, nil)
	if err != nil {
		t.Fatal(err)
	}
	// These are real reachable PostgreSQL databases, but no schema has been
	// installed. Readiness must fail before acquiring any ingress listener.
	if err := Serve(ctx, runtime, nil); err == nil {
		t.Fatal("runtime served without required migrations")
	}
	if conn, err := net.DialTimeout("tcp", net.JoinHostPort("127.0.0.1", port), 100*time.Millisecond); err == nil {
		_ = conn.Close()
		t.Fatal("direct TLS bound before database readiness")
	}
	for _, app := range runtime.applications {
		if err := app.Ready(ctx); !errors.Is(err, achrix.ErrNotReady) {
			t.Fatalf("failed startup left an Application ready: %v", err)
		}
	}
}
