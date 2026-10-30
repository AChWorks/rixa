// SPDX-License-Identifier: MPL-2.0

package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/AChWorks/rixa/internal/serverinstall"
)

type bootstrapTestFacade struct {
	calls    []string
	request  serverinstall.BootstrapRequest
	checkErr error
}

func (f *bootstrapTestFacade) DiscoverBootstrap(context.Context) (serverinstall.BootstrapDiscovery, error) {
	f.calls = append(f.calls, "inspect")
	return serverinstall.BootstrapDiscovery{Schema: "rixa.native-bootstrap.v1", State: "blocked", Code: "inspection-required", Message: "Inspect the exact retained operation.", Actions: []string{"status"}}, serverinstall.ErrState
}
func (f *bootstrapTestFacade) PrepareBootstrap(_ context.Context, r serverinstall.BootstrapRequest) (serverinstall.BootstrapPrepared, error) {
	f.calls = append(f.calls, "prepare")
	f.request = r
	return serverinstall.BootstrapPrepared{Schema: "rixa.native-bootstrap.v1", State: "prepared", Code: "manager-prepared", Route: bootstrapTestRoute()}, nil
}
func bootstrapTestRoute() serverinstall.BootstrapRoute {
	return serverinstall.BootstrapRoute{ManagerPath: "/private/exact-retained-manager", ManagerSHA256: strings.Repeat("a", 64), OperationKind: "update", OperationID: "retained-update", InstallationOperation: "original-install", Arguments: []string{"manage", "update", "menu", "--operation", "retained-update"}}
}
func (f *bootstrapTestFacade) OpenBootstrap(context.Context, string) (serverinstall.BootstrapRoute, error) {
	f.calls = append(f.calls, "route")
	return bootstrapTestRoute(), nil
}
func (f *bootstrapTestFacade) CheckBootstrapRoute(context.Context, serverinstall.BootstrapRoute) error {
	f.calls = append(f.calls, "check")
	return f.checkErr
}
func TestBootstrapDirectCommandsNeverOpenTTYOrLaunch(t *testing.T) {
	for _, args := range [][]string{{"inspect", "--non-interactive"}, {"route", "--operation", "retained-update"}, {"prepare", "--channel", "development", "--operation", "new-update", "--update"}, {"prepare", "--release", "v1.2.3", "--postgres", "retained-pg"}} {
		f := &bootstrapTestFacade{}
		var out strings.Builder
		launch := func(context.Context, serverinstall.BootstrapRoute, *os.File, io.Writer) error {
			t.Fatal("direct command launched a manager")
			return nil
		}
		tty := func() (*os.File, error) {
			t.Fatal("direct command opened controlling terminal")
			return nil, errors.New("unreachable")
		}
		e := bootstrapWithFacade(context.Background(), args, nil, &out, f, launch, tty)
		if args[0] == "inspect" {
			if !errors.Is(e, serverinstall.ErrState) || !strings.Contains(out.String(), "Inspect the exact retained") {
				t.Fatalf("lost actionable JSON: %v %q", e, out.String())
			}
		} else if e != nil {
			t.Fatal(e)
		}
		var result map[string]any
		if json.Unmarshal([]byte(out.String()), &result) != nil || strings.Contains(out.String(), "\x1b") {
			t.Fatalf("invalid direct JSON: %q", out.String())
		}
		if len(f.calls) != 1 {
			t.Fatalf("unexpected dispatch: %#v", f.calls)
		}
	}
}
func TestBootstrapInvalidFlagsRejectBeforeEffectsOrTTY(t *testing.T) {
	cases := [][]string{
		{"--non-interactive"}, {"menu", "--non-interactive"}, {"prepare", "--channel", "stable", "--release", "v1.2.3"}, {"prepare", "--channel", "latest"},
		{"inspect", "--update"}, {"route", "--channel", "development"}, {"route", "--postgres", "case"}, {"prepare", "--plain"}, {"prepare", "--no-color"}, {"inspect", "--operation", "case"}, {"menu", "--update"}, {"menu", "--postgres", "case"}, {"prepare", "extra"}, {"--bad=PRIVATE-CANARY"},
	}
	for _, args := range cases {
		f := &bootstrapTestFacade{}
		var out strings.Builder
		e := bootstrapWithFacade(context.Background(), args, nil, &out, f, nil, func() (*os.File, error) {
			t.Fatal("invalid input opened terminal")
			return nil, errors.New("unreachable")
		})
		if e == nil || len(f.calls) != 0 || out.Len() != 0 || strings.Contains(e.Error(), "PRIVATE-CANARY") {
			t.Fatalf("invalid flags reached effects: %v %#v %q", e, f.calls, out.String())
		}
	}
}
func TestBootstrapLaunchRequiresExactBackendRecheck(t *testing.T) {
	f := &bootstrapTestFacade{checkErr: serverinstall.ErrManagerChanged}
	launched := 0
	launch := func(_ context.Context, r serverinstall.BootstrapRoute, _ *os.File, _ io.Writer) error {
		launched++
		if !reflect.DeepEqual(r, bootstrapTestRoute()) {
			t.Fatal("route changed")
		}
		return nil
	}
	e := checkedBootstrapLaunch(context.Background(), f, launch, bootstrapTestRoute(), nil, io.Discard)
	if !errors.Is(e, serverinstall.ErrManagerChanged) || launched != 0 {
		t.Fatal("stale manager launched")
	}
	f.checkErr = nil
	if e = checkedBootstrapLaunch(context.Background(), f, launch, bootstrapTestRoute(), nil, io.Discard); e != nil || launched != 1 {
		t.Fatal("verified route was not handed off")
	}
}

func TestBootstrapRenderingPreferencesSurviveManagerHandoff(t *testing.T) {
	env := bootstrapEnvironment([]string{"PATH=/unrelated", "TERM=xterm-256color", "NO_COLOR=", "LANG=fa_IR.UTF-8"}, "/protected/frozen-helper/gh", true, true)
	joined := strings.Join(env, "\n")
	if !strings.Contains(joined, "TERM=dumb") || !strings.Contains(joined, "NO_COLOR=1") || !strings.Contains(joined, "PATH=/protected/frozen-helper:") || strings.Contains(joined, "TERM=xterm") {
		t.Fatalf("lost rendering/helper preferences: %q", env)
	}
}
