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
	input     *os.File
	output    io.Writer
	full      bool
	color     bool
	width     int
	height    int
	old       *term.State
	pending   []byte
	lastPaint string
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
		if len(s.pending) == 0 {
			p := []unix.PollFd{{Fd: int32(s.input.Fd()), Events: unix.POLLIN}}
			n, err := unix.Poll(p, 150)
			if err == unix.EINTR {
				continue
			}
			if err != nil {
				return key{}, err
			}
			if n == 0 {
				return key{name: "tick"}, nil
			}
			if p[0].Revents&(unix.POLLHUP|unix.POLLERR|unix.POLLNVAL) != 0 {
				return key{}, io.EOF
			}
			b := make([]byte, 256)
			n, err = unix.Read(int(s.input.Fd()), b)
			if err == unix.EINTR {
				continue
			}
			if err != nil {
				return key{}, err
			}
			if n == 0 {
				return key{}, io.EOF
			}
			s.pending = append(s.pending, b[:n]...)
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
			// A short bounded poll distinguishes Escape from an ANSI key sequence.
			if len(s.pending) == 1 {
				p := []unix.PollFd{{Fd: int32(s.input.Fd()), Events: unix.POLLIN}}
				if n, _ := unix.Poll(p, 40); n > 0 {
					b := make([]byte, 16)
					n, _ := unix.Read(int(s.input.Fd()), b)
					s.pending = append(s.pending, b[:n]...)
				}
			}
			if strings.HasPrefix(string(s.pending), "\x1b[200~") {
				s.pending = s.pending[6:]
				return s.readPaste(ctx)
			}

			for _, v := range []struct{ seq, name string }{{"\x1b[A", "up"}, {"\x1b[B", "down"}, {"\x1b[Z", "back"}, {"\x1bOP", "help"}, {"\x1b[11~", "help"}, {"\x1b[C", "next"}, {"\x1b[D", "back"}} {
				if strings.HasPrefix(string(s.pending), v.seq) {
					s.pending = s.pending[len(v.seq):]
					return key{name: v.name}, nil
				}
			}
			s.pending = s.pending[1:]
			// Discard unsupported escape controls rather than treating their bytes as data.
			if len(s.pending) > 0 && (s.pending[0] == '[' || s.pending[0] == 'O') {
				for len(s.pending) > 0 {
					c := s.pending[0]
					s.pending = s.pending[1:]
					if c >= 0x40 && c <= 0x7e {
						break
					}
				}
				continue
			}
			return key{name: "back"}, nil
		}
		if b < 32 {
			s.pending = s.pending[1:]
			continue
		}
		if !utf8.FullRune(s.pending) {
			// UTF-8 key bytes may arrive in separate reads.
			p := []unix.PollFd{{Fd: int32(s.input.Fd()), Events: unix.POLLIN}}
			if n, _ := unix.Poll(p, 100); n == 0 {
				s.pending = nil
				return key{name: "tick"}, nil
			}
			more := make([]byte, 8)
			n, err := unix.Read(int(s.input.Fd()), more)
			if err != nil {
				return key{}, err
			}
			s.pending = append(s.pending, more[:n]...)
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

// Bracketed paste is one bounded input event; embedded newlines cannot submit
// passwords or silently advance the wizard.
func (s *session) readPaste(ctx context.Context) (key, error) {
	b := append([]byte(nil), s.pending...)
	s.pending = nil
	deadline := time.Now().Add(3 * time.Second)
	overflow := false
	for {
		if i := strings.Index(string(b), "\x1b[201~"); i >= 0 {
			s.pending = append(s.pending, b[i+6:]...)
			if overflow {
				return key{name: "paste-invalid"}, nil
			}
			return key{name: "paste", text: string(b[:i])}, nil
		}
		if len(b) > 8192 {
			overflow = true
			b = append([]byte(nil), b[len(b)-6:]...)
		}
		if err := ctx.Err(); err != nil {
			return key{}, err
		}
		if time.Now().After(deadline) {
			return key{name: "paste-invalid"}, nil
		}
		p := []unix.PollFd{{Fd: int32(s.input.Fd()), Events: unix.POLLIN}}
		n, err := unix.Poll(p, 100)
		if err == unix.EINTR {
			continue
		}
		if err != nil {
			return key{}, err
		}
		if n == 0 {
			continue
		}
		more := make([]byte, 1024)
		n, err = unix.Read(int(s.input.Fd()), more)
		if err == unix.EINTR {
			continue
		}
		if err != nil {
			return key{}, err
		}
		if n == 0 {
			return key{}, io.EOF
		}
		b = append(b, more[:n]...)
	}
}
