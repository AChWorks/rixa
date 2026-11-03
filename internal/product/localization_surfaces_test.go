// SPDX-License-Identifier: MPL-2.0

package product

import (
	"bytes"
	"html/template"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/AChWorks/achrix/admin/localize"
)

func TestProductSurfaceTemplatesUseCatalogsAndEscapeData(t *testing.T) {
	for _, language := range []string{"en", "fa"} {
		t.Run(language, func(t *testing.T) {
			locale := testProductLocalizer(t, language)
			unsafe := `<script>alert("unsafe")</script>`
			views := []struct {
				view *template.Template
				data any
			}{
				{inventoryTemplate, inventoryView{Locale: locale, Sites: []inventoryItem{{ID: unsafe, Origin: unsafe, CanManage: true}, {ID: "disabled", Disabled: true}}}},
				{accountCreateTemplate, accountCreateView{Locale: locale, Site: inventoryItem{ID: unsafe, Origin: unsafe}}},
				{contentIndexTemplate, contentIndexView{Locale: locale, Count: 2, Items: []ContentSummary{{Title: unsafe, Kind: ContentPost}, {Title: unsafe, Kind: ContentPage}}}},
				{contentEditTemplate, contentEditView{Locale: locale, Revision: ContentRevision{Title: unsafe, Kind: ContentPost}, Publication: PublicationState{Enabled: true}, History: []RevisionSummary{{Title: unsafe}}}},
				{contentPreviewTemplate, contentPreviewView{Locale: locale, Revision: ContentRevision{Title: unsafe}, Appearance: Appearance{SiteTitle: unsafe, SiteLanguage: "fa", HeaderShowTitle: true, HeaderTagline: unsafe, FooterText: unsafe}, ContentDirection: "rtl"}},
				{appearanceTemplate, appearanceView{Locale: locale, ContentLocales: []localize.Locale{{Tag: "en", Name: "English", Direction: "ltr"}, {Tag: "fa", Name: "فارسی", Direction: "rtl"}}, Appearance: Appearance{SiteTitle: unsafe, SiteDescription: unsafe, SiteLanguage: "fa", HeaderTagline: unsafe, FooterText: unsafe}, Publication: PublicationState{Enabled: true}}},
			}
			for _, current := range views {
				var output bytes.Buffer
				if err := current.view.ExecuteTemplate(&output, "content", current.data); err != nil {
					t.Fatalf("%s: %v", current.view.Name(), err)
				}
				if strings.Contains(output.String(), "<script>alert") || !strings.Contains(output.String(), "&lt;script&gt;") {
					t.Fatalf("%s did not escape untrusted content", current.view.Name())
				}
			}
		})
	}
}

func TestProductCatalogFallbackPluralAndNamedMessages(t *testing.T) {
	third := fstest.MapFS{"third.json": &fstest.MapFile{Data: []byte(`{"schema":1,"locale":"de","name":"Deutsch","direction":"ltr","domain":"rixa","messages":{"formatting":{"other":"<script>unsafe</script>"}}}`)}}
	catalog, err := localize.Load(bundledProductCatalogs(), third)
	if err != nil {
		t.Fatal(err)
	}
	locale, err := catalog.For("de")
	if err != nil {
		t.Fatal(err)
	}
	// English fallback uses English plural rules for a wholly missing message.
	for count, want := range map[int64]string{1: "1 saved item", 2: "2 saved items"} {
		got, err := locale.Plural("rixa", "saved_content_count", count, nil)
		if err != nil || got != want {
			t.Fatalf("plural(%d)=%q, %v", count, got, err)
		}
	}
	var output bytes.Buffer
	if err = contentEditTemplate.ExecuteTemplate(&output, "content", contentEditView{Locale: locale}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output.String(), "&lt;script&gt;unsafe&lt;/script&gt;") || strings.Contains(output.String(), "<script>unsafe") || !strings.Contains(output.String(), "Save new revision") {
		t.Fatal("installed translation escaped text or English fallback failed")
	}
	fa, err := catalog.For("fa")
	if err != nil {
		t.Fatal(err)
	}
	got, err := fa.Text("rixa", "outcome_unknown", map[string]string{"Operation": "id-123"})
	if err != nil || !strings.Contains(got, "id-123") || strings.Contains(got, "{Operation}") {
		t.Fatalf("named operation message: %q, %v", got, err)
	}
	if _, err = fa.Text("rixa", "outcome_unknown", nil); err == nil {
		t.Fatal("missing required operation parameter was silently hidden")
	}
}

// The inventory is not an account-editor grid. Only a deliberately selected
// authorized site gets an account form, using the existing Control endpoint.
func TestControlAccountTemplatesSelectSingleSite(t *testing.T) {
	for _, language := range []string{"en", "fa"} {
		t.Run(language, func(t *testing.T) {
			locale := testProductLocalizer(t, language)
			var inventory bytes.Buffer
			err := inventoryTemplate.ExecuteTemplate(&inventory, "content", inventoryView{
				Locale: locale,
				Sites: []inventoryItem{
					{ID: "site-a", CanManage: true},
					{ID: "disabled", Disabled: true},
					{ID: "site-b", CanManage: false},
				},
			})
			if err != nil {
				t.Fatal(err)
			}
			html := inventory.String()
			if strings.Count(html, "/admin/sites/account-create/") != 1 ||
				!strings.Contains(html, "/account-create/site-a") ||
				strings.Contains(html, "/account-create/disabled") ||
				strings.Contains(html, "/account-create/site-b") ||
				strings.Contains(html, "action=\"/admin/sites/account-create\"") {
				t.Fatal("inventory exposed extra account controls")
			}
			var form bytes.Buffer
			if err := accountCreateTemplate.ExecuteTemplate(&form, "content", accountCreateView{
				Locale: locale, Site: inventoryItem{ID: "site-a"},
			}); err != nil {
				t.Fatal(err)
			}
			body := form.String()
			if strings.Count(body, "name=\"site_id\"") != 1 ||
				!strings.Contains(body, "value=\"site-a\"") ||
				!strings.Contains(body, "autocomplete=\"new-password\"") ||
				strings.Contains(body, "autofocus") {
				t.Fatal("selected-site form has unsafe scope or credential behavior")
			}
		})
	}
}
