// SPDX-License-Identifier: MPL-2.0
package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/AChWorks/rixa/internal/manageweb"
)

func TestManageWebParsingScopeAndNoSecretOutput(t *testing.T) {
	args := []string{"--kind", "update", "--operation", "next-update", "--installation", "original-install", "--tls-cert", "/root/console.pem", "--tls-key", "/root/console.key", "--permit-file", "/root/new.permit", "--actions", "status,remove"}
	var output bytes.Buffer
	called := 0
	err := manageWebWithStarter(context.Background(), args, &output, func(_ context.Context, o manageweb.Options, ready func(string)) error {
		called++
		if o.Scope.Operation != "next-update" || o.Scope.Installation != "original-install" || o.Scope.Kind != "update" || len(o.Scope.Actions) != 2 {
			t.Fatal("scope changed")
		}
		ready("https://127.0.0.1:9444/access")
		return nil
	})
	if err != nil || called != 1 {
		t.Fatalf("parse/start %v", err)
	}
	if strings.Contains(output.String(), "token=") || strings.Contains(output.String(), "permit=") {
		t.Fatal("permit included in URL")
	}
	for _, extra := range [][]string{{"--listen", "0.0.0.0:9444"}, {"--actions", "status,arbitrary"}, {"--actions", "status,status"}, {"--lifetime", "24h"}, {"--operation", "../bad"}, {"--unknown", "value"}, {"--non-interactive"}} {
		invoked := false
		err = manageWebWithStarter(context.Background(), append(append([]string{}, args...), extra...), &bytes.Buffer{}, func(context.Context, manageweb.Options, func(string)) error { invoked = true; return nil })
		if err == nil || invoked {
			t.Fatalf("invalid flags started console: %v", extra)
		}
	}
}
func TestManageWebHelpNeverStartsAConsole(t *testing.T) {
	called := false
	var out bytes.Buffer
	err := manageWebWithStarter(context.Background(), []string{"--help"}, &out, func(context.Context, manageweb.Options, func(string)) error { called = true; return nil })
	if err != nil || called || !strings.Contains(out.String(), "single-use") {
		t.Fatal("help side effect or missing authority")
	}
}

func TestRunManageDispatchesWebHelpWithoutStartingConsole(t *testing.T) {
	path := filepath.Join(t.TempDir(), "help")
	output, e := os.Create(path)
	if e != nil {
		t.Fatal(e)
	}
	defer output.Close()
	if e = runManage(context.Background(), []string{"web", "--help"}, os.Stdin, output); e != nil {
		t.Fatal(e)
	}
	data, e := os.ReadFile(path)
	if e != nil || !strings.Contains(string(data), "Temporary native management browser console") || !strings.Contains(string(data), "single-use") {
		t.Fatal("web subcommand was not dispatched to its nonmutating help")
	}
}
