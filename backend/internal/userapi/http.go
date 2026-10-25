package userapi

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"

	"github.com/alfa-hackaton-team/burmalda/backend/internal/authn"
	"github.com/alfa-hackaton-team/burmalda/backend/internal/httpx"
	"github.com/google/uuid"
)

const defaultPageLimit = 50

type Store interface {
	ListContexts(context.Context, string, string, int) ([]AvailableContext, error)
}

type Me struct {
	UserID      string  `json:"user_id"`
	DisplayName *string `json:"display_name,omitempty"`
}

type BusinessContext struct {
	ContextID string `json:"context_id"`
	Kind      string `json:"kind"`
	CompanyID string `json:"company_id,omitempty"`
	GroupID   string `json:"group_id,omitempty"`
}

type ProfileRef struct {
	ProfileID string `json:"profile_id"`
	Version   string `json:"version"`
}

type AvailableContext struct {
	Context     BusinessContext `json:"context"`
	DisplayName string          `json:"display_name"`
	RoleID      string          `json:"role_id"`
	Profile     *ProfileRef     `json:"profile,omitempty"`
}

type ContextPage struct {
	Items      []AvailableContext `json:"items"`
	NextCursor *string            `json:"next_cursor"`
}

type contextCursor struct {
	UserID    string `json:"user_id"`
	SessionID string `json:"session_id"`
	AfterID   string `json:"after_id"`
}

func ParseCursorSigningKey(value string) ([sha256.Size]byte, error) {
	var key [sha256.Size]byte
	decoded, err := base64.StdEncoding.DecodeString(strings.TrimSpace(value))
	if err != nil || len(decoded) != sha256.Size {
		return key, fmt.Errorf("CURSOR_SIGNING_KEY must be base64 for exactly %d random bytes", sha256.Size)
	}
	copy(key[:], decoded)
	return key, nil
}

func Handler(store Store, cursorSigningKey [sha256.Size]byte) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v0.1/me", getMe)
	mux.HandleFunc("/v0.1/me", methodNotAllowed(http.MethodGet+", "+http.MethodHead))
	mux.HandleFunc("GET /v0.1/contexts", func(w http.ResponseWriter, r *http.Request) {
		listContexts(w, r, store, cursorSigningKey)
	})
	mux.HandleFunc("/v0.1/contexts", methodNotAllowed(http.MethodGet+", "+http.MethodHead))
	mux.HandleFunc("/v0.1/", func(w http.ResponseWriter, _ *http.Request) {
		httpx.WriteError(w, http.StatusNotFound, "NOT_FOUND", "resource not found", false)
	})
	return mux
}

func methodNotAllowed(allow string) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Allow", allow)
		httpx.WriteError(w, http.StatusMethodNotAllowed, "VALIDATION_ERROR", "method not allowed", false)
	}
}

func getMe(w http.ResponseWriter, r *http.Request) {
	principal, ok := authn.PrincipalFromContext(r.Context())
	if !ok {
		httpx.WriteError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "internal error", false)
		return
	}
	if !validEmptyGET(w, r, nil) {
		return
	}
	httpx.WriteJSON(w, http.StatusOK, Me{
		UserID:      principal.UserID,
		DisplayName: principal.DisplayName,
	})
}

func listContexts(w http.ResponseWriter, r *http.Request, store Store, cursorSigningKey [sha256.Size]byte) {
	principal, ok := authn.PrincipalFromContext(r.Context())
	if !ok {
		httpx.WriteError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "internal error", false)
		return
	}
	if !validEmptyGET(w, r, map[string]bool{"cursor": true, "limit": true}) {
		return
	}

	limit, ok := parseLimit(w, r)
	if !ok {
		return
	}
	afterID, ok := parseCursor(w, r, principal.UserID, principal.SessionID, cursorSigningKey)
	if !ok {
		return
	}

	items, err := store.ListContexts(r.Context(), principal.UserID, afterID, limit+1)
	if err != nil {
		if errors.Is(r.Context().Err(), context.DeadlineExceeded) {
			httpx.WriteError(w, http.StatusGatewayTimeout, httpx.ErrorCodeTimeout, "request timed out", true)
			return
		}
		httpx.WriteError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "internal error", false)
		return
	}
	if items == nil {
		items = []AvailableContext{}
	}

	var nextCursor *string
	if len(items) > limit {
		items = items[:limit]
		encoded, err := encodeCursor(contextCursor{
			UserID:    principal.UserID,
			SessionID: principal.SessionID,
			AfterID:   items[len(items)-1].Context.ContextID,
		}, cursorSigningKey)
		if err != nil {
			httpx.WriteError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "internal error", false)
			return
		}
		nextCursor = &encoded
	}

	httpx.WriteJSON(w, http.StatusOK, ContextPage{Items: items, NextCursor: nextCursor})
}

