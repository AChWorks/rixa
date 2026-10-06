// SPDX-License-Identifier: MPL-2.0

package product

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"encoding/xml"
	"html/template"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	xhtml "golang.org/x/net/html"
	"golang.org/x/net/html/atom"
)

const generatedFileCache = "public, max-age=60, must-revalidate"

type publicPageView struct {
	Language    string
	Direction   string
	Theme       string
	SiteTitle   string
	SiteDescription string
	ShowSiteTitle bool
	HeaderTagline string
	FooterText  string
	Title       string
	Description string
	Canonical   string
	Robots      string
	PublishedAt time.Time
	ModifiedAt  time.Time
	Body        template.HTML
	Structured  template.JS
}

type publicHomeItem struct {
	Title string
	URL   string
}

type publicHomeView struct {
	Language       string
	Direction      string
	Theme          string
	SiteTitle      string
	SiteDescription string
	ShowSiteTitle  bool
	HeaderTagline  string
	FooterText     string
	Canonical      string
	Robots         string
	Items          []publicHomeItem
	Structured     template.JS
}

var publicPageTemplate = template.Must(template.New("public-page").Parse(`<!doctype html>
<html lang="{{.Language}}" dir="{{.Direction}}" data-theme="{{.Theme}}">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width,initial-scale=1">
<title>{{.Title}} · {{.SiteTitle}}</title>
{{if .Description}}<meta name="description" content="{{.Description}}">{{end}}
<meta name="robots" content="{{.Robots}}">
<link rel="canonical" href="{{.Canonical}}">
<style>body{font-family:system-ui,sans-serif;line-height:1.6;max-width:72rem;margin:auto;padding:1.25rem}img{max-width:100%;height:auto}header,footer{padding-block:1rem}article{max-width:52rem}html[data-theme="dark"]{color-scheme:dark;background:#171717;color:#f2f2f2}html[data-theme="light"]{color-scheme:light;background:#fff;color:#111}.rixa-meta{font-size:.9em}</style>
<script type="application/ld+json">{{.Structured}}</script>
</head>
<body>
<header>{{if .ShowSiteTitle}}<p><strong><a href="/">{{.SiteTitle}}</a></strong></p>{{end}}{{if .HeaderTagline}}<p dir="auto">{{.HeaderTagline}}</p>{{end}}</header>
<main><article>
<h1 dir="auto">{{.Title}}</h1>
<p class="rixa-meta"><span>{{if eq .Language "fa"}}انتشار{{else}}Published{{end}} <time datetime="{{.PublishedAt.Format "2006-01-02T15:04:05Z07:00"}}">{{.PublishedAt.Format "2006-01-02"}}</time></span> · <span>{{if eq .Language "fa"}}به‌روزرسانی{{else}}Updated{{end}} <time datetime="{{.ModifiedAt.Format "2006-01-02T15:04:05Z07:00"}}">{{.ModifiedAt.Format "2006-01-02"}}</time></span></p>
<div>{{.Body}}</div>
</article></main>
{{if .FooterText}}<footer dir="auto">{{.FooterText}}</footer>{{end}}
</body></html>`))

var publicHomeTemplate = template.Must(template.New("public-home").Parse(`<!doctype html>
<html lang="{{.Language}}" dir="{{.Direction}}" data-theme="{{.Theme}}">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width,initial-scale=1">
<title>{{.SiteTitle}}</title>
{{if .SiteDescription}}<meta name="description" content="{{.SiteDescription}}">{{end}}
<meta name="robots" content="{{.Robots}}">
<link rel="canonical" href="{{.Canonical}}">
<style>body{font-family:system-ui,sans-serif;line-height:1.6;max-width:72rem;margin:auto;padding:1.25rem}header,footer{padding-block:1rem}main{max-width:52rem}html[data-theme="dark"]{color-scheme:dark;background:#171717;color:#f2f2f2}html[data-theme="light"]{color-scheme:light;background:#fff;color:#111}</style>
<script type="application/ld+json">{{.Structured}}</script>
</head>
<body>
<header>{{if .ShowSiteTitle}}<h1 dir="auto">{{.SiteTitle}}</h1>{{end}}{{if .HeaderTagline}}<p dir="auto">{{.HeaderTagline}}</p>{{end}}{{if .SiteDescription}}<p dir="auto">{{.SiteDescription}}</p>{{end}}</header>
<main>{{if .ShowSiteTitle}}<h2>{{if eq .Language "fa"}}آخرین نوشته‌ها{{else}}Latest posts{{end}}</h2>{{else}}<h1>{{if eq .Language "fa"}}آخرین نوشته‌ها{{else}}Latest posts{{end}}</h1>{{end}}{{if .Items}}<ol>{{range .Items}}<li><a href="{{.URL}}" dir="auto">{{.Title}}</a></li>{{end}}</ol>{{else}}<p>{{if eq .Language "fa"}}هنوز نوشته‌ای منتشر نشده است.{{else}}No published posts.{{end}}</p>{{end}}</main>
{{if .FooterText}}<footer dir="auto">{{.FooterText}}</footer>{{end}}
</body></html>`))

