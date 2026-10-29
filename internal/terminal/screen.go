// SPDX-License-Identifier: MPL-2.0

// Package terminal presents the native Rixa operator facade. It owns no host
// effects or installation planning.
package terminal

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"golang.org/x/sys/unix"
	"golang.org/x/term"
)

var ErrTerminal = errors.New("interactive management needs a terminal input; use direct manage commands for automation")

type key struct {
	name string
	text string
}

type session struct {
	input         *os.File
	output        io.Writer
	full          bool
	color         bool
	width         int
	height        int
	old           *term.State
	pending       []byte
	escape        []byte
	inEscape      bool
	inPaste       bool
	pasteDiscard  bool
	paste         []byte
	pasteMatch    int
	pasteDeadline time.Time
	lastPaint     string
}

// SafeText makes untrusted values inert in the terminal. Bidi controls are
// displayed explicitly; ordinary UTF-8, including ZWNJ, is retained.
func SafeText(value string) string {
	var out strings.Builder
	for _, r := range value {
		if r == utf8.RuneError || unicode.IsControl(r) || r >= 0x202a && r <= 0x202e || r >= 0x2066 && r <= 0x2069 || r == 0x061c || r == 0x200e || r == 0x200f {
			fmt.Fprintf(&out, "\\u%04x", r)
		} else {
			out.WriteRune(r)
		}
		if out.Len() >= 4096 {
			out.WriteString("...")
			break
		}
	}
	return out.String()
}

func newSession(input *os.File, output io.Writer, plain, noColor bool) (*session, error) {
	if input == nil || !term.IsTerminal(int(input.Fd())) {
		return nil, ErrTerminal
	}
	s := &session{input: input, output: output, width: 80, height: 24}
	s.full = !plain && os.Getenv("TERM") != "" && os.Getenv("TERM") != "dumb"
	if f, ok := output.(*os.File); !ok || !term.IsTerminal(int(f.Fd())) {
		s.full = false
	}
	_, colorDisabled := os.LookupEnv("NO_COLOR")
	s.color = s.full && !noColor && !colorDisabled
	old, err := term.MakeRaw(int(input.Fd()))
	if err != nil {
		return nil, err
	}
	s.old = old
	raw, rawErr := unix.IoctlGetTermios(int(input.Fd()), unix.TCGETS)
	if rawErr == nil {
		raw.Lflag |= unix.ISIG
		raw.Cc[unix.VQUIT] = 0
		raw.Cc[unix.VSUSP] = 0
		rawErr = unix.IoctlSetTermios(int(input.Fd()), unix.TCSETS, raw)
	}
	if rawErr != nil {
		_ = term.Restore(int(input.Fd()), old)
		return nil, rawErr
	}
	if s.full {
		if _, err = io.WriteString(output, "\x1b[?1049h\x1b[?25l\x1b[?2004h"); err != nil {
			_ = term.Restore(int(input.Fd()), old)
			return nil, err
		}
	}
	s.resize()
	return s, nil
}
func (s *session) close() error {
	var err error
	if s.full {
		_, err = io.WriteString(s.output, "\x1b[0m\x1b[?2004l\x1b[?25h\x1b[?1049l")
	}
	if !s.full {
		_, err = io.WriteString(s.output, "\r\n")
	}
	return errors.Join(err, term.Restore(int(s.input.Fd()), s.old))
}
func (s *session) resize() bool {
	w, h, err := term.GetSize(int(s.input.Fd()))
	if err != nil || w <= 0 || h <= 0 {
		return false
	}
	changed := w != s.width || h != s.height
	s.width = w
	s.height = h
	return changed
}
func (s *session) readKey(ctx context.Context) (key, error) {
	for {
		if err := ctx.Err(); err != nil {
			return key{}, err
		}
		if s.inPaste {
			return s.readPaste(ctx)
		}
		if s.inEscape {
			return s.readEscape(ctx)
		}
		if len(s.pending) == 0 {
			available, err := s.readMore(ctx, 150*time.Millisecond)
			if err != nil {
				return key{}, err
			}
			if !available {
				return key{name: "tick"}, nil
			}
		}
		b := s.pending[0]
		switch b {
		case 3, 4:
			s.pending = s.pending[1:]
			return key{name: "quit"}, nil
		case '\r', '\n':
			s.pending = s.pending[1:]
			return key{name: "enter"}, nil
		case '\t':
			s.pending = s.pending[1:]
			return key{name: "next"}, nil
		case 21:
			s.pending = s.pending[1:]
			return key{name: "clear"}, nil
		case 127, 8:
			s.pending = s.pending[1:]
			return key{name: "backspace"}, nil
		case 27:
			s.pending = s.pending[1:]
			// Only a standalone Escape has the short ambiguity timeout. Once a
			// CSI/SS3 introducer arrives, its body remains control data across reads.
			if len(s.pending) == 0 {
				available, err := s.readMore(ctx, 40*time.Millisecond)
				if err != nil {
					return key{}, err
				}
				if !available {
					return key{name: "back"}, nil
				}
			}
			if s.pending[0] == '[' || s.pending[0] == 'O' {
				s.escape = []byte{27, s.pending[0]}
				s.inEscape = true
				s.pending = s.pending[1:]
				continue
			}
			return key{name: "back"}, nil
		}
		if b < 32 {
			s.pending = s.pending[1:]
			continue
		}
		if !utf8.FullRune(s.pending) {
			available, err := s.readMore(ctx, 100*time.Millisecond)
			if err != nil {
				return key{}, err
			}
			if !available {
				return key{name: "tick"}, nil
			}
			continue
		}
		r, n := utf8.DecodeRune(s.pending)
		s.pending = s.pending[n:]
		if r == utf8.RuneError || unicode.IsControl(r) {
			continue
		}
		return key{name: "text", text: string(r)}, nil
	}
}

