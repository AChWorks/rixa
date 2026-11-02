// SPDX-License-Identifier: MPL-2.0
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"io"

	"github.com/AChWorks/rixa/internal/serverinstall"
)

const manageAddSiteHelp = "Rixa after-install Add Site (development source; managed Apply/Resume)\n\n" +
	"  rixa manage add-site inspect --source <private-json>\n" +
	"  rixa manage add-site draft --source <private-json>\n" +
	"  rixa manage add-site review --operation <new-operation-id>\n" +
	"  rixa manage add-site apply --operation <new-operation-id> --review-token <token>\n" +
	"  rixa manage add-site resume --operation <new-operation-id>\n" +
	"  rixa manage add-site status --operation <new-operation-id>\n\n" +
	"Inspection needs installation_operation, site_b_origin, site_b_dsn,\n" +
	"site_b_login and site_b_password. A saved Draft additionally requires\n" +
	"a separate operation_id. Both sources must be same-operator 0600 JSON.\n" +
	"Review returns a one-use explicit token; Status never discloses it.\n" +
	"Apply performs independent bounded effect stages; Resume requires the frozen manager.\n"

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