func validEmptyGET(w http.ResponseWriter, r *http.Request, allowedQuery map[string]bool) bool {
	for key := range r.URL.Query() {
		if !allowedQuery[key] {
			httpx.WriteError(w, http.StatusBadRequest, "VALIDATION_ERROR", "unknown query parameter", false)
			return false
		}
	}
	if r.ContentLength != 0 || len(r.TransferEncoding) != 0 {
		httpx.WriteError(w, http.StatusBadRequest, "VALIDATION_ERROR", "GET request body must be empty", false)
		return false
	}
	return true
}

func parseLimit(w http.ResponseWriter, r *http.Request) (int, bool) {
	values, present := r.URL.Query()["limit"]
	if !present {
		return defaultPageLimit, true
	}
	if len(values) != 1 {
		httpx.WriteError(w, http.StatusBadRequest, "VALIDATION_ERROR", "limit must be specified once", false)
		return 0, false
	}
	limit, err := strconv.Atoi(values[0])
	if err != nil || limit < 1 || limit > 100 {
		httpx.WriteError(w, http.StatusBadRequest, "VALIDATION_ERROR", "limit must be an integer from 1 to 100", false)
		return 0, false
	}
	return limit, true
}

func parseCursor(w http.ResponseWriter, r *http.Request, userID, sessionID string, signingKey [sha256.Size]byte) (string, bool) {
	values, present := r.URL.Query()["cursor"]
	if !present {
		return "", true
	}
	if len(values) != 1 || values[0] == "" || len(values[0]) > 1024 {
		httpx.WriteError(w, http.StatusBadRequest, "VALIDATION_ERROR", "invalid cursor", false)
		return "", false
	}
	signed, err := base64.RawURLEncoding.DecodeString(values[0])
	if err != nil || len(signed) <= sha256.Size {
		httpx.WriteError(w, http.StatusBadRequest, "VALIDATION_ERROR", "invalid cursor", false)
		return "", false
	}
	body, signature := signed[:len(signed)-sha256.Size], signed[len(signed)-sha256.Size:]
	mac := hmac.New(sha256.New, signingKey[:])
	_, _ = mac.Write(body)
	if !hmac.Equal(signature, mac.Sum(nil)) {
		httpx.WriteError(w, http.StatusBadRequest, "VALIDATION_ERROR", "invalid cursor", false)
		return "", false
	}
	var cursor contextCursor
	decoder := json.NewDecoder(io.LimitReader(bytes.NewReader(body), 2048))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&cursor); err != nil || cursor.UserID != userID || cursor.SessionID != sessionID || uuid.Validate(cursor.AfterID) != nil {
		httpx.WriteError(w, http.StatusBadRequest, "VALIDATION_ERROR", "invalid cursor", false)
		return "", false
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		httpx.WriteError(w, http.StatusBadRequest, "VALIDATION_ERROR", "invalid cursor", false)
		return "", false
	}
	return cursor.AfterID, true
}

func encodeCursor(cursor contextCursor, signingKey [sha256.Size]byte) (string, error) {
	body, err := json.Marshal(cursor)
	if err != nil {
		return "", err
	}
	mac := hmac.New(sha256.New, signingKey[:])
	_, _ = mac.Write(body)
	signed := append(body, mac.Sum(nil)...)
	return base64.RawURLEncoding.EncodeToString(signed), nil
}
