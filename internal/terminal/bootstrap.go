// SPDX-License-Identifier: MPL-2.0

package terminal

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"

	"github.com/AChWorks/rixa/internal/serverinstall"
)

type BootstrapOptions struct {
	Options
	Channel    string
	ReleaseTag string
	OutputDir  string
}
type BootstrapSelection struct {
	Route            *serverinstall.BootstrapRoute
	NewPrerequisites bool
}

func bootstrapActions(d serverinstall.BootstrapDiscovery) []menuAction {
	var choices []menuAction
	for _, a := range d.Actions {
		switch a {
		case "install":
			choices = append(choices, menuAction{"install", "Install with supplied compatible databases"})
			for _, pg := range d.Postgres {
				if pg.State == "installed" {
					choices = append(choices, menuAction{"install-pg:" + pg.OperationID, "Install using retained PostgreSQL operation " + SafeText(pg.OperationID)})
				}
			}
		case "update":
			choices = append(choices, menuAction{"update", "Select a new authenticated update"})
		case "prerequisites":
			choices = append(choices, menuAction{"prerequisites", "Prepare PostgreSQL on a clean dedicated host"})
		}
	}
	for _, op := range d.Operations {
		choices = append(choices, menuAction{"open:" + op.OperationID, "Inspect " + SafeText(op.OperationKind) + " " + SafeText(op.OperationID) + " (" + SafeText(op.State) + ")"})
	}
	for _, pg := range d.Postgres {
		choices = append(choices, menuAction{"open:" + pg.OperationID, "Inspect PostgreSQL " + SafeText(pg.OperationID) + " (" + SafeText(pg.State) + ")"})
	}
	return append(choices, menuAction{"status", "Refresh discovery"}, menuAction{"details", "View ownership and trust details"}, menuAction{"exit", "Exit"})
}
func RunBootstrap(ctx context.Context, backend serverinstall.BootstrapFacade, options BootstrapOptions) (selection BootstrapSelection, err error) {
	ctx, stop := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM, syscall.SIGHUP)
	defer stop()
	if backend == nil || options.Output == nil {
		return selection, errors.New("bootstrap backend and output are required")
	}
	s, e := newSession(options.Input, options.Output, options.Plain, options.NoColor)
	if e != nil {
		return selection, e
	}
	defer func() { err = errors.Join(err, s.close()) }()
	current, e := backend.DiscoverBootstrap(ctx)
	notice := ""
	if e != nil {
		notice = publicError(e, serverinstall.Draft{})
	}
	selected := 0
	for {
		actions := bootstrapActions(current)
		if selected >= len(actions) {
			selected = 0
		}
		result := serverinstall.Result{OperationKind: "bootstrap", OperationID: current.SelectedOperation, State: current.State, Code: current.Code, Message: current.Message}
		lines := menuLines(s, options.Version, result, notice, serverinstall.Draft{}, actions, selected)
		if e = s.paint("RIXA / Authenticated bootstrap", lines, "Up/Down or Tab · Enter · F1 help · Ctrl-C exit"); e != nil {
			return selection, e
		}
		k, e := s.readKey(ctx)
		if e != nil {
			return selection, e
		}
		if k.name == "quit" {
			return selection, nil
		}
		if s.full && (s.width < 40 || s.height < 12) {
			s.resize()
			continue
		}
		switch k.name {
		case "tick":
			s.resize()
			continue
		case "up", "back":
			selected = (selected + len(actions) - 1) % len(actions)
			continue
		case "down", "next":
			selected = (selected + 1) % len(actions)
			continue
		case "help":
			e = showDetails(ctx, s, []string{
				"Discovery inspects existing ownership before any installation choice.",
				"A retained operation opens only its exact trusted manager.",
				"No operation is automatically applied, resumed, reset or reinstalled.",
				"New update selection is explicit and preserves the original manager.",
				"Stable never silently falls back to development.",
				"Package/source/component and publisher checks precede selected-manager execution.",
				"PostgreSQL provisioning requires its own exact clean-host review.",
				"Supplied TLS/names remain required; no DNS, firewall or certificate issuance.",
				"The terminal always uses English.",
			}, serverinstall.Draft{})
			if e != nil {
				return selection, e
			}
			continue
		case "text":
			number, ne := strconv.Atoi(k.text)
			if ne == nil && number >= 1 && number <= len(actions) {
				selected = number - 1
			}
			continue
		case "enter":
		default:
			continue
		}
		action := actions[selected].id
		switch {
		case action == "exit":
			return selection, nil
		case action == "status":
			current, e = backend.DiscoverBootstrap(ctx)
			notice = ""
			if e != nil {
				notice = publicError(e, serverinstall.Draft{})
			}
		case action == "details":
			detail := []string{current.Message, "Original installation: " + SafeText(current.InstallationOperation), "Selected retained operation: " + SafeText(current.SelectedOperation)}
			for _, op := range current.Operations {
				detail = append(detail, SafeText(op.OperationKind)+" "+SafeText(op.OperationID)+" · "+SafeText(op.State), "Manager: "+SafeText(op.ManagerPath), "Manager SHA-256: "+SafeText(op.ManagerSHA256), SafeText(op.Message))
			}
			if e = showDetails(ctx, s, detail, serverinstall.Draft{}); e != nil {
				return selection, e
			}
		case action == "prerequisites":
			selection.NewPrerequisites = true
			return selection, nil
		case strings.HasPrefix(action, "open:"):
			route, re := backend.OpenBootstrap(ctx, strings.TrimPrefix(action, "open:"))
			if re != nil {
				notice = publicError(re, serverinstall.Draft{})
				continue
			}
			selection.Route = &route
			return selection, nil
		case action == "install" || action == "update" || strings.HasPrefix(action, "install-pg:"):
			token := make([]byte, 6)
			if _, e = rand.Read(token); e != nil {
				return selection, e
			}
			d := serverinstall.Draft{OperationID: "bootstrap-" + hex.EncodeToString(token)}
			request := serverinstall.BootstrapRequest{Update: action == "update"}
			if strings.HasPrefix(action, "install-pg:") {
				request.PostgresOperation = strings.TrimPrefix(action, "install-pg:")
			}
			var prepared serverinstall.BootstrapPrepared
			callback := func(ctx context.Context, tag, channel, output string) (FetchedArtifact, error) {
				request.ReleaseTag, request.Channel, request.OutputDir = tag, channel, output
				var pe error
				prepared, pe = backend.PrepareBootstrap(ctx, request)
				if pe != nil {
					return FetchedArtifact{}, pe
				}
				if prepared.State != "prepared" || prepared.Code != "manager-prepared" {
					return FetchedArtifact{}, errors.New("selected manager was not prepared")
				}
				return FetchedArtifact{ArchivePath: prepared.ArchivePath, BundlePath: prepared.BundlePath, ReleaseTag: prepared.Route.Release, SourceCommit: prepared.SourceCommit}, nil
			}
			initial := options.Channel
			if options.ReleaseTag != "" {
				initial = options.ReleaseTag
			}
			completed, fe := fetchReleaseSelection(ctx, s, callback, &d, initial, options.OutputDir)
			if fe != nil {
				return selection, fe
			}
			if completed {
				selection.Route = &prepared.Route
				return selection, nil
			}
			notice = "Release selection cancelled. No installation or update was applied."
		}
	}
}
