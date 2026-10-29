// SPDX-License-Identifier: MPL-2.0

package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"io"

	"github.com/AChWorks/rixa/internal/releaseartifact"
)

func runArtifact(ctx context.Context, args []string, output io.Writer) error {
	if len(args) != 0 && args[0] == "fetch" {
		return runArtifactFetch(ctx, args[1:], output)
	}
	if len(args) == 0 || args[0] != "verify" {
		return errors.New("usage: rixa artifact <verify|fetch>; verify --archive <path> --bundle <path> --release <tag> --source-commit <sha> [--sha256 <digest>]; fetch --output <new-private-directory> [--release <tag> | --channel stable|development]")
	}
	fs := flag.NewFlagSet("artifact verify", flag.ContinueOnError)
	archive := fs.String("archive", "", "downloaded archive; never extracted or executed")
	bundle := fs.String("bundle", "", "adjacent offline .sigstore.json provenance bundle")
	release := fs.String("release", "", "exact intended release tag")
	source := fs.String("source-commit", "", "independently selected full source commit SHA")
	digest := fs.String("sha256", "", "optional independently selected archive SHA-256")
	if err := fs.Parse(args[1:]); err != nil {
		return err
	}
	if fs.NArg() != 0 || *archive == "" || *bundle == "" || *release == "" || *source == "" {
		return errors.New("artifact verify requires --archive, --bundle, --release and --source-commit")
	}
	result, err := releaseartifact.Verify(ctx, releaseartifact.Request{ArchivePath: *archive, BundlePath: *bundle, ReleaseTag: *release, SourceCommit: *source, ExpectedSHA256: *digest})
	if encodeErr := json.NewEncoder(output).Encode(result); encodeErr != nil {
		return encodeErr
	}
	return err
}

func runArtifactFetch(ctx context.Context, args []string, output io.Writer) error {
	fs := flag.NewFlagSet("artifact fetch", flag.ContinueOnError)
	directory := fs.String("output", "", "new absolute private directory; existing directories are never modified")
	release := fs.String("release", "", "exact published release tag, mutually exclusive with --channel")
	channel := fs.String("channel", "", "stable (default) or development; no automatic prerelease fallback")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 0 || *directory == "" {
		return errors.New("artifact fetch requires --output and no positional arguments")
	}
	result, err := releaseartifact.Fetch(ctx, releaseartifact.FetchRequest{OutputDir: *directory, ReleaseTag: *release, Channel: *channel})
	if encodeErr := json.NewEncoder(output).Encode(result); encodeErr != nil {
		return encodeErr
	}
	return err
}
