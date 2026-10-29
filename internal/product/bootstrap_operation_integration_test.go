// SPDX-License-Identifier: MPL-2.0

package product

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/AChWorks/achrix"
	"github.com/AChWorks/achrix/identity"
	"github.com/jackc/pgx/v5"
)

// This test uses public identity operations against real PostgreSQL and the
// same test executable in separate processes. It proves retained effects, not
// a production service, privileged installer or external-creator exclusion.
func TestBootstrapOperationPostgresProcessRecovery(t *testing.T) {
	adminDSN := os.Getenv("RIXA_TEST_POSTGRES_DSN")
	if adminDSN == "" {
		if os.Getenv("CI") != "" {
			t.Fatal("RIXA_TEST_POSTGRES_DSN is required in CI")
		}
		t.Skip("set RIXA_TEST_POSTGRES_DSN for real bootstrap process proof")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	dsns := createTestDatabases(t, ctx, adminDSN)
	root := t.TempDir()
	if err := os.Chmod(root, 0700); err != nil {
		t.Fatal(err)
	}
	media := filepath.Join(root, "media")
	if err := os.Mkdir(media, 0700); err != nil {
		t.Fatal(err)
	}
	cert, key, _ := writeTestCertificate(t, root, []string{"control.rixa.test", "a.rixa.test"})
	raw := fileConfig{Listen: "127.0.0.1:19443", TLS: TLSConfig{CertFile: cert, KeyFile: key}, Control: ControlConfig{Origin: "https://control.rixa.test", DatabaseEnv: "BOOT_CONTROL"}, Sites: []SiteConfig{{ID: "site-a", Origin: "https://a.rixa.test", DatabaseEnv: "BOOT_SITE", MediaRoot: media}}}
	data, _ := json.Marshal(raw)
	config, err := ParseConfig(data)
	if err != nil {
		t.Fatal(err)
	}
	values := map[string]string{"BOOT_CONTROL": dsns["control"], "BOOT_SITE": dsns["a"]}
	if err = Migrate(ctx, config, mapLookup(values)); err != nil {
		t.Fatal(err)
	}
	request := func(name string) BootstrapOperationRequest {
		dir := filepath.Join(root, name)
		if err := os.Mkdir(dir, 0700); err != nil {
			t.Fatal(err)
		}
		return BootstrapOperationRequest{OperationID: name, StatePath: filepath.Join(dir, "bootstrap.json"), Configuration: data, Scope: "control", Login: name, Password: testControlPassword}
	}
	t.Run("created-and-passwordless-inspection", func(t *testing.T) {
		r := request("fresh-admin")
		got, err := ApplyBootstrapOperation(ctx, r, mapLookup(values), nil)
		if err != nil || got.State != BootstrapVerified || !got.Created {
			t.Fatalf("real create: %+v %v", got, err)
		}
		id := got.Principal
		r.Password = ""
		got, err = InspectBootstrapOperation(ctx, r, mapLookup(values), nil)
		if err != nil || got.Principal != id || got.State != BootstrapVerified {
			t.Fatalf("real inspect: %+v %v", got, err)
		}
		assertBootstrapDatabaseCounts(t, ctx, dsns["control"], r.Login, id, 0)
	})
	t.Run("fresh-existing-is-conflict", func(t *testing.T) {
		r := request("preexisting-admin")
		created, err := BootstrapAdmin(ctx, config, r.Scope, r.Login, r.Password, mapLookup(values), nil)
		if err != nil {
			t.Fatal(err)
		}
		got, err := ApplyBootstrapOperation(ctx, r, mapLookup(values), nil)
		if !errors.Is(err, identity.ErrConflict) || got.Principal != "" || got.State != BootstrapConflict {
			t.Fatalf("existing adopted: %+v %v", got, err)
		}
		assertBootstrapDatabaseCounts(t, ctx, dsns["control"], r.Login, created.Account.ID, 0)
	})
	t.Run("process-loss-after-account-commit", func(t *testing.T) {
		r := request("interrupted-admin")
		command := bootstrapOperationChild(t, ctx, r, values, "crash-after-create")
		output, err := command.CombinedOutput()
		var exit *exec.ExitError
		if !errors.As(err, &exit) || exit.ExitCode() != 73 {
			t.Fatalf("child interruption: %v %s", err, output)
		}
		got, err := InspectBootstrapOperation(ctx, r, mapLookup(values), nil)
		if !errors.Is(err, ErrBootstrapReconciliation) || got.Code != "account-needs-authentication" || got.Principal != "" {
			t.Fatalf("unproven account exposed: %+v %v", got, err)
		}
		r.Password = "Wrong-password-never-persisted"
		got, err = ApplyBootstrapOperation(ctx, r, mapLookup(values), nil)
		if !errors.Is(err, ErrBootstrapReconciliation) || got.Code != "account-proof-failed" {
			t.Fatalf("wrong proof accepted: %+v %v", got, err)
		}
		r.Password = testControlPassword
		got, err = ApplyBootstrapOperation(ctx, r, mapLookup(values), nil)
		if err != nil || got.State != BootstrapVerified || !got.Reconciled || got.Created {
			t.Fatalf("real resumed proof: %+v %v", got, err)
		}
		assertBootstrapDatabaseCounts(t, ctx, dsns["control"], r.Login, got.Principal, 0)
		journal, _ := os.ReadFile(r.StatePath)
		receipt, _ := json.Marshal(got)
		for _, secret := range []string{testControlPassword, dsns["control"], "Wrong-password-never-persisted"} {
			if bytes.Contains(journal, []byte(secret)) || bytes.Contains(receipt, []byte(secret)) {
				t.Fatal("protected input leaked into durable/public output")
			}
		}
		spare := createTestDatabases(t, ctx, adminDSN)
		changed := map[string]string{"BOOT_CONTROL": spare["control"], "BOOT_SITE": dsns["a"]}
		got, err = ApplyBootstrapOperation(ctx, r, mapLookup(changed), nil)
		if !errors.Is(err, ErrBootstrapOperationStale) || got.Code != "stale-database" {
			t.Fatalf("physical target drift: %+v %v", got, err)
		}
	})
	t.Run("cross-process-parent-lock", func(t *testing.T) {
		r := request("concurrent-admin")
		command := bootstrapOperationChild(t, ctx, r, values, "hold-before-create")
		stdout, err := command.StdoutPipe()
		if err != nil {
			t.Fatal(err)
		}
		stdin, err := command.StdinPipe()
		if err != nil {
			t.Fatal(err)
		}
		var stderr bytes.Buffer
		command.Stderr = &stderr
		if err = command.Start(); err != nil {
			t.Fatal(err)
		}
		ready := make(chan bool, 1)
		go func() {
			scanner := bufio.NewScanner(stdout)
			for scanner.Scan() {
				if scanner.Text() == "BOOTSTRAP-READY" {
					ready <- true
					return
				}
			}
			ready <- false
		}()
		select {
		case ok := <-ready:
			if !ok {
				t.Fatalf("child exited before lock: %s", stderr.String())
			}
		case <-ctx.Done():
			_ = command.Process.Kill()
			t.Fatal("child readiness deadline")
		}
		other := r
		other.OperationID = "another-operation"
		other.StatePath = filepath.Join(filepath.Dir(r.StatePath), "other.json")
		got, err := ApplyBootstrapOperation(ctx, other, mapLookup(values), nil)
		if !errors.Is(err, ErrSetupBusy) || got.Code != "busy" {
			_ = command.Process.Kill()
			t.Fatalf("concurrent executor not fenced: %+v %v", got, err)
		}
		_, _ = fmt.Fprintln(stdin, "continue")
		_ = stdin.Close()
		if err = command.Wait(); err != nil {
			t.Fatalf("child complete: %v %s", err, stderr.String())
		}
		got, err = ApplyBootstrapOperation(ctx, r, mapLookup(values), nil)
		if err != nil || got.State != BootstrapVerified {
			t.Fatalf("child result not durable: %+v %v", got, err)
		}
		assertBootstrapDatabaseCounts(t, ctx, dsns["control"], r.Login, got.Principal, 0)
	})
	t.Run("unknown-authentication-is-not-reissued", func(t *testing.T) {
		r := request("uncertain-auth")
		command := bootstrapOperationChild(t, ctx, r, values, "crash-after-login")
		output, err := command.CombinedOutput()
		var exit *exec.ExitError
		if !errors.As(err, &exit) || exit.ExitCode() != 74 {
			t.Fatalf("auth child interruption: %v %s", err, output)
		}
		conn, err := pgx.Connect(ctx, dsns["control"])
		if err != nil {
			t.Fatal(err)
		}
		defer conn.Close(context.Background())
		var id string
		var count int
		var expires time.Time
		if err = conn.QueryRow(ctx, "SELECT a.id,count(s.*),max(s.expires_at) FROM identity.accounts a JOIN identity.sessions s ON s.account_id=a.id WHERE a.login=$1 GROUP BY a.id", r.Login).Scan(&id, &count, &expires); err != nil || count != 1 {
			t.Fatalf("uncertain session evidence: count=%d err=%v", count, err)
		}
		if remaining := time.Until(expires); remaining > time.Minute+2*time.Second {
			t.Fatal("temporary session exceeds bounded lifetime")
		}
		for i := 0; i < 2; i++ {
			got, err := ApplyBootstrapOperation(ctx, r, mapLookup(values), nil)
			if !errors.Is(err, ErrBootstrapReconciliation) || got.Code != "authentication-attempt-unknown" || got.Principal != "" {
				t.Fatalf("unknown auth resumed unsafely: %+v %v", got, err)
			}
		}
		assertBootstrapDatabaseCounts(t, ctx, dsns["control"], r.Login, id, 1)
		t.Log("Unknown authentication is retained without a second Login; the unreachable token expires after one minute. Disposable test database cleanup removes this fixture.")
	})
}

type bootstrapOperationChildInput struct {
	Request BootstrapOperationRequest
	Values  map[string]string
	Mode    string
}

func bootstrapOperationChild(t *testing.T, ctx context.Context, r BootstrapOperationRequest, values map[string]string, mode string) *exec.Cmd {
	t.Helper()
	input := bootstrapOperationChildInput{Request: r, Values: values, Mode: mode}
	data, _ := json.Marshal(input)
	path := filepath.Join(filepath.Dir(r.StatePath), "private-test-input.json")
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestBootstrapOperationProcessHelper$", "-test.v")
	cmd.Env = append(os.Environ(), "RIXA_BOOTSTRAP_PROCESS_INPUT="+path)
	return cmd
}

type bootstrapOperationProcessAccounts struct {
	bootstrapOperationAccounts
	mode string
}

func (p bootstrapOperationProcessAccounts) CreateAccount(ctx context.Context, actor achrix.Principal, login, password string) (identity.Account, error) {
	if p.mode == "hold-before-create" {
		fmt.Println("BOOTSTRAP-READY")
		if _, err := bufio.NewReader(os.Stdin).ReadString('\n'); err != nil {
			return identity.Account{}, err
		}
	}
	a, err := p.bootstrapOperationAccounts.CreateAccount(ctx, actor, login, password)
	if err == nil && (p.mode == "crash-after-create" || p.mode == "crash-after-login") {
		if p.mode == "crash-after-create" {
			os.Exit(73)
		}
		return a, identity.ErrUnavailable
	}
	return a, err
}
func (p bootstrapOperationProcessAccounts) Login(ctx context.Context, login, password, previous string) (identity.Session, error) {
	s, err := p.bootstrapOperationAccounts.Login(ctx, login, password, previous)
	if err == nil && p.mode == "crash-after-login" {
		os.Exit(74)
	}
	return s, err
}
func TestBootstrapOperationProcessHelper(t *testing.T) {
	path := os.Getenv("RIXA_BOOTSTRAP_PROCESS_INPUT")
	if path == "" {
		return
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var input bootstrapOperationChildInput
	if json.Unmarshal(data, &input) != nil || !strings.HasPrefix(input.Request.StatePath, filepath.Dir(path)+string(filepath.Separator)) {
		t.Fatal("invalid owned helper fixture")
	}
	factory := func(ctx context.Context, c Config, dsn, login string, logger *slog.Logger) (bootstrapOperationAccounts, func() error, error) {
		accounts, close, err := newBootstrapOperationAccounts(ctx, c, dsn, login, logger)
		if err != nil {
			return nil, nil, err
		}
		return bootstrapOperationProcessAccounts{accounts, input.Mode}, close, nil
	}
	_, err = bootstrapOperation(context.Background(), input.Request, mapLookup(input.Values), nil, true, probeBootstrapOperation, factory)
	if err != nil {
		t.Fatal(err)
	}
}
func assertBootstrapDatabaseCounts(t *testing.T, ctx context.Context, dsn, login, id string, sessions int) {
	t.Helper()
	conn, err := pgx.Connect(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close(context.Background())
	var accounts, count int
	if err = conn.QueryRow(ctx, "SELECT count(*) FROM identity.accounts WHERE login=$1 AND id=$2", login, id).Scan(&accounts); err != nil || accounts != 1 {
		t.Fatalf("account uniqueness: count=%d err=%v", accounts, err)
	}
	if err = conn.QueryRow(ctx, "SELECT count(*) FROM identity.sessions WHERE account_id=$1", id).Scan(&count); err != nil || count != sessions {
		t.Fatalf("session cleanup/count: got=%d want=%d err=%v", count, sessions, err)
	}
}
