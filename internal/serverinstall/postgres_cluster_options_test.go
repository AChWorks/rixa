// SPDX-License-Identifier: MPL-2.0
package serverinstall

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Use an independently trusted PostgreSQL18 tool directory. The fixture
// initializes only its private TempDir; no server/listener or external SQL
// session starts. An absent real tool fixture is an honest skip.
func TestPostgresClusterInitDBOptionsWithRealTools(t *testing.T) {
	postgres := os.Getenv("RIXA_POSTGRES_CONFIG_PARSER")
	if postgres == "" {
		t.Skip("RIXA_POSTGRES_CONFIG_PARSER requires trusted PostgreSQL18 postgres/initdb tools")
	}
	if !filepath.IsAbs(postgres) || os.Geteuid() == 0 {
		t.Fatal("an absolute trusted tool path and a non-root test account are required")
	}
	initdb := filepath.Join(filepath.Dir(postgres), "initdb")
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	run := func(tool string, args ...string) (string, string, error) {
		t.Helper()
		cmd := exec.CommandContext(ctx, tool, args...)
		cmd.Env = []string{"PATH=/usr/bin:/bin", "LANG=C.UTF-8", "LC_ALL=C.UTF-8"}
		cmd.WaitDelay = time.Second
		var stdout, stderr boundedOutput
		cmd.Stdout, cmd.Stderr = &stdout, &stderr
		err := cmd.Run()
		if ctx.Err() != nil || stdout.exceeded || stderr.exceeded {
			t.Fatalf("tool exceeded its fixed context/output bounds: %v", err)
		}
		return strings.TrimSpace(stdout.String()), stderr.String(), err
	}
	version, diagnostic, err := run(postgres, "--version")
	if err != nil || !strings.HasPrefix(version, "postgres (PostgreSQL) 18.") {
		t.Fatalf("PostgreSQL18 fixture required: %q %s %v", version, diagnostic, err)
	}
	initVersion, diagnostic, err := run(initdb, "--version")
	if err != nil || initVersion != strings.Replace(version, "postgres (", "initdb (", 1) {
		t.Fatalf("matching initdb required: %q %s %v", initVersion, diagnostic, err)
	}
	root := t.TempDir()
	oldConfig := filepath.Join(root, "old.conf")
	if err := os.WriteFile(oldConfig, []byte("listen_addresses = 127.0.0.1\n"), 0600); err != nil {
		t.Fatal(err)
	}
	// Preserve the exact failed native output as a negative grammar control.
	if _, diagnostic, err := run(postgres, "-D", root, "-c", "config_file="+oldConfig, "-C", "listen_addresses"); err == nil || !strings.Contains(diagnostic, "syntax error") {
		t.Fatalf("unquoted dotted-address control was not rejected: %s %v", diagnostic, err)
	}
	data := filepath.Join(root, "data")
	args := []string{"-D", data, "--locale=C.UTF-8", "--encoding=UTF8"}
	args = append(args, postgresInitDBOptions()...)
	if _, diagnostic, err := run(initdb, args...); err != nil {
		t.Fatalf("real initdb rejected the production options: %s %v", diagnostic, err)
	}
	for _, setting := range []struct{ name, want string }{
		{"listen_addresses", "127.0.0.1"},
		{"password_encryption", "scram-sha-256"},
		{"ssl", "off"},
	} {
		value, diagnostic, err := run(postgres, "-D", data, "-C", setting.name)
		if err != nil || value != setting.want {
			t.Fatalf("generated %s is not accepted by PostgreSQL18: got %q, %s %v", setting.name, value, diagnostic, err)
		}
	}
	hba, err := os.ReadFile(filepath.Join(data, "pg_hba.conf"))
	if err != nil || !postgresHBA(hba) {
		t.Fatalf("real initdb lost the peer/SCRAM authentication profile: %v", err)
	}
	if _, err := os.Stat(filepath.Join(data, "postmaster.pid")); !os.IsNotExist(err) {
		t.Fatalf("fixture unexpectedly has a server PID file: %v", err)
	}
}
