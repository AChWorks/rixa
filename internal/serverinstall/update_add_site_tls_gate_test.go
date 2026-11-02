// SPDX-License-Identifier: MPL-2.0
package serverinstall

import (
	"context"
	"crypto/tls"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
)

// These tests intentionally invoke the exact served-leaf/SNI primitive reused
// by Review, Apply's repeated Review, executor pre-stop, postflight, commit and
// final claim reconciliation. A successful CA-trusted HTTPS request, a sound
// on-disk certificate and an owned service are not substitutes for this proof.
func TestPostAddSiteUpdateTLSProofChecksActualLiveLeafAndSNI(t *testing.T) {
	pinned := addSiteTestMaterial(t, nil, "control.rixa.test", "site-a.rixa.test", "site-b.rixa.test")
	changedSAN := addSiteTestMaterial(t, &pinned, "control.rixa.test", "site-a.rixa.test", "site-b.rixa.test")
	missingSiteB := addSiteTestMaterial(t, &pinned, "control.rixa.test", "site-a.rixa.test")

	for _, tc := range []struct {
		name        string
		served      addSiteTestCertificate
		expectedSNI string
		routeWrong  bool
		shouldPass  bool
	}{
		{"correct-leaf-and-sni", pinned, "site-b.rixa.test", false, true},
		{"ca-trusted-different-leaf", changedSAN, "site-b.rixa.test", false, false},
		{"different-leaf-wrong-site", missingSiteB, "site-b.rixa.test", false, false},
		{"wrong-served-sni-route", pinned, "site-b.rixa.test", true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			observedSNI := ""
			server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(http.StatusOK)
			}))
			server.TLS = &tls.Config{
				MinVersion: tls.VersionTLS12,
				GetCertificate: func(hello *tls.ClientHelloInfo) (*tls.Certificate, error) {
					observedSNI = hello.ServerName
					if tc.routeWrong {
						// Simulate a server that routes a valid Site-B SNI
						// handshake to a different CA-trusted certificate,
						// while the pinned on-disk original is unchanged.
						return &changedSAN.pair, nil
					}
					return &tc.served.pair, nil
				},
			}
			server.StartTLS()
			defer server.Close()
			origin := "https://" + tc.expectedSNI + ":8443"
			err := proveServedSiteBCertificate(context.Background(), server.Listener.Addr().String(), origin, pinned.certPEM)
			if tc.shouldPass && err != nil {
				t.Fatalf("correct currently served leaf rejected: %v", err)
			}
			if !tc.shouldPass && !errors.Is(err, ErrPrerequisite) {
				t.Fatalf("wrong served identity accepted: %v", err)
			}
			if observedSNI != tc.expectedSNI {
				t.Fatalf("unexpected client SNI %q, expected %q", observedSNI, tc.expectedSNI)
			}
		})
	}
}

func TestPostAddSiteUpdateTLSGuardKeepsNativeTwoSiteCompatibility(t *testing.T) {
	m := testManager(t)
	original := installedParent(t, m)
	native := *original
	native.Draft = validTwoSiteDraft()
	native.Draft.ControlPassword = ""
	native.Draft.SitePassword = ""
	native.Draft.SiteBPassword = ""
	u := frozenUpdate()
	u.Draft.InstallationOperation = original.Draft.OperationID
	// An original native two-site operation has no separately committed A1
	// marker. Update's exact-SNI proof is consequently not strengthened.
	if err := m.verifyCommittedUpdateSiteBTLS(context.Background(), u, &native); err != nil {
		t.Fatalf("original native two-site Update incorrectly requires A1: %v", err)
	}

	// An active A1 marker with an absent, invalid or foreign journal cannot
	// suppress the new live-site TLS requirement by falling back to nil.
	path := filepath.Join(m.stateRoot, "active-add-site")
	if err := writeNew(path, []byte("unknown-add-site\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := m.verifyCommittedUpdateSiteBTLS(context.Background(), u, &native); !errors.Is(err, ErrState) {
		t.Fatalf("untrusted committed A1 marker bypassed exact TLS proof: %v", err)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	h := makeAddedSiteUpdateHistory(t)
	view := h.beforeU1Parent(t)
	// Current source tests have no root-owned protected original certificate
	// files (nor an active TLS listener). The existing verifier must fail
	// closed rather than claim Review/Update's serving-state evidence.
	if err := h.m.verifyCommittedUpdateSiteBTLS(context.Background(), h.first, view); !errors.Is(err, ErrPrerequisite) {
		t.Fatalf("unavailable actual protected leaf evidence accepted: %v", err)
	}
}

// A peer can change its served certificate between a successful operator
// Review and Apply's mandatory *fresh* Review. The shared live proof must
// observe the change, rather than treating a historical affirmative result,
// a still-correct on-disk snapshot, or CA trust as authorization.
func TestPostAddSiteTLSStaleReviewRejectedBeforeUpdatedServiceEffects(t *testing.T) {
	pinned := addSiteTestMaterial(t, nil, "control.rixa.test", "site-a.rixa.test", "site-b.rixa.test")
	changed := addSiteTestMaterial(t, &pinned, "control.rixa.test", "site-a.rixa.test", "site-b.rixa.test")
	var servingChanged atomic.Bool
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	server.TLS = &tls.Config{
		MinVersion: tls.VersionTLS12,
		GetCertificate: func(_ *tls.ClientHelloInfo) (*tls.Certificate, error) {
			if servingChanged.Load() {
				return &changed.pair, nil
			}
			return &pinned.pair, nil
		},
	}
	server.StartTLS()
	defer server.Close()
	probe := func() error {
		return proveServedSiteBCertificate(context.Background(),
			server.Listener.Addr().String(), "https://site-b.rixa.test:8443", pinned.certPEM)
	}
	if err := probe(); err != nil {
		t.Fatalf("exact pinned leaf not valid at Review: %v", err)
	}
	servingChanged.Store(true)
	if err := probe(); !errors.Is(err, ErrPrerequisite) {
		t.Fatalf("changed live TLS identity accepted by fresh Apply/commit probe: %v", err)
	}
	servingChanged.Store(false)
	if err := probe(); err != nil {
		t.Fatalf("restored originally pinned TLS identity rejected: %v", err)
	}
	server.Close()
	if err := probe(); !errors.Is(err, ErrPrerequisite) {
		t.Fatalf("inactive service falsely admitted from previous positive proof: %v", err)
	}
}
