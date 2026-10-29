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
	if len(args) == 0 || args[0] != "verify" {
		return errors.New("usage: rixa artifact verify --archive <path> --bundle <path> --release <tag> --source-commit <sha> [--sha256 <digest>]")
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
