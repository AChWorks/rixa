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

// Update remains an optional capability so existing installation facades keep
// their original contract. The nested commands never interpret host effects.
func manageUpdateWithFacade(ctx context.Context, args []string, input *os.File, output io.Writer, backend serverinstall.UpdateFacade) error {
	action := "menu"
	if len(args) > 0 && len(args[0]) > 0 && args[0][0] != '-' {
		action, args = args[0], args[1:]
	}
	switch action {
	case "menu", "draft", "review", "apply", "resume", "status", "remove":
	default:
		return errors.New("usage: rixa manage update <menu|draft|review|apply|resume|status|remove>; use --help for supported flags")
	}
	fs := flag.NewFlagSet("manage update "+action, flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	id := fs.String("operation", "", "retained update operation ID")
	installation := fs.String("installation", "", "owning installation operation for a new menu draft")
	token := fs.String("review-token", "", "token from the exact successful update review")
	source := fs.String("source", "", "absolute private update draft JSON")
	prefill := fs.String("prefill", "", "absolute private partial update JSON for the menu")
	plain := fs.Bool("plain", false, "disable full-screen rendering")
	noColor := fs.Bool("no-color", false, "disable semantic colors")
	noninteractive := fs.Bool("non-interactive", false, "never open a menu or prompt")
	confirmRemove := fs.Bool("confirm-remove", false, "confirm retained-data executable/service removal")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			_, err = io.WriteString(output, manageUpdateHelp)
			return err
		}
		return errors.New("invalid update flags; use rixa manage update --help")
	}
	if fs.NArg() != 0 {
		return errors.New("unexpected update positional arguments")
	}
	if action == "menu" {
		if *noninteractive {
			return errors.New("noninteractive update requires draft, review, apply, resume, status or remove; no prompts were opened")
		}
		if *source != "" || *token != "" || *confirmRemove {
			return errors.New("--source belongs to update draft, --review-token to apply, and --confirm-remove to remove")
		}
		var initial *serverinstall.UpdateDraft
		if *prefill != "" {
			d, err := readManageUpdateDraft(*prefill)
			if err != nil {
				return err
			}
			initial = &d
		}
		err := terminal.RunUpdate(ctx, backend, terminal.UpdateOptions{
			Options:               terminal.Options{Input: input, Output: output, Plain: *plain, NoColor: *noColor, Version: product.Version, OperationID: *id, FetchRelease: fetchForManage},
			InstallationOperation: *installation, UpdatePrefill: initial,
		})
		if errors.Is(err, context.Canceled) || errors.Is(err, io.EOF) {
			return nil
		}
		return err
	}
	if *prefill != "" || *installation != "" {
		return errors.New("--prefill and --installation belong to the update menu; direct draft binds installation_operation in JSON")
	}
	if *plain || *noColor {
		return errors.New("--plain and --no-color apply only to the interactive update menu; direct commands emit JSON")
	}
	if action != "draft" && *source != "" {
		return errors.New("--source belongs to update draft")
	}
	if action != "apply" && *token != "" {
		return errors.New("--review-token belongs to update apply")
	}
	if action != "remove" && *confirmRemove {
		return errors.New("--confirm-remove belongs to update remove")
	}
	var result serverinstall.Result
	var err error
	if action == "draft" {
		if *source == "" {
			return errors.New("update draft requires --source <absolute-private-json>")
		}
		draft, readErr := readManageUpdateDraft(*source)
		if readErr != nil {
			return readErr
		}
		if *id != "" && *id != draft.OperationID {
			return errors.New("--operation must match the private update draft operation_id")
		}
		result, err = backend.SaveUpdateDraft(ctx, draft)
	} else {
		if *id == "" {
			return errors.New("--operation is required for the update operation")
		}
		switch action {
		case "review":
			result, err = backend.ReviewUpdate(ctx, *id)
		case "apply":
			if *token == "" {
				return errors.New("update apply requires --review-token from the exact successful update review JSON; review again if inputs changed")
			}
			result, err = backend.ApplyUpdate(ctx, *id, *token)
		case "resume":
			result, err = backend.ResumeUpdate(ctx, *id)
		case "status":
			result, err = backend.UpdateStatus(ctx, *id)
		case "remove":
			if !*confirmRemove {
				return errors.New("removal stops the owned service and removes its active executable while retaining data, configuration and original manager; inspect update status, then pass --confirm-remove")
			}
			result, err = backend.RemoveUpdatedInstallation(ctx, *id)
		}
	}
	if encodeErr := json.NewEncoder(output).Encode(result); encodeErr != nil {
		return encodeErr
	}
	return err
}

func readManageUpdateDraft(path string) (serverinstall.UpdateDraft, error) {
	var draft serverinstall.UpdateDraft
	if err := readManagePrivateJSON(path, &draft); err != nil {
		return serverinstall.UpdateDraft{}, err
	}
	return draft, nil
}

const manageUpdateHelp = "Rixa native retained-state update\n\n" +
	"  rixa manage update [menu] [--operation <update-id>] [--installation <install-id>] [--prefill <private-json>] [--plain] [--no-color]\n" +
	"  rixa manage update draft --source <private-json> [--operation <matching-update-id>]\n" +
	"  rixa manage update <review|resume|status> --operation <update-id>\n" +
	"  rixa manage update apply --operation <update-id> --review-token <from-update-review>\n" +
	"  rixa manage update remove --operation <update-id> --confirm-remove\n\n" +
	"Update draft/prefill fields: operation_id, installation_operation, archive, bundle, release, source_commit.\n" +
	"The original installation and its frozen manager are retained; update uses this executing manager and the authenticated candidate.\n" +
	"No replacement credentials, bootstrap, password reset or database major upgrade is accepted.\n" +
	"Direct commands emit safe JSON and never prompt. --non-interactive rejects the menu.\n" +
	"Draft/prefill sources must be same-operator private regular JSON files.\n" +
	"Apply queues only the exact reviewed update; inspect status before claiming completion.\n" +
	"Inspect uncertain outcomes before resume; do not repeat effects or assume database rollback.\n" +
	"Ordinary removal stops serving while retaining configuration, credentials, databases, site data, original Media and prerequisites.\n"
