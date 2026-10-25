package sources

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"

	"github.com/alfa-hackaton-team/burmalda/backend/internal/httpx"
)

type Permission string

const (
	ReadSources   Permission = "sources:read"
	ManageSources Permission = "sources:manage"
	SetTrust      Permission = "sources:trust"
)

type Authorize func(*http.Request, Permission) int

type Registry interface {
	Create(ctx context.Context, cfg Config) (Source, error)
	Get(ctx context.Context, id string) (Source, error)
	Update(ctx context.Context, id string, expectedVersion int64, patch []byte) (Source, error)
	List(ctx context.Context, limit int, cursor string) (Page, error)
}

func AdminHandler(registry Registry, authorize Authorize, cursorKey [sha256.Size]byte) (http.Handler, error) {
	if registry == nil || authorize == nil || cursorKey == ([sha256.Size]byte{}) {
		return nil, fmt.Errorf("source admin handler requires registry, authorizer and cursor key")
	}
	mux := http.NewServeMux()
	h := adminHandler{registry: registry, authorize: authorize, cursorKey: cursorKey}
	mux.HandleFunc("GET /v0.1/admin/sources", h.list)
	mux.HandleFunc("POST /v0.1/admin/sources", h.create)
	mux.HandleFunc("GET /v0.1/admin/sources/{source_id}", h.get)
	mux.HandleFunc("PATCH /v0.1/admin/sources/{source_id}", h.patch)
	mux.HandleFunc("/v0.1/admin/sources", sourceMethodNotAllowed("GET, HEAD, POST"))
	mux.HandleFunc("/v0.1/admin/sources/{source_id}", sourceMethodNotAllowed("GET, HEAD, PATCH"))
	mux.HandleFunc("/", func(w http.ResponseWriter, _ *http.Request) {
		writeError(w, 404, "NOT_FOUND", "resource not found")
	})
	return mux, nil
}

type adminHandler struct {
	registry  Registry
	authorize Authorize
	cursorKey [sha256.Size]byte
}

func (h adminHandler) allowed(w http.ResponseWriter, r *http.Request, permission Permission) bool {
	status := h.authorize(r, permission)
	if status == 0 {
		return true
	}
	if status == http.StatusGatewayTimeout {
		httpx.WriteError(w, status, httpx.ErrorCodeTimeout, "request timed out", true)
		return false
	}
	if status == http.StatusInternalServerError {
		writeError(w, status, "INTERNAL_ERROR", "internal error")
		return false
	}
	if status != http.StatusUnauthorized {
		status = http.StatusForbidden
	}
	code := "FORBIDDEN"
	if status == http.StatusUnauthorized {
		code = "UNAUTHENTICATED"
	}
	writeError(w, status, code, "access denied")
	return false
}

func (h adminHandler) list(w http.ResponseWriter, r *http.Request) {
	if !h.allowed(w, r, ReadSources) {
		return
	}
	if !validSourceRequest(w, r, map[string]bool{"limit": true, "cursor": true}) {
		return
	}
	limit := 25
	if values, present := r.URL.Query()["limit"]; present {
		raw := values[0]
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed < 1 || parsed > 100 {
			writeError(w, 400, "VALIDATION_ERROR", "invalid limit")
			return
		}
		limit = parsed
	}
	if values, present := r.URL.Query()["cursor"]; present && (len(values) != 1 || values[0] == "") {
		writeError(w, 400, "VALIDATION_ERROR", "invalid cursor")
		return
	}
	cursor := r.URL.Query().Get("cursor")
	if cursor != "" {
		var err error
		cursor, err = h.parseCursor(r, cursor, limit)
		if err != nil {
			respondError(w, r.Context(), err)
			return
		}
	}
	page, err := h.registry.List(r.Context(), limit, cursor)
	if err != nil {
		respondError(w, r.Context(), err)
		return
	}
	if page.NextCursor != nil {
		signed, err := h.signCursor(r, *page.NextCursor, limit)
		if err != nil {
			respondError(w, r.Context(), err)
			return
		}
		page.NextCursor = &signed
	}
	for index := range page.Items {
		sanitizeSource(&page.Items[index])
	}
	writeJSON(w, http.StatusOK, page)
}

func (h adminHandler) create(w http.ResponseWriter, r *http.Request) {
	if !h.allowed(w, r, ManageSources) {
		return
	}
	if !h.allowed(w, r, SetTrust) {
		return
	}
	if !validSourceRequest(w, r, nil) {
		return
	}
	if len(r.Header.Values("Idempotency-Key")) != 0 {
		writeError(w, 400, "VALIDATION_ERROR", "Idempotency-Key is not supported for source creation yet")
		return
	}
	body, ok := readBody(w, r)
	if !ok {
		return
	}
	cfg, err := ParseCreateJSON(body)
	if err != nil {
		respondError(w, r.Context(), err)
		return
	}
	source, err := h.registry.Create(r.Context(), cfg)
	if err != nil {
		respondError(w, r.Context(), err)
		return
	}
	w.Header().Set("ETag", `"`+source.ConfigVersion+`"`)
	w.Header().Set("Location", "/v0.1/admin/sources/"+source.SourceID)
	sanitizeSource(&source)
	writeJSON(w, http.StatusCreated, source)
}

func (h adminHandler) get(w http.ResponseWriter, r *http.Request) {
	if !h.allowed(w, r, ReadSources) {
		return
	}
	if !validSourceRequest(w, r, nil) || !validSourceID(w, r) {
		return
	}
	source, err := h.registry.Get(r.Context(), r.PathValue("source_id"))
	if err != nil {
		respondError(w, r.Context(), err)
		return
	}
	w.Header().Set("ETag", `"`+source.ConfigVersion+`"`)
	sanitizeSource(&source)
	writeJSON(w, http.StatusOK, source)
}

