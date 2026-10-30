// SPDX-License-Identifier: MPL-2.0
package serverinstall

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"strings"
	"testing"
)

func TestBoundedOutputCopyAndWriteStringCannotBypassLimit(t *testing.T) {
	for name, write := range map[string]func(io.Writer, string) error{
		"write":        func(w io.Writer, s string) error { _, e := w.Write([]byte(s)); return e },
		"write-string": func(w io.Writer, s string) error { _, e := io.WriteString(w, s); return e },
		"copy": func(w io.Writer, s string) error {
			// Hide WriterTo to exercise io.Copy's destination ReaderFrom path.
			_, e := io.Copy(w, struct{ io.Reader }{strings.NewReader(s)})
			return e
		},
	} {
		t.Run(name, func(t *testing.T) {
			var output boundedOutput
			if _, bypass := any(&output).(io.ReaderFrom); bypass {
				t.Fatal("bounded output exposes a copy bypass")
			}
			if _, bypass := any(&output).(io.StringWriter); bypass {
				t.Fatal("bounded output exposes a string-write bypass")
			}
			prefix := "native failure context\n"
			if _, e := output.Write([]byte(prefix)); e != nil {
				t.Fatal(e)
			}
			if e := write(&output, strings.Repeat("x", (64<<10)+1)); e == nil || !output.exceeded {
				t.Fatal("child output overflow was accepted")
			}
			if output.Len() > 64<<10 || !bytes.HasPrefix(output.Bytes(), []byte(prefix)) || output.String() != string(output.Bytes()) {
				t.Fatal("output exceeded its memory bound or lost accepted diagnostic bytes")
			}
		})
	}
}

func TestBoundedOutputAllowsExactLimitAndRetainsAcceptedBytes(t *testing.T) {
	var output boundedOutput
	expected := strings.Repeat("x", 64<<10)
	n, e := io.WriteString(&output, expected)
	if e != nil || n != len(expected) || output.Len() != len(expected) || output.exceeded {
		t.Fatal("valid exact-bound output was rejected")
	}
	n, e = output.Write([]byte("y"))
	if n != 0 || e == nil || !output.exceeded || output.String() != expected {
		t.Fatal("overflow changed already accepted output or lost exceeded state")
	}
}

func TestNativeCommandsBoundActualChildOutput(t *testing.T) {
	for _, command := range []string{"fixed", "postgres"} {
		for _, stream := range []string{"stdout", "stderr"} {
			t.Run(command+"-"+stream, func(t *testing.T) {
				args := []string{"-test.run=^TestBoundedOutputChild$", "--", "bounded-output-child", stream}
				var output []byte
				var e error
				if command == "fixed" {
					output, e = fixedCommand(context.Background(), os.Args[0], args...)
				} else {
					output, e = postgresCommand(context.Background(), os.Args[0], args, nil)
				}
				if !errors.Is(e, ErrPrerequisite) || len(output) > 64<<10 || !bytes.Contains(output, []byte("fixture native bounded output context")) {
					t.Fatalf("native child output lost its bound/failure evidence: bytes=%d err=%v", len(output), e)
				}
			})
		}
	}
}

func TestBoundedOutputChild(t *testing.T) {
	for i, arg := range os.Args {
		if arg != "bounded-output-child" {
			continue
		}
		writer := os.Stdout
		if i+1 < len(os.Args) && os.Args[i+1] == "stderr" {
			writer = os.Stderr
		}
		_, _ = writer.WriteString("fixture native bounded output context\n")
		chunk := []byte(strings.Repeat("x", 32<<10))
		for n := 0; n < 4; n++ {
			if _, e := writer.Write(chunk); e != nil {
				break
			}
		}
		os.Exit(0)
	}
}
