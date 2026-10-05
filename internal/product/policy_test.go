// SPDX-License-Identifier: MPL-2.0

package product

import (
	"context"
	"errors"
	"testing"

	"github.com/AChWorks/achrix"
	"github.com/AChWorks/achrix/identity"
	"github.com/AChWorks/achrix/media"
)

func TestSitePolicyKeepsAdministratorsScopedToTheirApplication(t *testing.T) {
	a := achrix.Principal(testSiteAAdmin)
	b := achrix.Principal(testSiteBAdmin)
	operatorA := operatorPrincipal(achrix.Principal(testControlAdmin), "site-a")
	operatorB := operatorPrincipal(achrix.Principal(testControlAdmin), "site-b")
	policyA := sitePolicy(a, operatorA)
	policyB := sitePolicy(b, operatorB)

	if err := policyA.Authorize(context.Background(), a, media.List, media.LibraryTarget); err != nil {
		t.Fatalf("site A admin denied its own library: %v", err)
	}
	if err := policyB.Authorize(context.Background(), a, media.List, media.LibraryTarget); !errors.Is(err, achrix.ErrDenied) {
		t.Fatalf("site A admin reached site B: %v", err)
	}
	if err := policyA.Authorize(context.Background(), operatorB, identity.AccountCreate, ""); !errors.Is(err, achrix.ErrDenied) {
		t.Fatalf("site B control operator reached site A: %v", err)
	}
	if err := policyA.Authorize(context.Background(), identity.PublicPrincipal, identity.Authentication, ""); err != nil {
		t.Fatalf("public authentication admission denied: %v", err)
	}
	if err := policyA.Authorize(context.Background(), identity.PublicPrincipal, media.List, media.LibraryTarget); !errors.Is(err, achrix.ErrDenied) {
		t.Fatalf("public principal gained media permission: %v", err)
	}
}

func TestControlPolicyDeniesDisabledAndUnknownSites(t *testing.T) {
	admin := achrix.Principal(testControlAdmin)
	policy := controlPolicy(admin, map[string]bool{"a": true, "disabled": true}, map[string]bool{"a": true})
	if err := policy.Authorize(context.Background(), admin, CapabilityControlSiteManage, "a"); err != nil {
		t.Fatal(err)
	}
	if err := policy.Authorize(context.Background(), admin, identity.AccountCreate, ""); !errors.Is(err, achrix.ErrDenied) {
		t.Fatalf("control admin gained global account-management authority: %v", err)
	}
	for _, target := range []string{"disabled", "unknown"} {
		if err := policy.Authorize(context.Background(), admin, CapabilityControlSiteManage, target); !errors.Is(err, achrix.ErrDenied) {
			t.Fatalf("target %s was not denied: %v", target, err)
		}
	}
}
