package mljobs

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/alfa-hackaton-team/burmalda/backend/internal/config"
	"github.com/alfa-hackaton-team/burmalda/backend/internal/observability"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/propagation"
)

type Result struct {
	ProcessingID      string
	DocumentVersionID string
	EventIDs          []string
	Succeeded         bool
	Pending           bool
	Permanent         bool
	RetryAfter        time.Duration
	Error             string
	ErrorCode         string
	ErrorRetryable    bool
}

type Client struct {
	BaseURL      string
	Token        string
	MaxTextBytes int
	HTTP         *http.Client
	Settings     func() config.Runtime
}

func (c Client) settings() config.Runtime {
	if c.Settings != nil {
		return c.Settings()
	}
	return config.DefaultRuntime()
}

type operation struct {
	ProcessingID      string     `json:"processing_id"`
	ArticleVersionID  string     `json:"article_version_id"`
	PipelineVersion   string     `json:"pipeline_version"`
	State             string     `json:"state"`
	TraceID           string     `json:"trace_id"`
	CreatedAt         *time.Time `json:"created_at"`
	DocumentVersionID string     `json:"document_version_id"`
	EventIDs          *[]string  `json:"event_ids"`
	Error             *struct {
		Code      string  `json:"code"`
		Message   string  `json:"message"`
		Retryable *bool   `json:"retryable"`
		TraceID   *string `json:"trace_id,omitempty"`
	} `json:"error"`
}

func (c Client) Process(ctx context.Context, job Job) Result {
	if job.ProcessingID != nil {
		result, missing := c.get(ctx, job)
		if !missing {
			return result
		}
	}
	return c.post(ctx, job)
}

func (c Client) get(ctx context.Context, job Job) (Result, bool) {
	u := c.BaseURL + "/internal/v0.1/article-processings/" + url.PathEscape(*job.ProcessingID)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return Result{Permanent: true, Error: "invalid ML operation URL"}, false
	}
	status, body, retryAfter, err := c.do(req)
	if err != nil {
		return Result{Error: "ML status request failed", ErrorCode: "ML_TRANSPORT_ERROR", ErrorRetryable: true}, false
	}
	if status == http.StatusNotFound {
		return Result{}, true
	}
	if status != http.StatusOK {
		return classifyStatus(status, retryAfter, body), false
	}
	return decodeOperationWithInterval(body, job, c.settings().ML.StatusPollInterval.Std()), false
}

