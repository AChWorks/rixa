// SPDX-License-Identifier: MPL-2.0
package serverinstall

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func addSiteDraftFixture() AddSiteDraft {
	return AddSiteDraft{
		OperationID: "add-site-operation-one",
		AddSiteInspectionDraft: AddSiteInspectionDraft{
			InstallationOperation: "original-installation",
			SiteBOrigin:           "https://site-b.rixa.test:8443",
			SiteBDSN:              "postgres://siteb:separate-db-secret@127.0.0.1:5432/siteb?sslmode=disable",
			SiteBLogin:            "site-b-admin",
			SiteBPassword:         "strong-site-b-password",
		},
	}
}

func addSiteJournalFixture() *addSiteJournal {
	return &addSiteJournal{
		Schema: addSiteSchema, Draft: addSiteDraftFixture(), State: "draft",
		Code: "draft-saved", Message: "Private Add Site draft saved.",
		Updated: time.Now().UTC().Format(time.RFC3339Nano), Attempts: map[string]string{},
	}
}

func reviewedAddSiteFixture() *addSiteJournal {
	j := addSiteJournalFixture()
	j.State, j.Stage, j.Code = "ready", "review", "ready"
	j.ReviewToken = strings.Repeat("a", 64)
	j.ParentSHA256 = strings.Repeat("b", 64)
	j.ConfigSHA256 = strings.Repeat("c", 64)
	j.EnvironmentSHA256 = strings.Repeat("d", 64)
	j.UnitSHA256 = strings.Repeat("e", 64)
	j.DatabaseTargets = map[string]string{"control": "1:100", "site-a": "1:101", "site-b": "1:102"}
	return j
}

func installedAddSiteFixture() *addSiteJournal {
	j := reviewedAddSiteFixture()
	j.State, j.Stage, j.Code = "installed", "commit", "installed"
	j.ReviewToken = ""
	j.Frozen = addSiteDraftDigest(j.Draft)
	j.ManagerSHA256 = strings.Repeat("f", 64)
	j.ExecutorGeneration, j.ExecutorAcknowledged = 1, true
	j.Attempts["executor"] = "verified"
	for _, stage := range addSiteStages {
		j.Attempts[stage] = "verified"
	}
	j.SiteBPrincipal = strings.Repeat("A", 26)
	j.Draft.SiteBPassword = ""
	return j
}

func TestAddSitePrivateJournalPersistenceAndSecretFreeReceipt(t *testing.T) {
	m := testManager(t)
	j := addSiteJournalFixture()
	if err := m.saveAddSite(j); err != nil {
		t.Fatal(err)
	}
	path := m.addSitePath(j.Draft.OperationID)
	info, err := os.Lstat(path)
	if err != nil || info.Mode().Perm() != 0600 || !info.Mode().IsRegular() {
		t.Fatalf("private journal owner/mode missing: %v", err)
	}
	copy, err := m.loadAddSite(j.Draft.OperationID)
	if err != nil || !reflect.DeepEqual(copy, j) {
		t.Fatalf("private journal changed: %v", err)
	}
	public, err := json.Marshal(addSiteResult(j))
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{j.Draft.SiteBDSN, j.Draft.SiteBPassword, "separate-db-secret"} {
		if bytes.Contains(public, []byte(secret)) {
			t.Fatal("Add Site private input leaked in public result")
		}
	}
	if err = os.Chmod(path, 0644); err != nil {
		t.Fatal(err)
	}
	if _, err = m.loadAddSite(j.Draft.OperationID); err == nil {
		t.Fatal("insecure-mode Add Site journal accepted")
	}
	if err = os.Remove(path); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(m.stateRoot, "other-private")
	if err = os.WriteFile(target, []byte("unexpected"), 0600); err != nil {
		t.Fatal(err)
	}
	if err = os.Symlink(target, path); err != nil {
		t.Fatal(err)
	}
	if _, err = m.loadAddSite(j.Draft.OperationID); err == nil {
		t.Fatal("symlinked Add Site journal accepted")
	}
}

