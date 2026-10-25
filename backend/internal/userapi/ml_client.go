package userapi

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"github.com/alfa-hackaton-team/burmalda/backend/internal/apicontract"
	"github.com/alfa-hackaton-team/burmalda/backend/internal/config"
)

type MLClient struct {
	BaseURL, Token string
	HTTP           *http.Client
	Settings       func() config.Runtime
}

func (m MLClient) call(ctx context.Context, method, path, key string, input any) (object, int, error) {
	if m.BaseURL == "" || m.Token == "" {
		return nil, 0, unavailable()
	}
	settings := config.DefaultRuntime()
	if m.Settings != nil {
		settings = m.Settings()
	}
	var body io.Reader
	if input != nil {
		body = bytes.NewReader(raw(input))
	}
	req, err := http.NewRequestWithContext(ctx, method, m.BaseURL+path, body)
	if err != nil {
		return nil, 0, unavailable()
	}
	req.Header.Set("Authorization", "Bearer "+m.Token)
	req.Header.Set("Accept", "application/json")
	if input != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if key != "" {
		req.Header.Set("Idempotency-Key", key)
	}
	client := m.HTTP
	if client == nil {
		client = &http.Client{Timeout: settings.ML.RequestTimeout.Std(), CheckRedirect: func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse }}
	}
	response, err := client.Do(req)
	if err != nil {
		return nil, 0, unavailable()
	}
	defer response.Body.Close()
	data, err := io.ReadAll(io.LimitReader(response.Body, settings.ML.MaxResponseBytes+1))
	if err != nil || int64(len(data)) > settings.ML.MaxResponseBytes {
		return nil, response.StatusCode, fail(502, "INVALID_ML_RESPONSE", "invalid service response")
	}

	if response.StatusCode == 429 || response.StatusCode >= 500 {
		f := unavailable().(*apiFailure)
		delay := time.Duration(0)
		value := response.Header.Get("Retry-After")
		if seconds, err := strconv.ParseInt(value, 10, 32); err == nil && seconds > 0 {
			delay = time.Duration(seconds) * time.Second
		} else if when, err := http.ParseTime(value); err == nil {
			delay = time.Until(when)
		}
		if delay > settings.ML.RetryAfterMax.Std() {
			delay = settings.ML.RetryAfterMax.Std()
		}
		if delay > 0 {
			f.retryAfter = delay
		}
		return nil, response.StatusCode, f
	}
	if response.StatusCode == 404 && method != "GET" {
		return nil, 404, unavailable()
	}
	if response.StatusCode == 404 {
		return nil, 404, fail(404, "NOT_FOUND", "service operation not found")
	}
	var result object
	if err = json.Unmarshal(data, &result); err != nil || result == nil {
		return nil, response.StatusCode, fail(502, "INVALID_ML_RESPONSE", "invalid service response")
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		if response.StatusCode == 404 {
			return nil, 404, fail(404, "NOT_FOUND", "service operation not found")
		}
		if response.StatusCode == 429 || response.StatusCode >= 500 {
			return nil, response.StatusCode, unavailable()
		}
		return nil, response.StatusCode, fail(502, "ML_PROCESSING_ERROR", "service rejected the request")
	}
	return result, response.StatusCode, nil
}
func (m MLClient) analysis(ctx context.Context, request object, analysisID string) (object, string, error) {
	expectedID := analysisID
	var result object
	var status int
	var err error
	if analysisID != "" {
		result, status, err = m.call(ctx, "GET", "/internal/v0.1/analyses/"+url.PathEscape(analysisID), "", nil)
		if status == 404 {
			analysisID = ""
		} else if err != nil {
			return nil, analysisID, err
		}
	}
	if analysisID == "" {
		path := "/internal/v0.1/analyses"
		if request["request_type"] == "follow_up" {
			path = "/internal/v0.1/follow-ups"
		}
		result, status, err = m.call(ctx, "POST", path, str(request["request_operation_id"]), request)
		if err != nil {
			return nil, expectedID, err
		}
	}
	if result["contract_version"] == "v1" {
		if status != 200 {
			return nil, analysisID, fail(502, "INVALID_ML_RESPONSE", "invalid service status")
		}
		if e := checked("ml", "AnalysisResponse", result); e != nil {
			return nil, analysisID, e
		}
		if expectedID != "" && str(result["analysis_run_id"]) != expectedID {
			return nil, analysisID, fail(502, "INVALID_ML_RESPONSE", "analysis identity changed")
		}
		return result, str(result["analysis_run_id"]), nil
	}
	if err = apicontract.Validate("public", "AnalysisOperation", result); err != nil {
		return nil, analysisID, fail(502, "INVALID_ML_RESPONSE", "invalid service operation")
	}
	returned := str(result["analysis_id"])
	if expectedID != "" && returned != expectedID {
		return nil, analysisID, fail(502, "INVALID_ML_RESPONSE", "operation identity changed")
	}
	if result["trace_id"] != request["trace_id"] {
		return nil, returned, fail(502, "INVALID_ML_RESPONSE", "trace identity changed")
	}
	response := obj(result["response"])
	if response != nil && response["analysis_run_id"] != returned {
		return nil, returned, fail(502, "INVALID_ML_RESPONSE", "analysis identity changed")
	}
	return response, returned, nil
}
