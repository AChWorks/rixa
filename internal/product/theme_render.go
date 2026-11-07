// SPDX-License-Identifier: MPL-2.0

package product

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"html/template"
	"sort"
	"strings"
	"time"

	xhtml "golang.org/x/net/html"
)

// Public metadata remains Rixa-owned even for fully custom templates.
// Trusted template source is emitted only on isolated public/preview hosts.
// Editorial BodyHTML is already publication-transformed and Media-resolved.
var themePageWrapper = template.Must(template.New("theme-public-page").Parse(`<!doctype html>
<html lang="{{.Language}}" dir="{{.Direction}}">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width,initial-scale=1">
<title>{{.Title}}</title>
{{if .Description}}<meta name="description" content="{{.Description}}">{{end}}
<meta name="robots" content="{{.Robots}}">
<link rel="canonical" href="{{.Canonical}}">
{{if .CSSURL}}<link rel="stylesheet" href="{{.CSSURL}}">{{end}}
<script type="application/ld+json">{{.Structured}}</script>
{{.Head}}
</head>
<body>
{{.Header}}
{{.Body}}
{{.Footer}}
{{if .JSURL}}<script src="{{.JSURL}}" defer></script>{{end}}
</body>
</html>`))

type themePublicPage struct {
	Language, Direction, Title, Description, Canonical, Robots string
	CSSURL, JSURL                                              string
	Structured                                                 template.JS
	Head, Header, Body, Footer                                 template.HTML
}

func renderThemeHTML(bundle ThemeBundle, appearance Appearance, origin string, page any, home *publicHomeView, cssURL, jsURL string) ([]byte, error) {
	data := themeRenderBinding{
		Site: themeSiteBinding{
			Title: appearance.SiteTitle, Description: appearance.SiteDescription,
			Language: appearance.SiteLanguage, Origin: origin, HomeURL: "/",
		},
	}
	switch appearance.SiteLanguage {
	case "fa":
		data.Site.Direction = "rtl"
	default:
		data.Site.Direction = "ltr"
	}
	var view themePublicPage
	view.Language = data.Site.Language
	view.Direction = data.Site.Direction
	view.CSSURL, view.JSURL = cssURL, jsURL
	if home != nil {
		data.Home.Items = home.Items
		view.Title = appearance.SiteTitle
		view.Description = appearance.SiteDescription
		view.Canonical = home.Canonical
		view.Robots = home.Robots
		view.Structured = home.Structured
	} else {
		details, ok := page.(publicPageView)
		if !ok {
			return nil, ErrEditorialInvalid
		}
		data.Content = &themeContentBinding{
			Kind: details.Kind, Title: details.Title, BodyHTML: details.Body,
			CanonicalURL: details.Canonical, PublishedAt: details.PublishedAt.UTC().Format(time.RFC3339),
			ModifiedAt: details.ModifiedAt.UTC().Format(time.RFC3339),
		}
		view.Title = details.Title + " · " + appearance.SiteTitle
		view.Description = details.Description
		view.Canonical = details.Canonical
		view.Robots = details.Robots
		view.Structured = details.Structured
	}
	parts := []string{bundle.HeadHTML, bundle.HeaderHTML, bundle.BodyHTML, bundle.FooterHTML}
	target := []*template.HTML{&view.Head, &view.Header, &view.Body, &view.Footer}
	for i, part := range parts {
		output, err := executeThemeSlot(part, bundle, data)
		if err != nil {
			return nil, fmt.Errorf("%w: theme slot %d: %v", ErrEditorialInvalid, i, err)
		}
		*target[i] = template.HTML(output)
	}
	if data.Content != nil && !strings.Contains(string(view.Body), string(data.Content.BodyHTML)) {
		return nil, fmt.Errorf("%w: theme body omitted published content", ErrEditorialInvalid)
	}
	out := &cappedThemeWriter{max: 3 << 20}
	if err := themePageWrapper.Execute(out, view); err != nil {
		return nil, fmt.Errorf("%w: theme page: %v", ErrEditorialInvalid, err)
	}
	return out.Bytes(), nil
}

func stageThemeAssets(publicDir string, manifest *publicationManifest, bundle ThemeBundle) (cssURL, jsURL string, size int64, err error) {
	if err = bundle.validate(); err != nil {
		return "", "", 0, err
	}
	for name, asset := range bundle.Assets {
		url := themeAssetPath(name, asset)
		relative := strings.TrimPrefix(url, "/")
		n, e := addGeneratedFile(publicDir, manifest, relative, asset.Data, asset.MIME, "public, max-age=31536000, immutable", "")
		if e != nil {
			return "", "", 0, e
		}
		size += n
		manifest.Routes[url] = publicationRoute{Kind: "file", File: relative}
	}
	add := func(filename, source, mime string) (string, error) {
		if source == "" {
			return "", nil
		}
		digest := sha256.Sum256([]byte(source))
		url := "/theme-assets/" + hex.EncodeToString(digest[:]) + "-" + filename
		relative := strings.TrimPrefix(url, "/")
		n, e := addGeneratedFile(publicDir, manifest, relative, []byte(source), mime, "public, max-age=31536000, immutable", "")
		if e != nil {
			return "", e
		}
		size += n
		manifest.Routes[url] = publicationRoute{Kind: "file", File: relative}
		return url, nil
	}
	cssSource, renderErr := resolveThemeCSS(bundle)
	if renderErr != nil {
		return "", "", 0, fmt.Errorf("%w: Theme CSS asset reference", ErrEditorialInvalid)
	}
	cssURL, err = add("site.css", cssSource, "text/css; charset=utf-8")
	if err != nil {
		return "", "", 0, err
	}
	jsURL, err = add("site.js", bundle.JavaScript, "text/javascript; charset=utf-8")
	if err != nil {
		return "", "", 0, err
	}
	return cssURL, jsURL, size, nil
}

// Only the exact static, emitted inline script source receives CSP hashes.
// Arbitrary inline event handlers remain disallowed. The JSON-LD block is
// included as data, not delegated to a caller to provide metadata.
func publicationFileWithThemeCSP(file publicationFile, page []byte) publicationFile {
	file.CSP = themeCSP(page)
	return file
}

func themeCSP(page []byte) string {
	const base = "default-src 'none'; script-src 'self'"
	scriptHashes := make(map[string]struct{})
	doc, err := xhtml.Parse(bytes.NewReader(page))
	if err == nil {
		var collect func(*xhtml.Node)
		collect = func(n *xhtml.Node) {
			if n.Type == xhtml.ElementNode && n.Data == "script" {
				for child := n.FirstChild; child != nil; child = child.NextSibling {
					if child.Type == xhtml.TextNode && child.Data != "" {
						digest := sha256.Sum256([]byte(child.Data))
						scriptHashes["'sha256-"+base64.StdEncoding.EncodeToString(digest[:])+"'"] = struct{}{}
					}
				}
			}
			for child := n.FirstChild; child != nil; child = child.NextSibling {
				collect(child)
			}
		}
		collect(doc)
	}
	hashes := make([]string, 0, len(scriptHashes))
	for hash := range scriptHashes {
		hashes = append(hashes, hash)
	}
	// A stable order is essential for deterministic manifest integrity.
	sort.Strings(hashes)
	suffix := "; style-src 'self' 'unsafe-inline'; img-src 'self'; font-src 'self'; connect-src 'self'; base-uri 'none'; form-action 'none'; frame-ancestors 'none'; object-src 'none'"
	if len(hashes) > 64 {
		return base + suffix
	}
	if len(hashes) == 0 {
		return base + suffix
	}
	return base + " " + strings.Join(hashes, " ") + suffix
}
