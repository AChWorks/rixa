// SPDX-License-Identifier: MPL-2.0

//go:build linux

package terminal

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/AChWorks/rixa/internal/serverinstall"
	"golang.org/x/sys/unix"
)

type bootstrapPTYFacade struct {
	mode                      string
	prepares, opens, inspects int
	channels                  []string
}

func (f *bootstrapPTYFacade) DiscoverBootstrap(context.Context) (serverinstall.BootstrapDiscovery, error) {
	f.inspects++
	if f.mode == "bootstrap-unknown" {
		message := "Retained ownership is unknown."
		if f.inspects > 1 {
			message = "Retained ownership remains unknown after inspection."
		}
		return serverinstall.BootstrapDiscovery{State: "blocked", Code: "inspect", Message: message, Actions: []string{"status"}}, serverinstall.ErrState
	}
	if f.mode == "bootstrap-retained" {
		return serverinstall.BootstrapDiscovery{State: "retained", SelectedOperation: "retained-update", InstallationOperation: "original", Operations: []serverinstall.BootstrapOperation{{OperationKind: "update", OperationID: "retained-update", State: "reconciliation-required", ManagerPath: "/protected/original-exact-manager", ManagerSHA256: strings.Repeat("b", 64)}}, Actions: []string{"manage", "status"}}, nil
	}
	return serverinstall.BootstrapDiscovery{State: "clean", Code: "not-installed", Actions: []string{"install", "prerequisites", "status"}}, nil
}
func (f *bootstrapPTYFacade) OpenBootstrap(_ context.Context, id string) (serverinstall.BootstrapRoute, error) {
	f.opens++
	if id != "retained-update" {
		return serverinstall.BootstrapRoute{}, serverinstall.ErrInput
	}
	return serverinstall.BootstrapRoute{ManagerPath: "/protected/original-exact-manager", ManagerSHA256: strings.Repeat("b", 64), OperationKind: "update", OperationID: id, Arguments: []string{"manage", "update", "menu", "--operation", id}}, nil
}
func (f *bootstrapPTYFacade) PrepareBootstrap(_ context.Context, r serverinstall.BootstrapRequest) (serverinstall.BootstrapPrepared, error) {
	f.prepares++
	f.channels = append(f.channels, r.Channel)
	if r.Channel == "stable" {
		return serverinstall.BootstrapPrepared{}, errors.New("stable release unavailable; no fallback performed")
	}
	if r.Channel != "development" || r.OutputDir != "/tmp/bootstrap-explicit-development" {
		return serverinstall.BootstrapPrepared{}, errors.New("unexpected explicit selection")
	}
	return serverinstall.BootstrapPrepared{State: "prepared", Code: "manager-prepared", ArchivePath: "/protected/archive", BundlePath: "/protected/bundle", SourceCommit: strings.Repeat("a", 40), Route: serverinstall.BootstrapRoute{ManagerPath: "/protected/selected-manager", ManagerSHA256: strings.Repeat("a", 64), OperationKind: "install", OperationID: "new-install", Release: "v9.8.7", Arguments: []string{"manage", "menu", "--operation", "new-install"}}}, nil
}
func bootstrapPTYChild(t *testing.T, mode string) {
	f := &bootstrapPTYFacade{mode: mode}
	choice, e := RunBootstrap(context.Background(), f, BootstrapOptions{Options: Options{Input: os.Stdin, Output: os.Stdout, Version: "test"}})
	if e != nil && !errors.Is(e, context.Canceled) {
		t.Fatal(e)
	}
	receipt := map[string]any{"prepares": f.prepares, "opens": f.opens, "inspects": f.inspects, "channels": f.channels, "new_prerequisites": choice.NewPrerequisites}
	if choice.Route != nil {
		receipt["manager"] = choice.Route.ManagerPath
		receipt["manager_sha256"] = choice.Route.ManagerSHA256
		receipt["operation_kind"] = choice.Route.OperationKind
	}
	data, _ := json.Marshal(receipt)
	if e = os.WriteFile(os.Getenv("RIXA_TERMINAL_PTY_RECEIPT"), data, 0600); e != nil {
		t.Fatal(e)
	}
}
func TestBootstrapPTYExplicitChannelAndSelectedManagerHandoff(t *testing.T) {
	p := startPTY(t, "bootstrap-new")
	pos := p.await(t, 0, "1. Install with supplied")
	p.send(t, "\r")
	pos = p.await(t, pos, "Release selection")
	p.send(t, "\r")
	pos = p.await(t, pos, "New private download folder")
	p.send(t, "\r")
	pos = p.await(t, pos, "no fallback performed")
	p.send(t, "\x1b")
	pos = p.await(t, pos, "Release selection")
	p.send(t, "\x15development\r")
	pos = p.await(t, pos, "New private download folder")
	p.send(t, "\x15/tmp/bootstrap-explicit-development\r")
	r := p.finish(t)
	channels, ok := r["channels"].([]any)
	if !ok || len(channels) != 2 || channels[0] != "stable" || channels[1] != "development" || r["prepares"] != float64(2) || r["manager"] != "/protected/selected-manager" || r["opens"] != float64(0) {
		t.Fatalf("wrong explicit handoff: %#v", r)
	}
}
func TestBootstrapPTYRetainedManagerAndUnknownOwnership(t *testing.T) {
	p := startPTY(t, "bootstrap-retained")
	pos := p.await(t, 0, "Inspect update retained-update")
	p.send(t, "\x1bOP")
	pos = p.await(t, pos, "No operation is automatically")
	p.send(t, "\x1b")
	p.await(t, pos, "Authenticated bootstrap")
	p.send(t, "1\r")
	r := p.finish(t)
	if r["opens"] != float64(1) || r["prepares"] != float64(0) || r["manager"] != "/protected/original-exact-manager" || r["manager_sha256"] != strings.Repeat("b", 64) {
		t.Fatalf("retained manager replaced: %#v", r)
	}
	p = startPTY(t, "bootstrap-unknown")
	pos = p.await(t, 0, "Authenticated bootstrap")
	if strings.Contains(lastScreen(p.text()), "Install with supplied") || strings.Contains(lastScreen(p.text()), "new authenticated update") {
		t.Fatal("unknown state offered effects")
	}
	p.send(t, "1\r")
	p.await(t, pos, "unknown after inspection")
	p.send(t, "\x03")
	r = p.finish(t)
	if r["prepares"] != float64(0) || r["opens"] != float64(0) {
		t.Fatalf("unknown ownership caused effects: %#v", r)
	}
}

