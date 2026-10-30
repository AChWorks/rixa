// SPDX-License-Identifier: MPL-2.0

//go:build linux

package terminal

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"

	"golang.org/x/sys/unix"
)

func runUpdatePTYChild(t *testing.T, mode string) {
	f := &updateTestFacade{draft: updateTestDraft(), failSave: mode == "update-full"}
	f.ready = mode == "update-unreviewed"
	f.recovery = mode == "update-unknown"
	f.installed = mode == "update-remove"
	f.resumeAllowed = mode == "update-resume"
	options := UpdateOptions{Options: Options{Input: os.Stdin, Output: os.Stdout, OperationID: "pty-case", Version: "v0.4.0", Plain: mode == "update-plain", NoColor: mode == "update-no-color"}}
	channels := []string{}
	if mode == "update-fetch" {
		f.draft.ArchivePath = ""
		f.draft.BundlePath = ""
		f.draft.Release = ""
		f.draft.SourceCommit = ""
		options.FetchRelease = func(_ context.Context, tag, channel, dir string) (FetchedArtifact, error) {
			channels = append(channels, channel)
			if tag != "" || channel != "stable" || dir != "/tmp/rixa-pty-case-download" {
				return FetchedArtifact{}, errors.New("unexpected update artifact selection")
			}
			return FetchedArtifact{ArchivePath: "/private/authenticated-update.tar.gz", BundlePath: "/private/authenticated-update.tar.gz.sigstore.json", ReleaseTag: "v0.5.0", SourceCommit: strings.Repeat("b", 40)}, nil
		}
	}
	err := RunUpdate(context.Background(), f, options)
	if err != nil && !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	receipt := map[string]any{"saves": f.saves, "reviews": f.reviews, "applies": f.applies, "resumes": f.resumes, "removes": f.removes, "review_token_used": f.token, "installation": f.draft.InstallationOperation, "archive": f.draft.ArchivePath, "release": f.draft.Release, "source_commit": f.draft.SourceCommit, "fetch_channels": channels}
	body, _ := json.Marshal(receipt)
	if err := os.WriteFile(os.Getenv("RIXA_TERMINAL_PTY_RECEIPT"), body, 0600); err != nil {
		t.Fatal(err)
	}
}

func TestUpdatePTYConfigureCorrectionBackReviewAndExactApply(t *testing.T) {
	p := startPTY(t, "update-full")
	pos := p.await(t, 0, "1. Configure update")
	p.send(t, "\r")
	pos = p.await(t, pos, "Installation operation")
	p.send(t, "\x15INVALID\r")
	pos = p.await(t, pos, "[input]")
	p.send(t, "\x15owner-install\r")
	pos = p.await(t, pos, "Candidate release archive")
	p.send(t, "\x15/private/edited-candidate.tar.gz\x1b")
	pos = p.await(t, pos, "Installation operation")
	p.send(t, "\r")
	pos = p.await(t, pos, "/private/edited-candidate.tar.gz")
	p.send(t, strings.Repeat("\r", 4))
	pos = p.await(t, pos, "Update journal temporarily unavailable")
	p.send(t, "\r")
	pos = p.await(t, pos, "Draft saved privately.")
	if err := unix.IoctlSetWinsize(int(p.master.Fd()), unix.TIOCSWINSZ, &unix.Winsize{Col: 30, Row: 8}); err != nil {
		t.Fatal(err)
	}
	pos = p.await(t, pos, "Terminal too small")
	p.send(t, "3")
	if err := unix.IoctlSetWinsize(int(p.master.Fd()), unix.TIOCSWINSZ, &unix.Winsize{Col: 100, Row: 30}); err != nil {
		t.Fatal(err)
	}
	pos = p.await(t, pos, "Update management")
	p.send(t, "2\r")
	pos = p.await(t, pos, "State: ready")
	p.send(t, "3\r")
	pos = p.await(t, pos, "Start reviewed update?")
	p.send(t, "WRONG\r")
	pos = p.await(t, pos, "Confirmation did not match")
	p.send(t, "\x1b")
	pos = p.await(t, pos, "Update management")
	p.send(t, "3\r")
	pos = p.await(t, pos, "Start reviewed update?")
	p.send(t, "APPLY\r")
	p.await(t, pos, "State: applying")
	p.send(t, "\x03")
	r := p.finish(t)
	if r["saves"] != float64(2) || r["reviews"] != float64(1) || r["applies"] != float64(1) || r["removes"] != float64(0) || r["resumes"] != float64(0) || r["review_token_used"] != "exact-update-token" || r["installation"] != "owner-install" || r["archive"] != "/private/edited-candidate.tar.gz" {
		t.Fatalf("incorrect typed update effects: %#v", r)
	}
	out := p.text()
	for _, forbidden := range []string{"Control database connection", "administrator password", "TLS private key", "Web interface language"} {
		if strings.Contains(out, forbidden) {
			t.Fatalf("update recollected %s", forbidden)
		}
	}
	if !strings.Contains(out, "\x1b[?1049l") {
		t.Fatal("alternate screen not restored")
	}
}

