// SPDX-License-Identifier: MPL-2.0
package serverinstall

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/AChWorks/rixa/internal/product"
	"github.com/jackc/pgx/v5/pgconn"
)

func TestAddSiteStageDiagnosticCategoriesNeverExposeUnderlyingSecrets(t *testing.T) {
	const private = "postgres://siteb:never-show-password@127.0.0.1:5432/site-b"
	for _, tc := range []struct {
		stage          string
		err            error
		category, code string
	}{
		{"migrate", fmt.Errorf("wrapped: %w", &pgconn.PgError{Code: "42501", Message: private}), "postgresql", "42501"},
		{"migrate", &pgconn.PgError{Code: "ABC!1", Message: private}, "stage", "migrate-failed"},
		{"migrate", fmt.Errorf("timeout %s: %w", private, context.DeadlineExceeded), "timeout", "deadline-exceeded"},
		{"bootstrap-site-b", fmt.Errorf("wrapped %s: %w", private, product.ErrBootstrapReconciliation), "bootstrap", "reconciliation-required"},
		{"bootstrap-site-b", product.ErrBootstrapOperationStale, "bootstrap", "stale-binding"},
		{"bootstrap-site-b", product.ErrBootstrapOperationInput, "bootstrap", "invalid-input"},
		{"check", errors.New("invalid config SQL=" + private), "stage", "check-failed"},
		{"prepare", context.Canceled, "interrupted", "context-canceled"},
	} {
		t.Run(tc.stage+"-"+tc.code, func(t *testing.T) {
			category, code := addSiteStageErrorCategory(tc.stage, tc.err)
			if category != tc.category || code != tc.code {
				t.Fatalf("inaccurate safe category %s %s want %s %s", category, code, tc.category, tc.code)
			}
			// The only permitted diagnostic data are fixed enums, a
			// validated operation ID and the fixed stage name.
			protected := fmt.Sprintf("operation=site-b-test stage=%s category=%s code=%s", tc.stage, category, code)
			if strings.Contains(protected, private) || strings.Contains(protected, "password") ||
				strings.Contains(protected, "invalid config SQL") {
				t.Fatal("raw database/runtime diagnostic leaked into protected error")
			}
		})
	}
}