func renderPublication(publicDir string, manifest *publicationManifest, snapshot publicationSnapshot, assets map[string]publicationAsset) (int64, error) {
	if manifest == nil {
		return 0, ErrEditorialUnavailable
	}
	contentDir := filepath.Join(publicDir, "content")
	if err := os.Mkdir(contentDir, 0o700); err != nil {
		return 0, ErrEditorialUnavailable
	}

	contents := make(map[string]ContentRevision, len(snapshot.Contents))
	for _, content := range snapshot.Contents {
		contents[content.ID] = content
	}
	ids := make([]string, 0, len(contents))
	for id := range contents {
		ids = append(ids, id)
	}
	sort.Strings(ids)

	var total int64
	for _, id := range ids {
		content := contents[id]
		entry := manifest.Entries[id]
		body, err := publicEditorialHTML(content.BodyHTML, assets)
		if err != nil {
			return 0, err
		}
		sourceKey := publicationEntrySourceKey(entry, content, assets)
		if entry.SourceKey != sourceKey || entry.ModifiedAt.IsZero() {
			entry.ModifiedAt = manifest.CreatedAt
		}
		entry.SourceKey = sourceKey
		manifest.Entries[id] = entry

		structured, err := structuredContentJSON(manifest, snapshot.Appearance, entry, content)
		if err != nil {
			return 0, err
		}
		view := publicPageView{
			Language: snapshot.Appearance.SiteLanguage,
			Direction: publicationDirection(snapshot.Appearance.SiteLanguage),
			Theme: snapshot.Appearance.Theme,
			SiteTitle: snapshot.Appearance.SiteTitle,
			SiteDescription: snapshot.Appearance.SiteDescription,
			ShowSiteTitle: snapshot.Appearance.HeaderShowTitle,
			HeaderTagline: snapshot.Appearance.HeaderTagline,
			FooterText: snapshot.Appearance.FooterText,
			Title: content.Title,
			Description: publicDescription(content.BodyHTML),
			Canonical: manifest.Origin + entry.CanonicalRoute,
			Robots: publicRobotsDirective(manifest.Policy),
			PublishedAt: entry.FirstPublishedAt,
			ModifiedAt: entry.ModifiedAt,
			Body: body,
			Structured: template.JS(structured),
		}
		var page bytes.Buffer
		if err = publicPageTemplate.Execute(&page, view); err != nil {
			return 0, ErrEditorialUnavailable
		}
		n, err := addGeneratedFile(publicDir, manifest, entry.File, page.Bytes(), "text/html; charset=utf-8", generatedFileCache, view.Robots)
		if err != nil {
			return 0, err
		}
		total += n
	}

	if snapshot.Appearance.HomeMode == "latest_posts" {
		items := make([]ContentRevision, 0)
		for _, content := range snapshot.Contents {
			if content.Kind == ContentPost {
				items = append(items, content)
			}
		}
		sort.Slice(items, func(i, j int) bool {
			if items[i].CreatedAt.Equal(items[j].CreatedAt) {
				return items[i].ID < items[j].ID
			}
			return items[i].CreatedAt.After(items[j].CreatedAt)
		})
		homeItems := make([]publicHomeItem, 0, len(items))
		for _, content := range items {
			entry := manifest.Entries[content.ID]
			homeItems = append(homeItems, publicHomeItem{Title: content.Title, URL: entry.CanonicalRoute})
		}
		structured, err := structuredHomeJSON(manifest, snapshot.Appearance)
		if err != nil {
			return 0, err
		}
		view := publicHomeView{
			Language: snapshot.Appearance.SiteLanguage,
			Direction: publicationDirection(snapshot.Appearance.SiteLanguage),
			Theme: snapshot.Appearance.Theme,
			SiteTitle: snapshot.Appearance.SiteTitle,
			SiteDescription: snapshot.Appearance.SiteDescription,
			ShowSiteTitle: snapshot.Appearance.HeaderShowTitle,
			HeaderTagline: snapshot.Appearance.HeaderTagline,
			FooterText: snapshot.Appearance.FooterText,
			Canonical: manifest.Origin + "/",
			Robots: publicRobotsDirective(manifest.Policy),
			Items: homeItems,
			Structured: template.JS(structured),
		}
		var home bytes.Buffer
		if err = publicHomeTemplate.Execute(&home, view); err != nil {
			return 0, ErrEditorialUnavailable
		}
		n, err := addGeneratedFile(publicDir, manifest, "home.html", home.Bytes(), "text/html; charset=utf-8", generatedFileCache, view.Robots)
		if err != nil {
			return 0, err
		}
		total += n
	}

	if err := syncDir(contentDir); err != nil {
		return 0, ErrEditorialUnavailable
	}

	for _, asset := range assets {
		if _, exists := manifest.Files[asset.File]; exists {
			continue
		}
		manifest.Files[asset.File] = publicationFile{
			Path: asset.File, ContentType: asset.MIME,
			Cache: generatedFileCache,
			Size: asset.Size, SHA256: asset.SHA256,
		}
	}

	sitemap, err := publicationSitemap(manifest)
	if err != nil {
		return 0, err
	}
	n, err := addGeneratedFile(publicDir, manifest, "sitemap.xml", sitemap, "application/xml; charset=utf-8", generatedFileCache, "")
	if err != nil {
		return 0, err
	}
	total += n

	robots := publicationRobots(manifest)
	n, err = addGeneratedFile(publicDir, manifest, "robots.txt", []byte(robots), "text/plain; charset=utf-8", generatedFileCache, "")
	if err != nil {
		return 0, err
	}
	total += n
	return total, nil
}

