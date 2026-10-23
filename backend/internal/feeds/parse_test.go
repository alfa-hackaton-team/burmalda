package feeds

import (
	"os"
	"strings"
	"testing"
)

func TestParseRSSAndAtom(t *testing.T) {
	for _, test := range []struct {
		file, adapter, body, canonical string
	}{
		{"rss.xml", "rss", "First story.", "https://example.com/news/1"},
		{"atom.xml", "atom", "Second story.", "https://example.com/news/2"},
	} {
		t.Run(test.adapter, func(t *testing.T) {
			data, err := os.ReadFile("testdata/" + test.file)
			if err != nil {
				t.Fatal(err)
			}
			entries, err := Parse(data, "https://example.com/feed.xml", test.adapter)
			if err != nil {
				t.Fatal(err)
			}
			if len(entries) != 1 || entries[0].Body != test.body || entries[0].CanonicalURL != test.canonical || entries[0].PublishedAt == nil {
				t.Fatalf("unexpected entries: %+v", entries)
			}
		})
	}
}

func TestParseRejectsMissingTextAndWrongAdapter(t *testing.T) {
	data := []byte(`<rss><channel><item><guid>1</guid><title>Only title</title></item></channel></rss>`)
	if _, err := Parse(data, "https://example.com/feed.xml", "rss"); err == nil || !strings.Contains(err.Error(), "text is empty") {
		t.Fatalf("missing text error = %v", err)
	}
	if _, err := Parse(data, "https://example.com/feed.xml", "atom"); err == nil {
		t.Fatal("RSS data accepted as Atom")
	}
}

func TestParseRejectsConflictingIdentity(t *testing.T) {
	data := []byte(`<rss><channel>
		<item><guid>same</guid><description>First text</description></item>
		<item><guid>same</guid><description>Different text</description></item>
	</channel></rss>`)
	if _, err := Parse(data, "https://example.com/feed.xml", "rss"); err == nil || !strings.Contains(err.Error(), "conflicting") {
		t.Fatalf("conflicting identity error = %v", err)
	}
}
