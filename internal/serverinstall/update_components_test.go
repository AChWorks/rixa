// SPDX-License-Identifier: MPL-2.0
package serverinstall

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"runtime/debug"
	"testing"

	"github.com/AChWorks/rixa/internal/product"
	"github.com/AChWorks/rixa/internal/releaseartifact"
)

func candidateGraphFixture() map[string]string {
	components := product.FoundationVersions()
	if components["github.com/AChWorks/achrix/admin"] != "v0.1.0" {
		components["github.com/AChWorks/achrix/admin"] = "v0.1.0"
	} else {
		components["github.com/AChWorks/achrix/admin"] = "v9.8.7"
	}
	return components
}
func compiledCandidateFixture(components map[string]string) (*debug.BuildInfo, packageInfo) {
	info := &debug.BuildInfo{GoVersion: "go1.25.1", Path: "github.com/AChWorks/rixa/cmd/rixa", Main: debug.Module{Path: "github.com/AChWorks/rixa"}, Settings: []debug.BuildSetting{{Key: "GOOS", Value: "linux"}, {Key: "GOARCH", Value: "amd64"}, {Key: "CGO_ENABLED", Value: "0"}}}
	for _, path := range updateComponentPaths {
		info.Deps = append(info.Deps, &debug.Module{Path: path, Version: components[path]})
	}
	metadata := packageInfo{Rixa: validDraft().Release, Source: validDraft().SourceCommit, Target: "linux-amd64", Go: info.GoVersion, AChrix: components["github.com/AChWorks/achrix"], Components: components}
	return info, metadata
}
func TestUpdateCandidateGraphMayDifferFromManagerButRetainsExactCompiledIdentity(t *testing.T) {
	components := candidateGraphFixture()
	if reflect.DeepEqual(components, product.FoundationVersions()) {
		t.Fatal("fixture did not exercise an independently versioned candidate")
	}
	info, metadata := compiledCandidateFixture(components)
	if !validPackageIdentity(metadata, validDraft(), false) || validateCompiledInfo(info, metadata, true) != nil {
		t.Fatal("authenticated tagged candidate graph coupled to manager version values")
	}
	if validPackageIdentity(metadata, validDraft(), true) {
		t.Fatal("legacy installer exact-set contract changed")
	}
	for _, tc := range []struct {
		name   string
		change func(*debug.BuildInfo, *packageInfo)
	}{
		{"missing-declared", func(_ *debug.BuildInfo, m *packageInfo) { delete(m.Components, "github.com/AChWorks/achrix/admin") }},
		{"extra-declared", func(_ *debug.BuildInfo, m *packageInfo) { m.Components["github.com/AChWorks/achrix/other"] = "v1.0.0" }},
		{"development-declared", func(_ *debug.BuildInfo, m *packageInfo) {
			m.Components["github.com/AChWorks/achrix/admin"] = "development"
		}},
		{"pseudo-declared", func(_ *debug.BuildInfo, m *packageInfo) {
			m.Components["github.com/AChWorks/achrix/admin"] = "v0.1.1-0.20261030000000-abcdefabcdef"
		}},
		{"noncanonical-tag", func(_ *debug.BuildInfo, m *packageInfo) { m.Components["github.com/AChWorks/achrix/admin"] = "v01.1.0" }},
		{"invalid-prerelease", func(_ *debug.BuildInfo, m *packageInfo) {
			m.Components["github.com/AChWorks/achrix/admin"] = "v1.0.0-01"
		}},
		{"missing-compiled", func(i *debug.BuildInfo, _ *packageInfo) { i.Deps = i.Deps[1:] }},
		{"extra-compiled", func(i *debug.BuildInfo, _ *packageInfo) {
			i.Deps = append(i.Deps, &debug.Module{Path: "github.com/AChWorks/achrix/undeclared", Version: "v1.0.0"})
		}},
		{"duplicate-compiled", func(i *debug.BuildInfo, _ *packageInfo) { i.Deps = append(i.Deps, i.Deps[0]) }},
		{"replaced-component", func(i *debug.BuildInfo, _ *packageInfo) { i.Deps[0].Replace = &debug.Module{Path: "/local/runtime"} }},
		{"replaced-unrelated-dependency", func(i *debug.BuildInfo, _ *packageInfo) {
			i.Deps = append(i.Deps, &debug.Module{Path: "example.test/dependency", Version: "v1.0.0", Replace: &debug.Module{Path: "/local/dependency"}})
		}},
		{"replaced-main", func(i *debug.BuildInfo, _ *packageInfo) { i.Main.Replace = &debug.Module{Path: "/local/rixa"} }},
		{"compiled-version-mismatch", func(i *debug.BuildInfo, _ *packageInfo) { i.Deps[0].Version = "v9.9.9" }},
		{"wrong-target", func(i *debug.BuildInfo, _ *packageInfo) { i.Settings[0].Value = "windows" }},
		{"cgo", func(i *debug.BuildInfo, _ *packageInfo) { i.Settings[2].Value = "1" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			i, m := compiledCandidateFixture(candidateGraphFixture())
			tc.change(i, &m)
			if validPackageIdentity(m, validDraft(), false) && validateCompiledInfo(i, m, true) == nil {
				t.Fatal("unbound or unsupported component identity accepted")
			}
		})
	}
}
func TestCandidateCheckReceiptBindsAuthenticatedCandidateGraph(t *testing.T) {
	candidate := candidateGraphFixture()
	core := candidate["github.com/AChWorks/achrix"]
	if !validCandidateCheckReceipt("ok", 1, core, candidate, candidate) {
		t.Fatal("candidate's own readiness receipt rejected")
	}
	if validCandidateCheckReceipt("ok", 1, product.FoundationVersions()["github.com/AChWorks/achrix"], product.FoundationVersions(), candidate) {
		t.Fatal("manager graph substituted for candidate readiness receipt")
	}
	altered := candidateGraphFixture()
	delete(altered, "github.com/AChWorks/achrix/admin")
	if validCandidateCheckReceipt("ok", 1, core, altered, candidate) || validCandidateCheckReceipt("ok", 2, core, candidate, candidate) || validCandidateCheckReceipt("ok", 1, "v99.0.0", candidate, candidate) {
		t.Fatal("incomplete or retargeted candidate receipt accepted")
	}
}
func TestGenuinePublishedUpdatePayloadComponentIdentity(t *testing.T) {
	archive := os.Getenv("RIXA_NATIVE_UPDATE_SIGNED_ARCHIVE")
	if archive == "" {
		t.Skip("requires actual signed published candidate; unit fixtures are not publisher proof")
	}
	d := validDraft()
	d.ArchivePath = archive
	d.BundlePath = os.Getenv("RIXA_NATIVE_UPDATE_SIGNED_BUNDLE")
	d.Release = os.Getenv("RIXA_NATIVE_UPDATE_RELEASE")
	d.SourceCommit = os.Getenv("RIXA_NATIVE_UPDATE_SOURCE")
	verified, e := releaseartifact.Verify(context.Background(), releaseartifact.Request{ArchivePath: d.ArchivePath, BundlePath: d.BundlePath, ReleaseTag: d.Release, SourceCommit: d.SourceCommit})
	if e != nil || verified.Status != "verified" {
		t.Fatalf("actual publisher proof failed: %s %v", verified.Code, e)
	}
	out := filepath.Join(t.TempDir(), "release")
	if e = os.Mkdir(out, 0700); e != nil {
		t.Fatal(e)
	}
	hash, components, e := extractUpdateAuthenticated(archive, out, d)
	if e != nil || !hashSyntax.MatchString(hash) || !validUpdateComponents(components) {
		t.Fatalf("actual authenticated update package failed: %v", e)
	}
	if os.Getenv("RIXA_NATIVE_UPDATE_EXPECT_DIFFERENT_COMPONENTS") == "1" && reflect.DeepEqual(components, product.FoundationVersions()) {
		t.Fatal("actual signed candidate did not exercise an independent component graph")
	}
	b, e := os.ReadFile(filepath.Join(out, "BUILDINFO.json"))
	if e != nil {
		t.Fatal(e)
	}
	var metadata packageInfo
	if e = json.Unmarshal(b, &metadata); e != nil || !reflect.DeepEqual(metadata.Components, components) {
		t.Fatal("candidate graph was not its own authenticated metadata")
	}
	if e = validateCompiledProfile(filepath.Join(out, "rixa"), metadata, true); e != nil {
		t.Fatalf("actual compiled candidate identity failed: %v", e)
	}
}
