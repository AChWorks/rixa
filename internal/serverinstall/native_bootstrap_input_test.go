// SPDX-License-Identifier: MPL-2.0

package serverinstall

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/AChWorks/rixa/internal/product"
)

func TestNativeTwoSiteBootstrapReadOnlyInputAdmission(t *testing.T) {
	d := validTwoSiteDraft()
	document := configBytes(d, "", "")
	if _, err := product.ParseConfig(document); err != nil {
		t.Fatalf("native wire configuration refused: %v", err)
	}
	for _, site := range []struct{ scope, login string }{
		{"site:site-a", d.SiteLogin},
		{"site:site-b", d.SiteBLogin},
	} {
		root := t.TempDir()
		if err := os.Chmod(root, 0700); err != nil {
			t.Fatal(err)
		}
		path := filepath.Join(root, "bootstrap.json")
		r, err := product.InspectBootstrapOperation(context.Background(), product.BootstrapOperationRequest{
			OperationID: d.OperationID + "-bootstrap-" + site.scope[5:],
			StatePath:   path, Configuration: document, Scope: site.scope, Login: site.login,
		}, databaseLookup(d), nil)
		if err != nil || r.State != product.BootstrapPlanned || r.Code != "ok" {
			t.Fatalf("private bootstrap input admission failed for %s: state=%s code=%s err=%v", site.scope, r.State, r.Code, err)
		}
	}
}
