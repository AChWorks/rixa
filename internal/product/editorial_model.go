// SPDX-License-Identifier: MPL-2.0

package product

import (
	"bytes"
	"crypto/rand"
	"errors"
	"net/url"
	"regexp"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"

	xhtml "golang.org/x/net/html"
	"golang.org/x/net/html/atom"
)

const (
	maxEditorialBodyBytes = 64 << 10
	maxEditorialNodes     = 2048
	maxEditorialDepth     = 32
	maxEditorialMediaRefs = 64
	maxContentTitleRunes  = 200
	maxMediaCaptionRunes  = 300
)

var (
	ErrEditorialInvalid        = errors.New("invalid editorial input")
	ErrEditorialNotFound       = errors.New("editorial record not found")
	ErrEditorialConflict       = errors.New("editorial precondition conflict")
	ErrEditorialUnavailable    = errors.New("editorial unavailable")
	ErrEditorialUnknownOutcome = errors.New("editorial outcome requires reconciliation")
	ErrEditorialLimited        = errors.New("editorial capacity exceeded")

	editorialIDPattern = regexp.MustCompile(`^[A-Z2-7]{26}$`)
	operationIDPattern = regexp.MustCompile(`^(?:[A-Z2-7]{26}|[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12})$`)
)

type ContentKind string

const (
	ContentPost ContentKind = "post"
	ContentPage ContentKind = "page"
)

func validContentKind(kind ContentKind) bool {
	return kind == ContentPost || kind == ContentPage
}

func validEditorialID(value string) bool {
	return editorialIDPattern.MatchString(value)
}

func validOperationID(value string) bool {
	return operationIDPattern.MatchString(value)
}

func newOperationID() string {
	return rand.Text()
}

func validPlainText(value string, maximum int, required bool) bool {
	if !utf8.ValidString(value) || strings.TrimSpace(value) != value {
		return false
	}
	count := utf8.RuneCountInString(value)
	if count > maximum || required && count == 0 {
		return false
	}
	for _, r := range value {
		if unicode.IsControl(r) || r == 0x061c || r == 0x200e || r == 0x200f ||
			r >= 0x202a && r <= 0x202e || r >= 0x2066 && r <= 0x2069 {
			return false
		}
	}
	return true
}

func validContentTitle(value string) bool {
	return validPlainText(value, maxContentTitleRunes, true)
}

func validRichText(value string) bool {
	if !utf8.ValidString(value) {
		return false
	}
	for _, r := range value {
		if r == 0 || unicode.IsControl(r) && r != '\n' && r != '\t' ||
			r == 0x061c || r == 0x200e || r == 0x200f ||
			r >= 0x202a && r <= 0x202e || r >= 0x2066 && r <= 0x2069 {
			return false
		}
	}
	return true
}

type canonicalState struct {
	nodes int
	refs  map[string]struct{}
}

