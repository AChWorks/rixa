// SPDX-License-Identifier: MPL-2.0
package serverinstall

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/AChWorks/achrix/identity"
)

func TestAddSiteUnreadyPreSessionProofKeepsPostflightUnattempted(t *testing.T) {
	m := testManager(t)
	j := interruptedAddSiteAt(t, "start")
	j.Attempts["start"] = "verified"
	j.State, j.Code, j.Message = "applying", "stage-verified", "start verified."
	if err := m.saveAddSite(j); err != nil {
		t.Fatal(err)
	}
	result, err := m.addSiteFailure(j, "postflight-readiness-required",
		"Read-only HTTPS/TLS readiness is unavailable.", ErrPrerequisite)
	if !errors.Is(err, ErrState) || result.Code != "postflight-readiness-required" {
		t.Fatalf("pre-session failure was not retained: %+v %v", result, err)
	}
	observed, err := m.loadAddSite(j.Draft.OperationID)
	if err != nil || !validAddSiteJournal(observed) || observed.State != "reconciliation-required" ||
		observed.Stage != "start" || observed.Attempts["start"] != "verified" ||
		observed.Attempts["postflight"] != "" || observed.Attempts["commit"] != "" {
		t.Fatalf("read-only readiness falsely recorded unknown session effects: %+v %v", observed, err)
	}
	if addSiteRecoveryServiceRequirement(observed) != "owned-running" {
		t.Fatal("pre-session failure lost active-service recovery evidence")
	}
}

func TestVerifyLoginFailureDiagnosticsDoNotDiscloseCredentials(t *testing.T) {
	const password = "do-not-report-this-credential"
	t.Run("login-http-status", func(t *testing.T) {
		s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path != "/auth/login" {
				t.Errorf("unexpected route: %s", r.URL.Path)
			}
			w.WriteHeader(http.StatusServiceUnavailable)
		}))
		defer s.Close()
		err := verifyLogin(context.Background(), s.Client(), s.URL,
			"private-login", password, "fixture-principal", "/admin/content/")
		if !errors.Is(err, ErrPrerequisite) || !strings.Contains(err.Error(), "administrator-login-http-503") ||
			strings.Contains(err.Error(), password) || strings.Contains(err.Error(), "private-login") {
			t.Fatalf("incorrect or private login diagnostic: %v", err)
		}
	})
	t.Run("admin-page-http-status", func(t *testing.T) {
		s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			switch r.URL.Path {
			case "/auth/login":
				http.SetCookie(w, &http.Cookie{Name: identity.CookieName, Value: "fixture", Path: "/"})
				_, _ = w.Write([]byte(`{"principal":"fixture-principal","csrf":"fixture-csrf"}`))
			case "/admin/content/":
				w.WriteHeader(http.StatusForbidden)
			case "/auth/logout":
				w.WriteHeader(http.StatusNoContent)
			case "/auth/csrf":
				w.WriteHeader(http.StatusUnauthorized)
			default:
				t.Errorf("unexpected route: %s", r.URL.Path)
				w.WriteHeader(http.StatusNotFound)
			}
		}))
		defer s.Close()
		err := verifyLogin(context.Background(), s.Client(), s.URL,
			"private-login", password, "fixture-principal", "/admin/content/")
		if !errors.Is(err, ErrPrerequisite) || !strings.Contains(err.Error(), "administrator-page-http-403") ||
			strings.Contains(err.Error(), password) || strings.Contains(err.Error(), "private-login") {
			t.Fatalf("incorrect or private admin page diagnostic: %v", err)
		}
	})
}
