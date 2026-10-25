package feeds

import (
	"os"
	"strings"
	"testing"
)

func TestExtractArticlePDF(t *testing.T) {
	data, err := os.ReadFile("testdata/article.pdf")
	if err != nil {
		t.Fatal(err)
	}
	text, err := ExtractArticlePDF(data)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(text, "Sample PDF article text") {
		t.Fatalf("unexpected PDF text: %q", text)
	}
	if _, err := ExtractArticlePDF([]byte("%PDF-not-a-real-file")); err == nil {
		t.Fatal("invalid PDF was accepted")
	}
	if _, err := ExtractArticlePDF([]byte("not a PDF")); err == nil {
		t.Fatal("non-PDF was accepted")
	}
}
