// SPDX-License-Identifier: MPL-2.0

package terminal

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"
	"time"
)

func awaitInvalidPaste(t *testing.T, s *session) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	for {
		k, e := s.readKey(ctx)
		if e != nil {
			t.Fatal(e)
		}
		if k.name == "paste-invalid" {
			return
		}
		if k.name != "tick" {
			t.Fatalf("incomplete paste emitted %#v", k)
		}
	}
}
func TestTerminalPasteTimeoutTailStaysQuarantined(t *testing.T) {
	s, w := keyDecoder(t, "\x1b[200~START-SECRET-CANARY")
	started := time.Now()
	awaitInvalidPaste(t, s)
	if time.Since(started) < 3*time.Second {
		t.Fatal("regression did not exercise the actual paste timeout")
	}
	if _, e := io.WriteString(w, "LATE-SECRET-CANARY\n\x1b[20"); e != nil {
		t.Fatal(e)
	}
	k, e := s.readKey(context.Background())
	if e != nil || k.name != "tick" || k.text != "" {
		t.Fatalf("timed-out paste tail became an ordinary key: %#v %v", k, e)
	}
	if _, e = io.WriteString(w, "1~x"); e != nil {
		t.Fatal(e)
	}
	assertDecoded(t, s, "text", "x")
}
func TestTerminalPasteOverflowDiscardsThroughSplitEndMarker(t *testing.T) {
	s, w := keyDecoder(t, "\x1b[200~"+strings.Repeat("a", 8193))
	awaitInvalidPaste(t, s)
	if _, e := io.WriteString(w, "SECRET\n\x1b[200~NESTED-SECRET\r\x1b[20"); e != nil {
		t.Fatal(e)
	}
	k, e := s.readKey(context.Background())
	if e != nil || k.name != "tick" || k.text != "" {
		t.Fatalf("oversize paste tail emitted %#v %v", k, e)
	}
	if _, e = io.WriteString(w, "1~x"); e != nil {
		t.Fatal(e)
	}
	assertDecoded(t, s, "text", "x")
}
func TestTerminalPasteDiscardStillAllowsCancellationAndEOF(t *testing.T) {
	t.Run("context", func(t *testing.T) {
		s, _ := keyDecoder(t, "\x1b[200~"+strings.Repeat("a", 8193))
		awaitInvalidPaste(t, s)
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		timer := time.AfterFunc(20*time.Millisecond, cancel)
		defer timer.Stop()
		started := time.Now()
		_, e := s.readKey(ctx)
		if !errors.Is(e, context.Canceled) || time.Since(started) > time.Second {
			t.Fatalf("discard ignored cancellation: %v", e)
		}
	})
	t.Run("quit", func(t *testing.T) {
		s, w := keyDecoder(t, "\x1b[200~"+strings.Repeat("a", 8193))
		awaitInvalidPaste(t, s)
		if _, e := io.WriteString(w, "\x03"); e != nil {
			t.Fatal(e)
		}
		assertDecoded(t, s, "quit", "")
	})
	t.Run("eof", func(t *testing.T) {
		s, w := keyDecoder(t, "\x1b[200~"+strings.Repeat("a", 8193))
		awaitInvalidPaste(t, s)
		if e := w.Close(); e != nil {
			t.Fatal(e)
		}
		if _, e := s.readKey(context.Background()); !errors.Is(e, io.EOF) {
			t.Fatalf("discard ignored EOF: %v", e)
		}
	})
}
func TestTerminalCompletePasteRemainsAtomicAtItsLimit(t *testing.T) {
	for _, text := range []string{"رمز خصوصی\nexact", strings.Repeat("a", 8192)} {
		s, _ := keyDecoder(t, "\x1b[200~"+text+"\x1b[201~x")
		assertDecoded(t, s, "paste", text)
		assertDecoded(t, s, "text", "x")
	}
}
