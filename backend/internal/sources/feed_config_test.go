package sources

import "testing"

func TestParseFeedAdapterConfig(t *testing.T) {
	for _, raw := range []string{`{}`, `{"article_html":false}`, `{"article_html":true}`} {
		if _, err := ParseFeedAdapterConfig([]byte(raw)); err != nil {
			t.Fatalf("config %s: %v", raw, err)
		}
	}
	for _, raw := range []string{`null`, `[]`, `{"article_html":null}`, `{"article_html":"yes"}`, `{"other":true}`} {
		if _, err := ParseFeedAdapterConfig([]byte(raw)); err == nil {
			t.Fatalf("invalid config accepted: %s", raw)
		}
	}
}
