// SPDX-License-Identifier: MPL-2.0

package product

import (
	"bytes"
	"html/template"
	"strings"
	"testing"
)

func TestContentEditorPersianMixedDirectionSurface(t *testing.T) {
	body, _, err := canonicalBody(`<p>سلام English 123</p><blockquote>متن mixed</blockquote>`)
	if err != nil {
		t.Fatal(err)
	}
	trusted, err := trustedEditorialHTML(body)
	if err != nil {
		t.Fatal(err)
	}
	data := contentEditView{
		Language: "fa",
		Revision: ContentRevision{
			ID:                       "AAAAAAAAAAAAAAAAAAAAAAAAAA",
			Kind:                     ContentPost,
			Revision:                 2,
			Title:                    "عنوان English",
			BodyHTML:                 body,
			PublicationIntentRevision: 2,
			PublicationIntentVersion: 3,
		},
		History:     []RevisionSummary{{Revision: 2, Title: "عنوان English", Actor: "actor"}},
		Body: trusted,
		Publication: PublicationState{
			Enabled: true, Generation: "CCCCCCCCCCCCCCCCCCCCCCCCCC",
			DesiredRoute: "/posts/aaaaaaaaaaaaaaaaaaaaaaaaaa/",
			ActiveRevision: 1,
		},
		OperationID: "BBBBBBBBBBBBBBBBBBBBBBBBBB",
	}
	var output bytes.Buffer
	if err = contentEditTemplate.ExecuteTemplate(&output, "content", data); err != nil {
		t.Fatal(err)
	}
	page := output.String()
	for _, expected := range []string{
		`contenteditable="true"`, `aria-multiline="true"`, `dir="auto"`,
		`قالب‌بندی`, `پیش‌نمایش خصوصی همین نسخه`, `بررسی نتیجهٔ عملیات`,
		`name="expected_publication_version" value="3"`,
		`action="/admin/content/publish"`,
		`data-publication-intent-output`,
		`data-publication-generation-output`,
		`name="expected_generation" value="CCCCCCCCCCCCCCCCCCCCCCCCCC"`,
		`<p dir="auto">سلام English 123</p>`,
	} {
		if !strings.Contains(page, expected) {
			t.Fatalf("editor page missing %q", expected)
		}
	}
	if strings.Contains(page, "StorageRoot") || strings.Contains(page, "/var/lib/") {
		t.Fatal("editor exposed a private storage path")
	}
}

func TestPrivatePreviewUsesSiteLanguageWithoutInliningPrivateMedia(t *testing.T) {
	body := template.HTML(`<p dir="auto">سلام English</p><figure data-media-id="AAAAAAAAAAAAAAAAAAAAAAAAAA"><figcaption dir="auto">تصویر</figcaption></figure>`)
	data := contentPreviewView{
		Language: "en",
		Revision: ContentRevision{ID: "BBBBBBBBBBBBBBBBBBBBBBBBBB", Revision: 3, Title: "Mixed عنوان"},
		Body:     body,
		Appearance: Appearance{
			SiteTitle: "نمونه", SiteLanguage: "fa", HeaderShowTitle: true,
			HeaderTagline: "Hello سلام", FooterText: "پابرگ", Theme: "dark",
		},
	}
	var output bytes.Buffer
	if err := contentPreviewTemplate.ExecuteTemplate(&output, "content", data); err != nil {
		t.Fatal(err)
	}
	page := output.String()
	for _, expected := range []string{`lang="fa" dir="rtl"`, `data-theme="dark"`, `data-media-id="AAAAAAAAAAAAAAAAAAAAAAAAAA"`, `Media references intentionally do not inline private originals`} {
		if !strings.Contains(page, expected) {
			t.Fatalf("preview missing %q", expected)
		}
	}
	if strings.Contains(page, "<img") || strings.Contains(page, "src=\"") {
		t.Fatal("private preview inlined an asset")
	}
}

func TestEditorialClientAssetsKeepMutationRecoveryAndPasteBoundary(t *testing.T) {
	for _, expected := range []string{`addEventListener("paste"`, `getData("text/plain")`, `addEventListener("drop"`, `function safeLink`, `\u202a-\u202e`, `data-publication-generation-output`} {
		if !strings.Contains(contentEditorJS, expected) && !strings.Contains(contentIndexTemplate.Tree.Root.String(), expected) {
			t.Fatalf("editor client missing %q", expected)
		}
	}
	if !strings.Contains(appearanceJS, `publicationForm.querySelector("[data-operation-field]")`) {
		t.Fatal("appearance publication form does not rotate its initial operation identity")
	}
	for _, forbidden := range []string{"localStorage", "sessionStorage", "setInterval("} {
		if strings.Contains(contentEditorJS, forbidden) || strings.Contains(appearanceJS, forbidden) {
			t.Fatalf("client asset unexpectedly uses %q", forbidden)
		}
	}
}

func TestAppearanceSurfaceContainsOnlyContractFields(t *testing.T) {
	data := appearanceView{
		Language: "en",
		Appearance: Appearance{
			Revision: 1, SiteTitle: "site-a", SiteLanguage: "en", HomeMode: "latest_posts",
			HeaderShowTitle: true, Theme: "light",
		},
		Publication: PublicationState{Enabled: true, Generation: "DDDDDDDDDDDDDDDDDDDDDDDDDD"},
		OperationID: "AAAAAAAAAAAAAAAAAAAAAAAAAA",
	}
	var output bytes.Buffer
	if err := appearanceTemplate.ExecuteTemplate(&output, "content", data); err != nil {
		t.Fatal(err)
	}
	page := output.String()
	for _, field := range []string{"site_title", "site_description", "site_language", "home_mode", "home_page_id", "header_show_title", "header_tagline", "footer_text", "theme", "expected_head", "expected_generation", "operation_id"} {
		if !strings.Contains(page, `name="`+field+`"`) {
			t.Fatalf("appearance field %q missing", field)
		}
	}
	for _, forbidden := range []string{"database", "dsn", "secret", "storage_root"} {
		if strings.Contains(strings.ToLower(page), forbidden) {
			t.Fatalf("appearance leaked deployment field %q", forbidden)
		}
	}
}