func (c Client) post(ctx context.Context, job Job) Result {
	limit := c.MaxTextBytes
	if limit <= 0 {
		limit = config.MLTextByteLimit
	}
	limit = min(limit, config.MLTextByteLimit)
	if len(job.Body) > limit {
		return Result{Permanent: true, ErrorCode: "PAYLOAD_TOO_LARGE", Error: fmt.Sprintf("article.body exceeds ML limit: %d > %d UTF-8 bytes", len(job.Body), limit)}
	}

	if job.SourceConfigVersion <= 0 {
		return Result{Permanent: true, Error: "source config version is missing"}
	}
	body, err := RequestPayload(job)
	if err != nil {
		return Result{Permanent: true, Error: err.Error(), ErrorCode: "INVALID_ML_REQUEST"}
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		c.BaseURL+"/internal/v0.1/article-processings", bytes.NewReader(body))
	if err != nil {
		return Result{Permanent: true, Error: "invalid ML endpoint"}
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Idempotency-Key", job.DedupeKey)
	status, response, retryAfter, err := c.do(req)
	if err != nil {
		return Result{Error: "ML submission failed", ErrorCode: "ML_TRANSPORT_ERROR", ErrorRetryable: true}
	}
	if status != http.StatusAccepted {
		return classifyStatus(status, retryAfter, response)
	}
	return decodeOperationWithInterval(response, job, c.settings().ML.StatusPollInterval.Std())
}

func RequestPayload(job Job) ([]byte, error) {
	if len(job.DeliveryExtraction) != 0 {
		metadata, err := deliveryMetadata(job)
		if err != nil {
			return nil, err
		}
		job.Metadata = metadata
	}
	article := map[string]any{
		"article_id": job.ArticleID, "article_version_id": job.VersionID,
		"source_id": job.SourceID, "title": job.Title, "body": job.Body,
		"fetched_at":   job.FetchedAt.UTC().Format(time.RFC3339Nano),
		"raw_ref":      "raw-artifact:" + job.RawArtifactID,
		"content_hash": job.ContentHash, "metadata": json.RawMessage(job.Metadata),
	}
	if job.ExternalID != nil {
		article["external_id"] = *job.ExternalID
	}
	if job.CanonicalURL != nil {
		article["canonical_url"] = *job.CanonicalURL
	}
	if job.PublishedAt != nil {
		article["published_at"] = job.PublishedAt.UTC().Format(time.RFC3339Nano)
	}
	if job.Language != nil {
		article["language"] = *job.Language
	}
	if job.Author != nil {
		article["author"] = *job.Author
	}
	if len(job.Metadata) == 0 {
		article["metadata"] = map[string]any{}
	}
	return json.Marshal(map[string]any{
		"article": article,
		"source": map[string]string{
			"source_id": job.SourceID, "name": job.SourceName,
			"source_type": job.SourceType, "source_role": job.SourceRole,
			"trust_level":    job.TrustLevel,
			"config_version": strconv.FormatInt(job.SourceConfigVersion, 10),
		},
		"pipeline_version": job.PipelineVersion,
		"trace_id":         job.ID,
	})
}

func (c Client) do(req *http.Request) (int, []byte, time.Duration, error) {
	ctx, span := otel.Tracer("ingestion/mljobs").Start(req.Context(), "http.ml")
	defer span.End()
	req = req.WithContext(ctx)
	settings := c.settings()
	client := c.HTTP
	if client == nil {
		client = &http.Client{Timeout: settings.ML.RequestTimeout.Std()}
	}
	copyClient := *client
	copyClient.CheckRedirect = func(*http.Request, []*http.Request) error {
		return fmt.Errorf("ML redirect is not allowed")
	}
	req.Header.Set("Authorization", "Bearer "+c.Token)
	otel.GetTextMapPropagator().Inject(ctx, propagation.HeaderCarrier(req.Header))
	resp, err := copyClient.Do(req)
	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, "ML request failed")
		return 0, nil, 0, err
	}
	defer resp.Body.Close()
	span.SetAttributes(attribute.Int("http.response.status_code", resp.StatusCode))
	observability.Upstream("ml", resp.StatusCode)
	body, err := io.ReadAll(io.LimitReader(resp.Body, settings.ML.MaxResponseBytes+1))
	if err != nil || int64(len(body)) > settings.ML.MaxResponseBytes {
		return 0, nil, 0, fmt.Errorf("ML response is unreadable or too large")
	}
	return resp.StatusCode, body, parseRetryAfterWithMax(resp.Header.Get("Retry-After"), settings.ML.RetryAfterMax.Std()), nil
}

func decodeOperation(body []byte, job Job) Result {
	return decodeOperationWithInterval(body, job, 5*time.Second)
}

