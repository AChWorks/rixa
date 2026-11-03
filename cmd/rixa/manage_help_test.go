// SPDX-License-Identifier: MPL-2.0
package main

import (
	"context"
	"io"
	"os"
	"strings"
	"testing"
)

func TestNativeManageHelpAvailableWithoutEffectAuthorization(t *testing.T) {
	for _, tc := range []struct {
		name string
		args []string
		want string
	}{
		{"native", []string{"--help"}, manageHelp},
		{"native-short", []string{"-h"}, manageHelp},
		{"native-action", []string{"remove", "--help"}, manageHelp},
		{"add-site", []string{"add-site", "--help"}, manageAddSiteHelp},
		{"add-site-action", []string{"add-site", "resume", "-h"}, manageAddSiteHelp},
		{"update", []string{"update", "--help"}, manageUpdateHelp},
		{"update-action", []string{"update", "apply", "--help"}, manageUpdateHelp},
		{"prerequisites", []string{"prerequisites", "--help"}, managePostgresHelp},
		{"prerequisites-action", []string{"prerequisites", "review", "-h"}, managePostgresHelp},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if doc, ok := staticManageHelp(tc.args); !ok || doc != tc.want {
				t.Fatalf("help selection was not the canonical static text: %q %t", doc, ok)
			}
			out, err := os.CreateTemp(t.TempDir(), "manage-help-")
			if err != nil {
				t.Fatal(err)
			}
			defer out.Close()
			if err := runManage(context.Background(), tc.args, os.Stdin, out); err != nil {
				t.Fatalf("read-only operator help required authority: %v", err)
			}
			if _, err := out.Seek(0, io.SeekStart); err != nil {
				t.Fatal(err)
			}
			b, err := io.ReadAll(out)
			if err != nil || string(b) != tc.want {
				t.Fatalf("help output mismatch: %v %q", err, string(b))
			}
		})
	}
}

func TestNativeManageHelpCannotMaskEffectBearingArguments(t *testing.T) {
	for _, args := range [][]string{
		nil,
		{"status", "--operation", "case", "--help"},
		{"add-site", "apply", "--operation", "case", "--help"},
		{"add-site", "inspect", "--source", "/private/draft.json", "--help"},
		{"add-site", "invented", "--help"},
		{"update", "remove", "--confirm-remove", "--help"},
		{"prerequisites", "invented", "--help"},
		{"internal-add-site-apply", "--help"},
		{"--non-interactive", "--help"},
	} {
		if doc, ok := staticManageHelp(args); ok || doc != "" {
			t.Fatalf("unsupported or effect-bearing argument interpreted as public help: %q", strings.Join(args, " "))
		}
	}
}
