package mljobs

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) { return f(req) }

func response(status int, body string, headers http.Header) *http.Response {
	return &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader(body)), Header: headers}
}

func TestProcessUsesStableKeyAndRecoversMissingGET(t *testing.T) {
	var posts, gets int
	client := Client{BaseURL: "https://ml.example", Token: "secret", HTTP: &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.Header.Get("Authorization") != "Bearer secret" {
			t.Errorf("missing service token")
		}
		switch r.Method {
		case http.MethodPost:
			posts++
			if r.Header.Get("Idempotency-Key") != "version:pipe" {
				t.Errorf("unstable idempotency key: %q", r.Header.Get("Idempotency-Key"))
			}
			var body struct {
				Article map[string]any `json:"article"`
				Source  map[string]any `json:"source"`
			}
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Errorf("decode request: %v", err)
			}
			if body.Article["body"] != "Full text" || body.Article["raw_ref"] != "raw-artifact:raw" ||
				body.Source["name"] != "Example" || body.Source["endpoint"] != nil {
				t.Errorf("unexpected ML request: %+v", body)
			}
			return response(http.StatusAccepted, `{"processing_id":"op","article_version_id":"version","pipeline_version":"pipe","state":"RUNNING"}`, nil), nil
		case http.MethodGet:
			gets++
			if gets == 1 {
				return response(http.StatusNotFound, "", nil), nil
			}
			return response(http.StatusOK, `{"processing_id":"op","article_version_id":"version","pipeline_version":"pipe","state":"SUCCEEDED","document_version_id":"doc","event_ids":[]}`, nil), nil
		default:
			t.Errorf("unexpected method: %s", r.Method)
			return response(http.StatusMethodNotAllowed, "", nil), nil
		}
	})}}
	job := Job{ID: "trace", VersionID: "version", ArticleID: "article", SourceID: "source",
		DedupeKey: "version:pipe", PipelineVersion: "pipe", SourceName: "Example",
		SourceType: "news", SourceRole: "external", TrustLevel: "reviewed",
		Title: "Title", Body: "Full text", ContentHash: "hash", RawArtifactID: "raw",
		FetchedAt: time.Now().UTC(), Metadata: json.RawMessage(`{}`)}
	first := client.Process(context.Background(), job)
	if !first.Pending || first.ProcessingID != "op" {
		t.Fatalf("first result: %+v", first)
	}
	job.ProcessingID = &first.ProcessingID
	second := client.Process(context.Background(), job)
	if !second.Pending || second.ProcessingID != "op" || posts != 2 || gets != 1 {
		t.Fatalf("recovery: %+v, posts=%d gets=%d", second, posts, gets)
	}
	third := client.Process(context.Background(), job)
	if !third.Succeeded || third.DocumentVersionID != "doc" || third.EventIDs == nil || posts != 2 {
		t.Fatalf("success: %+v, posts=%d", third, posts)
	}
}

func TestProcessClassifiesTooManyRequests(t *testing.T) {
	client := Client{BaseURL: "https://ml.example", Token: "secret", HTTP: &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return response(http.StatusTooManyRequests, "", http.Header{"Retry-After": []string{"30"}}), nil
	})}}
	result := client.Process(context.Background(),
		Job{Metadata: json.RawMessage(`{}`)})
	if result.Permanent || result.RetryAfter != 30*time.Second {
		t.Fatalf("result=%+v", result)
	}
}
