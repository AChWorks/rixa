// SPDX-License-Identifier: MPL-2.0

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"

	"github.com/AChWorks/rixa/internal/product"
	"github.com/AChWorks/rixa/internal/releaseartifact"
	"github.com/AChWorks/rixa/internal/serverinstall"
	"github.com/AChWorks/rixa/internal/terminal"
	"golang.org/x/sys/unix"
)

// runManage is a facade over the native backend. It never emits the private
// draft, implements host effects, or accepts arbitrary privileged commands.
func runManage(parent context.Context, args []string, input *os.File, output *os.File) error {
	ctx, stop := signal.NotifyContext(parent, os.Interrupt, syscall.SIGTERM, syscall.SIGHUP)
	defer stop()
	if len(args) > 0 && args[0] == "web" {
		return runManageWeb(ctx, args[1:], output)
	}
	if len(args) > 0 && args[0] == "internal-stage" {
		if len(args) != 2 {
			return errors.New("invalid fixed runtime-stage arguments")
		}
		return serverinstall.RuntimeStage(ctx, args[1:])
	}
	if len(args) > 0 && args[0] == "internal-apply" {
		if len(args) != 2 {
			return errors.New("internal-apply requires one operation ID")
		}
		result, err := serverinstall.New().RunOperation(ctx, args[1])
		if encodeErr := json.NewEncoder(output).Encode(result); encodeErr != nil {
			return encodeErr
		}
		return err
	}
	if len(args) > 0 && args[0] == "internal-update-apply" {
		if len(args) != 2 {
			return errors.New("internal-update-apply requires one operation ID")
		}
		result, err := serverinstall.New().RunUpdateOperation(ctx, args[1])
		if encodeErr := json.NewEncoder(output).Encode(result); encodeErr != nil {
			return encodeErr
		}
		return err
	}
	if len(args) > 0 && args[0] == "internal-postgres-apply" {
		if len(args) != 2 {
			return errors.New("internal-postgres-apply requires one operation ID")
		}
		r, e := serverinstall.New().RunPostgresOperation(ctx, args[1])
		if encoded := json.NewEncoder(output).Encode(r); encoded != nil {
			return encoded
		}
		return e
	}
	if len(args) > 0 && args[0] == "internal-postgres-package-guard" {
		if len(args) != 3 || (args[2] != "common" && args[2] != "server") {
			return errors.New("internal-postgres-package-guard requires one operation ID and common or server phase")
		}
		return serverinstall.New().RunPostgresPackageGuard(ctx, args[1], args[2], input)
	}
	if os.Geteuid() != 0 {
		return serverinstall.ErrAuthority
	}
	return manageWithFacade(ctx, args, input, output, serverinstall.New())
}

