// SPDX-License-Identifier: MPL-2.0

package terminal

import (
	"context"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/AChWorks/rixa/internal/serverinstall"
)

// FetchedArtifact is presentation metadata from the shared authenticated fetch
// callback. The adapter returns it only after actual publisher verification.
type FetchedArtifact struct{ ArchivePath, BundlePath, ReleaseTag, SourceCommit string }

func fetchRelease(ctx context.Context, s *session, fetch func(context.Context, string, string, string) (FetchedArtifact, error), d *serverinstall.Draft) (bool, error) {
	values := []string{"stable", filepath.Join("/tmp", "rixa-"+d.OperationID+"-download")}
	labels := []string{"Release selection", "New private download folder"}
	hints := []string{"stable, development, or an exact tag. Stable never falls back to a prerelease.", "A new absolute folder. Existing folders are never adopted or overwritten."}
	index := 0
	notice := ""
	firstRender := true
	for {
		if s.full || firstRender {
			if err := s.paint("RIXA / Download authenticated release", []string{
				fmt.Sprintf("Download · %d / 2", index+1), "", labels[index], hints[index], "", "> " + SafeText(values[index]), "", SafeText(notice),
			}, "Enter next/download · Escape use existing files · Ctrl-U clear · Ctrl-C exit"); err != nil {
				return false, err
			}
			firstRender = false
		}

		k, err := s.readKey(ctx)
		if err != nil {
			return false, err
		}
		if k.name == "quit" {
			return false, context.Canceled
		}
		if s.full && (s.width < 40 || s.height < 12) {
			s.resize()
			continue
		}
		switch k.name {
		case "tick":
			s.resize()
		case "back":
			if index == 0 {
				return false, nil
			}
			index--
			firstRender = true
		case "clear":
			values[index] = ""
			notice = ""
			firstRender = true
		case "backspace":
			if values[index] != "" {
				_, n := utf8.DecodeLastRuneInString(values[index])
				values[index] = values[index][:len(values[index])-n]
				if !s.full {
					if _, err = io.WriteString(s.output, "\r\nInput edited.\r\n"); err != nil {
						return false, err
					}
					firstRender = true
				}
			}
		case "text", "paste":
			valid := utf8.ValidString(k.text)
			for _, r := range k.text {
				if unicode.IsControl(r) {
					valid = false
				}
			}
			if !valid {
				notice = "Control characters or multi-line paste are not accepted. No value changed."
				firstRender = true
				continue
			}
			if len(values[index])+len(k.text) > 4096 {
				notice = "Input is limited to 4096 bytes."
				firstRender = true
				continue
			}
			values[index] += k.text
			if !s.full {
				if _, err = io.WriteString(s.output, SafeText(k.text)); err != nil {
					return false, err
				}
			}
		case "paste-invalid":
			notice = "Paste rejected. Ctrl-C exits. Discarding late input until paste ends; nothing changed."
			firstRender = true
		case "next", "enter":
			if values[index] == "" {
				notice = "A value is required."
				firstRender = true
				continue
			}
			if index == 0 {
				index = 1
				notice = ""
				firstRender = true
				continue
			}
			if !filepath.IsAbs(values[1]) || filepath.Clean(values[1]) != values[1] {
				notice = "Use an absolute clean path for a new private folder."
				firstRender = true
				continue
			}
			tag, channel := values[0], ""
			if tag == "stable" || tag == "development" {
				channel = tag
				tag = ""
			}
			if err := s.paint("RIXA / Authenticating download", []string{
				"Resolving the explicit release and checking publisher provenance.",
				"No candidate executable is run during download verification.",
				"A checksum alone is not publisher authentication.",
				"Ctrl-C cancels; a partial new folder may remain for inspection.",
			}, "Waiting for the shared artifact verifier..."); err != nil {
				return false, err
			}
			result, fetchErr := fetch(ctx, tag, channel, values[1])
			if fetchErr != nil {
				if errors.Is(fetchErr, context.Canceled) || ctx.Err() != nil {
					return false, context.Canceled
				}
				notice = publicError(fetchErr, *d) + " Choose a new folder before another download attempt."
				firstRender = true
				continue
			}
			if result.ArchivePath == "" || result.BundlePath == "" || result.ReleaseTag == "" || result.SourceCommit == "" {
				notice = "Download returned incomplete authenticated metadata; no draft fields changed."
				firstRender = true
				continue
			}
			d.ArchivePath = result.ArchivePath
			d.BundlePath = result.BundlePath
			d.Release = result.ReleaseTag
			d.SourceCommit = result.SourceCommit
			return true, nil
		}
	}
}

// Metadata supplied by an authenticated fetch/prefill is reviewable with Back
// without asking the operator to retype it during ordinary configuration.
func initialField(fields []field, d serverinstall.Draft) int {
	metadata := map[string]bool{"archive": true, "bundle": true, "release": true, "source_commit": true}
	for _, p := range serverinstall.ValidateDraft(d) {
		if metadata[p.Field] {
			for i, f := range fields {
				if f.key == p.Field {
					return i
				}
			}
		}
	}
	if strings.TrimSpace(d.ArchivePath) != "" && d.BundlePath != "" && d.Release != "" && d.SourceCommit != "" {
		return 4
	}
	return 0
}
