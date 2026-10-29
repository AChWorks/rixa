// SPDX-License-Identifier: MPL-2.0

package terminal

import (
	"context"
	"errors"
	"io"
	"os"
	"strings"
	"testing"
	"time"
)

func keyDecoder(t *testing.T, pending string) (*session, *os.File) {
	t.Helper()
	r, w, e := os.Pipe()
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { _ = r.Close(); _ = w.Close() })
	return &session{input: r, pending: []byte(pending)}, w
}
func nextDecoded(t *testing.T, s *session) key {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	for {
		k, e := s.readKey(ctx)
		if e != nil {
			t.Fatal(e)
		}
		if k.name != "tick" {
			return k
		}
	}
}
func assertDecoded(t *testing.T, s *session, name, text string) {
	t.Helper()
	k := nextDecoded(t, s)
	if k.name != name || k.text != text {
		t.Fatalf("decoded %#v; want name=%q text=%q", k, name, text)
	}
}
func writeChunks(w *os.File, chunks []string) <-chan error {
	done := make(chan error, 1)
	go func() {
		for _, chunk := range chunks {
			// Distinct writes exercise the actual polling/read boundary.
			timer := time.NewTimer(2 * time.Millisecond)
			<-timer.C
			if _, e := io.WriteString(w, chunk); e != nil {
				done <- e
				return
			}
		}
		done <- nil
	}()
	return done
}
func TestTerminalKeyUnsupportedSequencesDoNotBecomeText(t *testing.T) {
	for _, sequence := range []string{"\x1b[H", "\x1b[F", "\x1b[3~", "\x1bOH", "\x1bOF", "\x1b[1;2H", "\x1b[ q", "\x1bO1;2P"} {
		t.Run(sequence, func(t *testing.T) { s, _ := keyDecoder(t, sequence+"x"); assertDecoded(t, s, "text", "x") })
	}
}
func TestTerminalKeySupportedControlsAndLiteralText(t *testing.T) {
	for _, v := range []struct{ sequence, name string }{{"\x1b[A", "up"}, {"\x1b[B", "down"}, {"\x1b[C", "next"}, {"\x1b[D", "back"}, {"\x1b[Z", "back"}, {"\x1bOP", "help"}, {"\x1b[11~", "help"}, {"\t", "next"}, {"\r", "enter"}, {"\x15", "clear"}, {"\x7f", "backspace"}} {
		t.Run(v.name+v.sequence, func(t *testing.T) {
			s, _ := keyDecoder(t, v.sequence+"x")
			assertDecoded(t, s, v.name, "")
			assertDecoded(t, s, "text", "x")
		})
	}
	s, _ := keyDecoder(t, "[Oف")
	assertDecoded(t, s, "text", "[")
	assertDecoded(t, s, "text", "O")
	assertDecoded(t, s, "text", "ف")
	s, _ = keyDecoder(t, "\x1bx")
	assertDecoded(t, s, "back", "")
	assertDecoded(t, s, "text", "x")
}
func TestTerminalKeyFragmentedSequencesKeepTheirIdentity(t *testing.T) {
	for _, v := range []struct {
		prefix string
		chunks []string
		name   string
	}{
		{"\x1b", []string{"[", "A"}, "up"},
		{"\x1b[", []string{"1", "1", "~"}, "help"},
		{"\x1bO", []string{"P"}, "help"},
		{"\x1b[", []string{"H"}, ""},
		{"\x1b[", []string{"3", "~"}, ""},
		{"\x1bO", []string{"1;", "2F"}, ""},
	} {
		t.Run(v.prefix+strings.Join(v.chunks, ""), func(t *testing.T) {
			s, w := keyDecoder(t, v.prefix)
			chunks := append(append([]string{}, v.chunks...), "x")
			done := writeChunks(w, chunks)
			if v.name != "" {
				assertDecoded(t, s, v.name, "")
			}
			assertDecoded(t, s, "text", "x")
			if e := <-done; e != nil {
				t.Fatal(e)
			}
		})
	}
}
func TestTerminalKeyIncompleteControlSurvivesPollingTick(t *testing.T) {
	for _, prefix := range []string{"\x1b[1;", "\x1b[" + strings.Repeat("1", 512)} {
		s, w := keyDecoder(t, prefix)
		started := time.Now()
		k, e := s.readKey(context.Background())
		if e != nil || k.name != "tick" || k.text != "" {
			t.Fatalf("incomplete control emitted a key: %#v %v", k, e)
		}
		if time.Since(started) > time.Second {
			t.Fatal("incomplete control blocked past its polling bound")
		}
		if _, e = io.WriteString(w, "~x"); e != nil {
			t.Fatal(e)
		}
		assertDecoded(t, s, "text", "x")
	}
}
func TestTerminalKeyStandaloneEscapeAndCanceledPartialControl(t *testing.T) {
	s, _ := keyDecoder(t, "\x1b")
	started := time.Now()
	assertDecoded(t, s, "back", "")
	if time.Since(started) > 500*time.Millisecond {
		t.Fatal("standalone Escape no longer has a short bound")
	}
	s, _ = keyDecoder(t, "\x1b[1;\x1bx")
	assertDecoded(t, s, "back", "")
	assertDecoded(t, s, "text", "x")
	s, _ = keyDecoder(t, "\x1b[")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	timer := time.AfterFunc(20*time.Millisecond, cancel)
	defer timer.Stop()
	started = time.Now()
	_, e := s.readKey(ctx)
	if !errors.Is(e, context.Canceled) {
		t.Fatalf("partial control ignored cancellation: %v", e)
	}
	if time.Since(started) > time.Second {
		t.Fatal("partial control cancellation was not bounded")
	}
}
func TestTerminalKeySplitUTF8SurvivesPollingTick(t *testing.T) {
	text := []byte("ف")
	s, w := keyDecoder(t, string(text[:1]))
	k, e := s.readKey(context.Background())
	if e != nil || k.name != "tick" {
		t.Fatalf("incomplete UTF-8: %#v %v", k, e)
	}
	if _, e = io.WriteString(w, string(text[1:])+"x"); e != nil {
		t.Fatal(e)
	}
	assertDecoded(t, s, "text", "ف")
	assertDecoded(t, s, "text", "x")
}
func TestTerminalKeyFragmentedPastePrefixCannotSubmitFields(t *testing.T) {
	s, w := keyDecoder(t, "\x1b[")
	done := writeChunks(w, []string{"2", "00~", "private\nvalue", "\x1b[20", "1~", "x"})
	assertDecoded(t, s, "paste", "private\nvalue")
	assertDecoded(t, s, "text", "x")
	if e := <-done; e != nil {
		t.Fatal(e)
	}
}
func TestTerminalKeyOversizePasteKeepsItsGuard(t *testing.T) {
	s, _ := keyDecoder(t, "\x1b[200~"+strings.Repeat("a", 8193)+"\x1b[201~x")
	assertDecoded(t, s, "paste-invalid", "")
	assertDecoded(t, s, "text", "x")
}