func manageWithFacade(ctx context.Context, args []string, input *os.File, output io.Writer, backend serverinstall.Facade) error {
	action := "menu"
	if len(args) > 0 && len(args[0]) > 0 && args[0][0] != '-' {
		action = args[0]
		args = args[1:]
	}
	if action == "prerequisites" {
		pg, ok := backend.(serverinstall.PostgresFacade)
		if !ok {
			return errors.New("this manager does not support PostgreSQL prerequisite provisioning; supply compatible databases or use a compatible manager")
		}
		return managePostgresWithFacade(ctx, args, input, output, pg)
	}
	if action == "add-site" {
		inspector, ok := backend.(serverinstall.AddSiteInspectionFacade)
		if !ok {
			return errors.New("this manager does not support after-install site admission inspection")
		}
		return manageAddSiteInspectionWithFacade(ctx, args, output, inspector)
	}
	if action == "update" {
		updater, ok := backend.(serverinstall.UpdateFacade)
		if !ok {
			return errors.New("this manager does not support native update; use a compatible manager and retain the original installation manager")
		}
		return manageUpdateWithFacade(ctx, args, input, output, updater)
	}
	if action != "menu" && action != "draft" && action != "review" && action != "apply" && action != "resume" && action != "status" && action != "remove" {
		return errors.New("usage: rixa manage <menu|draft|review|apply|resume|status|remove|web> [--operation <id>]; draft also needs --source <private-json>")
	}
	fs := flag.NewFlagSet("manage "+action, flag.ContinueOnError)
	// Flag diagnostics can contain user-supplied values. Keep them out of general
	// output; the fixed returned error describes the supported input.
	fs.SetOutput(io.Discard)
	id := fs.String("operation", "", "retained operation ID")
	reviewToken := fs.String("review-token", "", "opaque token from the exact successful review being confirmed")
	source := fs.String("source", "", "absolute private draft JSON; never printed")
	prefill := fs.String("prefill", "", "absolute private partial JSON for the guided menu")
	plain := fs.Bool("plain", false, "disable full-screen rendering")
	noColor := fs.Bool("no-color", false, "disable semantic colors")
	noninteractive := fs.Bool("non-interactive", false, "never open a menu or prompt")
	confirmRemove := fs.Bool("confirm-remove", false, "confirm stopping service/removing executable while retaining data")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			_, e := io.WriteString(output, manageHelp)
			return e
		}
		return errors.New("invalid manage flags; use --operation, --source, --plain, --no-color, --non-interactive or --confirm-remove")
	}
	if fs.NArg() != 0 {
		return errors.New("unexpected manage positional arguments")
	}
	if action == "menu" {
		if *source != "" || *confirmRemove || *reviewToken != "" {
			return errors.New("--source belongs to draft and --confirm-remove belongs to remove")
		}
		if *noninteractive {
			return errors.New("noninteractive management requires draft, review, apply, status or remove; no prompts were opened")
		}
		var initial *serverinstall.Draft
		if *prefill != "" {
			d, e := readManageDraft(*prefill)
			if e != nil {
				return e
			}
			initial = &d
		}
		err := terminal.Run(ctx, backend, terminal.Options{Input: input, Output: output, Plain: *plain, NoColor: *noColor, Version: product.Version, OperationID: *id, Prefill: initial, FetchRelease: fetchForManage})
		if errors.Is(err, context.Canceled) || errors.Is(err, io.EOF) {
			return nil
		}
		return err
	}
	if action != "apply" && *reviewToken != "" {
		return errors.New("--review-token belongs to apply")
	}
	if *prefill != "" {
		return errors.New("--prefill belongs to the guided menu")
	}
	if *plain || *noColor {
		return errors.New("--plain and --no-color apply only to the interactive menu; direct commands emit JSON")
	}
	if action != "draft" && *source != "" {
		return errors.New("--source belongs to the draft command")
	}
	if action != "remove" && *confirmRemove {
		return errors.New("--confirm-remove belongs to remove")
	}
	var result serverinstall.Result
	var err error
	switch action {
	case "draft":
		if *source == "" {
			return errors.New("draft requires --source <absolute-private-json>")
		}
		draft, readErr := readManageDraft(*source)
		if readErr != nil {
			return readErr
		}
		if *id != "" && draft.OperationID != *id {
			return errors.New("--operation must match the private draft operation_id")
		}
		result, err = backend.SaveDraft(ctx, draft)
	case "review", "apply", "resume", "status", "remove":
		if *id == "" {
			return errors.New("--operation is required")
		}
		switch action {
		case "review":
			result, err = backend.Review(ctx, *id)
		case "apply":
			if *reviewToken == "" {
				return errors.New("apply requires --review-token from the exact successful review JSON; review again if inputs changed")
			}
			result, err = backend.Apply(ctx, *id, *reviewToken)
		case "resume":
			result, err = backend.Resume(ctx, *id)
		case "status":
			result, err = backend.Status(ctx, *id)
		case "remove":
			if !*confirmRemove {
				return errors.New("removal stops serving and removes only owned executable/service material; data/configuration remain; inspect status, then pass --confirm-remove")
			}
			result, err = backend.Remove(ctx, *id)
		}
	}
	if encodeErr := json.NewEncoder(output).Encode(result); encodeErr != nil {
		return encodeErr
	}
	return err
}