func TestUpdatePTYPlainNoColorAndHelp(t *testing.T) {
	for _, mode := range []string{"update-plain", "update-no-color"} {
		t.Run(mode, func(t *testing.T) {
			p := startPTY(t, mode)
			pos := p.await(t, 0, "Update management")
			p.send(t, "\x1bOP")
			pos = p.await(t, pos, "uncertain effects require inspection")
			p.send(t, "\x1b")
			pos = p.await(t, pos, "Update management")
			p.send(t, "\r")
			pos = p.await(t, pos, "Installation operation")
			p.send(t, strings.Repeat("\r", 5))
			p.await(t, pos, "Draft saved privately.")
			p.send(t, "\x03")
			r := p.finish(t)
			if r["saves"] != float64(1) {
				t.Fatalf("plain/no-color form did not save: %#v", r)
			}
			if mode == "update-plain" && strings.Contains(p.text(), "\x1b") {
				t.Fatal("plain update emitted ANSI")
			}
			if mode == "update-no-color" && strings.Contains(p.text(), "\x1b[1;36m") {
				t.Fatal("no-color update emitted semantic color")
			}
		})
	}
}

func TestUpdatePTYUncertainAndForeignReviewCannotMutate(t *testing.T) {
	for _, mode := range []string{"update-unknown", "update-unreviewed"} {
		t.Run(mode, func(t *testing.T) {
			p := startPTY(t, mode)
			pos := p.await(t, 0, "Update management")
			screen := lastScreen(p.text())
			if strings.Contains(screen, "Apply reviewed update") || strings.Contains(screen, "Resume retained update") {
				t.Fatalf("unreviewed/uncertain effect offered: %s", screen)
			}
			if mode == "update-unreviewed" {
				p.send(t, "3\r")
				pos = p.await(t, pos, "Observed ready")
			} else {
				p.send(t, "\r")
				pos = p.await(t, pos, "Observed reconciliation-required")
			}
			if strings.Contains(lastScreen(p.text()), "Apply reviewed update") {
				t.Fatal("status adopted another client's review")
			}
			p.send(t, "\x03")
			r := p.finish(t)
			if r["applies"] != float64(0) || r["resumes"] != float64(0) || r["removes"] != float64(0) {
				t.Fatalf("inspection repeated effects: %#v", r)
			}
		})
	}
}

func TestUpdatePTYRetainedDataRemovalRequiresExactConfirmation(t *testing.T) {
	p := startPTY(t, "update-remove")
	pos := p.await(t, 0, "2. Remove executable and service")
	p.send(t, "2\r")
	pos = p.await(t, pos, "Keep configuration, credentials, databases")
	p.send(t, "\x1b")
	pos = p.await(t, pos, "Update management")
	p.send(t, "2\r")
	pos = p.await(t, pos, "Remove Rixa executable and service?")
	p.send(t, "REMOVE\r")
	p.await(t, pos, "State: removed")
	p.send(t, "\x03")
	r := p.finish(t)
	if r["removes"] != float64(1) || r["applies"] != float64(0) {
		t.Fatalf("incorrect removal effects: %#v", r)
	}
}

func TestUpdatePTYUsesExistingAuthenticatedFetchHook(t *testing.T) {
	p := startPTY(t, "update-fetch")
	pos := p.await(t, 0, "1. Configure update")
	p.send(t, "\r")
	pos = p.await(t, pos, "Release selection")
	p.send(t, "\r")
	pos = p.await(t, pos, "New private download folder")
	p.send(t, "\r")
	pos = p.await(t, pos, "Installation operation")
	p.send(t, strings.Repeat("\r", 5))
	p.await(t, pos, "Draft saved privately.")
	p.send(t, "\x03")
	r := p.finish(t)
	channels, ok := r["fetch_channels"].([]any)
	if !ok || len(channels) != 1 || channels[0] != "stable" || r["archive"] != "/private/authenticated-update.tar.gz" || r["source_commit"] != strings.Repeat("b", 40) {
		t.Fatalf("fetch policy/metadata drift: %#v", r)
	}
}

func TestUpdatePTYBackendAuthorizedResumeAndUnsavedFence(t *testing.T) {
	t.Run("safe-resume", func(t *testing.T) {
		p := startPTY(t, "update-resume")
		pos := p.await(t, 0, "1. Resume retained update")
		p.send(t, "\r")
		pos = p.await(t, pos, "Resume the retained update?")
		p.send(t, "RESUME\r")
		p.await(t, pos, "State: applying")
		p.send(t, "\x03")
		r := p.finish(t)
		if r["resumes"] != float64(1) || r["applies"] != float64(0) {
			t.Fatalf("incorrect resume: %#v", r)
		}
	})
	t.Run("unsaved-parent", func(t *testing.T) {
		p := startPTY(t, "update-unreviewed")
		pos := p.await(t, 0, "1. Configure update")
		p.send(t, "\r")
		pos = p.await(t, pos, "Installation operation")
		p.send(t, "\x15changed-owner\x1b")
		p.await(t, pos, "Unsaved edits:")
		screen := lastScreen(p.text())
		if strings.Contains(screen, "Review update") || strings.Contains(screen, "Apply reviewed update") {
			t.Fatalf("unsaved changes escaped fence: %s", screen)
		}
		p.send(t, "\x03")
		r := p.finish(t)
		if r["saves"] != float64(0) || r["applies"] != float64(0) || r["installation"] != "owner-install" {
			t.Fatalf("unsaved inputs reached backend: %#v", r)
		}
	})
}
