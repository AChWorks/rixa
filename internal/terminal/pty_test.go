// SPDX-License-Identifier: MPL-2.0

//go:build linux

package terminal

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/sys/unix"
	"golang.org/x/term"
)

type ptyCapture struct {
	master, slave *os.File
	command       *exec.Cmd
	mutex         sync.Mutex
	output        strings.Builder
	before        *term.State
	receipt       string
	done          chan error
}

func startPTY(t *testing.T, mode string) *ptyCapture {
	t.Helper()
	fd, e := unix.Open("/dev/ptmx", unix.O_RDWR|unix.O_NOCTTY|unix.O_CLOEXEC, 0)
	if e != nil {
		t.Fatal(e)
	}
	master := os.NewFile(uintptr(fd), "test-ptmx")
	if e = unix.IoctlSetPointerInt(fd, unix.TIOCSPTLCK, 0); e != nil {
		master.Close()
		t.Fatal(e)
	}
	number, e := unix.IoctlGetInt(fd, unix.TIOCGPTN)
	if e != nil {
		master.Close()
		t.Fatal(e)
	}
	slave, e := os.OpenFile(fmt.Sprintf("/dev/pts/%d", number), os.O_RDWR|unix.O_NOCTTY, 0)
	if e != nil {
		master.Close()
		t.Fatal(e)
	}
	before, e := term.GetState(int(slave.Fd()))
	if e != nil {
		t.Fatal(e)
	}
	receipt := filepath.Join(t.TempDir(), "receipt.json")
	cmd := exec.Command(os.Args[0], "-test.run=^TestTerminalPTYChild$")
	env := []string{}
	for _, v := range os.Environ() {
		if !strings.HasPrefix(v, "TERM=") && !strings.HasPrefix(v, "NO_COLOR=") && !strings.HasPrefix(v, "RIXA_TERMINAL_PTY_") {
			env = append(env, v)
		}
	}
	cmd.Env = append(env, "TERM=xterm-256color", "RIXA_TERMINAL_PTY_MODE="+mode, "RIXA_TERMINAL_PTY_RECEIPT="+receipt)
	if mode == "plain" {
		cmd.Env = append(cmd.Env, "TERM=dumb")
	}
	if mode == "no-color" {
		cmd.Env = append(cmd.Env, "NO_COLOR=1")
	}
	cmd.Stdin = slave
	cmd.Stdout = slave
	cmd.Stderr = slave
	cmd.SysProcAttr = &unix.SysProcAttr{Setsid: true, Setctty: true, Ctty: 0}
	if e = unix.IoctlSetWinsize(fd, unix.TIOCSWINSZ, &unix.Winsize{Col: 80, Row: 24}); e != nil {
		t.Fatal(e)
	}
	if e = cmd.Start(); e != nil {
		t.Fatal(e)
	}
	p := &ptyCapture{master: master, slave: slave, command: cmd, before: before, receipt: receipt, done: make(chan error, 1)}
	go func() { p.done <- cmd.Wait() }()
	go func() {
		b := make([]byte, 4096)
		for {
			n, e := master.Read(b)
			if n > 0 {
				p.mutex.Lock()
				p.output.Write(b[:n])
				p.mutex.Unlock()
			}
			if e != nil {
				return
			}
		}
	}()
	t.Cleanup(func() { _ = cmd.Process.Kill(); _ = slave.Close(); _ = master.Close() })
	return p
}
func (p *ptyCapture) text() string { p.mutex.Lock(); defer p.mutex.Unlock(); return p.output.String() }
func (p *ptyCapture) send(t *testing.T, v string) {
	t.Helper()
	if _, e := io.WriteString(p.master, v); e != nil {
		t.Fatal(e)
	}
}
func (p *ptyCapture) await(t *testing.T, after int, pattern string) int {
	t.Helper()
	deadline := time.NewTimer(12 * time.Second)
	defer deadline.Stop()
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		out := p.text()
		if after <= len(out) && strings.Contains(out[after:], pattern) {
			return len(out)
		}
		select {
		case <-deadline.C:
			t.Fatalf("PTY did not show %q; output tail %q", pattern, tail(out, 1800))
		case <-ticker.C:
		}
	}
}
func tail(s string, n int) string {
	if len(s) > n {
		return s[len(s)-n:]
	}
	return s
}
func (p *ptyCapture) finish(t *testing.T) map[string]any {
	t.Helper()
	select {
	case e := <-p.done:
		if e != nil {
			t.Fatalf("PTY child: %v; output=%q", e, tail(p.text(), 1200))
		}
	case <-time.After(8 * time.Second):
		t.Fatal("PTY child did not exit")
	}
	after, e := term.GetState(int(p.slave.Fd()))
	if e != nil {
		t.Fatal(e)
	}
	if !reflect.DeepEqual(p.before, after) {
		t.Fatal("terminal state was not restored")
	}
	b, e := os.ReadFile(p.receipt)
	if e != nil {
		t.Fatal(e)
	}
	var r map[string]any
	if e = json.Unmarshal(b, &r); e != nil {
		t.Fatal(e)
	}
	return r
}

