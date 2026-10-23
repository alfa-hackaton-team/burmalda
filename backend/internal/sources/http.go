package sources

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"strconv"
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

// AdminHandler is mountable only when real session/permission authorization is supplied.
func AdminHandler(registry Registry, authorize Authorize) (http.Handler, error) {
	if registry == nil || authorize == nil {
		return nil, fmt.Errorf("source admin handler requires registry and authorizer")
	}
	mux := http.NewServeMux()
	h := adminHandler{registry: registry, authorize: authorize}
	mux.HandleFunc("GET /v0.1/admin/sources", h.list)
	mux.HandleFunc("POST /v0.1/admin/sources", h.create)
	mux.HandleFunc("GET /v0.1/admin/sources/{source_id}", h.get)
	mux.HandleFunc("PATCH /v0.1/admin/sources/{source_id}", h.patch)
	return mux, nil
}

type adminHandler struct {
	registry  Registry
	authorize Authorize
}

func (h adminHandler) allowed(w http.ResponseWriter, r *http.Request, permission Permission) bool {
	status := h.authorize(r, permission)
	if status == 0 {
		return true
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
	limit := 25
	if raw := r.URL.Query().Get("limit"); raw != "" {
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
	page, err := h.registry.List(r.Context(), limit, r.URL.Query().Get("cursor"))
	if err != nil {
		respondError(w, err)
		return
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
		respondError(w, err)
		return
	}
	source, err := h.registry.Create(r.Context(), cfg)
	if err != nil {
		respondError(w, err)
		return
	}
	w.Header().Set("ETag", `"`+source.ConfigVersion+`"`)
	w.Header().Set("Location", "/v0.1/admin/sources/"+source.SourceID)
	writeJSON(w, http.StatusCreated, source)
}

func (h adminHandler) get(w http.ResponseWriter, r *http.Request) {
	if !h.allowed(w, r, ReadSources) {
		return
	}
	source, err := h.registry.Get(r.Context(), r.PathValue("source_id"))
	if err != nil {
		respondError(w, err)
		return
	}
	w.Header().Set("ETag", `"`+source.ConfigVersion+`"`)
	writeJSON(w, http.StatusOK, source)
}

func (h adminHandler) patch(w http.ResponseWriter, r *http.Request) {
	if !h.allowed(w, r, ManageSources) {
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
		respondError(w, err)
		return
	}
	w.Header().Set("ETag", `"`+source.ConfigVersion+`"`)
	writeJSON(w, http.StatusOK, source)
}

func parseIfMatch(raw string) (int64, error) {
	if len(raw) < 3 || raw[0] != '"' || raw[len(raw)-1] != '"' {
		return 0, fmt.Errorf("invalid If-Match")
	}
	value, err := strconv.ParseInt(raw[1:len(raw)-1], 10, 64)
	if err != nil || value < 1 {
		return 0, fmt.Errorf("invalid If-Match")
	}
	return value, nil
}

func readBody(w http.ResponseWriter, r *http.Request) ([]byte, bool) {
	contentType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || contentType != "application/json" {
		writeError(w, 400, "VALIDATION_ERROR", "Content-Type must be application/json")
		return nil, false
	}
	const maxBody = 64 << 10
	body, err := io.ReadAll(io.LimitReader(r.Body, maxBody+1))
	if err != nil || len(body) > maxBody {
		writeError(w, 400, "VALIDATION_ERROR", "request body is too large or unreadable")
		return nil, false
	}
	return body, true
}

func respondError(w http.ResponseWriter, err error) {
	switch {
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
	writeJSON(w, status, struct {
		Code      string `json:"code"`
		Message   string `json:"message"`
		Retryable bool   `json:"retryable"`
	}{Code: code, Message: message})
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	body, err := json.Marshal(value)
	if err != nil {
		http.Error(w, "internal error", 500)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = w.Write(append(body, '\n'))
}
