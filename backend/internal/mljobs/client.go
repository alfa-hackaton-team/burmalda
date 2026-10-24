package mljobs

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
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
}

type Client struct {
	BaseURL string
	Token   string
	HTTP    *http.Client
}

type operation struct {
	ProcessingID      string    `json:"processing_id"`
	ArticleVersionID  string    `json:"article_version_id"`
	PipelineVersion   string    `json:"pipeline_version"`
	State             string    `json:"state"`
	DocumentVersionID string    `json:"document_version_id"`
	EventIDs          *[]string `json:"event_ids"`
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
		return Result{Error: "ML status request failed"}, false
	}
	if status == http.StatusNotFound {
		return Result{}, true
	}
	if status != http.StatusOK {
		return classifyStatus(status, retryAfter), false
	}
	return decodeOperation(body, job), false
}

func (c Client) post(ctx context.Context, job Job) Result {
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
	body, err := json.Marshal(map[string]any{
		"article": article,
		"source": map[string]string{
			"source_id": job.SourceID, "name": job.SourceName,
			"source_type": job.SourceType, "source_role": job.SourceRole,
			"trust_level": job.TrustLevel,
		},
		"pipeline_version": job.PipelineVersion,
		"trace_id":         job.ID,
	})
	if err != nil {
		return Result{Permanent: true, Error: "cannot encode ML request"}
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
		return Result{Error: "ML submission failed"}
	}
	if status != http.StatusAccepted {
		return classifyStatus(status, retryAfter)
	}
	return decodeOperation(response, job)
}

func (c Client) do(req *http.Request) (int, []byte, time.Duration, error) {
	client := c.HTTP
	if client == nil {
		client = &http.Client{Timeout: 20 * time.Second}
	}
	copyClient := *client
	copyClient.CheckRedirect = func(*http.Request, []*http.Request) error {
		return fmt.Errorf("ML redirect is not allowed")
	}
	req.Header.Set("Authorization", "Bearer "+c.Token)
	resp, err := copyClient.Do(req)
	if err != nil {
		return 0, nil, 0, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, (1<<20)+1))
	if err != nil || len(body) > 1<<20 {
		return 0, nil, 0, fmt.Errorf("ML response is unreadable or too large")
	}
	return resp.StatusCode, body, parseRetryAfter(resp.Header.Get("Retry-After")), nil
}

func decodeOperation(body []byte, job Job) Result {
	var op operation
	if err := json.Unmarshal(body, &op); err != nil || strings.TrimSpace(op.ProcessingID) == "" ||
		op.ArticleVersionID != job.VersionID || op.PipelineVersion != job.PipelineVersion {
		return Result{Permanent: true, Error: "ML operation response does not match the job"}
	}
	if job.ProcessingID != nil && *job.ProcessingID != op.ProcessingID {
		return Result{Permanent: true, Error: "confirmed ML processing ID changed"}
	}
	result := Result{ProcessingID: op.ProcessingID}
	switch op.State {
	case "QUEUED", "RUNNING":
		result.Pending = true
		result.RetryAfter = 5 * time.Second
	case "SUCCEEDED":
		if strings.TrimSpace(op.DocumentVersionID) == "" || op.EventIDs == nil {
			return Result{ProcessingID: op.ProcessingID, Permanent: true, Error: "ML success is missing results"}
		}
		for _, eventID := range *op.EventIDs {
			if strings.TrimSpace(eventID) == "" {
				return Result{ProcessingID: op.ProcessingID, Permanent: true, Error: "ML success contains an empty event ID"}
			}
		}
		result.Succeeded = true
		result.DocumentVersionID = op.DocumentVersionID
		result.EventIDs = *op.EventIDs
	case "FAILED":
		result.Permanent = true
		result.Error = "ML processing failed"
	default:
		result.Permanent = true
		result.Error = "ML operation has an unknown state"
	}
	return result
}

func classifyStatus(status int, retryAfter time.Duration) Result {
	result := Result{RetryAfter: retryAfter, Error: fmt.Sprintf("ML returned HTTP %d", status)}
	if status == http.StatusTooManyRequests || status == http.StatusServiceUnavailable || status >= 500 {
		return result
	}
	result.Permanent = true
	return result
}

func parseRetryAfter(raw string) time.Duration {
	if raw == "" {
		return 0
	}
	if seconds, err := time.ParseDuration(strings.TrimSpace(raw) + "s"); err == nil {
		return min(max(seconds, 0), time.Hour)
	}
	if when, err := http.ParseTime(raw); err == nil {
		return min(max(time.Until(when), 0), time.Hour)
	}
	return 0
}
