package mljobs

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
)

func TestExtractionMetadata(t *testing.T) {
	for _, tc := range []struct {
		origin, issues, want string
		invalid              bool
	}{
		{"full_article", `[]`, `{"extraction_method":"full_article","extraction_errors":[]}`, false},
		{"rss_summary", `[]`, `{"extraction_method":"rss_summary","extraction_errors":[]}`, false},
		{"pdf_extracted", `["PAGE_COUNT_MISMATCH: TXT contains 4 pages; PDF contains 5","MISSING_PAGES","LAYOUT_LOSS"]`, `{"extraction_method":"pdf_extracted","extraction_errors":["LAYOUT_LOSS","MISSING_PAGES"]}`, false},
		{"unknown", `[]`, "", true},
		{"pdf_extracted", `["PAGE_COUNT_MISMATCH: TXT contains 5 pages; PDF contains 4"]`, "", true},
		{"pdf_extracted", `["PAGE_COUNT_MISMATCH"]`, "", true},
		{"pdf_extracted", `["unmapped"]`, "", true},
	} {
		t.Run(tc.origin+tc.issues, func(t *testing.T) {
			job := Job{SourceType: "bank_analytics", TrustLevel: "trusted", DeliveryExtraction: json.RawMessage(`{"text_origin":"` + tc.origin + `","extraction_issues":` + tc.issues + `}`)}
			got, err := deliveryMetadata(job)
			if tc.invalid {
				if err == nil {
					t.Fatalf("accepted invalid extraction: %s", got)
				}
				return
			}
			if err != nil || string(got) != tc.want {
				t.Fatalf("got %s err=%v want=%s", got, err, tc.want)
			}
		})
	}
}

func TestInvalidExtractionOrSourceNeverPosts(t *testing.T) {
	for _, tc := range []struct{ origin, kind, trust string }{
		{"unknown", "news", "standard"}, {"full_article", "made-up", "trusted"}, {"full_article", "news", "super-trusted"},
	} {
		calls := 0
		client := Client{BaseURL: "https://ml.example", HTTP: &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) { calls++; return response(202, "", nil), nil })}}
		job := Job{SourceConfigVersion: 1, SourceType: tc.kind, TrustLevel: tc.trust, DeliveryExtraction: json.RawMessage(`{"text_origin":"` + tc.origin + `","extraction_issues":[]}`)}
		result := client.Process(context.Background(), job)
		if calls != 0 || !result.Permanent || result.ErrorCode != "INVALID_ML_REQUEST" {
			t.Fatalf("calls=%d result=%+v", calls, result)
		}
	}
}

func TestLegacyRequestMetadataRemainsUnchanged(t *testing.T) {
	job := Job{Metadata: json.RawMessage(`{}`), SourceType: "historic-type", TrustLevel: "historic-trust"}
	first, err := RequestPayload(job)
	if err != nil {
		t.Fatal(err)
	}
	var request struct {
		Article struct {
			Metadata map[string]any `json:"metadata"`
		} `json:"article"`
	}
	if err = json.Unmarshal(first, &request); err != nil || len(request.Article.Metadata) != 0 {
		t.Fatalf("legacy request changed: %s %v", first, err)
	}
	var envelope struct {
		Article map[string]json.RawMessage `json:"article"`
	}
	if err := json.Unmarshal(first, &envelope); err != nil {
		t.Fatal(err)
	}
	if _, ok := envelope.Article["version_no"]; ok {
		t.Fatal("legacy request acquired a new parameter")
	}
}

func TestRequestVersionNumber(t *testing.T) {
	for _, n := range []int{1, 2, 100, 0, -1} {
		job := Job{VersionNo: &n}
		payload, err := RequestPayload(job)
		if n <= 0 {
			if err == nil {
				t.Fatalf("accepted version %d", n)
			}
			continue
		}
		var request struct {
			Article struct {
				VersionNo int `json:"version_no"`
			} `json:"article"`
		}
		if err != nil {
			t.Fatal(err)
		}
		if err = json.Unmarshal(payload, &request); err != nil || request.Article.VersionNo != n {
			t.Fatalf("version number missing: %s %v", payload, err)
		}
	}
}