func TestTerminalPTYCorrectionBackResumeResizeAndSecrets(t *testing.T) {
	p := startPTY(t, "full")
	position := p.await(t, 0, "1. Configure installation")
	p.send(t, "\r")
	position = p.await(t, position, "Control URL")
	p.send(t, "\x15http://invalid\r")
	position = p.await(t, position, "Use a distinct HTTPS")
	p.send(t, "\x15https://control.rixa.test:8443\r")
	position = p.await(t, position, "Site URL")
	backPosition := position
	p.send(t, "\x1b")
	p.await(t, backPosition, "Control URL")
	// A PTY read can end between the field label and its retained value.
	// Observe both in this Back rendering before sending the next input.
	position = p.await(t, backPosition, "https://control.rixa.test:8443")
	p.send(t, "\r")
	position = p.await(t, position, "Site URL")
	// All remaining values were restored privately. Enter keeps secret values.
	p.send(t, strings.Repeat("\r", 11))
	position = p.await(t, position, "temporarily unavailable")
	if strings.Contains(p.text(), "DB-CONTROL-CANARY") {
		t.Fatal("failed save exposed private DSN")
	}
	p.send(t, "\r")
	position = p.await(t, position, "Draft saved privately.")
	if e := unix.IoctlSetWinsize(int(p.master.Fd()), unix.TIOCSWINSZ, &unix.Winsize{Col: 30, Row: 8}); e != nil {
		t.Fatal(e)
	}
	position = p.await(t, position, "Terminal too small")
	p.send(t, "3") // hidden menu actions cannot run in an unusably small viewport.
	if e := unix.IoctlSetWinsize(int(p.master.Fd()), unix.TIOCSWINSZ, &unix.Winsize{Col: 100, Row: 30}); e != nil {
		t.Fatal(e)
	}
	position = p.await(t, position, "Server management")
	p.send(t, "2\r")
	position = p.await(t, position, "State: ready")
	p.send(t, "3\r")
	position = p.await(t, position, "Start reviewed installation?")
	p.send(t, "WRONG\r")
	position = p.await(t, position, "Confirmation did not match")
	p.send(t, "\x1b")
	position = p.await(t, position, "Server management")
	p.send(t, "3\r")
	position = p.await(t, position, "Start reviewed installation?")
	p.send(t, "APPLY\r")
	position = p.await(t, position, "State: applying")
	p.send(t, "\x03")
	receipt := p.finish(t)
	if receipt["review_token_used"] != "review-token-case" {
		t.Fatal("menu did not forward its exact reviewed token")
	}
	if receipt["saves"] != float64(2) || receipt["applies"] != float64(1) || receipt["removes"] != float64(0) {
		t.Fatalf("incorrect backend effects: %#v", receipt)
	}
	if receipt["control_origin"] != "https://control.rixa.test:8443" || receipt["site_origin"] != "https://site.rixa.test:8443" {
		t.Fatalf("valid fields lost: %#v", receipt)
	}
	for _, secret := range []string{"DB-CONTROL-CANARY", "DB-SITE-CANARY", "PASSWORD-CONTROL-CANARY", "PASSWORD-SITE-CANARY"} {
		if strings.Contains(p.text(), secret) {
			t.Fatalf("secret printed: %s", secret)
		}
	}
	if !strings.Contains(p.text(), "\x1b[?1049l") {
		t.Fatal("alternate screen was not restored")
	}
}
func TestTerminalPTYPlainAndNoColor(t *testing.T) {
	for _, mode := range []string{"plain", "no-color"} {
		t.Run(mode, func(t *testing.T) {
			p := startPTY(t, mode)
			p.await(t, 0, "Server management")
			p.send(t, "\x03")
			p.finish(t)
			out := p.text()
			if mode == "plain" && strings.Contains(out, "\x1b") {
				t.Fatalf("plain output emitted ANSI: %q", out)
			}
			if mode == "no-color" && strings.Contains(out, "\x1b[1;36m") {
				t.Fatal("NO_COLOR ignored")
			}
		})
	}
}
func TestTerminalPTYBracketedPasteCannotSubmitFields(t *testing.T) {
	p := startPTY(t, "full")
	position := p.await(t, 0, "1. Configure installation")
	p.send(t, "\r")
	position = p.await(t, position, "Control URL")
	p.send(t, "\x1b[200~https://evil.invalid\nSECRET-PASTE-CANARY\x1b[201~")
	position = p.await(t, position, "Multi-line/control-character paste")
	if strings.Contains(p.text(), "SECRET-PASTE-CANARY") {
		t.Fatal("rejected paste echoed")
	}
	p.send(t, "\x03")
	receipt := p.finish(t)
	if receipt["saves"] != float64(0) {
		t.Fatal("paste submitted a field")
	}
}
func TestTerminalPTYSignalRestoresState(t *testing.T) {
	p := startPTY(t, "full")
	p.await(t, 0, "Server management")
	if e := p.command.Process.Signal(unix.SIGTERM); e != nil {
		t.Fatal(e)
	}
	p.finish(t)
}

