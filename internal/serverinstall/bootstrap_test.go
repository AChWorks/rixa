// SPDX-License-Identifier: MPL-2.0

package serverinstall

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/AChWorks/rixa/internal/product"
	"github.com/AChWorks/rixa/internal/releaseartifact"
	"github.com/AChWorks/rixa/internal/releasepackage"
)

func bootstrapTestBackend(t *testing.T) *bootstrapBackend {
	t.Helper()
	m := testManager(t)
	root := t.TempDir()
	b := &bootstrapBackend{m: m, managerRoot: filepath.Join(root, "managers"), cacheRoot: filepath.Join(root, "selected"), clean: func(context.Context) error { return nil }, current: func() (string, string, error) { return "", "", ErrManagerChanged }}
	return b
}
func bootstrapFrozenFiles(t *testing.T, b *bootstrapBackend, manager, helper []byte) (string, string) {
	t.Helper()
	mh, hh := digest(manager), digest(helper)
	dir := filepath.Join(b.managerRoot, "helpers", hh)
	if e := os.MkdirAll(dir, 0755); e != nil {
		t.Fatal(e)
	}
	for _, p := range []string{b.managerRoot, filepath.Join(b.managerRoot, "helpers"), dir} {
		if e := os.Chmod(p, 0755); e != nil {
			t.Fatal(e)
		}
	}
	for p, data := range map[string][]byte{filepath.Join(b.managerRoot, mh): manager, filepath.Join(dir, "gh"): helper} {
		if e := os.WriteFile(p, data, 0755); e != nil {
			t.Fatal(e)
		}
	}
	return mh, hh
}
func bootstrapInstalled(t *testing.T, b *bootstrapBackend) *journal {
	t.Helper()
	j := installedParent(t, b.m)
	j.ManagerSHA256, j.HelperSHA256 = bootstrapFrozenFiles(t, b, []byte("original exact manager"), []byte("original exact helper"))
	j.ReviewedManagerSHA256 = j.ManagerSHA256
	if e := b.m.save(j); e != nil {
		t.Fatal(e)
	}
	if e := writeNew(filepath.Join(b.m.stateRoot, "installation"), []byte(j.Draft.OperationID+"\n"), 0600); e != nil {
		t.Fatal(e)
	}
	return j
}
func TestBootstrapDiscoveryNeverCreatesStateOrAdoptsUnknownMaterial(t *testing.T) {
	b := bootstrapTestBackend(t)
	ctx := context.Background()
	missing := filepath.Join(t.TempDir(), "missing")
	b.m.stateRoot = missing
	got, e := b.discover(ctx)
	if e != nil || got.State != "clean" || !bootstrapAllowed(got, "install") {
		t.Fatalf("clean discovery: %#v %v", got, e)
	}
	if _, e := os.Lstat(missing); !errors.Is(e, os.ErrNotExist) {
		t.Fatal("read-only discovery created state")
	}
	b.clean = func(context.Context) error { return ErrState }
	if got, e = b.discover(ctx); e == nil || got.State != "blocked" || bootstrapAllowed(got, "install") {
		t.Fatal("unknown host was adopted")
	}
	if e := os.Mkdir(missing, 0700); e != nil {
		t.Fatal(e)
	}
	if e := os.WriteFile(filepath.Join(missing, "orphan.private"), []byte("PRIVATE-CANARY"), 0600); e != nil {
		t.Fatal(e)
	}
	b.clean = func(context.Context) error { return nil }
	if got, e = b.discover(ctx); e == nil || got.State != "blocked" {
		t.Fatal("orphan state was ignored")
	}
}
func TestBootstrapRoutesFrozenOriginalAndPendingUpdateWithoutTakeover(t *testing.T) {
	b := bootstrapTestBackend(t)
	ctx := context.Background()
	j := bootstrapInstalled(t, b)
	original, e := b.open(ctx, "")
	if e != nil || original.ManagerSHA256 != j.ManagerSHA256 || !reflect.DeepEqual(original.Arguments, []string{"manage", "menu", "--operation", "first-install"}) {
		t.Fatalf("wrong original route: %#v %v", original, e)
	}
	u := frozenUpdate()
	u.ParentSHA256 = digest(mustReadFile(t, b.m.statePath(j.Draft.OperationID)))
	u.ManagerSHA256, u.HelperSHA256 = bootstrapFrozenFiles(t, b, []byte("separate exact update manager"), []byte("separate exact update helper"))
	u.ReviewedManagerSHA256 = u.ManagerSHA256
	if e = b.m.saveUpdate(u); e != nil {
		t.Fatal(e)
	}
	if e = writeNew(filepath.Join(b.m.stateRoot, "pending-update"), []byte(u.Draft.OperationID+"\n"), 0600); e != nil {
		t.Fatal(e)
	}
	route, e := b.open(ctx, "")
	if e != nil || route.OperationKind != "update" || route.ManagerSHA256 != u.ManagerSHA256 || !reflect.DeepEqual(route.Arguments, []string{"manage", "update", "menu", "--operation", "compatible-update"}) {
		t.Fatalf("wrong pending route: %#v %v", route, e)
	}
	d, e := b.discover(ctx)
	if e != nil || bootstrapAllowed(d, "update") || bootstrapAllowed(d, "install") {
		t.Fatal("pending update offered competing effects")
	}
	if e = os.WriteFile(route.ManagerPath, []byte("changed manager"), 0755); e != nil {
		t.Fatal(e)
	}
	if _, e = b.open(ctx, ""); e == nil {
		t.Fatal("changed frozen manager was launched")
	}
}
func mustReadFile(t *testing.T, path string) []byte {
	t.Helper()
	b, e := os.ReadFile(path)
	if e != nil {
		t.Fatal(e)
	}
	return b
}
func TestBootstrapDiscoveryRejectsAmbiguousIndexesAndUntrustedJournals(t *testing.T) {
	for _, name := range []string{"installation", "pending-update", "active-update"} {
		t.Run(name, func(t *testing.T) {
			b := bootstrapTestBackend(t)
			target := filepath.Join(t.TempDir(), "target")
			if e := os.WriteFile(target, []byte("first-install\n"), 0600); e != nil {
				t.Fatal(e)
			}
			if e := os.Symlink(target, filepath.Join(b.m.stateRoot, name)); e != nil {
				t.Fatal(e)
			}
			d, e := b.discover(context.Background())
			if e == nil || bootstrapAllowed(d, "install") {
				t.Fatal("symlink index accepted")
			}
		})
	}
	b := bootstrapTestBackend(t)
	if e := os.WriteFile(b.m.statePath("first-install"), []byte(`{"schema":"unknown","control_dsn":"PRIVATE-CANARY"}`), 0600); e != nil {
		t.Fatal(e)
	}
	d, e := b.discover(context.Background())
	data, _ := json.Marshal(d)
	if e == nil || strings.Contains(string(data), "PRIVATE-CANARY") || strings.Contains(e.Error(), "PRIVATE-CANARY") {
		t.Fatal("invalid journal accepted or disclosed")
	}
}
func bootstrapStageFixtures(t *testing.T, b *bootstrapBackend, events *[]string) string {
	t.Helper()
	archive, bundle := []byte("authenticated archive fixture"), []byte("authenticated bundle fixture")
	hash := digest(archive)
	b.fetch = func(_ context.Context, r releaseartifact.FetchRequest) (releaseartifact.FetchResult, error) {
		*events = append(*events, "fetch:"+r.Channel)
		if e := os.Mkdir(r.OutputDir, 0700); e != nil {
			return releaseartifact.FetchResult{}, e
		}
		ap, bp := filepath.Join(r.OutputDir, "archive"), filepath.Join(r.OutputDir, "bundle")
		if e := os.WriteFile(ap, archive, 0600); e != nil {
			return releaseartifact.FetchResult{}, e
		}
		if e := os.WriteFile(bp, bundle, 0600); e != nil {
			return releaseartifact.FetchResult{}, e
		}
		return releaseartifact.FetchResult{Schema: "rixa.artifact-fetch.v1", Status: "verified", Code: "ok", ArchivePath: ap, BundlePath: bp, ReleaseTag: "v9.8.7", SourceCommit: strings.Repeat("a", 40), Verification: &releaseartifact.Result{Status: "verified", Code: "ok", SHA256: hash, ReleaseTag: "v9.8.7", SourceCommit: strings.Repeat("a", 40)}}, nil
	}
	b.verify = func(_ context.Context, r releaseartifact.Request) (releaseartifact.Result, error) {
		*events = append(*events, "verify-protected")
		if r.ExpectedSHA256 != hash || string(mustReadFile(t, r.ArchivePath)) != string(archive) {
			t.Fatal("verification did not bind protected copies")
		}
		return releaseartifact.Result{Status: "verified", Code: "ok", SHA256: hash, ReleaseTag: r.ReleaseTag, SourceCommit: r.SourceCommit}, nil
	}
	b.extract = func(_ string, out string, r releasepackage.Expected) (releasepackage.Result, error) {
		*events = append(*events, "parse-authenticated")
		for _, name := range []string{"rixa", "LICENSE", "MANAGE.md", "VERIFY.md"} {
			mode := os.FileMode(0644)
			if name == "rixa" {
				mode = 0755
			}
			if e := os.WriteFile(filepath.Join(out, name), []byte(name+" exact bytes"), mode); e != nil {
				return releasepackage.Result{}, e
			}
		}
		return releasepackage.Result{BinarySHA256: digest([]byte("rixa exact bytes")), Metadata: releasepackage.Metadata{Rixa: r.Release, Source: r.SourceCommit, Target: "linux-amd64", Go: "fixture", AChrix: product.FoundationVersions()["github.com/AChWorks/achrix"], Components: product.FoundationVersions()}}, nil
	}
	return hash
}
func TestBootstrapPreparesAuthenticatedManagerWithoutExecutingOrInstalling(t *testing.T) {
	b := bootstrapTestBackend(t)
	var events []string
	bootstrapStageFixtures(t, b, &events)
	r, e := b.prepare(context.Background(), BootstrapRequest{Channel: "development", OperationID: "new-install"})
	if e != nil || r.State != "prepared" || r.Route.OperationKind != "install" || !reflect.DeepEqual(events, []string{"fetch:development", "verify-protected", "parse-authenticated"}) {
		t.Fatalf("wrong sequence: %#v %v %#v", r, e, events)
	}
	if _, e = os.Lstat(filepath.Join(b.m.stateRoot, "installation")); !errors.Is(e, os.ErrNotExist) {
		t.Fatal("prepare claimed an installation")
	}
	if _, e = os.Lstat(b.m.statePath("new-install")); !errors.Is(e, os.ErrNotExist) {
		t.Fatal("prepare created an installation journal")
	}
	if string(mustReadFile(t, r.Route.ManagerPath)) != "rixa exact bytes" {
		t.Fatal("wrong staged manager")
	}
	prefill := r.Route.Arguments[len(r.Route.Arguments)-1]
	data := mustReadFile(t, prefill)
	var draft Draft
	if e = json.Unmarshal(data, &draft); e != nil || draft.Release != "v9.8.7" || draft.ControlDSN != "" || draft.ControlPassword != "" {
		t.Fatal("wrong private metadata prefill")
	}
	// A changed content-addressed manager is a conflict, never an overwrite.
	if e = os.WriteFile(r.Route.ManagerPath, []byte("replacement"), 0755); e != nil {
		t.Fatal(e)
	}
	r, e = b.prepare(context.Background(), BootstrapRequest{Channel: "development"})
	if e == nil || r.Code != "manager-staging-conflict" {
		t.Fatalf("changed cache accepted: %#v %v", r, e)
	}
}
func TestBootstrapDoesNotParseFailedVerificationOrFallbackFromStable(t *testing.T) {
	b := bootstrapTestBackend(t)
	var events []string
	bootstrapStageFixtures(t, b, &events)
	b.verify = func(context.Context, releaseartifact.Request) (releaseartifact.Result, error) {
		events = append(events, "rejected")
		return releaseartifact.Result{}, releaseartifact.ErrPolicy
	}
	if _, e := b.prepare(context.Background(), BootstrapRequest{}); e == nil || !reflect.DeepEqual(events, []string{"fetch:", "rejected"}) {
		t.Fatalf("failed authentication parsed: %v %#v", e, events)
	}
	if _, e := os.Lstat(b.cacheRoot); !errors.Is(e, os.ErrNotExist) {
		t.Fatal("unauthenticated bytes reached manager staging")
	}
	b = bootstrapTestBackend(t)
	calls := 0
	b.fetch = func(_ context.Context, r releaseartifact.FetchRequest) (releaseartifact.FetchResult, error) {
		calls++
		if r.Channel != "stable" {
			t.Fatal("silent development fallback")
		}
		return releaseartifact.FetchResult{}, releaseartifact.ErrNoRelease
	}
	_, e := b.prepare(context.Background(), BootstrapRequest{Channel: "stable"})
	if !errors.Is(e, releaseartifact.ErrNoRelease) || calls != 1 {
		t.Fatal("stable absence was retried/fallback")
	}
}

