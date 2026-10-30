// SPDX-License-Identifier: MPL-2.0

package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"io"
	"os"

	"github.com/AChWorks/rixa/internal/product"
	"github.com/AChWorks/rixa/internal/serverinstall"
	"github.com/AChWorks/rixa/internal/terminal"
)

func managePostgresWithFacade(ctx context.Context, args []string, input *os.File, output io.Writer, backend serverinstall.PostgresFacade) error {
	action := "menu"
	if len(args) > 0 && len(args[0]) > 0 && args[0][0] != '-' {
		action, args = args[0], args[1:]
	}
	switch action {
	case "menu", "draft", "review", "apply", "resume", "status":
	default:
		return errors.New("usage: rixa manage prerequisites <menu|draft|review|apply|resume|status>; use --help")
	}
	fs := flag.NewFlagSet("manage prerequisites "+action, flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	id := fs.String("operation", "", "retained prerequisite operation ID")
	token := fs.String("review-token", "", "exact successful review token")
	source := fs.String("source", "", "absolute private draft JSON")
	prefill := fs.String("prefill", "", "absolute private menu prefill JSON")
	plain := fs.Bool("plain", false, "plain terminal")
	noColor := fs.Bool("no-color", false, "disable semantic color")
	noninteractive := fs.Bool("non-interactive", false, "never open a menu")
	if e := fs.Parse(args); e != nil {
		if errors.Is(e, flag.ErrHelp) {
			_, e = io.WriteString(output, managePostgresHelp)
			return e
		}
		return errors.New("invalid prerequisite flags; use --help")
	}
	if fs.NArg() != 0 {
		return errors.New("unexpected prerequisite positional arguments")
	}
	if action == "menu" {
		if *noninteractive {
			return errors.New("noninteractive prerequisites require draft, review, apply, resume or status; no prompts were opened")
		}
		if *source != "" || *token != "" {
			return errors.New("--source belongs to draft and --review-token belongs to apply")
		}
		var initial *serverinstall.PostgresDraft
		if *prefill != "" {
			d, e := readManagePostgresDraft(*prefill)
			if e != nil {
				return e
			}
			initial = &d
		}
		e := terminal.RunPostgres(ctx, backend, terminal.PostgresOptions{Options: terminal.Options{Input: input, Output: output, Plain: *plain, NoColor: *noColor, Version: product.Version, OperationID: *id}, PostgresPrefill: initial})
		if errors.Is(e, context.Canceled) || errors.Is(e, io.EOF) {
			return nil
		}
		return e
	}
	if *plain || *noColor || *prefill != "" {
		return errors.New("--plain, --no-color and --prefill belong to the prerequisite menu; direct commands emit JSON")
	}
	if action != "draft" && *source != "" {
		return errors.New("--source belongs to prerequisite draft")
	}
	if action != "apply" && *token != "" {
		return errors.New("--review-token belongs to prerequisite apply")
	}
	var r serverinstall.PostgresResult
	var e error
	if action == "draft" {
		if *source == "" {
			return errors.New("prerequisite draft requires --source <absolute-private-json>")
		}
		d, re := readManagePostgresDraft(*source)
		if re != nil {
			return re
		}
		if *id != "" && *id != d.OperationID {
			return errors.New("--operation must match the private prerequisite draft operation_id")
		}
		r, e = backend.SavePostgresDraft(ctx, d)
	} else {
		if *id == "" {
			return errors.New("--operation is required")
		}
		switch action {
		case "review":
			r, e = backend.ReviewPostgres(ctx, *id)
		case "apply":
			if *token == "" {
				return errors.New("prerequisite apply requires --review-token from the exact successful review JSON")
			}
			r, e = backend.ApplyPostgres(ctx, *id, *token)
		case "resume":
			r, e = backend.ResumePostgres(ctx, *id)
		case "status":
			r, e = backend.PostgresStatus(ctx, *id)
		}
	}
	if encoded := json.NewEncoder(output).Encode(r); encoded != nil {
		return encoded
	}
	return e
}
func readManagePostgresDraft(path string) (serverinstall.PostgresDraft, error) {
	var d serverinstall.PostgresDraft
	if e := readManagePrivateJSON(path, &d); e != nil {
		return serverinstall.PostgresDraft{}, e
	}
	return d, nil
}

const managePostgresHelp = "Rixa PostgreSQL 18 prerequisites for a clean dedicated supported host\n\n" +
	"  rixa manage prerequisites [menu] [--operation <id>] [--prefill <private-json>] [--plain] [--no-color]\n" +
	"  rixa manage prerequisites draft --source <private-json> [--operation <matching-id>]\n" +
	"  rixa manage prerequisites <review|resume|status> --operation <id>\n" +
	"  rixa manage prerequisites apply --operation <id> --review-token <exact-review-token>\n\n" +
	"Draft fields: operation_id, port, dedicated_host. Explicit dedicated_host confirmation is required.\n" +
	"Existing/shared PostgreSQL uses supplied compatible databases through normal installation.\n" +
	"Review shows exact package versions and owned cluster/role/database effects.\n" +
	"No automatic major upgrade, credential reset, shared service replacement or removal.\n" +
	"Inspect unknown outcomes before resume. Ordinary Rixa removal retains PostgreSQL and databases.\n" +
	"Direct commands emit safe JSON and never prompt; --non-interactive rejects the menu.\n" +
	"Generated credentials remain private; no credential-printing command is provided.\n"
