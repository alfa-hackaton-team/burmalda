package feeds

import (
	"os"
	"strings"
	"testing"
)

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
