// SPDX-License-Identifier: MPL-2.0

package product

import (
	"archive/zip"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"html/template"
	"image"
	_ "image/jpeg"
	_ "image/png"
	"io"
	"path"
	"regexp"
	"sort"
	"strings"
	texttemplate "text/template"
	"unicode/utf8"
)

const (
	ThemeTarget         = "site-theme"
	maxThemeSourceBytes = 1 << 20
	maxThemeBundleBytes = 4 << 20
	maxThemeAssets      = 24
	maxThemeAssetBytes  = 512 << 10
	maxThemeSettings    = 32
	themeBundleFormat   = "rixa.code-theme.v1"
)

var (
	themeAssetNamePattern   = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{0,47}\.(?:png|jpg|jpeg|woff2)$`)
	themeSettingNamePattern = regexp.MustCompile(`^[a-z][a-z0-9_]{0,39}$`)
	reservedThemeHead       = regexp.MustCompile(`(?i)<\s*(?:title|base)\b|<\s*meta\b[^>]*(?:name\s*=\s*["']?(?:robots|description)|http-equiv\s*=)|<\s*link\b[^>]*rel\s*=\s*["']?canonical\b`)
	inlineThemeEvent        = regexp.MustCompile(`(?i)\s+on[a-z]+\s*=`)
	externalThemeScript     = regexp.MustCompile(`(?i)<\s*script\b[^>]*\bsrc\s*=\s*["']?(?:https?:)?//`)
)

type ThemeSetting struct {
	Type  string `json:"type"`
	Value string `json:"value"`
}

type ThemeAsset struct {
	MIME string `json:"mime"`
	Data []byte `json:"data"`
}

// ThemeBundle is one immutable, site-local source revision. Assets are
// admitted byte content, never paths on the Rixa host or private Media IDs.
// The published content URL is derived from the actual source hash.
type ThemeBundle struct {
	HeadHTML   string                  `json:"head_html"`
	HeaderHTML string                  `json:"header_html"`
	BodyHTML   string                  `json:"body_html"`
	FooterHTML string                  `json:"footer_html"`
	CSS        string                  `json:"css"`
	JavaScript string                  `json:"javascript"`
	Settings   map[string]ThemeSetting `json:"settings,omitempty"`
	Assets     map[string]ThemeAsset   `json:"assets,omitempty"`
}

func defaultThemeBundle() ThemeBundle {
	return ThemeBundle{
		HeaderHTML: `<header class="theme-header"><a href="{{.Site.HomeURL}}">{{.Site.Title}}</a></header>`,
		BodyHTML:   `{{if .Content}}<main><article><h1>{{.Content.Title}}</h1><div class="entry">{{.Content.BodyHTML}}</div></article></main>{{else}}<main><h1>{{.Site.Title}}</h1><ol>{{range .Home.Items}}<li><a href="{{.URL}}">{{.Title}}</a></li>{{end}}</ol></main>{{end}}`,
		FooterHTML: `<footer><p>{{.Site.Title}}</p></footer>`,
		CSS:        `*{box-sizing:border-box}body{font:1rem/1.65 system-ui,sans-serif;margin:0;color:#182638;background:#fff}header,main,footer{width:min(100% - 2rem,72rem);margin-inline:auto;padding-block:1rem}img{max-width:100%;height:auto}`,
	}
}

func (b ThemeBundle) sourceHash() string {
	data, _ := json.Marshal(b)
	hash := sha256.Sum256(data)
	return hex.EncodeToString(hash[:])
}

func themeAssetPath(name string, asset ThemeAsset) string {
	digest := sha256.Sum256(asset.Data)
	return "/theme-assets/" + hex.EncodeToString(digest[:]) + "-" + name
}

func themeAssetMIME(name string, data []byte) (string, bool) {
	switch {
	case strings.HasSuffix(name, ".woff2"):
		return "font/woff2", len(data) >= 48 && string(data[:4]) == "wOF2"
	case strings.HasSuffix(name, ".png"), strings.HasSuffix(name, ".jpg"), strings.HasSuffix(name, ".jpeg"):
		cfg, format, err := image.DecodeConfig(bytes.NewReader(data))
		if err != nil || cfg.Width < 1 || cfg.Height < 1 || cfg.Width > 8192 || cfg.Height > 8192 {
			return "", false
		}
		if strings.HasSuffix(name, ".png") && format == "png" {
			return "image/png", true
		}
		if (strings.HasSuffix(name, ".jpg") || strings.HasSuffix(name, ".jpeg")) && format == "jpeg" {
			return "image/jpeg", true
		}
	}
	return "", false
}

func (b ThemeBundle) validate() error {
	parts := []string{b.HeadHTML, b.HeaderHTML, b.BodyHTML, b.FooterHTML, b.CSS, b.JavaScript}
	total := 0
	for _, part := range parts {
		if !utf8.ValidString(part) || len(part) > maxThemeSourceBytes || strings.IndexByte(part, 0) != -1 {
			return fmt.Errorf("%w: theme source invalid or exceeds slot limit", ErrEditorialInvalid)
		}
		total += len(part)
	}
	if len(b.BodyHTML) == 0 || strings.Count(b.BodyHTML, ".Content.BodyHTML") != 1 {
		return fmt.Errorf("%w: body.html must render selected content exactly once; Home is fully customizable", ErrEditorialInvalid)
	}
	for _, html := range parts[:4] {
		if reservedThemeHead.MatchString(html) {
			return fmt.Errorf("%w: Rixa-reserved metadata must not be overridden in Theme HTML", ErrEditorialInvalid)
		}
	}
	for _, html := range parts[:4] {
		if inlineThemeEvent.MatchString(html) || externalThemeScript.MatchString(html) {
			return fmt.Errorf("%w: inline event handlers and external scripts are not supported by public CSP; use site.js", ErrEditorialInvalid)
		}
	}
	if len(b.Settings) > maxThemeSettings || len(b.Assets) > maxThemeAssets {
		return ErrEditorialLimited
	}
	for name, setting := range b.Settings {
		if !themeSettingNamePattern.MatchString(name) || len(setting.Value) > 512 || !validRichText(setting.Value) {
			return ErrEditorialInvalid
		}
		switch setting.Type {
		case "text":
			if !validPlainText(setting.Value, 256, false) {
				return ErrEditorialInvalid
			}
		case "color":
			if !regexp.MustCompile(`^#[0-9a-fA-F]{6}$`).MatchString(setting.Value) {
				return ErrEditorialInvalid
			}
		case "url":
			if !validEditorialLink(setting.Value) {
				return ErrEditorialInvalid
			}
		default:
			return fmt.Errorf("%w: unknown Theme setting type", ErrEditorialInvalid)
		}
	}
	for name, asset := range b.Assets {
		if !themeAssetNamePattern.MatchString(name) || len(asset.Data) == 0 || len(asset.Data) > maxThemeAssetBytes {
			return ErrEditorialInvalid
		}
		mime, ok := themeAssetMIME(name, asset.Data)
		if !ok || mime != asset.MIME {
			return fmt.Errorf("%w: unsupported or mismatched Theme asset: %s", ErrEditorialInvalid, name)
		}
		total += len(asset.Data)
	}
	if total > maxThemeBundleBytes {
		return ErrEditorialLimited
	}
	// Compile and execute both discriminated render paths so invalid fields,
	// template actions and unknown asset names fail at draft-time.
	for _, html := range parts[:4] {
		if _, err := compileThemeSlot(html, b); err != nil {
			return fmt.Errorf("%w: template syntax: %v", ErrEditorialInvalid, err)
		}
	}
	if err := verifyThemeExampleRender(b); err != nil {
		return fmt.Errorf("%w: theme binding: %v", ErrEditorialInvalid, err)
	}
	if _, err := resolveThemeCSS(b); err != nil {
		return fmt.Errorf("%w: CSS asset binding: %v", ErrEditorialInvalid, err)
	}
	return nil
}

func compileThemeSlot(source string, bundle ThemeBundle) (*template.Template, error) {
	asset := func(name string) (string, error) {
		value, ok := bundle.Assets[name]
		if !ok {
			return "", fmt.Errorf("unknown approved theme asset %q", name)
		}
		return themeAssetPath(name, value), nil
	}
	return template.New("slot").Option("missingkey=error").Funcs(template.FuncMap{"asset": asset}).Parse(source)
}

type themeSiteBinding struct {
	Title, Description, Language, Direction, HomeURL, Origin string
}
type themeContentBinding struct {
	Kind                    ContentKind
	Title                   string
	BodyHTML                template.HTML
	CanonicalURL            string
	PublishedAt, ModifiedAt string
}
type themeHomeBinding struct{ Items []publicHomeItem }
type themeRenderBinding struct {
	Site    themeSiteBinding
	Content *themeContentBinding
	Home    themeHomeBinding
	Theme   struct{ Settings map[string]ThemeSetting }
}

// CSS is trusted public source, not HTML. Only explicitly admitted asset
// names may resolve, and text/template does not insert HTML escaping or
// expose server functions. The resulting file is immutable and content-hashed.
func resolveThemeCSS(bundle ThemeBundle) (string, error) {
	tmpl, err := texttemplate.New("site.css").Option("missingkey=error").Funcs(texttemplate.FuncMap{
		"asset": func(name string) (string, error) {
			asset, allowed := bundle.Assets[name]
			if !allowed {
				return "", fmt.Errorf("unknown approved theme asset %q", name)
			}
			return themeAssetPath(name, asset), nil
		},
	}).Parse(bundle.CSS)
	if err != nil {
		return "", err
	}
	out := &cappedThemeWriter{max: 2 << 20}
	if err = tmpl.Execute(out, nil); err != nil {
		return "", err
	}
	return out.String(), nil
}

type cappedThemeWriter struct {
	bytes.Buffer
	max int
}

func (w *cappedThemeWriter) Write(p []byte) (int, error) {
	if w.Len()+len(p) > w.max {
		return 0, ErrEditorialLimited
	}
	return w.Buffer.Write(p)
}

func executeThemeSlot(source string, bundle ThemeBundle, data themeRenderBinding) (string, error) {
	tmpl, err := compileThemeSlot(source, bundle)
	if err != nil {
		return "", err
	}
	data.Theme.Settings = bundle.Settings
	out := &cappedThemeWriter{max: 2 << 20}
	if err = tmpl.Execute(out, data); err != nil {
		return "", err
	}
	return out.String(), nil
}

func verifyThemeExampleRender(b ThemeBundle) error {
	example := themeRenderBinding{
		Site: themeSiteBinding{Title: "Example", Description: "Site", Language: "en", Direction: "ltr", HomeURL: "/", Origin: "https://example.test"},
		Home: themeHomeBinding{Items: []publicHomeItem{{Title: "Post", URL: "/posts/example/"}}},
	}
	for _, slot := range []string{b.HeadHTML, b.HeaderHTML, b.BodyHTML, b.FooterHTML} {
		if _, err := executeThemeSlot(slot, b, example); err != nil {
			return err
		}
	}
	example.Content = &themeContentBinding{Kind: ContentPost, Title: "Post", BodyHTML: template.HTML("<p>Published body</p>"), CanonicalURL: "https://example.test/post", PublishedAt: "2026-11-07", ModifiedAt: "2026-11-07"}
	for _, slot := range []string{b.HeadHTML, b.HeaderHTML, b.BodyHTML, b.FooterHTML} {
		if _, err := executeThemeSlot(slot, b, example); err != nil {
			return err
		}
	}
	// A conditional Page-only binding must fail during draft validation,
	// never first fail while staging an actual Page for publication.
	example.Content.Kind = ContentPage
	for _, slot := range []string{b.HeadHTML, b.HeaderHTML, b.BodyHTML, b.FooterHTML} {
		if _, err := executeThemeSlot(slot, b, example); err != nil {
			return err
		}
	}
	return nil
}

type themeArchiveDescriptor struct {
	Format   string                  `json:"format"`
	Settings map[string]ThemeSetting `json:"settings,omitempty"`
	Assets   map[string]string       `json:"assets,omitempty"`
}

func exportThemeBundle(bundle ThemeBundle) ([]byte, error) {
	if err := bundle.validate(); err != nil {
		return nil, err
	}
	descriptor := themeArchiveDescriptor{Format: themeBundleFormat, Settings: bundle.Settings, Assets: make(map[string]string)}
	for name, asset := range bundle.Assets {
		descriptor.Assets[name] = asset.MIME
	}
	metadata, err := json.MarshalIndent(descriptor, "", "  ")
	if err != nil {
		return nil, err
	}
	files := map[string][]byte{
		"theme.json": metadata, "head.html": []byte(bundle.HeadHTML), "header.html": []byte(bundle.HeaderHTML),
		"body.html": []byte(bundle.BodyHTML), "footer.html": []byte(bundle.FooterHTML),
		"site.css": []byte(bundle.CSS), "site.js": []byte(bundle.JavaScript),
	}
	for name, asset := range bundle.Assets {
		files["assets/"+name] = asset.Data
	}
	names := make([]string, 0, len(files))
	for name := range files {
		names = append(names, name)
	}
	sort.Strings(names)
	var result bytes.Buffer
	writer := zip.NewWriter(&result)
	for _, name := range names {
		header := &zip.FileHeader{Name: name, Method: zip.Deflate}
		header.SetMode(0600)
		entry, e := writer.CreateHeader(header)
		if e != nil {
			return nil, e
		}
		if _, e = entry.Write(files[name]); e != nil {
			return nil, e
		}
	}
	if err := writer.Close(); err != nil {
		return nil, err
	}
	if result.Len() > maxThemeBundleBytes {
		return nil, ErrEditorialLimited
	}
	return result.Bytes(), nil
}

func importThemeBundle(archive []byte) (ThemeBundle, error) {
	if len(archive) == 0 || len(archive) > maxThemeBundleBytes {
		return ThemeBundle{}, ErrEditorialLimited
	}
	reader, err := zip.NewReader(bytes.NewReader(archive), int64(len(archive)))
	if err != nil {
		return ThemeBundle{}, ErrEditorialInvalid
	}
	if len(reader.File) < 7 || len(reader.File) > 7+maxThemeAssets {
		return ThemeBundle{}, ErrEditorialInvalid
	}
	files := make(map[string][]byte, len(reader.File))
	used := 0
	for _, item := range reader.File {
		name := item.Name
		allowed := name == "theme.json" || name == "head.html" || name == "header.html" || name == "body.html" ||
			name == "footer.html" || name == "site.css" || name == "site.js" ||
			strings.HasPrefix(name, "assets/") && themeAssetNamePattern.MatchString(strings.TrimPrefix(name, "assets/"))
		if !allowed || path.Clean(name) != name || item.FileInfo().Mode().IsRegular() == false ||
			item.UncompressedSize64 > maxThemeSourceBytes || files[name] != nil {
			return ThemeBundle{}, ErrEditorialInvalid
		}
		body, e := item.Open()
		if e != nil {
			return ThemeBundle{}, ErrEditorialInvalid
		}
		data, e := io.ReadAll(io.LimitReader(body, maxThemeSourceBytes+1))
		closeErr := body.Close()
		if e != nil || closeErr != nil || len(data) > maxThemeSourceBytes {
			return ThemeBundle{}, ErrEditorialInvalid
		}
		used += len(data)
		if used > maxThemeBundleBytes {
			return ThemeBundle{}, ErrEditorialLimited
		}
		files[name] = data
	}
	for _, name := range []string{"theme.json", "head.html", "header.html", "body.html", "footer.html", "site.css", "site.js"} {
		if files[name] == nil {
			return ThemeBundle{}, ErrEditorialInvalid
		}
	}
	var descriptor themeArchiveDescriptor
	decoder := json.NewDecoder(bytes.NewReader(files["theme.json"]))
	decoder.DisallowUnknownFields()
	if err = decoder.Decode(&descriptor); err != nil || decoder.Decode(new(any)) != io.EOF || descriptor.Format != themeBundleFormat {
		return ThemeBundle{}, ErrEditorialInvalid
	}
	b := ThemeBundle{
		HeadHTML: string(files["head.html"]), HeaderHTML: string(files["header.html"]), BodyHTML: string(files["body.html"]),
		FooterHTML: string(files["footer.html"]), CSS: string(files["site.css"]), JavaScript: string(files["site.js"]),
		Settings: descriptor.Settings, Assets: map[string]ThemeAsset{},
	}
	for name, data := range files {
		if !strings.HasPrefix(name, "assets/") {
			continue
		}
		assetName := strings.TrimPrefix(name, "assets/")
		mime, ok := descriptor.Assets[assetName]
		if !ok {
			return ThemeBundle{}, ErrEditorialInvalid
		}
		b.Assets[assetName] = ThemeAsset{MIME: mime, Data: data}
	}
	if len(descriptor.Assets) != len(b.Assets) {
		return ThemeBundle{}, ErrEditorialInvalid
	}
	if err = b.validate(); err != nil {
		return ThemeBundle{}, err
	}
	return b, nil
}