func canonicalBody(input string) (string, []string, error) {
	if !utf8.ValidString(input) || len(input) > maxEditorialBodyBytes {
		return "", nil, ErrEditorialInvalid
	}
	contextNode := &xhtml.Node{Type: xhtml.ElementNode, DataAtom: atom.Div, Data: "div"}
	nodes, err := xhtml.ParseFragment(strings.NewReader(input), contextNode)
	if err != nil {
		return "", nil, ErrEditorialInvalid
	}
	state := &canonicalState{refs: make(map[string]struct{})}
	root := &xhtml.Node{Type: xhtml.ElementNode, DataAtom: atom.Div, Data: "div"}

	for _, node := range nodes {
		if node.Type == xhtml.TextNode && strings.TrimSpace(node.Data) != "" {
			wrapper := &xhtml.Node{
				Type: xhtml.ElementNode, DataAtom: atom.P, Data: "p",
				Attr: []xhtml.Attribute{{Key: "dir", Val: "auto"}},
			}
			textNode, e := sanitizeEditorialNode(node, 1, state)
			if e != nil {
				return "", nil, e
			}
			wrapper.AppendChild(textNode)
			root.AppendChild(wrapper)
			continue
		}
		safe, e := sanitizeEditorialNode(node, 1, state)
		if e != nil {
			return "", nil, e
		}
		if safe != nil {
			root.AppendChild(safe)
		}
	}
	if root.FirstChild == nil {
		paragraph := &xhtml.Node{
			Type: xhtml.ElementNode, DataAtom: atom.P, Data: "p",
			Attr: []xhtml.Attribute{{Key: "dir", Val: "auto"}},
		}
		paragraph.AppendChild(&xhtml.Node{Type: xhtml.ElementNode, DataAtom: atom.Br, Data: "br"})
		root.AppendChild(paragraph)
	}

	var out bytes.Buffer
	for node := root.FirstChild; node != nil; node = node.NextSibling {
		if err = xhtml.Render(&out, node); err != nil {
			return "", nil, ErrEditorialInvalid
		}
		if out.Len() > maxEditorialBodyBytes {
			return "", nil, ErrEditorialInvalid
		}
	}
	refs := make([]string, 0, len(state.refs))
	for id := range state.refs {
		refs = append(refs, id)
	}
	sort.Strings(refs)
	if len(refs) > maxEditorialMediaRefs {
		return "", nil, ErrEditorialInvalid
	}
	return out.String(), refs, nil
}

func sanitizeEditorialNode(node *xhtml.Node, depth int, state *canonicalState) (*xhtml.Node, error) {
	if depth > maxEditorialDepth {
		return nil, ErrEditorialInvalid
	}
	state.nodes++
	if state.nodes > maxEditorialNodes {
		return nil, ErrEditorialInvalid
	}
	switch node.Type {
	case xhtml.CommentNode:
		return nil, nil
	case xhtml.TextNode:
		if !validRichText(node.Data) {
			return nil, ErrEditorialInvalid
		}
		return &xhtml.Node{Type: xhtml.TextNode, Data: node.Data}, nil
	case xhtml.ElementNode:
		return sanitizeEditorialElement(node, depth, state)
	default:
		return nil, ErrEditorialInvalid
	}
}