func publicationEntrySourceKey(entry publicationEntry, content ContentRevision, assets map[string]publicationAsset) string {
	parts := []string{
		"rixa.publication.page.v1",
		content.ID, string(content.Kind), strconv.FormatInt(content.Revision, 10),
		content.Title, content.BodyHTML,
		entry.DesiredRoute, entry.CanonicalRoute,
	}
	for _, ref := range content.Media {
		asset := assets[ref.AssetID]
		parts = append(parts, ref.AssetID, strconv.FormatInt(ref.AssetRevision, 10), asset.SHA256)
	}
	return operationRequestHash(parts...)
}

func publicEditorialHTML(body string, assets map[string]publicationAsset) (template.HTML, error) {
	canonical, _, err := canonicalBody(body)
	if err != nil || canonical != body {
		return "", ErrEditorialInvalid
	}
	contextNode := &xhtml.Node{Type: xhtml.ElementNode, DataAtom: atom.Div, Data: "div"}
	nodes, err := xhtml.ParseFragment(strings.NewReader(body), contextNode)
	if err != nil {
		return "", ErrEditorialInvalid
	}
	var output bytes.Buffer
	for _, node := range nodes {
		clean, transformErr := publicEditorialNode(node, assets)
		if transformErr != nil {
			return "", transformErr
		}
		if clean == nil {
			continue
		}
		if err = xhtml.Render(&output, clean); err != nil {
			return "", ErrEditorialUnavailable
		}
	}
	return template.HTML(output.String()), nil
}