type postgresPTYFacade struct {
	draft                                         serverinstall.PostgresDraft
	saves, reviews, applies, resumes, credentials int
	token                                         string
	mode                                          string
}

func (f *postgresPTYFacade) result(state string, actions ...string) serverinstall.PostgresResult {
	return serverinstall.PostgresResult{Result: serverinstall.Result{OperationID: "pty-case", OperationKind: "postgres", State: state, Code: state, Actions: actions}, Plan: &serverinstall.PostgresPlan{Profile: "clean-dedicated", Cluster: "rixa", Port: 55432, Packages: []serverinstall.PostgresPackage{{Name: "postgresql-18", Version: "18.6-1.pgdg13+1"}}, Effects: []string{"Create exactly one owned loopback cluster.", "Create two separate application databases."}, Retained: []string{"Packages and databases remain after ordinary Rixa removal."}}}
}
func (f *postgresPTYFacade) SavePostgresDraft(_ context.Context, d serverinstall.PostgresDraft) (serverinstall.PostgresResult, error) {
	f.saves++
	f.draft = d
	if f.mode == "postgres-full" && f.saves == 1 {
		return f.result("draft", "configure", "status"), errors.New("Private prerequisite journal temporarily unavailable.")
	}
	return f.result("draft", "configure", "review", "status"), nil
}
func (f *postgresPTYFacade) LoadPostgresDraft(context.Context, string) (serverinstall.PostgresDraft, error) {
	return f.draft, nil
}
func (f *postgresPTYFacade) ReviewPostgres(context.Context, string) (serverinstall.PostgresResult, error) {
	f.reviews++
	r := f.result("ready", "configure", "review", "apply", "status")
	r.ReviewToken = "exact-postgres-token"
	return r, nil
}
func (f *postgresPTYFacade) ApplyPostgres(_ context.Context, _ string, token string) (serverinstall.PostgresResult, error) {
	f.applies++
	f.token = token
	return f.result("applying", "status"), nil
}
func (f *postgresPTYFacade) ResumePostgres(context.Context, string) (serverinstall.PostgresResult, error) {
	f.resumes++
	return f.result("applying", "status"), nil
}
func (f *postgresPTYFacade) PostgresStatus(context.Context, string) (serverinstall.PostgresResult, error) {
	if f.mode == "postgres-unknown" {
		return f.result("reconciliation-required", "status"), nil
	}
	return f.result("draft", "configure", "review", "status"), nil
}
func (f *postgresPTYFacade) DiscoverPostgres(context.Context) ([]serverinstall.PostgresDiscovery, error) {
	return nil, nil
}
func (f *postgresPTYFacade) LoadPostgresCredentials(context.Context, string) (serverinstall.PostgresCredentials, error) {
	f.credentials++
	return serverinstall.PostgresCredentials{}, errors.New("private credentials must not be read by this menu")
}
func postgresPTYChild(t *testing.T, mode string) {
	f := &postgresPTYFacade{mode: mode, draft: serverinstall.PostgresDraft{OperationID: "pty-case", Port: 55432}}
	e := RunPostgres(context.Background(), f, PostgresOptions{Options: Options{Input: os.Stdin, Output: os.Stdout, OperationID: "pty-case", Version: "test", Plain: mode == "postgres-plain", NoColor: mode == "postgres-no-color"}})
	if e != nil && !errors.Is(e, context.Canceled) {
		t.Fatal(e)
	}
	data, _ := json.Marshal(map[string]any{"saves": f.saves, "reviews": f.reviews, "applies": f.applies, "resumes": f.resumes, "credentials": f.credentials, "port": f.draft.Port, "dedicated": f.draft.DedicatedHost, "token": f.token})
	if e = os.WriteFile(os.Getenv("RIXA_TERMINAL_PTY_RECEIPT"), data, 0600); e != nil {
		t.Fatal(e)
	}
}
func TestPostgresPTYCorrectionBackRetainedInputAndExactPlanApply(t *testing.T) {
	p := startPTY(t, "postgres-full")
	pos := p.await(t, 0, "Configure clean-host prerequisites")
	p.send(t, "\r")
	pos = p.await(t, pos, "PostgreSQL loopback port")
	p.send(t, "\x1580\r")
	pos = p.await(t, pos, "explicitly unused")
	p.send(t, "\x1555432\r")
	pos = p.await(t, pos, "Dedicated clean host")
	p.send(t, "\r")
	pos = p.await(t, pos, "Explicitly confirm")
	p.send(t, "\x15DEDICATED\x1b")
	pos = p.await(t, pos, "PostgreSQL loopback port")
	p.send(t, "\x1b")
	pos = p.await(t, pos, "Unsaved edits:")
	if strings.Contains(lastScreen(p.text()), "Review exact prerequisite") {
		t.Fatal("unsaved prerequisite input exposed review")
	}
	p.send(t, "1\r")
	pos = p.await(t, pos, "55432")
	p.send(t, "\r")
	pos = p.await(t, pos, "DEDICATED")
	p.send(t, "\r")
	pos = p.await(t, pos, "temporarily unavailable")
	p.send(t, "\r")
	pos = p.await(t, pos, "Draft saved privately.")
	p.send(t, "2\r")
	pos = p.await(t, pos, "State: ready")
	p.send(t, "3\r")
	pos = p.await(t, pos, "Apply reviewed PostgreSQL prerequisites?")
	p.await(t, 0, "18.6-1.pgdg13+1")
	p.send(t, "WRONG\r")
	pos = p.await(t, pos, "Confirmation did not match")
	p.send(t, "\x1b")
	pos = p.await(t, pos, "PostgreSQL prerequisites")
	p.send(t, "3\r")
	pos = p.await(t, pos, "Apply reviewed PostgreSQL prerequisites?")
	p.send(t, "APPLY\r")
	p.await(t, pos, "State: applying")
	p.send(t, "\x03")
	r := p.finish(t)
	if r["saves"] != float64(2) || r["reviews"] != float64(1) || r["applies"] != float64(1) || r["credentials"] != float64(0) || r["dedicated"] != true || r["token"] != "exact-postgres-token" {
		t.Fatalf("wrong private exact-plan effects: %#v", r)
	}
}
func TestPostgresPTYPlainNoColorAndUnknownEffects(t *testing.T) {
	for _, mode := range []string{"postgres-plain", "postgres-no-color", "postgres-unknown"} {
		t.Run(mode, func(t *testing.T) {
			p := startPTY(t, mode)
			p.await(t, 0, "PostgreSQL prerequisites")
			if mode == "postgres-unknown" && (strings.Contains(lastScreen(p.text()), "Apply reviewed") || strings.Contains(lastScreen(p.text()), "Resume retained")) {
				t.Fatal("unknown effect offered replay")
			}
			p.send(t, "\x03")
			r := p.finish(t)
			if mode == "postgres-plain" && strings.Contains(p.text(), "\x1b") {
				t.Fatal("plain prerequisites emitted ANSI")
			}
			if mode == "postgres-no-color" && strings.Contains(p.text(), "\x1b[1;36m") {
				t.Fatal("no-color prerequisites emitted semantic color")
			}
			if r["applies"] != float64(0) || r["credentials"] != float64(0) {
				t.Fatal("inspection caused effects")
			}
		})
	}
}

func TestBootstrapPTYSignalRestoresState(t *testing.T) {
	p := startPTY(t, "bootstrap-unknown")
	p.await(t, 0, "Authenticated bootstrap")
	if e := p.command.Process.Signal(unix.SIGTERM); e != nil {
		t.Fatal(e)
	}
	p.finish(t)
}
