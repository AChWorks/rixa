// SPDX-License-Identifier: MPL-2.0
package serverinstall

import (
	"errors"
	"strings"
	"testing"
	"time"
)

func TestNativeServiceReadinessWindowPreservesSingleSite(t *testing.T) {
	single := validDraft()
	if got := nativeServiceReadinessWindow(single); got != 30*time.Second {
		t.Fatalf("single-site readiness window changed: %s", got)
	}
	both := validTwoSiteDraft()
	if got := nativeServiceReadinessWindow(both); got != 90*time.Second {
		t.Fatalf("three-origin initial installation window: %s", got)
	}
}

func TestRuntimeDatabaseBindingFailureIsUsefulAndSecretFree(t *testing.T) {
	const secret = "postgres://role:private-secret@127.0.0.1:5432/db"
	want := map[string]string{"control": "system:123"}
	for _, tt := range []struct {
		name    string
		got     map[string]string
		probe   error
		message string
		success bool
	}{
		{name: "same-target", got: map[string]string{"control": "system:123"}, success: true},
		{name: "probe-unavailable", probe: errors.New(secret), message: "observation unavailable"},
		{name: "physical-target-drift", got: map[string]string{"control": "system:999"}, message: "binding mismatch"},
		{name: "missing-target", got: map[string]string{}, message: "binding mismatch"},
		{name: "empty-expected", got: map[string]string{}, message: "binding mismatch"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			expected := want
			if tt.name == "empty-expected" {
				expected = map[string]string{}
			}
			err := runtimeDatabaseBindingError(tt.got, expected, tt.probe)
			if tt.success {
				if err != nil {
					t.Fatalf("unchanged physical DB rejected: %v", err)
				}
				return
			}
			if !errors.Is(err, ErrState) || !strings.Contains(err.Error(), tt.message) || strings.Contains(err.Error(), secret) {
				t.Fatalf("unsafe or indistinct diagnosis: %v", err)
			}
		})
	}
}

func TestNativeSystemdPostgreSQLOrderingIsPassive(t *testing.T) {
	for _, d := range []Draft{validDraft(), validTwoSiteDraft()} {
		j := &journal{Draft: d, ArchiveSHA256: strings.Repeat("a", 64)}
		unit := string(New().serviceBytes(j))
		if !strings.Contains(unit, "\nAfter=network-online.target postgresql.service\n") ||
			!strings.Contains(unit, "\nWants=network-online.target\n") ||
			strings.Contains(unit, "Wants=postgresql.service") || strings.Contains(unit, "Requires=postgresql.service") {
			t.Fatalf("native service unit must passively order after Debian PostgreSQL without activating or requiring it")
		}
		if !strings.Contains(unit, "\nStartLimitBurst=3\n") || !strings.Contains(unit, "\nRestart=on-failure\n") {
			t.Fatal("native service startup failure policy was silently weakened")
		}
	}
}