func decodeOperationWithInterval(body []byte, job Job, interval time.Duration) Result {
	var op operation
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&op); err != nil {
		return protocolError("ML operation response has invalid JSON or field types")
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return protocolError("ML operation response has trailing JSON")
	}
	if strings.TrimSpace(op.ProcessingID) == "" || strings.TrimSpace(op.TraceID) == "" || op.CreatedAt == nil ||
		strings.TrimSpace(op.ArticleVersionID) == "" || strings.TrimSpace(op.PipelineVersion) == "" {
		return protocolError("ML operation response is missing required fields")
	}
	if op.ArticleVersionID != job.VersionID || op.PipelineVersion != job.PipelineVersion {
		return protocolError("ML operation response does not match article version or pipeline")
	}
	if job.ProcessingID != nil && *job.ProcessingID != op.ProcessingID {
		return protocolError("confirmed ML processing ID changed")
	}
	var fields map[string]json.RawMessage
	_ = json.Unmarshal(body, &fields)
	for _, key := range []string{"document_version_id", "event_ids", "error"} {
		if value, ok := fields[key]; ok && bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
			return protocolError("ML operation contains null " + key)
		}
	}
	if _, present := fields["document_version_id"]; present && strings.TrimSpace(op.DocumentVersionID) == "" {
		return protocolError("ML operation contains empty document_version_id")
	}
	if op.EventIDs != nil {
		for _, id := range *op.EventIDs {
			if strings.TrimSpace(id) == "" {
				return protocolError("ML operation contains an empty event ID")
			}
		}
	}
	if op.Error != nil {
		if !validMLErrorCode(op.Error.Code) || strings.TrimSpace(op.Error.Message) == "" || op.Error.Retryable == nil || (op.Error.TraceID != nil && strings.TrimSpace(*op.Error.TraceID) == "") {
			return protocolError("ML operation contains an invalid structured error")
		}
		var errorFields map[string]json.RawMessage
		_ = json.Unmarshal(fields["error"], &errorFields)
		if value, ok := errorFields["trace_id"]; ok && bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
			return protocolError("ML error contains null trace_id")
		}
	}
	result := Result{ProcessingID: op.ProcessingID}
	switch op.State {
	case "QUEUED", "RUNNING":
		result.Pending = true
		result.RetryAfter = interval
	case "SUCCEEDED":
		if strings.TrimSpace(op.DocumentVersionID) == "" || op.EventIDs == nil {
			return protocolError("ML success is missing document_version_id or event_ids")
		}
		if op.DocumentVersionID != job.VersionID {
			return protocolError("ML document_version_id does not match article_version_id")
		}
		result.Succeeded = true
		result.DocumentVersionID = op.DocumentVersionID
		result.EventIDs = *op.EventIDs
	case "FAILED":
		result.Permanent = true
		if op.Error == nil {
			return protocolError("ML failure is missing structured error")
		}
		result.Error = op.Error.Message
		result.ErrorCode = op.Error.Code
		result.ErrorRetryable = *op.Error.Retryable
	default:
		result.Permanent = true
		result.Error = "ML operation has an unknown state"
		result.ErrorCode = "INVALID_ML_RESPONSE"
	}
	return result
}

func classifyStatus(status int, retryAfter time.Duration, body []byte) Result {
	temporary := status == http.StatusTooManyRequests || status >= 500
	result := Result{RetryAfter: retryAfter, Error: fmt.Sprintf("ML returned HTTP %d", status), ErrorRetryable: temporary}
	var remote struct {
		Code      string `json:"code"`
		Message   string `json:"message"`
		Retryable *bool  `json:"retryable"`
	}
	if json.Unmarshal(body, &remote) == nil && strings.TrimSpace(remote.Code) != "" &&
		strings.TrimSpace(remote.Message) != "" && remote.Retryable != nil {
		result.ErrorCode = remote.Code
		result.Error = remote.Message
		result.ErrorRetryable = *remote.Retryable
	}
	if status == http.StatusTooManyRequests || status == http.StatusServiceUnavailable || status >= 500 {
		return result
	}
	result.Permanent = true
	return result
}

func parseRetryAfter(raw string) time.Duration {
	return parseRetryAfterWithMax(raw, time.Hour)
}

func parseRetryAfterWithMax(raw string, maxDelay time.Duration) time.Duration {
	if raw == "" {
		return 0
	}
	if seconds, err := time.ParseDuration(strings.TrimSpace(raw) + "s"); err == nil {
		return min(max(seconds, 0), maxDelay)
	}
	if when, err := http.ParseTime(raw); err == nil {
		return min(max(time.Until(when), 0), maxDelay)
	}
	return 0
}

func protocolError(message string) Result {
	return Result{Permanent: true, ErrorCode: "INVALID_ML_RESPONSE", Error: message}
}

func validMLErrorCode(code string) bool {
	switch code {
	case "VALIDATION_ERROR", "NOT_FOUND", "FORBIDDEN_CONTEXT", "SOURCE_UNAVAILABLE", "ML_PROCESSING_ERROR", "INSUFFICIENT_EVIDENCE", "TIMEOUT", "BUDGET_EXHAUSTED", "CONFLICT", "PROFILE_NOT_READY", "UNSUPPORTED_ADAPTER", "UNSUPPORTED_PIPELINE_VERSION", "PAYLOAD_TOO_LARGE", "INTERNAL_ERROR", "UNAUTHENTICATED", "FORBIDDEN":
		return true
	default:
		return false
	}
}
