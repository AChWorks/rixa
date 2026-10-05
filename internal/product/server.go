// SPDX-License-Identifier: MPL-2.0

package product

import (
	"context"
	"errors"
	"log/slog"
	"net"
	"net/http"

	shell "github.com/AChWorks/achrix/admin"
)

func Serve(parent context.Context, runtime *Runtime, logger *slog.Logger) error {
	if runtime == nil {
		return ErrConfiguration
	}
	if logger == nil {
		logger = slog.Default()
	}
	listener, err := net.Listen("tcp", runtime.Config.Listen)
	if err != nil {
		return err
	}
	started := false
	defer func() {
		if !started {
			_ = listener.Close()
		}
	}()

	if err = runtime.Start(parent); err != nil {
		return err
	}
	started = true

	server := &http.Server{
		Addr:    runtime.Config.Listen,
		Handler: runtime.Handler,
	}
	shell.ConfigureServer(server)

	result := make(chan error, 1)
	go func() {
		result <- server.ServeTLS(listener, runtime.Config.TLS.CertFile, runtime.Config.TLS.KeyFile)
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
	moduleErr := runtime.Shutdown(shutdownCtx)

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
