package httpx

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

type decodePayload struct {
	Name   string            `json:"name"`
	Nested map[string]string `json:"nested,omitempty"`
}

func TestDecodeJSONObject(t *testing.T) {
	for _, contentType := range []string{"application/json", "application/json; charset=utf-8", "application/json; charset=UTF-8"} {
		t.Run(contentType, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(`{"name":"value"}`))
			req.Header.Set("Content-Type", contentType)
			res := httptest.NewRecorder()
			var payload decodePayload

			if !DecodeJSONObject(res, req, &payload) || payload.Name != "value" {
				t.Fatalf("status=%d payload=%+v body=%s", res.Code, payload, res.Body.String())
			}
		})
	}
}

func TestDecodeJSONObjectRejectsInvalidInput(t *testing.T) {
	tests := []struct {
		name        string
		contentType string
		body        string
	}{
		{name: "missing content type", body: `{}`},
		{name: "wrong content type", contentType: "text/plain", body: `{}`},
		{name: "unsupported charset", contentType: "application/json; charset=iso-8859-1", body: `{}`},
		{name: "empty", contentType: "application/json"},
		{name: "null", contentType: "application/json", body: `null`},
		{name: "array", contentType: "application/json", body: `[]`},
		{name: "scalar", contentType: "application/json", body: `1`},
		{name: "malformed", contentType: "application/json", body: `{"name":`},
		{name: "second object", contentType: "application/json", body: `{} {}`},
		{name: "trailing garbage", contentType: "application/json", body: `{} no`},
		{name: "unknown field", contentType: "application/json", body: `{"unknown":1}`},
		{name: "wrong type", contentType: "application/json", body: `{"name":1}`},
		{name: "duplicate key", contentType: "application/json", body: `{"name":"first","name":"second"}`},
		{name: "nested duplicate key", contentType: "application/json", body: `{"nested":{"key":"first","key":"second"}}`},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(tc.body))
			if tc.contentType != "" {
				req.Header.Set("Content-Type", tc.contentType)
			}
			res := httptest.NewRecorder()

			if DecodeJSONObject(res, req, &decodePayload{}) {
				t.Fatal("invalid body accepted")
			}
			assertDecodeError(t, res, http.StatusBadRequest, "VALIDATION_ERROR")
		})
	}
}

func TestDecodeJSONObjectMapsMaxBytesError(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(`{"name":"too large"}`))
	req.Header.Set("Content-Type", "application/json")
	req.ContentLength = -1
	res := httptest.NewRecorder()
	req.Body = http.MaxBytesReader(res, req.Body, 2)

	if DecodeJSONObject(res, req, &decodePayload{}) {
		t.Fatal("oversized body accepted")
	}
	assertDecodeError(t, res, http.StatusRequestEntityTooLarge, "PAYLOAD_TOO_LARGE")
}

func assertDecodeError(t *testing.T, res *httptest.ResponseRecorder, status int, code string) {
	t.Helper()
	var response Error
	if err := json.Unmarshal(res.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode error response: %v body=%q", err, res.Body.String())
	}
	if res.Code != status || response.Code != code {
		t.Fatalf("status=%d error=%+v", res.Code, response)
	}
}
