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

const manageAddSiteHelp = "Rixa after-install Add Site (development source; managed Apply/Resume)\n\n" +
	"  rixa manage add-site [menu] [--operation <new-id>] [--installation <original-id>] [--prefill <private-json>] [--plain] [--no-color]\n" +
	"  rixa manage add-site inspect --source <private-json>\n" +
	"  rixa manage add-site draft --source <private-json>\n" +
	"  rixa manage add-site review --operation <new-operation-id>\n" +
	"  rixa manage add-site apply --operation <new-operation-id> --review-token <token>\n" +
	"  rixa manage add-site resume --operation <new-operation-id>\n" +
	"  rixa manage add-site status --operation <new-operation-id>\n\n" +
	"Inspection needs installation_operation, site_b_origin, site_b_dsn,\n" +
	"site_b_login and site_b_password. A saved Draft additionally requires\n" +
	"a separate operation_id. Both sources must be same-operator 0600 JSON.\n" +
	"The interactive menu uses the same private Draft/Review/Apply/Resume backend.\n" +
	"Direct actions emit JSON and never prompt; --non-interactive explicitly rejects the menu.\n" +
	"Review returns a one-use explicit token; Status never discloses it.\n" +
	"Apply performs independent bounded effect stages; Resume requires the frozen manager.\n"

// manageAddSiteMenuWithFacade uses the same typed effects and retained status
// as direct commands. It does not expose arbitrary host execution or modify
// the original installation's frozen journal.
func manageAddSiteMenuWithFacade(ctx context.Context, args []string, input *os.File, output io.Writer, backend serverinstall.AddSiteEffects) error {
	if len(args) > 0 && args[0] == "menu" {
		args = args[1:]
	}
	fs := flag.NewFlagSet("manage add-site menu", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	id := fs.String("operation", "", "new Add Site operation ID")
	installation := fs.String("installation", "", "existing original installation ID")
	prefill := fs.String("prefill", "", "private Add Site JSON prefill")
	plain := fs.Bool("plain", false, "simple terminal rendering")
	noColor := fs.Bool("no-color", false, "disable semantic colors")
	noninteractive := fs.Bool("non-interactive", false, "never open a prompt")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			_, err = io.WriteString(output, manageAddSiteHelp)
			return err
		}
		return errors.New("invalid Add Site menu flags; use rixa manage add-site --help")
	}
	if fs.NArg() != 0 {
		return errors.New("unexpected Add Site menu positional arguments")
	}
	if *noninteractive {
		return errors.New("noninteractive Add Site requires direct inspect/draft/review/apply/resume/status; no prompts were opened")
	}
	var initial *serverinstall.AddSiteDraft
	if *prefill != "" {
		var d serverinstall.AddSiteDraft
		if err := readManagePrivateJSON(*prefill, &d); err != nil {
			return err
		}
		initial = &d
	}
	err := terminal.RunAddSite(ctx, backend, terminal.AddSiteOptions{
		Options: terminal.Options{Input: input, Output: output, Version: product.Version,
			OperationID: *id, Plain: *plain, NoColor: *noColor},
		InstallationOperation: *installation, AddSitePrefill: initial,
	})
	if errors.Is(err, context.Canceled) || errors.Is(err, io.EOF) {
		return nil
	}
	return err
}

func manageAddSiteInspectionWithFacade(ctx context.Context, args []string, output io.Writer, backend serverinstall.AddSiteInspectionFacade) error {
	if len(args) == 1 && (args[0] == "--help" || args[0] == "-h") {
		_, err := io.WriteString(output, manageAddSiteHelp)
		return err
	}
	if len(args) == 0 {
		return errors.New("usage: rixa manage add-site <inspect|draft|review|apply|resume|status>; use --help")
	}
	action := args[0]
	if action != "inspect" && action != "draft" && action != "review" && action != "status" && action != "apply" && action != "resume" {
		return errors.New("usage: rixa manage add-site <inspect|draft|review|apply|resume|status>; use --help")
	}
	fs := flag.NewFlagSet("manage add-site "+action, flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	var source, operation, reviewToken *string
	if action == "inspect" || action == "draft" {
		source = fs.String("source", "", "same-operator private JSON input")
	} else {
		operation = fs.String("operation", "", "Add Site operation ID")
		if action == "apply" {
			reviewToken = fs.String("review-token", "", "opaque review confirmation token")
		}
	}
	if err := fs.Parse(args[1:]); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			_, err = io.WriteString(output, manageAddSiteHelp)
			return err
		}
		return errors.New("invalid add-site flags; use --help")
	}
	if fs.NArg() != 0 || (source != nil && *source == "") || (operation != nil && *operation == "") || (reviewToken != nil && *reviewToken == "") {
		return errors.New("add-site action requires exactly --source or --operation and no positional arguments")
	}
	var result serverinstall.Result
	var err error
	switch action {
	case "inspect":
		var draft serverinstall.AddSiteInspectionDraft
		if err = readManagePrivateJSON(*source, &draft); err != nil {
			return err
		}
		result, err = backend.InspectAddSite(ctx, draft)
	case "draft", "review", "status", "apply", "resume":
		prepare, ok := backend.(serverinstall.AddSitePreparationFacade)
		if !ok {
			return errors.New("this manager supports only read-only Add Site inspection; use a compatible manager")
		}
		switch action {
		case "draft":
			var draft serverinstall.AddSiteDraft
			if err = readManagePrivateJSON(*source, &draft); err != nil {
				return err
			}
			result, err = prepare.SaveAddSiteDraft(ctx, draft)
		case "review":
			result, err = prepare.ReviewAddSite(ctx, *operation)
		case "status":
			result, err = prepare.AddSiteStatus(ctx, *operation)
		case "apply", "resume":
			effects, ok := backend.(serverinstall.AddSiteEffects)
			if !ok {
				return errors.New("this manager does not support Add Site Apply/Resume")
			}
			if action == "apply" {
				result, err = effects.ApplyAddSite(ctx, *operation, *reviewToken)
			} else {
				result, err = effects.ResumeAddSite(ctx, *operation)
			}
		}
	}
	if encoded := json.NewEncoder(output).Encode(result); encoded != nil {
		return encoded
	}
	return err
}
