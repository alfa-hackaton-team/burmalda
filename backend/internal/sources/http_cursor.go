package sources

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"

	"github.com/alfa-hackaton-team/burmalda/backend/internal/authn"
)

type adminCursor struct {
	UserID    string `json:"user_id"`
	SessionID string `json:"session_id"`
	Limit     int    `json:"limit"`
	Cursor    string `json:"cursor"`
	Scope     string `json:"scope"`
}

func (h adminHandler) signCursor(r *http.Request, cursor string, limit int) (string, error) {
	principal, ok := authn.PrincipalFromContext(r.Context())
	if !ok {
		return "", errors.New("missing principal")
	}
	body, err := json.Marshal(adminCursor{principal.UserID, principal.SessionID, limit, cursor, "admin/sources"})
	if err != nil {
		return "", err
	}
	mac := hmac.New(sha256.New, h.cursorKey[:])
	_, _ = mac.Write(body)
	return base64.RawURLEncoding.EncodeToString(append(body, mac.Sum(nil)...)), nil
}

func (h adminHandler) parseCursor(r *http.Request, value string, limit int) (string, error) {
	principal, ok := authn.PrincipalFromContext(r.Context())
	if !ok || len(value) > 2048 {
		return "", ErrInvalid
	}
	data, err := base64.RawURLEncoding.DecodeString(value)
	if err != nil || len(data) <= sha256.Size {
		return "", ErrInvalid
	}
	body, signature := data[:len(data)-sha256.Size], data[len(data)-sha256.Size:]
	mac := hmac.New(sha256.New, h.cursorKey[:])
	_, _ = mac.Write(body)
	if !hmac.Equal(signature, mac.Sum(nil)) {
		return "", ErrInvalid
	}
	var cursor adminCursor
	if json.Unmarshal(body, &cursor) != nil || cursor.UserID != principal.UserID || cursor.SessionID != principal.SessionID || cursor.Limit != limit || cursor.Scope != "admin/sources" || cursor.Cursor == "" {
		return "", ErrInvalid
	}
	return cursor.Cursor, nil
}
