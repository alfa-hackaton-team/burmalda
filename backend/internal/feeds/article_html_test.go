package feeds

import (
	"os"
	"strings"
	"testing"
)

func TestExtractConfiguredArticleBody(t *testing.T) {
	htmlBody := []byte(`<html><body><nav>Menu text</nav><div id="article">
		<div class="heading"><h3>Repeated title</h3></div>
		<div class="col-xs-12 col-sm-4"></div>
		<div class="col-xs-12 col-sm-8"><p>Full release body with details.</p></div>
		</div><footer>Footer text</footer></body></html>`)
	body, err := ExtractArticleHTMLWithOptions(htmlBody, "text/html", "article", []string{"heading"})
	if err != nil || body != "Full release body with details." {
		t.Fatalf("body=%q err=%v", body, err)
	}
	if _, err := ExtractArticleHTMLWithOptions(htmlBody, "text/html", "missing", nil); err == nil {
		t.Fatal("missing configured container accepted")
	}
	first := ArticleHTMLParserVersionForOptions("article", []string{"heading", "share"})
	if first == ArticleHTMLParserVersion || first != ArticleHTMLParserVersionForOptions("article", []string{"share", "heading"}) ||
		first == ArticleHTMLParserVersionForOptions("story", []string{"heading", "share"}) {
		t.Fatalf("unexpected parser version %q", first)
	}
}

func TestExtractArticleHTMLIgnoresNavigation(t *testing.T) {
	original, err := os.ReadFile("testdata/article.html")
	if err != nil {
		t.Fatal(err)
	}
	changedMenu := []byte(strings.Replace(string(original), "Top menu", "New menu", 1))
	first, err := ExtractArticleHTML(original, "text/html; charset=utf-8")
	if err != nil {
		t.Fatal(err)
	}
	second, err := ExtractArticleHTML(changedMenu, "text/html; charset=utf-8")
	if err != nil {
		t.Fatal(err)
	}
	if first != "First story The market opened higher today." || second != first {
		t.Fatalf("first=%q second=%q", first, second)
	}
	changedBody := []byte(strings.Replace(string(original), "opened higher", "opened lower", 1))
	third, err := ExtractArticleHTML(changedBody, "text/html")
	if err != nil || third == first {
		t.Fatalf("changed body=%q err=%v", third, err)
	}
}

func TestExtractArticleHTMLRejectsMissingMainContent(t *testing.T) {
	_, err := ExtractArticleHTML([]byte("<html><body><nav>Only menu</nav></body></html>"), "text/html")
	if err == nil {
		t.Fatal("navigation-only page accepted")
	}
}