// Only a same-operator private regular JSON file may supply real credentials.
// O_NONBLOCK and O_NOFOLLOW make FIFOs/final symlinks fail without prompting.
func readManageDraft(path string) (serverinstall.Draft, error) {
	var draft serverinstall.Draft
	if err := readManagePrivateJSON(path, &draft); err != nil {
		return serverinstall.Draft{}, err
	}
	return draft, nil
}

func readManagePrivateJSON(path string, destination any) error {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return errors.New("draft source must be an absolute clean private JSON path")
	}
	fd, err := unix.Open(path, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW|unix.O_NONBLOCK, 0)
	if err != nil {
		return errors.New("private draft source is unavailable")
	}
	f := os.NewFile(uintptr(fd), "private-draft")
	defer f.Close()
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() == 0 || info.Size() > 128<<10 || info.Mode().Perm()&0077 != 0 {
		return errors.New("draft source must be a private regular JSON file of at most 128 KiB")
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || int(stat.Uid) != os.Geteuid() {
		return errors.New("draft source must belong to the executing operator")
	}
	data, err := io.ReadAll(io.LimitReader(f, (128<<10)+1))
	if err != nil || len(data) > 128<<10 {
		return errors.New("private draft source could not be read within its bound")
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if decoder.Decode(destination) != nil {
		return errors.New("private draft JSON is malformed or contains unknown fields")
	}
	var extra any
	if decoder.Decode(&extra) != io.EOF {
		return errors.New("private draft JSON must contain exactly one document")
	}
	return nil
}

// fetchForManage delegates the entire channel/tag/producer policy. Presentation
// receives paths only after the shared verifier has authenticated the bytes.
func fetchForManage(ctx context.Context, tag, channel, output string) (terminal.FetchedArtifact, error) {
	r, e := releaseartifact.Fetch(ctx, releaseartifact.FetchRequest{OutputDir: output, ReleaseTag: tag, Channel: channel})
	if e != nil {
		detail := r.Detail
		if detail == "" {
			detail = "artifact fetch failed"
		}
		return terminal.FetchedArtifact{}, errors.New(r.Code + ": " + r.Field + ": " + detail)
	}
	if r.Status != "verified" || r.Code != "ok" || r.Verification == nil || r.Verification.Status != "verified" || r.Verification.Code != "ok" {
		return terminal.FetchedArtifact{}, errors.New("artifact fetch did not establish publisher verification")
	}
	return terminal.FetchedArtifact{ArchivePath: r.ArchivePath, BundlePath: r.BundlePath, ReleaseTag: r.ReleaseTag, SourceCommit: r.SourceCommit}, nil
}

const manageHelp = "Rixa native operator management\n\n" +
	"  rixa manage web --help  (temporary scoped browser console)\n" +
	"  rixa manage [menu] [--operation <id>] [--prefill <private-json>] [--plain] [--no-color]\n" +
	"  rixa manage draft --source <private-json> [--operation <matching-id>]\n" +
	"  rixa manage <review|resume|status> --operation <id>\n" +
	"  rixa manage apply --operation <id> --review-token <from-review>\n" +
	"  rixa manage remove --operation <id> --confirm-remove\n" +
	"  rixa manage update <menu|draft|review|apply|resume|status|remove> [flags]\n" +
	"  rixa manage update --help\n" +
	"  rixa manage add-site inspect --source <private-json>\n" +
	"  rixa manage prerequisites --help\n\n" +
	"Direct commands emit safe JSON and never prompt. --non-interactive rejects the menu.\n" +
	"Draft/prefill JSON contains credentials: same-operator private regular files only.\n" +
	"Apply queues a fixed persistent operation; inspect status before claiming completion.\n" +
	"Ordinary removal retains configuration, credentials, databases, site data and prerequisites.\n"
