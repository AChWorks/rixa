// SPDX-License-Identifier: MPL-2.0

package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/AChWorks/rixa/internal/product"
	"github.com/AChWorks/rixa/internal/serverinstall"
	"github.com/AChWorks/rixa/internal/terminal"
	"golang.org/x/term"
)

type bootstrapRouteChecker interface {
	CheckBootstrapRoute(context.Context, serverinstall.BootstrapRoute) error
}
type bootstrapLaunch func(context.Context, serverinstall.BootstrapRoute, *os.File, io.Writer) error

func runBootstrap(parent context.Context, args []string, input *os.File, output *os.File) error {
	ctx, stop := signal.NotifyContext(parent, os.Interrupt, syscall.SIGTERM, syscall.SIGHUP)
	defer stop()
	return bootstrapWithFacade(ctx, args, input, output, serverinstall.New(), launchBootstrap, func() (*os.File, error) { return os.OpenFile("/dev/tty", os.O_RDWR, 0) })
}
func bootstrapWithFacade(ctx context.Context, args []string, input *os.File, output io.Writer, backend serverinstall.BootstrapFacade, launch bootstrapLaunch, openTTY func() (*os.File, error)) error {
	action := "menu"
	if len(args) > 0 && len(args[0]) > 0 && args[0][0] != '-' {
		action, args = args[0], args[1:]
	}
	switch action {
	case "menu", "inspect", "prepare", "route":
	default:
		return errors.New("usage: rixa bootstrap <menu|inspect|prepare|route>; use --help")
	}
	fs := flag.NewFlagSet("bootstrap "+action, flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	id := fs.String("operation", "", "retained operation or new prepared operation ID")
	channel := fs.String("channel", "", "stable (default) or explicit development")
	release := fs.String("release", "", "exact versioned release, exclusive with channel")
	directory := fs.String("output", "", "new private download folder")
	postgres := fs.String("postgres", "", "explicit completed PostgreSQL operation for private new-install prefill")
	update := fs.Bool("update", false, "explicitly prepare a new update candidate for the retained installation")
	plain := fs.Bool("plain", false, "plain terminal")
	noColor := fs.Bool("no-color", false, "disable semantic colors")
	noninteractive := fs.Bool("non-interactive", false, "never open a menu")
	if e := fs.Parse(args); e != nil {
		if errors.Is(e, flag.ErrHelp) {
			_, e = io.WriteString(output, bootstrapHelp)
			return e
		}
		return errors.New("invalid bootstrap flags; use --help")
	}
	if fs.NArg() != 0 {
		return errors.New("unexpected bootstrap positional arguments")
	}
	if *release != "" && *channel != "" || *channel != "" && *channel != "stable" && *channel != "development" {
		return errors.New("select stable, development or one exact release; no channel fallback is performed")
	}
	if *update && *postgres != "" {
		return errors.New("--update and --postgres are separate choices")
	}
	if action != "menu" && (*plain || *noColor) {
		return errors.New("--plain and --no-color belong to the menu; direct bootstrap commands emit JSON")
	}
	if action == "menu" {
		if *noninteractive {
			return errors.New("noninteractive bootstrap requires inspect, prepare or route; no prompts were opened")
		}
		if *update || *postgres != "" {
			return errors.New("new update and completed PostgreSQL choices are explicit menu actions; --update and --postgres belong to prepare")
		}
		terminalInput := input
		var tty *os.File
		if input == nil || !term.IsTerminal(int(input.Fd())) {
			if openTTY == nil {
				return terminal.ErrTerminal
			}
			var e error
			tty, e = openTTY()
			if e != nil {
				return terminal.ErrTerminal
			}
			defer tty.Close()
			terminalInput = tty
		}
		if *id != "" {
			if *channel != "" || *release != "" || *directory != "" {
				return errors.New("a retained operation uses its exact manager; release selection belongs to a new explicit prepare/update")
			}
			route, e := backend.OpenBootstrap(ctx, *id)
			if e != nil {
				return e
			}
			route.Plain, route.NoColor = *plain, *noColor
			return checkedBootstrapLaunch(ctx, backend, launch, route, terminalInput, output)
		}
		for {
			choice, e := terminal.RunBootstrap(ctx, backend, terminal.BootstrapOptions{Options: terminal.Options{Input: terminalInput, Output: output, Plain: *plain, NoColor: *noColor, Version: product.Version}, Channel: *channel, ReleaseTag: *release, OutputDir: *directory})
			if errors.Is(e, context.Canceled) || errors.Is(e, io.EOF) {
				return nil
			}
			if e != nil {
				return e
			}
			if choice.NewPrerequisites {
				pg, ok := backend.(serverinstall.PostgresFacade)
				if !ok {
					return errors.New("this starter does not support native PostgreSQL prerequisite provisioning")
				}
				e = terminal.RunPostgres(ctx, pg, terminal.PostgresOptions{Options: terminal.Options{Input: terminalInput, Output: output, Plain: *plain, NoColor: *noColor, Version: product.Version}})
				if errors.Is(e, context.Canceled) || errors.Is(e, io.EOF) {
					return nil
				}
				if e != nil {
					return e
				}
				continue
			}
			if choice.Route == nil {
				return nil
			}
			choice.Route.Plain, choice.Route.NoColor = *plain, *noColor
			return checkedBootstrapLaunch(ctx, backend, launch, *choice.Route, terminalInput, output)
		}
	}
	if action != "prepare" && (*channel != "" || *release != "" || *directory != "" || *update || *postgres != "") {
		return errors.New("release/download/update/PostgreSQL flags belong to prepare")
	}
	switch action {
	case "inspect":
		if *id != "" {
			return errors.New("--operation belongs to route, prepare or the menu")
		}
		r, e := backend.DiscoverBootstrap(ctx)
		if encoded := json.NewEncoder(output).Encode(r); encoded != nil {
			return encoded
		}
		return e
	case "route":
		r, e := backend.OpenBootstrap(ctx, *id)
		receipt := map[string]any{"schema": "rixa.native-bootstrap-route.v1", "state": "prepared", "code": "route-ready", "route": r}
		if e != nil {
			receipt = map[string]any{"schema": "rixa.native-bootstrap-route.v1", "state": "blocked", "code": "inspection-required", "message": "Inspect retained ownership and the exact manager with bootstrap inspect before opening an operation."}
			if errors.Is(e, serverinstall.ErrManagerChanged) {
				receipt["code"] = "retained-manager-required"
			}
		}
		if encoded := json.NewEncoder(output).Encode(receipt); encoded != nil {
			return encoded
		}
		return e
	case "prepare":
		r, e := backend.PrepareBootstrap(ctx, serverinstall.BootstrapRequest{OperationID: *id, Channel: *channel, ReleaseTag: *release, OutputDir: *directory, Update: *update, PostgresOperation: *postgres})
		if encoded := json.NewEncoder(output).Encode(r); encoded != nil {
			return encoded
		}
		return e
	}
	return nil
}
func checkedBootstrapLaunch(ctx context.Context, backend serverinstall.BootstrapFacade, launch bootstrapLaunch, route serverinstall.BootstrapRoute, input *os.File, output io.Writer) error {
	check, ok := backend.(bootstrapRouteChecker)
	if !ok || launch == nil {
		return errors.New("this starter cannot positively validate a selected manager route")
	}
	if e := check.CheckBootstrapRoute(ctx, route); e != nil {
		return e
	}
	return launch(ctx, route, input, output)
}
func launchBootstrap(ctx context.Context, route serverinstall.BootstrapRoute, input *os.File, output io.Writer) error {
	if os.Geteuid() != 0 {
		return serverinstall.ErrAuthority
	}
	if input == nil || !term.IsTerminal(int(input.Fd())) {
		return terminal.ErrTerminal
	}
	if !filepath.IsAbs(route.ManagerPath) || len(route.ManagerSHA256) != 64 {
		return serverinstall.ErrState
	}
	// Check the actual opened image again immediately before the fixed exec.
	f, e := os.Open(route.ManagerPath)
	if e != nil {
		return serverinstall.ErrState
	}
	data, e := io.ReadAll(io.LimitReader(f, (64<<20)+1))
	closeErr := f.Close()
	sum := sha256.Sum256(data)
	if e != nil || closeErr != nil || len(data) > 64<<20 || hex.EncodeToString(sum[:]) != route.ManagerSHA256 {
		return serverinstall.ErrManagerChanged
	}
	cmd := exec.CommandContext(ctx, route.ManagerPath, route.Arguments...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = input, output, output
	cmd.Env = bootstrapEnvironment(os.Environ(), route.HelperPath, route.Plain, route.NoColor)

	return runBootstrapChild(cmd, input)
}

// The parent owns final terminal restoration even if cancellation must kill an
// unresponsive selected manager before its own restoration defer can run.
func runBootstrapChild(cmd *exec.Cmd, input *os.File) (err error) {
	state, e := term.GetState(int(input.Fd()))
	if e != nil {
		return e
	}
	defer func() { err = errors.Join(err, term.Restore(int(input.Fd()), state)) }()
	cmd.Cancel = func() error { return cmd.Process.Signal(syscall.SIGTERM) }
	cmd.WaitDelay = 2 * time.Second
	return cmd.Run()
}

func bootstrapEnvironment(base []string, helper string, plain, noColor bool) []string {
	result := []string{}
	for _, value := range base {
		if helper != "" && strings.HasPrefix(value, "PATH=") || plain && strings.HasPrefix(value, "TERM=") || noColor && strings.HasPrefix(value, "NO_COLOR=") {
			continue
		}
		result = append(result, value)
	}
	if helper != "" {
		result = append(result, "PATH="+filepath.Dir(helper)+":/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin")
	}
	if plain {
		result = append(result, "TERM=dumb")
	}
	if noColor {
		result = append(result, "NO_COLOR=1")
	}
	return result
}

const bootstrapHelp = "Rixa authenticated native bootstrap starter\n\n" +
	"  rixa bootstrap [menu] [--operation <retained-id>] [--channel stable|development | --release <tag>] [--plain] [--no-color]\n" +
	"  rixa bootstrap inspect [--non-interactive]\n" +
	"  rixa bootstrap route [--operation <retained-id>] [--non-interactive]\n" +
	"  rixa bootstrap prepare [--channel stable|development | --release <tag>] [--operation <new-id>] [--output <new-private-dir>] [--update | --postgres <completed-pg-id>]\n\n" +
	"Inspect retained ownership first. A retained operation uses its exact frozen manager.\n" +
	"Prepare authenticates/stages a manager and returns its explicit menu route; it never applies or resumes.\n" +
	"Stable never silently selects development. New update is an explicit choice.\n" +
	"Direct commands emit JSON and never open a terminal or launch a manager.\n" +
	"Interactive mode uses the controlling terminal even when the starter input is a download pipe.\n" +
	"PostgreSQL provisioning has a separate reviewed clean-host operation. Supplied TLS/names remain required.\n" +
	"No shell generation or fabricated release pin is supplied by this starter command.\n"
