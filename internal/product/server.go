// SPDX-License-Identifier: MPL-2.0

package product

import (
	"context"
	"crypto/tls"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"time"

	shell "github.com/AChWorks/achrix/admin"
)

func Serve(parent context.Context, runtime *Runtime, logger *slog.Logger) error {
	if runtime == nil || runtime.Handler == nil {
		return ErrConfiguration
	}
	if logger == nil {
		logger = slog.Default()
	}
	pair, err := runtime.Config.Config.loadRuntimeTLS()
	if err != nil {
		return err
	}
	if err = runtime.Start(parent); err != nil {
		return err
	}
	stopRuntime := func(cause error) error {
		ctx, cancel := context.WithTimeout(context.WithoutCancel(parent), runtime.Config.ShutdownTimeout)
		defer cancel()
		return errors.Join(cause, runtime.Shutdown(ctx))
	}
	if err = parent.Err(); err != nil {
		return stopRuntime(err)
	}
	if runtime.Config.hostingProfile() == HostingProfileDirectTLS {
		if err = validateDirectTLSCertificate(runtime.Config.Config, pair, time.Now()); err != nil {
			return stopRuntime(err)
		}
	}
	listener, err := net.Listen("tcp", runtime.Config.Listen)
	if err != nil {
		return stopRuntime(err)
	}

	server := &http.Server{
		Addr:      runtime.Config.Listen,
		Handler:   runtime.Handler,
		TLSConfig: &tls.Config{Certificates: []tls.Certificate{pair}, MinVersion: tls.VersionTLS12},
	}
	shell.ConfigureServer(server)

	result := make(chan error, 1)
	go func() {
		result <- server.ServeTLS(listener, "", "")
	}()

	var serveErr error
	select {
	case <-parent.Done():
	case serveErr = <-result:
		if errors.Is(serveErr, http.ErrServerClosed) {
			serveErr = nil
		}
	}

	shutdownCtx, shutdownCancel := context.WithTimeout(context.WithoutCancel(parent), runtime.Config.ShutdownTimeout)
	defer shutdownCancel()
	httpErr := server.Shutdown(shutdownCtx)
	if httpErr != nil {
		httpErr = errors.Join(httpErr, server.Close())
	}
	moduleErr := stopRuntime(nil)

	if serveErr != nil {
		logger.Error("ingress stopped", "component", "rixa.ingress", "reason", "serve_failed")
	}
	return errors.Join(serveErr, httpErr, moduleErr)
}

func Check(parent context.Context, runtime *Runtime) error {
	if runtime == nil {
		return ErrConfiguration
	}
	if err := runtime.Start(parent); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(parent), runtime.Config.ShutdownTimeout)
	defer cancel()
	return runtime.Shutdown(ctx)
}
