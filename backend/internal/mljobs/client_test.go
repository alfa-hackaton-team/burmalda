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
				Article         map[string]any `json:"article"`
				Source          map[string]any `json:"source"`
				PipelineVersion string         `json:"pipeline_version"`
			}
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Errorf("decode request: %v", err)
			}
			if body.PipelineVersion != "pipe" ||
				body.Article["body"] != "Full text" || body.Article["raw_ref"] != "raw-artifact:raw" ||
				body.Source["name"] != "Example" || body.Source["config_version"] != "7" ||
				body.Source["endpoint"] != nil {
				t.Errorf("unexpected ML request: %+v", body)
			}
			return response(http.StatusAccepted, `{"processing_id":"op","article_version_id":"version","pipeline_version":"pipe","trace_id":"trace","created_at":"2026-10-24T12:00:00Z","state":"RUNNING"}`, nil), nil
		case http.MethodGet:
			gets++
			if gets == 1 {
				return response(http.StatusNotFound, "", nil), nil
			}
			return response(http.StatusOK, `{"processing_id":"op","article_version_id":"version","pipeline_version":"pipe","trace_id":"trace","created_at":"2026-10-24T12:00:00Z","state":"SUCCEEDED","document_version_id":"doc","event_ids":[]}`, nil), nil
		default:
			t.Errorf("unexpected method: %s", r.Method)
			return response(http.StatusMethodNotAllowed, "", nil), nil
		}
	})}}
	job := Job{ID: "trace", VersionID: "version", ArticleID: "article", SourceID: "source",
		DedupeKey: "version:pipe", PipelineVersion: "pipe", SourceName: "Example",
		SourceType: "news", SourceRole: "external", TrustLevel: "reviewed", SourceConfigVersion: 7,
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
		Job{Metadata: json.RawMessage(`{}`), SourceConfigVersion: 1})
	if result.Permanent || result.RetryAfter != 30*time.Second {
		t.Fatalf("result=%+v", result)
	}
}

func TestProcessPreservesPermanentMLError(t *testing.T) {
	client := Client{BaseURL: "https://ml.example", Token: "secret", HTTP: &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return response(http.StatusRequestEntityTooLarge,
			`{"code":"PAYLOAD_TOO_LARGE","message":"Article text exceeds the limit","retryable":false}`, nil), nil
	})}}
	result := client.Process(context.Background(), Job{SourceConfigVersion: 1})
	if !result.Permanent || result.ErrorCode != "PAYLOAD_TOO_LARGE" ||
		result.Error != "Article text exceeds the limit" || result.ErrorRetryable {
		t.Fatalf("result=%+v", result)
	}
}

func TestDecodeFailedOperationPreservesStructuredError(t *testing.T) {
	result := decodeOperation([]byte(`{"processing_id":"op","article_version_id":"version","pipeline_version":"pipe","trace_id":"trace","created_at":"2026-10-24T12:00:00Z","state":"FAILED","error":{"code":"INSUFFICIENT_EVIDENCE","message":"No usable evidence","retryable":false}}`),
		Job{VersionID: "version", PipelineVersion: "pipe"})
	if !result.Permanent || result.ErrorCode != "INSUFFICIENT_EVIDENCE" ||
		result.Error != "No usable evidence" || result.ErrorRetryable {
		t.Fatalf("result=%+v", result)
	}
	missing := decodeOperation([]byte(`{"processing_id":"op","article_version_id":"version","pipeline_version":"pipe","trace_id":"trace","created_at":"2026-10-24T12:00:00Z","state":"FAILED"}`),
		Job{VersionID: "version", PipelineVersion: "pipe"})
	if missing.ErrorCode != "INVALID_ML_RESPONSE" {
		t.Fatalf("missing error=%+v", missing)
	}
}

func TestBodyLimitCountsUTF8BytesWithoutTitle(t *testing.T) {
	for _, unit := range []string{"a", "я", "😀"} {
		t.Run(unit, func(t *testing.T) {
			requests := 0
			job := Job{ID: "trace", VersionID: "v", PipelineVersion: "p", SourceConfigVersion: 1, Title: strings.Repeat("T", 200000), Body: strings.Repeat(unit, 131072/len(unit))}
			client := Client{BaseURL: "http://ml.example", HTTP: &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
				requests++
				return response(202, `{"processing_id":"op","article_version_id":"v","pipeline_version":"p","trace_id":"trace","created_at":"2026-10-24T12:00:00Z","state":"RUNNING"}`, nil), nil
			})}}
			result := client.Process(context.Background(), job)
			if !result.Pending || requests != 1 {
				t.Fatalf("131072 bytes rejected: %+v", result)
			}
			job.Body += "a"
			result = client.Process(context.Background(), job)
			if !result.Permanent || result.ErrorCode != "PAYLOAD_TOO_LARGE" || requests != 1 {
				t.Fatalf("131073 bytes sent: %+v requests=%d", result, requests)
			}
		})
	}
}

func TestDecodeRejectsInvalidOperationSchema(t *testing.T) {
	valid := map[string]any{"processing_id": "op", "article_version_id": "version", "pipeline_version": "pipe", "trace_id": "trace", "created_at": "2026-10-24T12:00:00Z", "state": "SUCCEEDED", "document_version_id": "doc", "event_ids": []string{}}
	cases := map[string]func(map[string]any){
		"null_error": func(m map[string]any) { m["error"] = nil },
		"invalid_optional_error": func(m map[string]any) {
			m["error"] = map[string]any{"code": "UNEXPECTED_CODE", "message": "bad", "retryable": false}
		},
		"missing_trace":   func(m map[string]any) { delete(m, "trace_id") },
		"missing_created": func(m map[string]any) { delete(m, "created_at") },
		"null_created":    func(m map[string]any) { m["created_at"] = nil },
		"invalid_date":    func(m map[string]any) { m["created_at"] = "yesterday" },
		"wrong_version":   func(m map[string]any) { m["article_version_id"] = "other" },
		"wrong_pipeline":  func(m map[string]any) { m["pipeline_version"] = "other" },
		"missing_result":  func(m map[string]any) { delete(m, "document_version_id") },
		"missing_events":  func(m map[string]any) { delete(m, "event_ids") },
		"null_events":     func(m map[string]any) { m["event_ids"] = nil },
		"blank_event":     func(m map[string]any) { m["event_ids"] = []string{" "} },
		"unknown_field":   func(m map[string]any) { m["unknown"] = true },
		"unknown_state":   func(m map[string]any) { m["state"] = "PASS" },
	}
	for name, change := range cases {
		t.Run(name, func(t *testing.T) {
			m := map[string]any{}
			for k, v := range valid {
				m[k] = v
			}
			change(m)
			data, _ := json.Marshal(m)
			r := decodeOperation(data, Job{VersionID: "version", PipelineVersion: "pipe"})
			if r.Succeeded || !r.Permanent || r.ErrorCode != "INVALID_ML_RESPONSE" {
				t.Fatalf("invalid response accepted: %+v", r)
			}
		})
	}
	data, _ := json.Marshal(valid)
	op := "confirmed"
	result := decodeOperation(data, Job{VersionID: "version", PipelineVersion: "pipe", ProcessingID: &op})
	if result.ErrorCode != "INVALID_ML_RESPONSE" {
		t.Fatalf("processing id changed: %+v", result)
	}
	result = decodeOperation(append(data, []byte(` {}`)...), Job{VersionID: "version", PipelineVersion: "pipe"})
	if result.ErrorCode != "INVALID_ML_RESPONSE" {
		t.Fatal("trailing response accepted")
	}
}
