package feeds

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"
	"strings"

	"golang.org/x/net/html"
	"golang.org/x/net/html/charset"
)

const ArticleHTMLParserVersion = "article-html-v1"
const ConfiguredArticleHTMLParserVersion = "article-html-config-v1"

func ArticleHTMLParserVersionForOptions(containerID string, excludedClasses []string) string {
	if containerID == "" {
		return ArticleHTMLParserVersion
	}
	classes := append([]string(nil), excludedClasses...)
	sort.Strings(classes)
	sum := sha256.Sum256([]byte(containerID + "\x00" + strings.Join(classes, "\x00")))
	return ConfiguredArticleHTMLParserVersion + "-" + hex.EncodeToString(sum[:8])
}

func ExtractArticleHTML(data []byte, contentType string) (string, error) {
	return ExtractArticleHTMLWithOptions(data, contentType, "", nil)
}

func ExtractArticleHTMLWithOptions(data []byte, contentType, containerID string, excludedClasses []string) (string, error) {
	reader, err := charset.NewReader(bytes.NewReader(data), contentType)
	if err != nil {
		return "", fmt.Errorf("decode article character set: %w", err)
	}
	root, err := html.Parse(reader)
	if err != nil {
		return "", fmt.Errorf("parse article HTML: %w", err)
	}
	if containerID != "" {
		for _, node := range findElementsByID(root, containerID) {
			var out strings.Builder
			writeArticleTextWithExclusions(&out, node, excludedClasses)
			body := strings.Join(strings.Fields(out.String()), " ")
			if body != "" {
				return body, nil
			}
		}
		return "", fmt.Errorf("configured article HTML container has no nonempty text")
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

func findElementsByID(root *html.Node, id string) []*html.Node {
	var found []*html.Node
	var walk func(*html.Node)
	walk = func(node *html.Node) {
		if node.Type == html.ElementNode {
			for _, attr := range node.Attr {
				if attr.Key == "id" && attr.Val == id {
					found = append(found, node)
				}
			}
		}
		for child := node.FirstChild; child != nil; child = child.NextSibling {
			walk(child)
		}
	}
	walk(root)
	return found
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
	writeArticleTextWithExclusions(out, node, nil)
}

func writeArticleTextWithExclusions(out *strings.Builder, node *html.Node, excludedClasses []string) {
	if node.Type == html.ElementNode {
		switch node.Data {
		case "script", "style", "nav", "footer", "aside", "form", "noscript", "template", "svg":
			return
		}
		for _, attr := range node.Attr {
			if attr.Key == "class" {
				for _, class := range strings.Fields(attr.Val) {
					for _, excluded := range excludedClasses {
						if class == excluded {
							return
						}
					}
				}
			}
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
		writeArticleTextWithExclusions(out, child, excludedClasses)
	}
}
