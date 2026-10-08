// Package extract is the recipe extraction chain of the URL/webview import (legacy
// url_parsing: extractors/schema_org.py, heuristic.py, llm.py and parsing_utils.py), ported to
// golang.org/x/net/html. It is pure: no I/O, no LLM calls (the module makes those with the
// prompts built here). The goldens in fixtures/golden/recipes/extract.json and prompts/P1*.json
// are the contract.
//
// BeautifulSoup's lxml tree differs from an HTML5 tree on malformed markup; the extractors only
// read element names, a few attributes and text, so the common shapes agree (goldened).
package extract

import (
	"regexp"
	"slices"
	"strings"

	"golang.org/x/net/html"

	"github.com/alexberardi/jarvis-server/internal/modules/recipes/quantity"
)

// doc is a parsed page.
type doc struct {
	root *html.Node
	// noBody is lxml's "the document has no <body>": an HTML5 parser always makes one, lxml
	// only when there is body content (a page of only <script> blocks has none).
	noBody bool
}

// parse parses markup like BeautifulSoup(markup, "lxml"). <noscript> content is parsed as
// markup (lxml does not run scripts), not as raw text.
func parse(markup string) *doc {
	root, err := html.ParseWithOptions(strings.NewReader(markup), html.ParseOptionEnableScripting(false))
	if err != nil {
		root = &html.Node{Type: html.DocumentNode}
	}
	d := &doc{root: root}
	if b := d.find(byTag("body")); b == nil || b.FirstChild == nil {
		d.noBody = true
	}
	return d
}

func (d *doc) find(m func(*html.Node) bool) *html.Node { return findFirst(d.root, m) }

// body is soup.body (nil when lxml would have made none).
func (d *doc) body() *html.Node {
	if d.noBody {
		return nil
	}
	return d.find(byTag("body"))
}

// title is `soup.find("h1") or soup.title`.
func (d *doc) title() *html.Node {
	if h := d.find(byTag("h1")); h != nil {
		return h
	}
	return d.find(byTag("title"))
}

func byTag(names ...string) func(*html.Node) bool {
	return func(n *html.Node) bool { return n.Type == html.ElementNode && slices.Contains(names, n.Data) }
}

func attr(n *html.Node, key string) (string, bool) {
	for _, a := range n.Attr {
		if a.Key == key {
			return a.Val, true
		}
	}
	return "", false
}

// byAttrRe matches an element whose attribute key matches re (bs4 attrs={key: re}).
func byAttrRe(key string, re *regexp.Regexp) func(*html.Node) bool {
	return func(n *html.Node) bool {
		if n.Type != html.ElementNode {
			return false
		}
		v, ok := attr(n, key)
		return ok && re.MatchString(v)
	}
}

// byClassRe is bs4 class_=re: any one class token, or the whole class string, matches.
func byClassRe(re *regexp.Regexp) func(*html.Node) bool {
	return func(n *html.Node) bool {
		if n.Type != html.ElementNode {
			return false
		}
		v, ok := attr(n, "class")
		if !ok {
			return false
		}
		for _, c := range quantity.PySplit(v) {
			if re.MatchString(c) {
				return true
			}
		}
		return re.MatchString(v)
	}
}

// findFirst is Tag.find: the first descendant (document order, not n itself) matching m.
func findFirst(n *html.Node, m func(*html.Node) bool) *html.Node {
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		if m(c) {
			return c
		}
		if f := findFirst(c, m); f != nil {
			return f
		}
	}
	return nil
}

// findAll is Tag.find_all: every descendant matching m, in document order.
func findAll(n *html.Node, m func(*html.Node) bool) []*html.Node {
	var out []*html.Node
	var walk func(*html.Node)
	walk = func(p *html.Node) {
		for c := p.FirstChild; c != nil; c = c.NextSibling {
			if m(c) {
				out = append(out, c)
			}
			walk(c)
		}
	}
	walk(n)
	return out
}

// isString matches the nodes bs4's find(string=...) searches: text (including script and
// style contents) and comments.
func isString(re *regexp.Regexp) func(*html.Node) bool {
	return func(n *html.Node) bool {
		return (n.Type == html.TextNode || n.Type == html.CommentNode) && re.MatchString(n.Data)
	}
}

// nextSibling is find_next_sibling(names): the next sibling element with one of the names.
func nextSibling(n *html.Node, names ...string) *html.Node {
	for s := n.NextSibling; s != nil; s = s.NextSibling {
		if s.Type == html.ElementNode && (len(names) == 0 || slices.Contains(names, s.Data)) {
			return s
		}
	}
	return nil
}

// rawTextTag reports elements whose text bs4 (4.10+) keeps as Script/Stylesheet/Template
// strings, which get_text on any other tag skips.
func rawTextTag(name string) bool { return name == "script" || name == "style" || name == "template" }

// strings_ is Tag._all_strings: the text nodes get_text joins. On a script/style/template tag
// that is its own content; elsewhere their content is skipped. Comments never count.
func strings_(n *html.Node) []string {
	var out []string
	own := n.Type == html.ElementNode && rawTextTag(n.Data)
	var walk func(*html.Node)
	walk = func(p *html.Node) {
		for c := p.FirstChild; c != nil; c = c.NextSibling {
			switch c.Type {
			case html.TextNode:
				out = append(out, c.Data)
			case html.ElementNode:
				if !own && rawTextTag(c.Data) {
					continue
				}
				walk(c)
			}
		}
	}
	walk(n)
	return out
}

// text is get_text(sep, strip): each string (stripped and dropped when empty, with strip).
func text(n *html.Node, sep string, strip bool) string {
	var parts []string
	for _, s := range strings_(n) {
		if strip {
			s = quantity.PyStrip(s)
			if s == "" {
				continue
			}
		}
		parts = append(parts, s)
	}
	return strings.Join(parts, sep)
}

// decompose removes n from the tree.
func decompose(n *html.Node) {
	if n.Parent != nil {
		n.Parent.RemoveChild(n)
	}
}

// render is str(tag).
func render(n *html.Node) string {
	var b strings.Builder
	_ = html.Render(&b, n)
	return b.String()
}

// cleanForContent is heuristic.clean_soup_for_content: drop boilerplate, then scripts, styles
// and metadata.
func cleanForContent(d *doc) {
	for _, n := range findAll(d.root, byTag("header", "footer", "nav", "aside", "form")) {
		decompose(n)
	}
	for _, n := range findAll(d.root, byTag("script", "style", "noscript", "link", "meta")) {
		decompose(n)
	}
}

var recipeItemtype = regexp.MustCompile(`(?i)Recipe`)

// findMainNode is heuristic.find_main_node.
func findMainNode(d *doc) *html.Node {
	for _, m := range []func(*html.Node) bool{byAttrRe("itemtype", recipeItemtype), byTag("article"), byTag("main")} {
		if n := d.find(m); n != nil {
			return n
		}
	}
	return d.body()
}

// isLDScript matches <script type="application/ld+json"> (bs4 type= is an exact match).
func isLDScript(n *html.Node) bool {
	v, ok := attr(n, "type")
	return n.Type == html.ElementNode && n.Data == "script" && ok && v == "application/ld+json"
}
