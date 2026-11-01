// SPDX-License-Identifier: MPL-2.0
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/AChWorks/rixa/internal/product"
)

const (
	serveDatabaseReadinessLimit = 90 * time.Second
	serveDatabaseRetryInterval  = 2 * time.Second
)

// awaitRuntimeDatabaseReadiness retries only the read-only physical-database
// identity preflight, before any Application or HTTP listener is created.
// This is limited to `rixa run`: setup, migration and `rixa check` retain their
// existing fail-fast contract. No failed module start or mutation is retried.
func awaitRuntimeDatabaseReadiness(parent context.Context, limit, interval time.Duration, resolve func(context.Context) (product.RuntimeConfig, error), logger *slog.Logger) (product.RuntimeConfig, error) {
	if parent == nil || resolve == nil || limit <= 0 || interval <= 0 {
		return product.RuntimeConfig{}, product.ErrConfiguration
	}
	ctx, cancel := context.WithTimeout(parent, limit)
	defer cancel()
	var attempts int
	for {
		if err := ctx.Err(); err != nil {
			if parent.Err() != nil {
				return product.RuntimeConfig{}, parent.Err()
			}
			return product.RuntimeConfig{}, fmt.Errorf("%w: runtime database readiness not established after %d read-only attempts", product.ErrUnavailable, attempts)
		}
		attempts++
		result, err := resolve(ctx)
		if err == nil {
			if attempts > 1 && logger != nil {
				logger.Info("runtime database readiness recovered", "component", "rixa.startup", "attempts", attempts)
			}
			return result, nil
		}
		if errors.Is(err, product.ErrConfiguration) || !errors.Is(err, product.ErrUnavailable) && !errors.Is(err, context.DeadlineExceeded) {
			return product.RuntimeConfig{}, err
		}
		if attempts == 1 && logger != nil {
			logger.Warn("waiting for read-only database readiness", "component", "rixa.startup", "retry_interval", interval.String())
		}
		timer := time.NewTimer(interval)
		select {
		case <-ctx.Done():
			if !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
			}
			if parent.Err() != nil {
				return product.RuntimeConfig{}, parent.Err()
			}
			return product.RuntimeConfig{}, fmt.Errorf("%w: runtime database readiness not established after %d read-only attempts", product.ErrUnavailable, attempts)
		case <-timer.C:
		}
	}
}
