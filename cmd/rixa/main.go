// SPDX-License-Identifier: MPL-2.0

package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"

	"github.com/AChWorks/achrix"
	"github.com/AChWorks/rixa/internal/product"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "rixa:", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	if len(args) == 0 {
		return errors.New("usage: rixa <migrate|bootstrap-admin|check|run|version>")
	}
	logger := slog.New(slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))
	switch args[0] {
	case "version":
		return json.NewEncoder(os.Stdout).Encode(map[string]string{"rixa": product.Version, "achrix": achrix.Version()})
	case "migrate":
		fs, configPath := commandFlags("migrate", args[1:])
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		config, err := product.LoadConfig(*configPath)
		if err != nil {
			return err
		}
		return product.Migrate(context.Background(), config, os.LookupEnv)
	case "bootstrap-admin":
		fs, configPath := commandFlags("bootstrap-admin", args[1:])
		scope := fs.String("scope", "", "control or site:<id>")
		login := fs.String("login", "", "exact initial administrator login")
		passwordEnv := fs.String("password-env", "", "environment variable containing the initial password")
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		if *passwordEnv == "" {
			return errors.New("--password-env is required")
		}
		password, ok := os.LookupEnv(*passwordEnv)
		if !ok || password == "" {
			return fmt.Errorf("password environment variable %s is unavailable", *passwordEnv)
		}
		config, err := product.LoadConfig(*configPath)
		if err != nil {
			return err
		}
		result, err := product.BootstrapAdmin(context.Background(), config, *scope, *login, password, os.LookupEnv, logger)
		password = ""
		if err != nil {
			return err
		}
		return json.NewEncoder(os.Stdout).Encode(map[string]any{
			"scope":      *scope,
			"login":      result.Account.Login,
			"principal":  result.Account.ID,
			"created":    result.Created,
			"reconciled": result.Reconciled,
		})
	case "check", "run":
		fs, configPath := commandFlags(args[0], args[1:])
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		config, err := product.LoadConfig(*configPath)
		if err != nil {
			return err
		}
		resolved, err := config.ResolveRuntime(os.LookupEnv)
		if err != nil {
			return err
		}
		runtime, err := product.BuildRuntime(resolved, logger)
		if err != nil {
			return err
		}
		if args[0] == "check" {
			if err = product.Check(context.Background(), runtime); err != nil {
				return err
			}
			return json.NewEncoder(os.Stdout).Encode(map[string]any{
				"status":    "ok",
				"sites":     len(runtime.Sites),
				"multisite": runtime.UsesMultiSite(),
				"achrix":    achrix.Version(),
			})
		}
		ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
		defer stop()
		return product.Serve(ctx, runtime, logger)
	default:
		return fmt.Errorf("unknown command %q", args[0])
	}
}

func commandFlags(name string, args []string) (*flag.FlagSet, *string) {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	defaultPath := os.Getenv("RIXA_CONFIG")
	if defaultPath == "" {
		defaultPath = "/etc/rixa/rixa.json"
	}
	if !filepath.IsAbs(defaultPath) {
		defaultPath = "/etc/rixa/rixa.json"
	}
	configPath := fs.String("config", defaultPath, "absolute Rixa configuration path")
	return fs, configPath
}