// This subprocess executes the actual raw terminal renderer/decoder and shared
// typed facade boundary. The fake host facade is not a host-installation proof.
func TestTerminalPTYChild(t *testing.T) {
	mode := os.Getenv("RIXA_TERMINAL_PTY_MODE")
	if mode == "" {
		return
	}
	if mode == "native-fields" {
		nativeFieldsPTYChild(t)
		return
	}
	if strings.HasPrefix(mode, "bootstrap-") {
		bootstrapPTYChild(t, mode)
		return
	}
	if strings.HasPrefix(mode, "postgres-") {
		postgresPTYChild(t, mode)
		return
	}
	if strings.HasPrefix(mode, "update-") {
		runUpdatePTYChild(t, mode)
		return
	}
	f := &testFacade{draft: testDraft(), firstSaveFailure: !strings.HasPrefix(mode, "fetch")}
	if strings.HasPrefix(mode, "web-locale-") {
		f.draft.Language = strings.TrimPrefix(mode, "web-locale-")
	}
	fetchChannels := []string{}
	f.ready = mode == "unsaved" || mode == "unreviewed"
	options := Options{Input: os.Stdin, Output: os.Stdout, OperationID: "pty-case", Version: "test", Plain: mode == "plain" || mode == "fetch-plain"}
	if strings.HasPrefix(mode, "fetch") {
		f.draft.ArchivePath = ""
		f.draft.BundlePath = ""
		f.draft.Release = ""
		f.draft.SourceCommit = ""
		options.FetchRelease = func(_ context.Context, tag, channel, dir string) (FetchedArtifact, error) {
			fetchChannels = append(fetchChannels, channel)
			if len(fetchChannels) == 1 {
				return FetchedArtifact{}, errors.New("stable-release-unavailable: no stable release exists; no fallback performed")
			}
			if tag != "" || channel != "development" || dir != "/tmp/explicit-second-folder" {
				return FetchedArtifact{}, errors.New("unexpected explicit selection")
			}
			return FetchedArtifact{ArchivePath: "/private/fetched.tar.gz", BundlePath: "/private/fetched.sigstore.json", ReleaseTag: "v0.2.0", SourceCommit: strings.Repeat("a", 40)}, nil
		}
	}
	e := Run(context.Background(), f, options)
	if e != nil && !errors.Is(e, context.Canceled) {
		t.Fatal(e)
	}
	receipt := map[string]any{"saves": f.saves, "applies": f.applies, "removes": f.removes, "control_origin": f.draft.ControlOrigin, "site_origin": f.draft.SiteOrigin, "fetch_channels": fetchChannels, "archive": f.draft.ArchivePath, "release": f.draft.Release, "source_commit": f.draft.SourceCommit, "review_token_used": f.token}
	b, _ := json.Marshal(receipt)
	if e = os.WriteFile(os.Getenv("RIXA_TERMINAL_PTY_RECEIPT"), b, 0600); e != nil {
		t.Fatal(e)
	}
}

