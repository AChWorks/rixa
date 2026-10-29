// SPDX-License-Identifier: MPL-2.0

package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"io"
	"os"
	"syscall"

	"github.com/AChWorks/achrix/installation"
	"github.com/AChWorks/rixa/internal/product"
)

// The CLI only presents shared product operations. A future terminal or web
// facade must consume the same assessment, execution and retained result.
func runSetup(ctx context.Context, args []string, lookup func(string) (string, bool), output io.Writer) error {
	if len(args) == 0 {
		return errors.New("usage: rixa setup <assess|apply|status> --target <absolute-config-path> --operation <id> [--source <candidate-json>]")
	}
	action := args[0]
	if action != "assess" && action != "apply" && action != "status" {
		return errors.New("setup action must be assess, apply or status")
	}
	fs := flag.NewFlagSet("setup "+action, flag.ContinueOnError)
	target := fs.String("target", "", "absolute configuration target in an existing private directory")
	operation := fs.String("operation", "", "non-secret operation ID, reused to inspect or resume this exact setup")
	var source *string
	if action != "status" {
		source = fs.String("source", "", "candidate JSON configuration containing secret environment names only")
	}
	if err := fs.Parse(args[1:]); err != nil {
		return err
	}
	if fs.NArg() != 0 || *target == "" || *operation == "" || source != nil && *source == "" {
		return errors.New("setup requires --target and --operation; assess/apply also require --source")
	}
	var result product.SetupResult
	var err error
	if action == "status" {
		result, err = product.InspectSetup(*target, *operation)
	} else {
		data, readErr := readSetupSource(*source)
		if readErr != nil {
			return readErr
		}
		request := product.SetupRequest{OperationID: *operation, TargetPath: *target, Configuration: data}
		if action == "assess" {
			result, err = product.AssessSetup(ctx, request, lookup)
		} else {
			result, err = product.ApplySetup(ctx, request, lookup)
		}
	}
	if encodeErr := json.NewEncoder(output).Encode(result); encodeErr != nil {
		return encodeErr
	}
	if err == nil && action != "status" && result.Plan != nil {
		switch result.Plan.Status() {
		case installation.OperatorActionRequired, installation.ReconciliationRequired, installation.UnsupportedPlan:
			return errors.New("setup cannot proceed; inspect the reported requirements")
		}
	}
	return err
}

func readSetupSource(path string) ([]byte, error) {
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, errors.New("setup configuration source is unavailable")
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() == 0 || info.Size() > 1<<20 {
		return nil, errors.New("setup configuration source must be a regular JSON file of at most 1 MiB")
	}
	data, err := io.ReadAll(io.LimitReader(f, (1<<20)+1))
	if err != nil || len(data) == 0 || len(data) > 1<<20 {
		return nil, errors.New("setup configuration source is unavailable or too large")
	}
	return data, nil
}
