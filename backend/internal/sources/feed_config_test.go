package sources

import "testing"

func TestParseFeedAdapterConfig(t *testing.T) {
	for _, raw := range []string{`{}`, `{"article_html":false}`, `{"article_html":true}`,
		`{"article_html":true,"article_container_id":"article","article_exclude_classes":["heading"]}`} {
		if _, err := ParseFeedAdapterConfig([]byte(raw)); err != nil {
			t.Fatalf("config %s: %v", raw, err)
		}
	}
	for _, raw := range []string{`null`, `[]`, `{"article_html":null}`, `{"article_html":"yes"}`, `{"other":true}`,
		`{"article_container_id":"article"}`, `{"article_html":true,"article_exclude_classes":["heading"]}`,
		`{"article_html":true,"article_container_id":""}`,
		`{"ARTICLE_HTML":true}`, `{"article_html":false,"Article_Html":true}`,
		`{"article_html":true,"article_container_id":null}`, `{"article_html":true,"article_container_id":"article","article_exclude_classes":null}`,
		`{"article_html":true,"article_container_id":"article","article_exclude_classes":["bad class"]}`} {
		if _, err := ParseFeedAdapterConfig([]byte(raw)); err == nil {
			t.Fatalf("invalid config accepted: %s", raw)
		}
	}
}