func runFetchPTY(t *testing.T, mode string) {
	p := startPTY(t, mode)
	position := p.await(t, 0, "1. Configure installation")
	p.send(t, "\r")
	position = p.await(t, position, "Release selection")
	p.send(t, "\r")
	position = p.await(t, position, "New private download folder")
	p.send(t, "\r")
	position = p.await(t, position, "no fallback performed")
	p.send(t, "\x1b")
	position = p.await(t, position, "Release selection")
	p.send(t, "\x15development\r")
	position = p.await(t, position, "New private download folder")
	p.send(t, "\x15/tmp/explicit-second-folder\r")
	position = p.await(t, position, "Control URL")
	p.send(t, strings.Repeat("\r", 12))
	p.await(t, position, "Draft saved privately.")
	p.send(t, "\x03")
	receipt := p.finish(t)
	if mode == "fetch-plain" && strings.Contains(p.text(), "\x1b") {
		t.Fatal("plain download wizard emitted ANSI")
	}
	channels, ok := receipt["fetch_channels"].([]any)
	if !ok || len(channels) != 2 || channels[0] != "stable" || channels[1] != "development" {
		t.Fatalf("implicit fallback or lost selection: %#v", receipt)
	}
	if receipt["archive"] != "/private/fetched.tar.gz" || receipt["release"] != "v0.2.0" || receipt["source_commit"] != strings.Repeat("a", 40) {
		t.Fatalf("authenticated metadata not carried: %#v", receipt)
	}
}

func TestTerminalPTYUnsavedEditsCannotApplyPersistedIntent(t *testing.T) {
	p := startPTY(t, "unsaved")
	position := p.await(t, 0, "1. Configure installation")
	p.send(t, "\r")
	position = p.await(t, position, "Control URL")
	p.send(t, "\x15https://changed.rixa.test:8443")
	// Back all the way to the menu, preserving edits only in memory.
	p.send(t, strings.Repeat("\x1b", 5))
	position = p.await(t, position, "Unsaved edits:")
	out := p.text()
	if strings.Contains(lastScreen(out), "Apply reviewed installation") || strings.Contains(lastScreen(out), "Review prerequisites") {
		t.Fatal("unsaved edit exposed Apply")
	}
	p.send(t, "\x03")
	receipt := p.finish(t)
	if receipt["applies"] != float64(0) || receipt["saves"] != float64(0) {
		t.Fatalf("unsaved draft caused effects: %#v", receipt)
	}
	if receipt["control_origin"] != "https://control.rixa.test:8443" {
		t.Fatal("unsaved edit reached persisted backend draft")
	}
}
func TestTerminalPTYStatusCannotAdoptAnotherClientsReview(t *testing.T) {
	p := startPTY(t, "unreviewed")
	position := p.await(t, 0, "1. Configure installation")
	if strings.Contains(p.text(), "Apply reviewed installation") {
		t.Fatal("Status implicitly granted Apply")
	}
	p.send(t, "3\r")
	position = p.await(t, position, "Observed ready")
	if strings.Contains(lastScreen(p.text()), "Apply reviewed installation") {
		t.Fatal("Refresh adopted another client's review")
	}
	p.send(t, "\x03")
	receipt := p.finish(t)
	if receipt["applies"] != float64(0) {
		t.Fatal("unreviewed intent applied")
	}
}

func lastScreen(s string) string {
	if i := strings.LastIndex(s, "\x1b[H\x1b[2J"); i >= 0 {
		return s[i:]
	}
	return s
}

func TestTerminalPTYFetchRequiresExplicitChannelAndRetainsFailure(t *testing.T) {
	for _, mode := range []string{"fetch", "fetch-plain"} {
		t.Run(mode, func(t *testing.T) { runFetchPTY(t, mode) })
	}
}
func TestTerminalPTYSmallConfirmationKeepsPromptVisible(t *testing.T) {
	p := startPTY(t, "full")
	position := p.await(t, 0, "1. Configure installation")
	p.send(t, "2\r")
	position = p.await(t, position, "State: ready")
	if e := unix.IoctlSetWinsize(int(p.master.Fd()), unix.TIOCSWINSZ, &unix.Winsize{Col: 40, Row: 12}); e != nil {
		t.Fatal(e)
	}
	position = p.await(t, position, "Recorded release: v0.2.0")
	p.send(t, "3\r")
	position = p.await(t, position, "Type APPLY to confirm")
	p.send(t, "NO\r")
	position = p.await(t, position, "Confirmation did not match.")
	if !strings.Contains(lastScreen(p.text()), "Type APPLY to confirm") {
		t.Fatal("confirmation correction hid the required instruction")
	}
	p.send(t, "\x1b")
	position = p.await(t, position, "Server management")
	p.send(t, "\x03")
	receipt := p.finish(t)
	if receipt["applies"] != float64(0) {
		t.Fatal("small-screen correction applied an operation")
	}
}

