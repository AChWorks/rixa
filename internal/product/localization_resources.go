// SPDX-License-Identifier: MPL-2.0

package product

import (
	"embed"
	"io/fs"

	shell "github.com/AChWorks/achrix/admin"
)

//go:embed locales/*.json
var productCatalogFiles embed.FS

// bundledProductCatalogs exposes only product-owned locale data. Compose it once
// per catalog instance, alongside shared and explicitly installed resources.
func bundledProductCatalogs() fs.FS {
	source, _ := fs.Sub(productCatalogFiles, "locales")
	return source
}

func renderProductPage(view shell.Request, key string, page shell.Page) error {
	title, err := view.Localizer.Text("rixa", key, nil)
	if err != nil {
		return err
	}
	page.Title = title
	return view.Render(page)
}
