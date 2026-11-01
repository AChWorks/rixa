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

const manageAddSiteHelp = "Rixa after-install site admission (read-only, development source)\n\n" +
	"  rixa manage add-site inspect --source <private-json>\n\n" +
	"The private JSON needs installation_operation, site_b_origin, site_b_dsn,\n" +
	"site_b_login and site_b_password. It must be a same-operator 0600 file.\n" +
	"This inspects the current one-site installation, an independent database\n" +
	"and existing TLS hostname coverage. No Add Site Apply or service effect exists.\n"

func manageAddSiteInspectionWithFacade(ctx context.Context, args []string, output io.Writer, backend serverinstall.AddSiteInspectionFacade) error {
	if len(args) == 1 && (args[0] == "--help" || args[0] == "-h") {
		_, err := io.WriteString(output, manageAddSiteHelp)
		return err
	}
	if len(args) == 0 || args[0] != "inspect" {
		return errors.New("usage: rixa manage add-site inspect --source <private-json>; use --help")
	}
	fs := flag.NewFlagSet("manage add-site inspect", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	source := fs.String("source", "", "same-operator private inspection input")
	if err := fs.Parse(args[1:]); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			_, err = io.WriteString(output, manageAddSiteHelp)
			return err
		}
		return errors.New("invalid add-site inspection flags; use --source <private-json>")
	}
	if fs.NArg() != 0 || *source == "" {
		return errors.New("add-site inspect requires --source <private-json> and no positional arguments")
	}
	var draft serverinstall.AddSiteInspectionDraft
	if err := readManagePrivateJSON(*source, &draft); err != nil {
		return err
	}
	result, err := backend.InspectAddSite(ctx, draft)
	if encoded := json.NewEncoder(output).Encode(result); encoded != nil {
		return encoded
	}
	return err
}