func TestTerminalPTYUnsupportedKeysDoNotEditTextOrSecrets(t *testing.T) {
	p := startPTY(t, "full")
	position := p.await(t, 0, "1. Configure installation")
	p.send(t, "\r")
	position = p.await(t, position, "Control URL")
	unsupported := "\x1b[H\x1b[3~\x1bOF"
	p.send(t, unsupported+"\r")
	position = p.await(t, position, "Site URL")
	p.send(t, strings.Repeat("\r", 4))
	position = p.await(t, position, "Control database connection")
	p.send(t, unsupported+"\r")
	position = p.await(t, position, "Site database connection")
	p.send(t, strings.Repeat("\r", 2))
	position = p.await(t, position, "Control administrator password")
	p.send(t, unsupported+"\r")
	position = p.await(t, position, "Site administrator login")
	p.send(t, strings.Repeat("\r", 3))
	position = p.await(t, position, "temporarily unavailable")
	p.send(t, "\r")
	p.await(t, position, "Draft saved privately.")
	p.send(t, "\x03")
	receipt := p.finish(t)
	if receipt["saves"] != float64(2) || receipt["control_origin"] != "https://control.rixa.test:8443" {
		t.Fatalf("unsupported key modified retained inputs: %#v", receipt)
	}
}

func TestTerminalPTYDelayedPasteCannotLeaveMaskedPassword(t *testing.T) {
	p := startPTY(t, "full")
	position := p.await(t, 0, "1. Configure installation")
	p.send(t, "\r")
	position = p.await(t, position, "Control URL")
	p.send(t, strings.Repeat("\r", 8))
	position = p.await(t, position, "Control administrator password")
	started := time.Now()
	p.send(t, "\x1b[200~PRIVATE-PASTE-START-CANARY")
	position = p.await(t, position, "Paste rejected")
	if time.Since(started) < 3*time.Second {
		t.Fatal("PTY regression did not cross the actual paste timeout")
	}
	p.send(t, "LATE-PRIVATE-PASSWORD-CANARY\nVISIBLE-TAIL-CANARY\x1b[20")
	// Allow a bounded discard poll before the split closing marker arrives.
	timer := time.NewTimer(200 * time.Millisecond)
	<-timer.C
	if !strings.Contains(lastScreen(p.text()), "Control administrator password") {
		t.Fatal("timed-out paste advanced out of the masked field")
	}
	for _, secret := range []string{"PRIVATE-PASTE-START-CANARY", "LATE-PRIVATE-PASSWORD-CANARY", "VISIBLE-TAIL-CANARY"} {
		if strings.Contains(p.text(), secret) {
			t.Fatalf("delayed paste exposed %s", secret)
		}
	}
	p.send(t, "1~FRESH-INDEPENDENT-SECRET-123\r")
	p.await(t, position, "Site administrator login")
	p.send(t, "\x03")
	receipt := p.finish(t)
	if receipt["saves"] != float64(0) || receipt["applies"] != float64(0) {
		t.Fatal("delayed paste caused a persisted effect")
	}
	if strings.Contains(p.text(), "FRESH-INDEPENDENT-SECRET-123") {
		t.Fatal("fresh input was not kept masked")
	}
}

func TestTerminalPTYCanCancelWhileDiscardingOversizePaste(t *testing.T) {
	p := startPTY(t, "full")
	position := p.await(t, 0, "1. Configure installation")
	p.send(t, "\r")
	position = p.await(t, position, "Control URL")
	p.send(t, strings.Repeat("\r", 8))
	position = p.await(t, position, "Control administrator password")
	p.send(t, "\x1b[200~"+strings.Repeat("a", 8193))
	p.await(t, position, "Paste rejected")
	p.send(t, "\x03")
	receipt := p.finish(t)
	if receipt["saves"] != float64(0) || receipt["applies"] != float64(0) {
		t.Fatal("discard cancellation caused a persisted effect")
	}
}

func TestTerminalPTYRemainsEnglishForWebLocales(t *testing.T) {
	for _, locale := range []string{"fa", "de"} {
		t.Run(locale, func(t *testing.T) {
			p := startPTY(t, "web-locale-"+locale)
			position := p.await(t, 0, "Configure installation")
			p.send(t, "\x1bOP")
			position = p.await(t, position, "The terminal interface always uses English")
			p.send(t, "\r")
			p.await(t, position, "Server management")
			p.send(t, "\x03")
			receipt := p.finish(t)
			if receipt["saves"] != float64(0) || receipt["applies"] != float64(0) {
				t.Fatal("language-independent inspection changed installation state")
			}
		})
	}
}
