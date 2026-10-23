package metadata

import (
	stdhtml "html"
	"regexp"
	"strings"

	"golang.org/x/net/html"
)

func parseHTML(data []byte) (*html.Node, error) {
	return html.Parse(strings.NewReader(string(data)))
}

func attr(n *html.Node, key string) string {
	for _, a := range n.Attr {
		if a.Key == key {
			return a.Val
		}
	}
	return ""
}

func hasClass(n *html.Node, class string) bool {
	for _, c := range strings.Fields(attr(n, "class")) {
		if c == class {
			return true
		}
	}
	return false
}

// find returns the first node matching ok, depth-first.
func find(n *html.Node, ok func(*html.Node) bool) *html.Node {
	if ok(n) {
		return n
	}
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		if found := find(c, ok); found != nil {
			return found
		}
	}
	return nil
}

var blockTags = map[string]bool{
	"address": true, "article": true, "blockquote": true, "br": true,
	"dd": true, "div": true, "dl": true, "dt": true, "h1": true, "h2": true,
	"h3": true, "h4": true, "h5": true, "h6": true, "li": true, "p": true,
	"section": true, "td": true, "th": true, "tr": true, "ul": true, "ol": true,
}

// lines renders a node's text, one line per block-level element.
func lines(n *html.Node) []string {
	var b strings.Builder
	writeLines(&b, n)
	var out []string
	for _, line := range strings.Split(b.String(), "\n") {
		line = strings.Join(strings.Fields(stdhtml.UnescapeString(line)), " ")
		if line != "" {
			out = append(out, line)
		}
	}
	return out
}

func writeLines(b *strings.Builder, n *html.Node) {
	if n.Type == html.TextNode {
		b.WriteString(n.Data)
		return
	}
	block := blockTags[n.Data]
	if block {
		b.WriteString("\n")
	}
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		writeLines(b, c)
	}
	if block {
		b.WriteString("\n")
	}
}

var tagPattern = regexp.MustCompile(`<[^>]+>`)

// htmlText converts an HTML fragment into readable text.
func htmlText(fragment string) string {
	fragment = strings.NewReplacer("<br>", "\n", "<br/>", "\n", "<br />", "\n", "</li>", "\n", "</p>", "\n").Replace(fragment)
	fragment = tagPattern.ReplaceAllString(fragment, "")
	return strings.Join(strings.Fields(stdhtml.UnescapeString(fragment)), " ")
}
