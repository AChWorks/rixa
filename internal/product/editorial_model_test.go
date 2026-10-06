// SPDX-License-Identifier: MPL-2.0

package product

import (
	"errors"
	"strings"
	"testing"
)

func TestCanonicalBodyBoundedSemanticProfile(t *testing.T) {
	assetA := "AAAAAAAAAAAAAAAAAAAAAAAAAA"
	assetB := "BBBBBBBBBBBBBBBBBBBBBBBBBB"
	input := `<div>سلام English <b>bold</b> <a href="/about?x=1#top">link</a></div>` +
		`<figure data-media-id="` + assetB + `"><figcaption dir="auto">تصویر دوم</figcaption></figure>` +
		`<figure data-media-id="` + assetA + `"><figcaption>First image</figcaption></figure>` +
		`<figure data-media-id="` + assetB + `"><figcaption>duplicate reference</figcaption></figure>`

	body, refs, err := canonicalBody(input)
	if err != nil {
		t.Fatal(err)
	}
	for _, expected := range []string{
		`<p dir="auto">سلام English <strong>bold</strong> <a href="/about?x=1#top">link</a></p>`,
		`<figure data-media-id="` + assetA + `">`,
		`<figure data-media-id="` + assetB + `">`,
		`<figcaption dir="auto">تصویر دوم</figcaption>`,
	} {
		if !strings.Contains(body, expected) {
			t.Fatalf("canonical body missing %q: %s", expected, body)
		}
	}
	if len(refs) != 2 || refs[0] != assetA || refs[1] != assetB {
		t.Fatalf("media refs = %#v", refs)
	}

	second, secondRefs, err := canonicalBody(body)
	if err != nil {
		t.Fatal(err)
	}
	if second != body {
		t.Fatalf("canonicalization is not idempotent:\nfirst: %s\nsecond: %s", body, second)
	}
	if strings.Join(secondRefs, ",") != strings.Join(refs, ",") {
		t.Fatalf("canonical media refs changed: %#v -> %#v", refs, secondRefs)
	}
}

func TestCanonicalBodyRejectsActiveOrAmbiguousMarkup(t *testing.T) {
	asset := "AAAAAAAAAAAAAAAAAAAAAAAAAA"
	caption := strings.Repeat("x", maxMediaCaptionRunes+1)
	tests := []string{
		`<script>alert(1)</script>`,
		`<p onclick="alert(1)">x</p>`,
		`<p style="direction:rtl">x</p>`,
		`<img src="/private/original.png">`,
		`<iframe src="https://example.com"></iframe>`,
		`<a href="javascript:alert(1)">x</a>`,
		`<a href="http://example.com">x</a>`,
		`<a href="//example.com">x</a>`,
		`<p dir="rtl">spoof</p>`,
		"<p>abc\u202edef</p>",
		`<figure data-media-id="not-an-id"><figcaption>x</figcaption></figure>`,
		`<figure data-media-id="` + asset + `"><strong>not caption text</strong></figure>`,
		`<figure data-media-id="` + asset + `"><figcaption><strong>rich caption</strong></figcaption></figure>`,
		`<figure data-media-id="` + asset + `"><figcaption>` + caption + `</figcaption></figure>`,
	}
	for _, input := range tests {
		if _, _, err := canonicalBody(input); !errors.Is(err, ErrEditorialInvalid) {
			t.Errorf("canonicalBody(%q) error = %v, want ErrEditorialInvalid", input, err)
		}
	}
}

func TestCanonicalBodyBounds(t *testing.T) {
	if _, _, err := canonicalBody(strings.Repeat("x", maxEditorialBodyBytes+1)); !errors.Is(err, ErrEditorialInvalid) {
		t.Fatalf("oversized body error = %v", err)
	}

	var body strings.Builder
	for range maxEditorialNodes + 1 {
		body.WriteString("<p>x</p>")
	}
	if _, _, err := canonicalBody(body.String()); !errors.Is(err, ErrEditorialInvalid) {
		t.Fatalf("node-heavy body error = %v", err)
	}
}

func TestEditorialTextAndAppearanceValidation(t *testing.T) {
	for _, value := range []string{"سلام دنیا", "English فارسی 123", "A simple title"} {
		if !validContentTitle(value) {
			t.Errorf("valid title rejected: %q", value)
		}
	}
	for _, value := range []string{"", " leading", "trailing ", "bad\nline", "abc\u202edef"} {
		if validContentTitle(value) {
			t.Errorf("invalid title accepted: %q", value)
		}
	}

	valid := AppearanceInput{
		SiteTitle:       "سایت Example",
		SiteDescription: "A small bilingual site",
		SiteLanguage:    "fa",
		HomeMode:        "latest_posts",
		HeaderShowTitle: true,
		HeaderTagline:   "سلام world",
		FooterText:      "Footer",
		Theme:           "dark",
	}
	if err := valid.validateShape(); err != nil {
		t.Fatalf("valid appearance rejected: %v", err)
	}
	invalid := valid
	invalid.SiteLanguage = "ar"
	if !errors.Is(invalid.validateShape(), ErrEditorialInvalid) {
		t.Fatal("unsupported site language accepted")
	}
	invalid = valid
	invalid.HomeMode = "page"
	if !errors.Is(invalid.validateShape(), ErrEditorialInvalid) {
		t.Fatal("page home without page ID accepted")
	}
	invalid.HomePageID = "AAAAAAAAAAAAAAAAAAAAAAAAAA"
	if err := invalid.validateShape(); err != nil {
		t.Fatalf("valid page-shaped appearance rejected: %v", err)
	}
}

func TestOperationFingerprintBindsExactIntent(t *testing.T) {
	refs := []ContentMediaRef{{AssetID: "AAAAAAAAAAAAAAAAAAAAAAAAAA", AssetRevision: 2}}
	base := contentRequestHash("content.save", "actor", "BBBBBBBBBBBBBBBBBBBBBBBBBB", 1, "title", `<p dir="auto">body</p>`, refs)
	if len(base) != 64 {
		t.Fatalf("fingerprint length = %d", len(base))
	}
	if base != contentRequestHash("content.save", "actor", "BBBBBBBBBBBBBBBBBBBBBBBBBB", 1, "title", `<p dir="auto">body</p>`, refs) {
		t.Fatal("same intent produced different fingerprint")
	}
	changes := []string{
		contentRequestHash("content.save", "other", "BBBBBBBBBBBBBBBBBBBBBBBBBB", 1, "title", `<p dir="auto">body</p>`, refs),
		contentRequestHash("content.save", "actor", "BBBBBBBBBBBBBBBBBBBBBBBBBB", 2, "title", `<p dir="auto">body</p>`, refs),
		contentRequestHash("content.save", "actor", "BBBBBBBBBBBBBBBBBBBBBBBBBB", 1, "other", `<p dir="auto">body</p>`, refs),
		contentRequestHash("content.save", "actor", "BBBBBBBBBBBBBBBBBBBBBBBBBB", 1, "title", `<p dir="auto">other</p>`, refs),
		contentRequestHash("content.save", "actor", "BBBBBBBBBBBBBBBBBBBBBBBBBB", 1, "title", `<p dir="auto">body</p>`, []ContentMediaRef{{AssetID: refs[0].AssetID, AssetRevision: 3}}),
	}
	for _, changed := range changes {
		if changed == base {
			t.Fatal("different intent reused fingerprint")
		}
	}
}