// readMore bounds each wait and checks cancellation between short polls. All
// reads have one owner; no partial control/UTF-8 bytes are discarded on a tick.
func (s *session) readMore(ctx context.Context, wait time.Duration) (bool, error) {
	deadline := time.Now().Add(wait)
	for {
		if err := ctx.Err(); err != nil {
			return false, err
		}
		remaining := time.Until(deadline)
		if remaining <= 0 {
			return false, nil
		}
		millis := int((min(remaining, 25*time.Millisecond) + time.Millisecond - 1) / time.Millisecond)
		p := []unix.PollFd{{Fd: int32(s.input.Fd()), Events: unix.POLLIN}}
		n, err := unix.Poll(p, millis)
		if err == unix.EINTR {
			continue
		}
		if err != nil {
			return false, err
		}
		if n == 0 {
			continue
		}
		if p[0].Revents&unix.POLLIN == 0 {
			return false, io.EOF
		}
		b := make([]byte, 256)
		n, err = unix.Read(int(s.input.Fd()), b)
		if err == unix.EINTR {
			continue
		}
		if err != nil {
			return false, err
		}
		if n == 0 {
			return false, io.EOF
		}
		s.pending = append(s.pending, b[:n]...)
		return true, nil
	}
}

func (s *session) readEscape(ctx context.Context) (key, error) {
	deadline := time.Now().Add(150 * time.Millisecond)
	for {
		if err := ctx.Err(); err != nil {
			return key{}, err
		}
		remaining := time.Until(deadline)
		if remaining <= 0 {
			return key{name: "tick"}, nil
		}
		if len(s.pending) == 0 {
			available, err := s.readMore(ctx, remaining)
			if err != nil {
				return key{}, err
			}
			if !available {
				return key{name: "tick"}, nil
			}
		}
		b := s.pending[0]
		if b == 27 || b >= 0x80 {
			// A new Escape or ordinary UTF-8 terminates a malformed control without
			// swallowing the following independent key/text.
			s.inEscape = false
			s.escape = nil
			return key{name: "tick"}, nil
		}
		s.pending = s.pending[1:]
		if b == 3 || b == 4 {
			return key{name: "quit"}, nil
		}
		if s.escape != nil {
			if len(s.escape) < 64 {
				s.escape = append(s.escape, b)
			} else {
				s.escape = nil
			}
		}
		if b >= 0x40 && b <= 0x7e {
			sequence := string(s.escape)
			s.escape = nil
			s.inEscape = false
			switch sequence {
			case "\x1b[200~":
				s.inPaste = true
				s.pasteDiscard = false
				s.pasteMatch = 0
				s.paste = make([]byte, 0, 8192+len(pasteEnd))
				s.pasteDeadline = time.Now().Add(3 * time.Second)
				return s.readPaste(ctx)
			case "\x1b[A":
				return key{name: "up"}, nil
			case "\x1b[B":
				return key{name: "down"}, nil
			case "\x1b[Z", "\x1b[D":
				return key{name: "back"}, nil
			case "\x1bOP", "\x1b[11~":
				return key{name: "help"}, nil
			case "\x1b[C":
				return key{name: "next"}, nil
			}
			// Unsupported Home/Delete/modifier controls are consumed as a whole;
			// their parameter/final bytes must never become field or password text.
			return key{name: "tick"}, nil
		}
		if b < 0x20 || b > 0x3f {
			s.escape = nil
		}
	}
}

