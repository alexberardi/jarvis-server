package display

import (
	"bytes"
	"encoding/base64"
	"mime"
	"path"
	"regexp"
	"strings"

	"golang.org/x/net/html"
)

var cssURL = regexp.MustCompile(`url\(\s*["']?([^"')]+)["']?\s*\)`)

// RenderPreview makes a self-contained preview; the live agent serves the original files.
func RenderPreview(t Theme) (string, error) {
	doc, err := html.Parse(bytes.NewReader(t.Files[t.Manifest.Entry]))
	if err != nil {
		return "", err
	}
	asset := func(base, name string) string { return path.Clean(path.Join(path.Dir(base), name)) }
	dataURL := func(name string) string {
		b, ok := t.Files[name]
		if !ok {
			return ""
		}
		mt := mime.TypeByExtension(path.Ext(name))
		if mt == "" {
			mt = "application/octet-stream"
		}
		return "data:" + mt + ";base64," + base64.StdEncoding.EncodeToString(b)
	}
	css := func(name string, b []byte) string {
		return cssURL.ReplaceAllStringFunc(string(b), func(s string) string {
			v := cssURL.FindStringSubmatch(s)
			return "url(" + dataURL(asset(name, v[1])) + ")"
		})
	}
	var visit func(*html.Node)
	visit = func(n *html.Node) {
		for child := n.FirstChild; child != nil; child = child.NextSibling {
			visit(child)
		}
		if n.Type != html.ElementNode {
			return
		}
		for i := 0; i < len(n.Attr); i++ {
			a := n.Attr[i]
			if n.Data == "script" && a.Key == "src" {
				name := asset(t.Manifest.Entry, a.Val)
				n.Attr = append(n.Attr[:i], n.Attr[i+1:]...)
				for n.FirstChild != nil {
					n.RemoveChild(n.FirstChild)
				}
				n.AppendChild(&html.Node{Type: html.TextNode, Data: string(t.Files[name])})
				return
			}
			if n.Data == "link" && a.Key == "href" {
				name := asset(t.Manifest.Entry, a.Val)
				if strings.HasSuffix(name, ".css") {
					n.Data = "style"
					n.Attr = nil
					n.AppendChild(&html.Node{Type: html.TextNode, Data: css(name, t.Files[name])})
					return
				}
			}
			if a.Key == "src" || a.Key == "poster" {
				n.Attr[i].Val = dataURL(asset(t.Manifest.Entry, a.Val))
			}
			if a.Key == "style" {
				n.Attr[i].Val = css(t.Manifest.Entry, []byte(a.Val))
			}
		}
		if n.Data == "style" && n.FirstChild != nil {
			n.FirstChild.Data = css(t.Manifest.Entry, []byte(n.FirstChild.Data))
		}
	}
	visit(doc)
	var b bytes.Buffer
	b.WriteString(`<meta http-equiv="Content-Security-Policy" content="default-src 'none'; script-src 'unsafe-inline'; style-src 'unsafe-inline'; img-src data:; font-src data:; connect-src 'none'; frame-src 'none'; object-src 'none'; base-uri 'none'; form-action 'none'">`)
	if err = html.Render(&b, doc); err != nil {
		return "", err
	}
	return b.String(), nil
}
