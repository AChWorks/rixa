// SPDX-License-Identifier: MPL-2.0
package main

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/AChWorks/rixa/internal/product"
)

func TestRuntimeReadinessRetriesOnlyTransientReadOnlyPreflight(t *testing.T) {
	attempts := 0
	result, err := awaitRuntimeDatabaseReadiness(context.Background(), time.Second, time.Millisecond, func(context.Context) (product.RuntimeConfig, error) {
		attempts++
		if attempts <= 2 {
			return product.RuntimeConfig{}, fmt.Errorf("database target control: %w", product.ErrUnavailable)
		}
		return product.RuntimeConfig{Config: product.Config{Listen: "127.0.0.1:12345"}}, nil
	}, nil)
	if err != nil || attempts != 3 || result.Listen != "127.0.0.1:12345" {
		t.Fatalf("transient preflight not reconciled: attempts=%d err=%v", attempts, err)
	}
}

func TestRuntimeReadinessDoesNotRetryConfigurationOrUnsafeErrors(t *testing.T) {
	for _, test := range []struct {
		name    string
		failure error
	}{
		{"configuration", fmt.Errorf("invalid TLS: %w", product.ErrConfiguration)},
		{"authentication", fmt.Errorf("database target control: %w: authentication rejected", product.ErrDatabaseAdmission)},
		{"permission", fmt.Errorf("database target site-a: %w: identity probe permission denied", product.ErrDatabaseAdmission)},
		{"tls", fmt.Errorf("database target site-b: %w: TLS verification", product.ErrDatabaseAdmission)},
		// Actual net.OpError causes are classified and sanitized in product preflight.
		// The run-only readiness loop must invoke its resolver just once for each.
		{"net-op-eacces", fmt.Errorf("database target control: %w: PostgreSQL network operation rejected", product.ErrDatabaseAdmission)},
		{"net-op-eperm", fmt.Errorf("database target site-a: %w: PostgreSQL network operation rejected", product.ErrDatabaseAdmission)},
		{"net-op-unknown", fmt.Errorf("database target site-b: %w: PostgreSQL network operation rejected", product.ErrDatabaseAdmission)},
		{"permanent-even-when-joined-to-transient", errors.Join(product.ErrDatabaseAdmission, product.ErrUnavailable)},
		{"unclassified", errors.New("unexpected internal failure")},
		{"canceled", context.Canceled},
	} {
		t.Run(test.name, func(t *testing.T) {
			attempts := 0
			_, err := awaitRuntimeDatabaseReadiness(context.Background(), time.Second, time.Millisecond, func(context.Context) (product.RuntimeConfig, error) {
				attempts++
				return product.RuntimeConfig{}, test.failure
			}, nil)
			if attempts != 1 || !errors.Is(err, test.failure) {
				t.Fatalf("unsafe retry: %d %v", attempts, err)
			}
		})
	}
}

func TestRuntimeReadinessBoundedAndNoSecretInTimeout(t *testing.T) {
	const secret = "postgres://sensitive-secret@localhost/test"
	attempts := 0
	_, err := awaitRuntimeDatabaseReadiness(context.Background(), 25*time.Millisecond, time.Millisecond, func(context.Context) (product.RuntimeConfig, error) {
		attempts++
		return product.RuntimeConfig{}, fmt.Errorf("%w: %s", product.ErrUnavailable, secret)
	}, nil)
	if !errors.Is(err, product.ErrUnavailable) || errors.Is(err, context.DeadlineExceeded) || attempts < 1 || strings.Contains(err.Error(), secret) {
		t.Fatalf("timeout must fail closed without secrets; attempts=%d err=%v", attempts, err)
	}
}

func TestRuntimeReadinessHonorsCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	attempts := 0
	_, err := awaitRuntimeDatabaseReadiness(ctx, time.Second, time.Millisecond, func(context.Context) (product.RuntimeConfig, error) {
		attempts++
		cancel()
		return product.RuntimeConfig{}, product.ErrUnavailable
	}, nil)
	if attempts != 1 || !errors.Is(err, context.Canceled) {
		t.Fatalf("cancel did not stop retry: %d %v", attempts, err)
	}
}

func TestRuntimeReadinessRejectsInvalidInternalParameters(t *testing.T) {
	_, err := awaitRuntimeDatabaseReadiness(context.Background(), 0, time.Second, nil, nil)
	if !errors.Is(err, product.ErrConfiguration) {
		t.Fatalf("invalid helper parameters admitted: %v", err)
	}
}

func TestRuntimeReadinessDeadlineAtLoopBoundaryKeepsUnavailable(t *testing.T) {
	var deadline context.Context
	called := 0
	_, err := awaitRuntimeDatabaseReadiness(context.Background(), 20*time.Millisecond, time.Millisecond, func(ctx context.Context) (product.RuntimeConfig, error) {
		deadline = ctx
		called++
		<-ctx.Done()
		return product.RuntimeConfig{}, product.ErrUnavailable
	}, nil)
	if called != 1 || !errors.Is(err, product.ErrUnavailable) || errors.Is(err, context.DeadlineExceeded) || deadline == nil {
		t.Fatalf("deadline at loop boundary must be bounded unavailable: calls=%d err=%v", called, err)
	}
}