func (s *session) paint(title string, lines []string, footer string) error {
	fingerprint := fmt.Sprint(s.width, "/", s.height, "/", title, "/", strings.Join(lines, "\n"), "/", footer)
	if fingerprint == s.lastPaint {
		return nil
	}
	s.lastPaint = fingerprint
	if s.full {
		if _, err := io.WriteString(s.output, "\x1b[H\x1b[2J"); err != nil {
			return err
		}
		if s.width < 40 || s.height < 12 {
			_, err := io.WriteString(s.output, "Terminal too small. Resize to at least 40 x 12, or use --plain.\r\nCtrl-C exits safely.\r\n")
			return err
		}
	}
	emit := func(line string) error {
		// Deliberately avoid slicing UTF-8 bytes. Wrapping is bounded by each screen's
		// content; technical values remain inspectable in plain/direct output.
		if s.full && len([]rune(line)) > s.width-2 {
			line = string([]rune(line)[:s.width-5]) + "..."
		}
		if s.color {
			style := ""
			switch {
			case strings.HasPrefix(line, "> "):
				style = "\x1b[1;36m"
			case strings.HasPrefix(line, "[input]"):
				style = "\x1b[31m"
			case strings.HasPrefix(line, "Notice:") || strings.HasPrefix(line, "Warning") || strings.Contains(line, "State: blocked") || strings.Contains(line, "State: reconciliation-required"):
				style = "\x1b[33m"
			case strings.Contains(line, "State: installed"):
				style = "\x1b[32m"
			}
			if style != "" {
				line = style + line + "\x1b[0m"
			}
		}
		_, err := io.WriteString(s.output, line+"\r\n")
		return err
	}
	if s.color {
		title = "\x1b[1;36m" + title + "\x1b[0m"
	}
	if err := emit(title); err != nil {
		return err
	}
	max := len(lines)
	if s.full && max > s.height-4 {
		max = s.height - 4
	}
	for _, line := range lines[:max] {
		if err := emit(line); err != nil {
			return err
		}
	}
	return emit(footer)
}
func (s *session) wait(ctx context.Context) error {
	for {
		k, err := s.readKey(ctx)
		if err != nil {
			return err
		}
		if k.name == "quit" {
			return context.Canceled
		}
		if k.name == "enter" || k.name == "back" {
			return nil
		}
	}
}

// Bracketed paste is atomic until its closing marker. After timeout/overflow,
// retain only delimiter matching state: every remaining byte of that same paste
// stays quarantined, including newlines, across subsequent polling ticks.
const pasteEnd = "\x1b[201~"

func (s *session) rejectPaste() {
	clear(s.paste)
	s.paste = nil
	s.pasteDiscard = true
}
func (s *session) finishPaste() {
	clear(s.paste)
	s.paste = nil
	s.inPaste = false
	s.pasteDiscard = false
	s.pasteMatch = 0
	s.pasteDeadline = time.Time{}
}
func (s *session) readPaste(ctx context.Context) (key, error) {
	deadline := time.Now().Add(150 * time.Millisecond)
	for {
		if err := ctx.Err(); err != nil {
			s.rejectPaste()
			return key{}, err
		}
		now := time.Now()
		if !s.pasteDiscard && !now.Before(s.pasteDeadline) {
			s.rejectPaste()
			return key{name: "paste-invalid"}, nil
		}
		if !now.Before(deadline) {
			return key{name: "tick"}, nil
		}
		if len(s.pending) == 0 {
			wait := time.Until(deadline)
			if !s.pasteDiscard {
				wait = min(wait, time.Until(s.pasteDeadline))
			}
			available, err := s.readMore(ctx, wait)
			if err != nil {
				s.rejectPaste()
				return key{}, err
			}
			if !available {
				continue
			}
		}
		b := s.pending[0]
		s.pending = s.pending[1:]
		if s.pasteDiscard && (b == 3 || b == 4) {
			return key{name: "quit"}, nil
		}
		if b == pasteEnd[s.pasteMatch] {
			s.pasteMatch++
		} else if b == pasteEnd[0] {
			s.pasteMatch = 1
		} else {
			s.pasteMatch = 0
		}
		if !s.pasteDiscard {
			s.paste = append(s.paste, b)
		}
		if s.pasteMatch == len(pasteEnd) {
			if s.pasteDiscard {
				s.finishPaste()
				return key{name: "tick"}, nil
			}
			text := string(s.paste[:len(s.paste)-len(pasteEnd)])
			s.finishPaste()
			return key{name: "paste", text: text}, nil
		}
		// A possible delimiter prefix is not payload. This admits exactly 8192
		// payload bytes even when the closing marker arrives in separate reads.
		if !s.pasteDiscard && len(s.paste)-s.pasteMatch > 8192 {
			s.rejectPaste()
			return key{name: "paste-invalid"}, nil
		}
	}
}
