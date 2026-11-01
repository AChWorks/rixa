// SPDX-License-Identifier: MPL-2.0

package serverinstall

import (
	"context"
	"errors"
	"fmt"

	"github.com/AChWorks/rixa/internal/product"
)

// This is an early *negative* safety gate for the existing schema-compatible
// native updater. It cannot approve or predict an authenticated candidate's
// versioned AChrix/Editorial migrations. Only exact candidate check, under the
// already-existing stopped-service fence, verifies positive compatibility.
var errPreStopEditorialPending = errors.New("native schema-changing update is not admitted")

func updateEditorialPreflight(ctx context.Context, prior *journal) error {
	if prior == nil {
		return ErrState
	}
	config, err := product.ParseConfig(configBytes(prior.Draft, prior.ControlPrincipal, prior.SitePrincipal))
	if err != nil {
		return ErrState
	}
	// The public product observer validates distinct physical DB identities,
	// then reads the actual editorial ledger in READ ONLY transactions. It does
	// not call product.Check, runtime.Start or migration/publication code.
	observed, err := product.InspectEditorialMigrations(ctx, config, databaseLookup(prior.Draft))
	if err != nil {
		if errors.Is(err, product.ErrConfiguration) {
			return fmt.Errorf("%w: unsupported or inconsistent editorial ledger/targets", ErrPrerequisite)
		}
		return fmt.Errorf("%w: read-only editorial observation unavailable", ErrPrerequisite)
	}
	return preStopEditorialDecision(observed)
}

// Even a "current" manager-owned ledger only allows the existing updater to
// continue to its independent post-stop candidate compatibility check.
func preStopEditorialDecision(observed product.EditorialMigrationInspection) error {
	if observed.Scope != "rixa-editorial-only" || observed.Admission != "not-authorized" || len(observed.Sites) != 1 {
		return ErrState
	}
	site := observed.Sites[0]
	if site.ID != "site-a" || site.Target < 1 || site.Current < 0 || site.Current > site.Target {
		return ErrState
	}
	if site.State == "pending" && site.Current < site.Target && len(site.Pending) == site.Target-site.Current {
		return errPreStopEditorialPending
	}
	if site.State != "current" || site.Current != site.Target || len(site.Pending) != 0 {
		return ErrState
	}
	return nil
}