func publicEditorialNode(node *xhtml.Node, assets map[string]publicationAsset) (*xhtml.Node, error) {
	if node.Type == xhtml.TextNode {
		return &xhtml.Node{Type: xhtml.TextNode, Data: node.Data}, nil
	}
	if node.Type != xhtml.ElementNode {
		return nil, ErrEditorialInvalid
	}
	if node.Data == "figure" {
		if len(node.Attr) != 1 || node.Attr[0].Key != "data-media-id" {
			return nil, ErrEditorialInvalid
		}
		asset, ok := assets[node.Attr[0].Val]
		if !ok {
			return nil, ErrEditorialConflict
		}
		caption := ""
		for child := node.FirstChild; child != nil; child = child.NextSibling {
			if child.Type == xhtml.ElementNode && child.Data == "figcaption" {
				for text := child.FirstChild; text != nil; text = text.NextSibling {
					if text.Type != xhtml.TextNode {
						return nil, ErrEditorialInvalid
					}
					caption += text.Data
				}
			} else if child.Type != xhtml.TextNode || strings.TrimSpace(child.Data) != "" {
				return nil, ErrEditorialInvalid
			}
		}
		if !validPlainText(caption, maxMediaCaptionRunes, true) {
			return nil, ErrEditorialInvalid
		}
		figure := &xhtml.Node{Type: xhtml.ElementNode, DataAtom: atom.Figure, Data: "figure"}
		image := &xhtml.Node{Type: xhtml.ElementNode, DataAtom: atom.Img, Data: "img", Attr: []xhtml.Attribute{
			{Key: "src", Val: asset.PublicPath},
			{Key: "alt", Val: caption},
			{Key: "width", Val: strconv.Itoa(asset.Width)},
			{Key: "height", Val: strconv.Itoa(asset.Height)},
			{Key: "loading", Val: "lazy"},
			{Key: "decoding", Val: "async"},
		}}
		figure.AppendChild(image)
		figcaption := &xhtml.Node{Type: xhtml.ElementNode, DataAtom: atom.Figcaption, Data: "figcaption", Attr: []xhtml.Attribute{{Key: "dir", Val: "auto"}}}
		figcaption.AppendChild(&xhtml.Node{Type: xhtml.TextNode, Data: caption})
		figure.AppendChild(figcaption)
		return figure, nil
	}
	copyNode := &xhtml.Node{Type: xhtml.ElementNode, DataAtom: node.DataAtom, Data: node.Data, Attr: append([]xhtml.Attribute(nil), node.Attr...)}
	for child := node.FirstChild; child != nil; child = child.NextSibling {
		transformed, err := publicEditorialNode(child, assets)
		if err != nil {
			return nil, err
		}
		if transformed != nil {
			copyNode.AppendChild(transformed)
		}
	}
	return copyNode, nil
}

func publicDescription(body string) string {
	contextNode := &xhtml.Node{Type: xhtml.ElementNode, DataAtom: atom.Div, Data: "div"}
	nodes, err := xhtml.ParseFragment(strings.NewReader(body), contextNode)
	if err != nil {
		return ""
	}
	var parts []string
	var walk func(*xhtml.Node)
	walk = func(node *xhtml.Node) {
		if node.Type == xhtml.TextNode {
			if value := strings.TrimSpace(node.Data); value != "" {
				parts = append(parts, value)
			}
		}
		for child := node.FirstChild; child != nil; child = child.NextSibling {
			walk(child)
		}
	}
	for _, node := range nodes {
		walk(node)
	}
	value := strings.Join(strings.Fields(strings.Join(parts, " ")), " ")
	if utf8.RuneCountInString(value) <= 160 {
		return value
	}
	runes := []rune(value)
	return strings.TrimSpace(string(runes[:157])) + "…"
}

func structuredContentJSON(manifest *publicationManifest, appearance Appearance, entry publicationEntry, content ContentRevision) (string, error) {
	contentType := "WebPage"
	if content.Kind == ContentPost {
		contentType = "BlogPosting"
	}
	canonical := manifest.Origin + entry.CanonicalRoute
	primary := map[string]any{
		"@type": contentType,
		"headline": content.Title,
		"url": canonical,
		"inLanguage": appearance.SiteLanguage,
		"datePublished": entry.FirstPublishedAt.UTC().Format(time.RFC3339),
		"dateModified": entry.ModifiedAt.UTC().Format(time.RFC3339),
	}
	breadcrumbItems := []map[string]any{{
		"@type": "ListItem", "position": 1, "name": appearance.SiteTitle, "item": manifest.Origin + "/",
	}}
	if entry.CanonicalRoute != "/" {
		breadcrumbItems = append(breadcrumbItems, map[string]any{
			"@type": "ListItem", "position": 2, "name": content.Title, "item": canonical,
		})
	}
	graph := []any{primary, map[string]any{
		"@type": "BreadcrumbList",
		"itemListElement": breadcrumbItems,
	}}
	value := map[string]any{"@context": "https://schema.org", "@graph": graph}
	encoded, err := json.Marshal(value)
	if err != nil {
		return "", ErrEditorialUnavailable
	}
	return string(encoded), nil
}

func structuredHomeJSON(manifest *publicationManifest, appearance Appearance) (string, error) {
	value := map[string]any{
		"@context": "https://schema.org",
		"@type": "WebSite",
		"name": appearance.SiteTitle,
		"url": manifest.Origin + "/",
		"inLanguage": appearance.SiteLanguage,
	}
	if appearance.SiteDescription != "" {
		value["description"] = appearance.SiteDescription
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		return "", ErrEditorialUnavailable
	}
	return string(encoded), nil
}