func sanitizeEditorialElement(node *xhtml.Node, depth int, state *canonicalState) (*xhtml.Node, error) {
	name := strings.ToLower(node.Data)
	switch name {
	case "div":
		name = "p"
	case "b":
		name = "strong"
	case "i":
		name = "em"
	}
	allowed := map[string]atom.Atom{
		"p": atom.P, "h2": atom.H2, "h3": atom.H3,
		"strong": atom.Strong, "em": atom.Em, "br": atom.Br,
		"blockquote": atom.Blockquote, "ul": atom.Ul, "ol": atom.Ol, "li": atom.Li,
		"a": atom.A, "figure": atom.Figure, "figcaption": atom.Figcaption,
	}
	dataAtom, ok := allowed[name]
	if !ok {
		return nil, ErrEditorialInvalid
	}
	safe := &xhtml.Node{Type: xhtml.ElementNode, DataAtom: dataAtom, Data: name}

	switch name {
	case "a":
		if len(node.Attr) != 1 || strings.ToLower(node.Attr[0].Key) != "href" || !validEditorialLink(node.Attr[0].Val) {
			return nil, ErrEditorialInvalid
		}
		safe.Attr = []xhtml.Attribute{{Key: "href", Val: node.Attr[0].Val}}
	case "figure":
		if len(node.Attr) != 1 || strings.ToLower(node.Attr[0].Key) != "data-media-id" || !validEditorialID(node.Attr[0].Val) {
			return nil, ErrEditorialInvalid
		}
		if len(state.refs) >= maxEditorialMediaRefs {
			if _, exists := state.refs[node.Attr[0].Val]; !exists {
				return nil, ErrEditorialInvalid
			}
		}
		state.refs[node.Attr[0].Val] = struct{}{}
		safe.Attr = []xhtml.Attribute{{Key: "data-media-id", Val: node.Attr[0].Val}}
	default:
		if editorialDirectionalBlock(name) {
			if len(node.Attr) > 1 || len(node.Attr) == 1 &&
				(strings.ToLower(node.Attr[0].Key) != "dir" || strings.ToLower(node.Attr[0].Val) != "auto") {
				return nil, ErrEditorialInvalid
			}
			safe.Attr = []xhtml.Attribute{{Key: "dir", Val: "auto"}}
		} else if len(node.Attr) != 0 {
			return nil, ErrEditorialInvalid
		}
	}

	if name == "br" {
		if node.FirstChild != nil {
			return nil, ErrEditorialInvalid
		}
		return safe, nil
	}
	if name == "figcaption" {
		var caption strings.Builder
		for child := node.FirstChild; child != nil; child = child.NextSibling {
			if child.Type != xhtml.TextNode || !validRichText(child.Data) {
				return nil, ErrEditorialInvalid
			}
			caption.WriteString(child.Data)
		}
		if !validPlainText(caption.String(), maxMediaCaptionRunes, false) {
			return nil, ErrEditorialInvalid
		}
		if caption.Len() > 0 {
			safe.AppendChild(&xhtml.Node{Type: xhtml.TextNode, Data: caption.String()})
		}
		return safe, nil
	}

	figcaptions := 0
	for child := node.FirstChild; child != nil; child = child.NextSibling {
		clean, err := sanitizeEditorialNode(child, depth+1, state)
		if err != nil {
			return nil, err
		}
		if clean == nil {
			continue
		}
		if name == "figure" {
			if clean.Type == xhtml.TextNode && strings.TrimSpace(clean.Data) == "" {
				continue
			}
			if clean.Type != xhtml.ElementNode || clean.Data != "figcaption" {
				return nil, ErrEditorialInvalid
			}
			figcaptions++
			if figcaptions > 1 {
				return nil, ErrEditorialInvalid
			}
		}
		safe.AppendChild(clean)
	}
	return safe, nil
}

func editorialDirectionalBlock(name string) bool {
	switch name {
	case "p", "h2", "h3", "blockquote", "li", "figcaption":
		return true
	default:
		return false
	}
}

func validEditorialLink(raw string) bool {
	if raw == "" || len(raw) > 2048 || strings.ContainsAny(raw, "\\\r\n\t") {
		return false
	}
	u, err := url.Parse(raw)
	if err != nil || u.User != nil {
		return false
	}
	if strings.HasPrefix(raw, "/") {
		return !strings.HasPrefix(raw, "//") && u.Scheme == "" && u.Host == "" &&
			u.RawPath == "" && strings.HasPrefix(u.Path, "/")
	}
	return u.Scheme == "https" && u.Host != "" && u.Opaque == "" && u.RawPath == ""
}

type AppearanceInput struct {
	SiteTitle       string
	SiteDescription string
	SiteLanguage    string
	HomeMode        string
	HomePageID      string
	HeaderShowTitle bool
	HeaderTagline   string
	FooterText      string
	Theme           string
}

func (a AppearanceInput) validateShape() error {
	if !validPlainText(a.SiteTitle, 120, true) ||
		!validPlainText(a.SiteDescription, 300, false) ||
		!validPlainText(a.HeaderTagline, 160, false) ||
		!validPlainText(a.FooterText, 500, false) {
		return ErrEditorialInvalid
	}
	if a.SiteLanguage != "en" && a.SiteLanguage != "fa" {
		return ErrEditorialInvalid
	}
	if a.HomeMode != "latest_posts" && a.HomeMode != "page" {
		return ErrEditorialInvalid
	}
	if a.HomeMode == "page" {
		if !validEditorialID(a.HomePageID) {
			return ErrEditorialInvalid
		}
	} else if a.HomePageID != "" {
		return ErrEditorialInvalid
	}
	if a.Theme != "light" && a.Theme != "dark" {
		return ErrEditorialInvalid
	}
	return nil
}
