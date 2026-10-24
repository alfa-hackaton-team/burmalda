package feeds

import (
	"bytes"
	"fmt"
	"strings"

	"golang.org/x/net/html"
	"golang.org/x/net/html/charset"
)

const ArticleHTMLParserVersion = "article-html-v1"

func ExtractArticleHTML(data []byte, contentType string) (string, error) {
	reader, err := charset.NewReader(bytes.NewReader(data), contentType)
	if err != nil {
		return "", fmt.Errorf("decode article character set: %w", err)
	}
	root, err := html.Parse(reader)
	if err != nil {
		return "", fmt.Errorf("parse article HTML: %w", err)
	}
	for _, tag := range []string{"article", "main"} {
		for _, node := range findElements(root, tag) {
			var out strings.Builder
			writeArticleText(&out, node)
			body := strings.Join(strings.Fields(out.String()), " ")
			if body != "" {
				return body, nil
			}
		}
	}
	return "", fmt.Errorf("article HTML has no nonempty article or main text")
}

func findElements(node *html.Node, tag string) []*html.Node {
	var found []*html.Node
	var walk func(*html.Node)
	walk = func(current *html.Node) {
		if current.Type == html.ElementNode && current.Data == tag {
			found = append(found, current)
		}
		for child := current.FirstChild; child != nil; child = child.NextSibling {
			walk(child)
		}
	}
	walk(node)
	return found
}

func writeArticleText(out *strings.Builder, node *html.Node) {
	if node.Type == html.ElementNode {
		switch node.Data {
		case "script", "style", "nav", "footer", "aside", "form", "noscript", "template", "svg":
			return
		}
		for _, attr := range node.Attr {
			if attr.Key == "aria-hidden" && attr.Val == "true" {
				return
			}
			if attr.Key == "role" && (attr.Val == "navigation" || attr.Val == "complementary") {
				return
			}
		}
	}
	if node.Type == html.TextNode {
		out.WriteString(node.Data)
		out.WriteByte(' ')
	}
	for child := node.FirstChild; child != nil; child = child.NextSibling {
		writeArticleText(out, child)
	}
}