func publicationSitemap(manifest *publicationManifest) ([]byte, error) {
	type location struct {
		Loc     string `xml:"loc"`
		LastMod string `xml:"lastmod,omitempty"`
	}
	type urlset struct {
		XMLName xml.Name   `xml:"urlset"`
		XMLNS   string     `xml:"xmlns,attr"`
		URLs    []location `xml:"url"`
	}
	seen := make(map[string]struct{})
	values := make([]location, 0)
	add := func(route string, modified time.Time) {
		if _, exists := seen[route]; exists {
			return
		}
		seen[route] = struct{}{}
		item := location{Loc: manifest.Origin + route}
		if !modified.IsZero() {
			item.LastMod = modified.UTC().Format(time.RFC3339)
		}
		values = append(values, item)
	}
	if route, ok := manifest.Routes["/"]; ok && route.Kind == "file" {
		modified := time.Time{}
		if route.ContentID != "" {
			modified = manifest.Entries[route.ContentID].ModifiedAt
		}
		add("/", modified)
	}
	for _, entry := range manifest.Entries {
		if entry.Active {
			add(entry.CanonicalRoute, entry.ModifiedAt)
		}
	}
	sort.Slice(values, func(i, j int) bool { return values[i].Loc < values[j].Loc })
	body, err := xml.Marshal(urlset{XMLNS: "http://www.sitemaps.org/schemas/sitemap/0.9", URLs: values})
	if err != nil {
		return nil, ErrEditorialUnavailable
	}
	return append([]byte(xml.Header), body...), nil
}

func publicationRobots(manifest *publicationManifest) string {
	var output strings.Builder
	output.WriteString("# Rixa crawler policy. Purpose annotations describe operator intent; they are not an authentication or outcome guarantee.\n")
	for _, crawler := range manifest.Policy.Crawlers {
		output.WriteString("# purpose: ")
		output.WriteString(crawler.Purpose)
		output.WriteString("\nUser-agent: ")
		output.WriteString(crawler.UserAgent)
		output.WriteString("\n")
		if crawler.Access == "disallow" {
			output.WriteString("Disallow: /\n\n")
		} else {
			output.WriteString("Allow: /\n\n")
		}
	}
	output.WriteString("Sitemap: ")
	output.WriteString(manifest.Origin)
	output.WriteString("/sitemap.xml\n")
	return output.String()
}

func publicRobotsDirective(policy PublicPolicyConfig) string {
	values := make([]string, 0, 3)
	if policy.Indexing == "noindex" {
		values = append(values, "noindex")
	} else {
		values = append(values, "index")
	}
	values = append(values, "follow")
	if policy.Snippet == "none" {
		values = append(values, "nosnippet")
	} else {
		values = append(values, "max-snippet:-1")
	}
	return strings.Join(values, ", ")
}

func publicationDirection(language string) string {
	if language == "fa" {
		return "rtl"
	}
	return "ltr"
}

func addGeneratedFile(publicDir string, manifest *publicationManifest, relative string, body []byte, contentType, cache, robots string) (int64, error) {
	if !safeGeneratedFilePath(relative) {
		return 0, ErrEditorialInvalid
	}
	path := filepath.Join(publicDir, filepath.FromSlash(relative))
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return 0, ErrEditorialUnavailable
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return 0, ErrEditorialUnavailable
	}
	n, writeErr := file.Write(body)
	syncErr := file.Sync()
	closeErr := file.Close()
	if writeErr != nil || syncErr != nil || closeErr != nil || n != len(body) {
		_ = os.Remove(path)
		return 0, ErrEditorialUnavailable
	}
	sum := sha256.Sum256(body)
	manifest.Files[relative] = publicationFile{
		Path: relative, ContentType: contentType, Cache: cache, Robots: robots,
		Size: int64(len(body)), SHA256: hex.EncodeToString(sum[:]),
	}
	return int64(len(body)), nil
}

func safeGeneratedFilePath(value string) bool {
	if value == "" || filepath.IsAbs(value) || strings.Contains(value, "\\") {
		return false
	}
	clean := filepath.ToSlash(filepath.Clean(filepath.FromSlash(value)))
	return clean == value && clean != "." && clean != ".." && !strings.HasPrefix(clean, "../")
}