func (h adminHandler) patch(w http.ResponseWriter, r *http.Request) {
	if !h.allowed(w, r, ManageSources) {
		return
	}
	if !validSourceRequest(w, r, nil) || !validSourceID(w, r) {
		return
	}
	if len(r.Header.Values("If-Match")) > 1 {
		writeError(w, 400, "VALIDATION_ERROR", "If-Match must be specified once")
		return
	}
	version, err := parseIfMatch(r.Header.Get("If-Match"))
	if err != nil {
		status := http.StatusBadRequest
		if r.Header.Get("If-Match") == "" {
			status = http.StatusPreconditionRequired
		}
		writeError(w, status, "VALIDATION_ERROR", "If-Match must be a quoted config_version")
		return
	}
	body, ok := readBody(w, r)
	if !ok {
		return
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(body, &fields); err != nil || fields == nil {
		writeError(w, 400, "VALIDATION_ERROR", "expected a JSON object")
		return
	}
	if _, hasTrust := fields["trust_level"]; hasTrust && !h.allowed(w, r, SetTrust) {
		return
	}
	if _, hasRole := fields["source_role"]; hasRole && !h.allowed(w, r, SetTrust) {
		return
	}
	source, err := h.registry.Update(r.Context(), r.PathValue("source_id"), version, body)
	if err != nil {
		respondError(w, r.Context(), err)
		return
	}
	w.Header().Set("ETag", `"`+source.ConfigVersion+`"`)
	sanitizeSource(&source)
	writeJSON(w, http.StatusOK, source)
}

func parseIfMatch(raw string) (int64, error) {
	if len(raw) < 3 || raw[0] != '"' || raw[len(raw)-1] != '"' {
		return 0, fmt.Errorf("invalid If-Match")
	}
	value, err := strconv.ParseInt(raw[1:len(raw)-1], 10, 64)
	if err != nil || value < 1 || strconv.FormatInt(value, 10) != raw[1:len(raw)-1] {
		return 0, fmt.Errorf("invalid If-Match")
	}
	return value, nil
}

func readBody(w http.ResponseWriter, r *http.Request) ([]byte, bool) {
	const maxBody = 64 << 10
	r.Body = http.MaxBytesReader(w, r.Body, maxBody)
	var fields sourceInput
	if !httpx.DecodeJSONObject(w, r, &fields) {
		return nil, false
	}
	body, err := json.Marshal(fields)
	if err != nil {
		respondError(w, r.Context(), err)
		return nil, false
	}
	return body, true
}

// Raw fields preserve absence/null for the existing create/patch validators.
type sourceInput struct {
	Name                   json.RawMessage `json:"name,omitempty"`
	SourceType             json.RawMessage `json:"source_type,omitempty"`
	SourceRole             json.RawMessage `json:"source_role,omitempty"`
	TrustLevel             json.RawMessage `json:"trust_level,omitempty"`
	AdapterType            json.RawMessage `json:"adapter_type,omitempty"`
	Endpoint               json.RawMessage `json:"endpoint,omitempty"`
	Enabled                json.RawMessage `json:"enabled,omitempty"`
	PollingIntervalSeconds json.RawMessage `json:"polling_interval_seconds,omitempty"`
	CredentialRef          json.RawMessage `json:"credential_ref,omitempty"`
	AdapterConfig          json.RawMessage `json:"adapter_config,omitempty"`
}

func validSourceID(w http.ResponseWriter, r *http.Request) bool {
	if !uuidPattern.MatchString(r.PathValue("source_id")) {
		writeError(w, 400, "VALIDATION_ERROR", "invalid source_id")
		return false
	}
	return true
}

func validSourceRequest(w http.ResponseWriter, r *http.Request, allowed map[string]bool) bool {
	query, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil {
		writeError(w, 400, "VALIDATION_ERROR", "invalid query")
		return false
	}
	for name, values := range query {
		if !allowed[name] || len(values) != 1 || values[0] == "" {
			writeError(w, 400, "VALIDATION_ERROR", "invalid query parameter")
			return false
		}
	}
	if (r.Method == http.MethodGet || r.Method == http.MethodHead) && (r.ContentLength != 0 || len(r.TransferEncoding) != 0) {
		writeError(w, 400, "VALIDATION_ERROR", "GET request body must be empty")
		return false
	}
	return true
}

func sourceMethodNotAllowed(allow string) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Allow", allow)
		writeError(w, 405, "VALIDATION_ERROR", "method not allowed")
	}
}

func sanitizeSource(source *Source) {
	// Worker diagnostics may contain private upstream URLs or tokens. The public
	// projection exposes a stable summary, never the stored diagnostic text.
	if source.LastError != nil {
		message := "source synchronization failed"
		source.LastError = &message
	}
}

func respondError(w http.ResponseWriter, ctx context.Context, err error) {
	switch {
	case errors.Is(ctx.Err(), context.DeadlineExceeded), errors.Is(err, context.DeadlineExceeded):
		httpx.WriteError(w, 504, httpx.ErrorCodeTimeout, "request timed out", true)
	case errors.Is(err, ErrInvalid):
		writeError(w, 400, "VALIDATION_ERROR", "invalid source configuration")
	case errors.Is(err, ErrNotFound):
		writeError(w, 404, "NOT_FOUND", "source not found")
	case errors.Is(err, ErrConflict):
		writeError(w, 409, "CONFLICT", "source configuration changed")
	default:
		writeError(w, 500, "INTERNAL_ERROR", "internal error")
	}
}

func writeError(w http.ResponseWriter, status int, code, message string) {
	httpx.WriteError(w, status, code, message, false)
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	httpx.WriteJSON(w, status, value)
}