func TestAddSiteJournalRejectsIncompleteOwnershipAndStageClaims(t *testing.T) {
	invalid := []struct {
		name string
		edit func(*addSiteJournal)
	}{
		{"empty-password-before-bootstrap", func(j *addSiteJournal) { j.Draft.SiteBPassword = "" }},
		{"same-operation-as-parent", func(j *addSiteJournal) { j.Draft.OperationID = j.Draft.InstallationOperation }},
		{"invalid-state", func(j *addSiteJournal) { j.State = "done" }},
		{"unknown-stage", func(j *addSiteJournal) { j.Attempts["arbitrary-command"] = "verified" }},
		{"untrusted-attempt", func(j *addSiteJournal) { j.Attempts["executor"] = "succeeded" }},
		{"stage-without-frozen-executor", func(j *addSiteJournal) { j.Attempts["migrate"] = "verified" }},
		{"ready-without-bounded-parent", func(j *addSiteJournal) { j.State, j.Code = "ready", "ready"; j.ReviewToken = strings.Repeat("a", 64) }},
		{"bad-review-token", func(j *addSiteJournal) { j.ReviewToken = "unsafe" }},
		{"unsafe-newline", func(j *addSiteJournal) { j.Draft.SiteBDSN += "\npassword=oops" }},
		{"altered-credential-hash", func(j *addSiteJournal) {
			j.Frozen = strings.Repeat("a", 64)
			j.State = "applying"
			j.ExecutorGeneration = 1
			j.ManagerSHA256 = strings.Repeat("b", 64)
			j.ParentSHA256 = strings.Repeat("c", 64)
			j.DatabaseTargets = map[string]string{"control": "1:1", "site-a": "1:2", "site-b": "1:3"}
		}},
	}
	for _, tc := range invalid {
		t.Run(tc.name, func(t *testing.T) {
			j := addSiteJournalFixture()
			tc.edit(j)
			if validAddSiteJournal(j) {
				t.Fatal("untrusted Add Site journal accepted")
			}
		})
	}
	ready := reviewedAddSiteFixture()
	if !validAddSiteJournal(ready) {
		t.Fatal("valid, separate reviewed Add Site receipt rejected")
	}
	ready.ActiveUpdate = "prior-update"
	if validAddSiteJournal(ready) {
		t.Fatal("unhashed predecessor update accepted")
	}
	ready.ActiveUpdateSHA256 = strings.Repeat("f", 64)
	if !validAddSiteJournal(ready) {
		t.Fatal("authentic predecessor update pair rejected")
	}
	ready.DatabaseTargets["site-b"] = ready.DatabaseTargets["site-a"]
	if validAddSiteJournal(ready) {
		t.Fatal("shared physical database admitted")
	}
}

func TestAddSiteJournalVerifiedTerminalRequiresEveryStageAndErasesPassword(t *testing.T) {
	installed := installedAddSiteFixture()
	if !validAddSiteJournal(installed) {
		t.Fatal("verified installed Add Site journal rejected")
	}
	m := testManager(t)
	if err := m.saveAddSite(installed); err != nil {
		t.Fatal(err)
	}
	got, err := m.loadAddSite(installed.Draft.OperationID)
	if err != nil || !reflect.DeepEqual(got, installed) {
		t.Fatalf("verified retained Add Site evidence not recovered: %v", err)
	}
	for _, stage := range append([]string{"executor"}, addSiteStages...) {
		t.Run(stage, func(t *testing.T) {
			j := *installed
			j.Attempts = map[string]string{}
			for k, v := range installed.Attempts {
				j.Attempts[k] = v
			}
			j.Attempts[stage] = "attempted"
			if validAddSiteJournal(&j) {
				t.Fatal("unfinished Add Site stage accepted as installed")
			}
		})
	}
	incomplete := *installed
	incomplete.Draft.SiteBPassword = "not-erased"
	if validAddSiteJournal(&incomplete) {
		t.Fatal("installed Add Site retained administrator bootstrap password")
	}
	altered := *installed
	altered.Draft.SiteBOrigin = "https://another.rixa.test:8443"
	if validAddSiteJournal(&altered) {
		t.Fatal("immutable Add Site operation binding changed")
	}
	if _, err := m.loadAddSite("/noncanonical"); !errors.Is(err, ErrInput) {
		t.Fatalf("invalid journal identity not rejected: %v", err)
	}
}
