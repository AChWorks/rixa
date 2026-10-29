// SPDX-License-Identifier: MPL-2.0
package serverinstall

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestExecutorRequiresAcknowledgedQuiescentObservation(t *testing.T) {
	for _, tc := range []struct {
		name, observation string
		ack               bool
		commandError      error
		want              bool
	}{
		{"unknown-start", "LoadState=not-found\nActiveState=inactive\nMainPID=0\nControlGroup=\n", false, ErrPrerequisite, false},
		{"active", "LoadState=loaded\nActiveState=active\nMainPID=123\nControlGroup=/system.slice/rixa-install-first-install-1.service\n", true, nil, false},
		{"collected-known", "LoadState=not-found\nActiveState=inactive\nMainPID=0\nControlGroup=\n", true, ErrPrerequisite, true},
		{"transport-unproved", "LoadState=not-found\nActiveState=inactive\nMainPID=0\nControlGroup=\n", true, context.DeadlineExceeded, false},
		{"missing-pid", "LoadState=not-found\nActiveState=inactive\nControlGroup=\n", true, nil, false},
		{"foreign-cgroup", "LoadState=loaded\nActiveState=failed\nMainPID=0\nControlGroup=/foreign.slice/other.service\n", true, nil, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := testManager(t)
			j := reviewedJournal(validDraft())
			j.ExecutorGeneration = 1
			j.ExecutorAcknowledged = tc.ack
			m.run = func(context.Context, string, ...string) ([]byte, error) {
				return []byte(tc.observation), tc.commandError
			}
			if got := m.executorFenced(context.Background(), j); got != tc.want {
				t.Fatalf("fence=%t want=%t", got, tc.want)
			}
		})
	}
}
func TestLoadedServiceRefusesOverridesAndForeignLiveExecutable(t *testing.T) {
	for _, tc := range []struct {
		name, observation string
		want              bool
	}{
		{"owned-inactive", "FragmentPath=" + unitPath + "\nDropInPaths=\nActiveState=inactive\nMainPID=0\n", true},
		{"foreign-fragment", "FragmentPath=/usr/lib/systemd/system/other.service\nDropInPaths=\nActiveState=inactive\nMainPID=0\n", false},
		{"drop-in", "FragmentPath=" + unitPath + "\nDropInPaths=/etc/systemd/system/rixa.service.d/override.conf\nActiveState=active\nMainPID=1\n", false},
		{"foreign-live", "FragmentPath=" + unitPath + "\nDropInPaths=\nActiveState=active\nMainPID=" + fmt.Sprint(os.Getpid()) + "\n", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := testManager(t)
			j := reviewedJournal(validDraft())
			m.run = func(context.Context, string, ...string) ([]byte, error) { return []byte(tc.observation), nil }
			e := m.loadedServiceOwned(context.Background(), j, false)
			if (e == nil) != tc.want {
				t.Fatalf("ownership err=%v want=%t", e, tc.want)
			}
		})
	}
}
func TestFixedCommandKeepsBoundedFailureEvidence(t *testing.T) {
	out, e := fixedCommand(context.Background(), os.Args[0], "-test.run=^TestCommandDiagnosticChild$", "--", "native-diag-child")
	if !errors.Is(e, ErrPrerequisite) || !strings.Contains(string(out), "fixture provider diagnostic") || !strings.Contains(e.Error(), "fixture provider diagnostic") {
		t.Fatalf("failure context missing: %q %v", out, e)
	}
}
func TestCommandDiagnosticChild(t *testing.T) {
	for _, arg := range os.Args {
		if arg == "native-diag-child" {
			_, _ = os.Stderr.WriteString("fixture provider diagnostic\n")
			os.Exit(42)
		}
	}
}

func TestExecutingImageBindingRejectsPathReplacement(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "manager")
	if e := os.WriteFile(path, []byte("original executing image"), 0600); e != nil {
		t.Fatal(e)
	}
	image, e := os.Open(path)
	if e != nil {
		t.Fatal(e)
	}
	defer image.Close()
	b, e := readBoundImage(image, path)
	if e != nil || string(b) != "original executing image" {
		t.Fatal("same image rejected")
	}
	_, _ = image.Seek(0, 0)
	replacement := filepath.Join(dir, "new")
	if e = os.WriteFile(replacement, []byte("replaced pathname image"), 0600); e != nil {
		t.Fatal(e)
	}
	if e = os.Rename(replacement, path); e != nil {
		t.Fatal(e)
	}
	if _, e = readBoundImage(image, path); e == nil {
		t.Fatal("new pathname substituted for original executing image")
	}
}