func TestBootstrapRouteRechecksPrivateSelectionAndRejectsArbitraryArguments(t *testing.T) {
	b := bootstrapTestBackend(t)
	var events []string
	bootstrapStageFixtures(t, b, &events)
	prepared, e := b.prepare(context.Background(), BootstrapRequest{Channel: "development"})
	if e != nil {
		t.Fatal(e)
	}
	prepared.Route.Plain, prepared.Route.NoColor = true, true
	if e = b.checkRoute(context.Background(), prepared.Route); e != nil {
		t.Fatalf("prepared route failed: %v", e)
	}
	tampered := prepared.Route
	tampered.Arguments = append([]string{}, prepared.Route.Arguments...)
	tampered.Arguments[0] = "run"
	if e = b.checkRoute(context.Background(), tampered); e == nil {
		t.Fatal("arbitrary manager command accepted")
	}
	if e = os.WriteFile(prepared.ArchivePath, []byte("changed archive"), 0600); e != nil {
		t.Fatal(e)
	}
	if e = b.checkRoute(context.Background(), prepared.Route); e == nil {
		t.Fatal("changed protected archive accepted")
	}
}
func TestBootstrapDelegatesPrerequisiteDiscoveryAndKeepsCredentialsPrivate(t *testing.T) {
	b := bootstrapTestBackend(t)
	if e := os.Mkdir(filepath.Join(b.m.stateRoot, "postgres"), 0700); e != nil {
		t.Fatal(e)
	}
	b.postgres = func(context.Context) ([]PostgresDiscovery, error) {
		return []PostgresDiscovery{{OperationID: "pg-case", State: "applying"}}, nil
	}
	d, e := b.discover(context.Background())
	if e != nil || bootstrapAllowed(d, "install") {
		t.Fatal("unfinished prerequisites were bypassed")
	}
	b.postgres = func(context.Context) ([]PostgresDiscovery, error) {
		return []PostgresDiscovery{{OperationID: "pg-case", State: "installed"}}, nil
	}
	b.credentials = func(_ context.Context, id string) (PostgresCredentials, error) {
		if id != "pg-case" {
			t.Fatal("changed credential target")
		}
		return PostgresCredentials{ControlDSN: "postgres://control:PRIVATE-CREDENTIAL-CANARY@127.0.0.1:55432/control?sslmode=disable", SiteDSN: "postgres://site:PRIVATE-CREDENTIAL-CANARY@127.0.0.1:55432/site?sslmode=disable"}, nil
	}
	var events []string
	bootstrapStageFixtures(t, b, &events)
	p, e := b.prepare(context.Background(), BootstrapRequest{Channel: "development", PostgresOperation: "pg-case"})
	if e != nil {
		t.Fatal(e)
	}
	result, _ := json.Marshal(p)
	if strings.Contains(string(result), "PRIVATE-CREDENTIAL-CANARY") {
		t.Fatal("private handoff leaked in JSON")
	}
	private := mustReadFile(t, p.Route.Arguments[len(p.Route.Arguments)-1])
	if !strings.Contains(string(private), "PRIVATE-CREDENTIAL-CANARY") {
		t.Fatal("private handoff was not retained")
	}
	b.postgres = func(context.Context) ([]PostgresDiscovery, error) { return nil, ErrState }
	d, e = b.discover(context.Background())
	if e == nil || bootstrapAllowed(d, "install") {
		t.Fatal("unsafe prerequisite discovery became a clean host")
	}
}

func TestBootstrapRejectsCrossTypeOperationIDAmbiguity(t *testing.T) {
	b := bootstrapTestBackend(t)
	if _, e := b.m.SaveDraft(context.Background(), validDraft()); e != nil {
		t.Fatal(e)
	}
	b.postgres = func(context.Context) ([]PostgresDiscovery, error) {
		return []PostgresDiscovery{{OperationID: "first-install", State: "installed"}}, nil
	}
	if d, e := b.discover(context.Background()); e == nil || bootstrapAllowed(d, "install") {
		t.Fatal("ambiguous native/PostgreSQL operation ID accepted")
	}
	b = bootstrapTestBackend(t)
	b.postgres = func(context.Context) ([]PostgresDiscovery, error) {
		return []PostgresDiscovery{{OperationID: "pg-case", State: "installed"}}, nil
	}
	calls := 0
	b.fetch = func(context.Context, releaseartifact.FetchRequest) (releaseartifact.FetchResult, error) {
		calls++
		return releaseartifact.FetchResult{}, ErrState
	}
	if _, e := b.prepare(context.Background(), BootstrapRequest{OperationID: "pg-case"}); e == nil || calls != 0 {
		t.Fatal("cross-type operation ID reached preparation effects")
	}
}
