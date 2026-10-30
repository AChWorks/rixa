// SPDX-License-Identifier: MPL-2.0
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/AChWorks/rixa/internal/manageweb"
	"github.com/AChWorks/rixa/internal/product"
	"github.com/AChWorks/rixa/internal/serverinstall"
)

// runManageWeb is intentionally separate from command registration so the
// integration owner can compose it with bootstrap/prerequisite adapters.
func runManageWeb(ctx context.Context, args []string, output io.Writer) error {
	return manageWebWithStarter(ctx, args, output, func(ctx context.Context, o manageweb.Options, ready func(string)) error {
		if os.Geteuid() != 0 {
			return serverinstall.ErrAuthority
		}
		if e := manageweb.PrepareStorage(o.StorageRoot); e != nil {
			return e
		}
		console, e := manageweb.New(o, serverinstall.New())
		if e != nil {
			return e
		}
		return console.Serve(ctx, ready)
	})
}
func manageWebWithStarter(ctx context.Context, args []string, output io.Writer, start func(context.Context, manageweb.Options, func(string)) error) error {
	fs := flag.NewFlagSet("manage web", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	o := manageweb.Options{}
	fs.StringVar(&o.Scope.Kind, "kind", "install", "install or update")
	fs.StringVar(&o.Scope.Operation, "operation", "", "one retained operation ID")
	fs.StringVar(&o.Scope.Installation, "installation", "", "original installation ID for update")
	fs.StringVar(&o.Listen, "listen", "127.0.0.1:9444", "explicit loopback TLS listener")
	fs.StringVar(&o.Certificate, "tls-cert", "", "protected console TLS certificate covering 127.0.0.1")
	fs.StringVar(&o.Key, "tls-key", "", "protected console TLS private key")
	fs.StringVar(&o.RuntimeCertificate, "runtime-cert", "", "operator-bound installation TLS certificate")
	fs.StringVar(&o.RuntimeKey, "runtime-key", "", "operator-bound installation TLS private key")
	fs.StringVar(&o.PermitFile, "permit-file", "", "new absolute root-private permit file")
	fs.StringVar(&o.StorageRoot, "storage-root", "/var/lib/rixa-manager-web", "private retained artifact storage")
	fs.StringVar(&o.CatalogRoot, "catalog-root", "", "optional root-private UI catalog directory")
	fs.DurationVar(&o.Lifetime, "lifetime", 10*time.Minute, "access lifetime between 1m and 15m")
	actionText := fs.String("actions", "configure,review,apply,resume,status", "exact action allowlist; use status,remove for later removal")
	if e := fs.Parse(args); e != nil {
		if errors.Is(e, flag.ErrHelp) {
			_, e = io.WriteString(output, manageWebHelp)
			return e
		}
		return errors.New("invalid web console flags; use rixa manage web --help")
	}
	if fs.NArg() != 0 {
		return errors.New("unexpected web console positional arguments")
	}
	o.Scope.Actions = strings.Split(*actionText, ",")
	o.ManagerVersion = product.Version
	if e := manageweb.ValidateOptions(o); e != nil {
		return e
	}
	return start(ctx, o, func(url string) {
		_, _ = fmt.Fprintf(output, "Temporary Rixa console: %s\nOperation: %s %s\nPrivate permit file: %s\nRead that private file locally and enter its permit in the console; it is never included in the URL.\nUse an authorized SSH tunnel and trust the selected TLS certificate. Closing this console does not stop a supervised native operation.\n", url, o.Scope.Kind, o.Scope.Operation, o.PermitFile)
	})
}

const manageWebHelp = "Temporary native management browser console\n\n" +
	"  rixa manage web --operation <id> --kind install --tls-cert <console-cert> --tls-key <console-key> --runtime-cert <runtime-cert> --runtime-key <runtime-key> --permit-file <new-private-file>\n" +
	"  rixa manage web --operation <update-id> --kind update --installation <original-id> --tls-cert <console-cert> --tls-key <console-key> --permit-file <new-private-file>\n\n" +
	"The default listener is loopback TLS 127.0.0.1:9444. Only explicit 127.0.0.1 high ports are accepted.\n" +
	"The permit is short-lived, single-use and bound to one operation/kind/action allowlist.\n" +
	"Use --actions status,remove with a fresh permit for ordinary retained-data removal.\n" +
	"No product login cookie grants infrastructure authority. No public root service, arbitrary commands, panel proxy, prerequisite installation or purge is provided.\n" +
	"Console and runtime TLS files must be protected regular files; console certificate must cover 127.0.0.1.\n" +
	"Staged artifact files are retained so independent executors can finish after the console closes.\n"
