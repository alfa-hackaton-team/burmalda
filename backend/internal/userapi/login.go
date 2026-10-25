package userapi

import (
	"context"
	"crypto/pbkdf2"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/alfa-hackaton-team/burmalda/backend/internal/authn"
	"github.com/alfa-hackaton-team/burmalda/backend/internal/httpx"
	"github.com/jackc/pgx/v5"
)

func (s *PostgresStore) LoginHandler(origins map[string]struct{}, enabled, secure bool) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" {
			methodNotAllowed("POST")(w, r)
			return
		}
		if !enabled {
			httpx.WriteError(w, 404, "NOT_FOUND", "resource not found", false)
			return
		}
		origin := r.Header.Values("Origin")
		if len(origin) != 1 {
			httpx.WriteError(w, 403, "FORBIDDEN", "origin is required", false)
			return
		}
		if _, ok := origins[origin[0]]; !ok {
			httpx.WriteError(w, 403, "FORBIDDEN", "origin is not allowed", false)
			return
		}
		if e := query(r); e != nil {
			productError(w, r, e)
			return
		}
		var input struct {
			Login    string `json:"login"`
			Password string `json:"password"`
		}
		if e := readInput(r, &input); e != nil {
			productError(w, r, e)
			return
		}
		if input.Login == "" || len(input.Login) > 200 || len(input.Password) > 1024 {
			httpx.WriteError(w, 401, "UNAUTHENTICATED", "invalid credentials", false)
			return
		}
		host, _, _ := net.SplitHostPort(r.RemoteAddr)
		if e := s.limit(r.Context(), host, "login-ip", 30); e != nil {
			productError(w, r, e)
			return
		}
		if e := s.limit(r.Context(), strings.ToLower(input.Login), "login-account", 15); e != nil {
			productError(w, r, e)
			return
		}
		var user string
		var salt, stored []byte
		var iterations int
		e := s.pool.QueryRow(r.Context(), `SELECT p.user_id::text,p.salt,p.password_hash,p.iterations FROM user_passwords p JOIN users u ON u.id=p.user_id AND u.disabled_at IS NULL WHERE p.login=$1`, strings.ToLower(input.Login)).Scan(&user, &salt, &stored, &iterations)
		if e != nil && e != pgx.ErrNoRows {
			productError(w, r, e)
			return
		}
		exists := e == nil
		if !exists {
			salt = make([]byte, 32)
			stored = make([]byte, 32)
			iterations = 600000
		}
		computed, err := pbkdf2.Key(sha256.New, input.Password, salt, iterations, 32)
		if err != nil {
			productError(w, r, err)
			return
		}
		if subtle.ConstantTimeCompare(computed, stored) != 1 || !exists {
			httpx.WriteError(w, 401, "UNAUTHENTICATED", "invalid credentials", false)
			return
		}
		session, csrf := make([]byte, 32), make([]byte, 32)
		if _, e = rand.Read(session); e != nil {
			productError(w, r, e)
			return
		}
		if _, e = rand.Read(csrf); e != nil {
			productError(w, r, e)
			return
		}
		sessionHash, csrfHash := sha256.Sum256(session), sha256.Sum256(csrf)
		expires := time.Now().UTC().Add(8 * time.Hour)

		tx, e := s.pool.Begin(r.Context())
		if e != nil {
			productError(w, r, e)
			return
		}
		defer tx.Rollback(r.Context())
		previous := r.CookiesNamed("session_id")
		if len(previous) == 1 {
			decoded, err := base64.RawURLEncoding.DecodeString(previous[0].Value)
			if err == nil && len(decoded) == 32 {
				oldHash := sha256.Sum256(decoded)
				if _, e = tx.Exec(r.Context(), `UPDATE user_sessions SET revoked_at=now() WHERE user_id=$1 AND session_token_hash=$2 AND revoked_at IS NULL`, user, oldHash[:]); e != nil {
					productError(w, r, e)
					return
				}
			}
		}
		if _, e = tx.Exec(r.Context(), `INSERT INTO user_sessions(user_id,session_token_hash,csrf_token_hash,expires_at) VALUES($1,$2,$3,$4)`, user, sessionHash[:], csrfHash[:], expires); e != nil {
			productError(w, r, e)
			return
		}
		if e = tx.Commit(r.Context()); e != nil {
			productError(w, r, e)
			return
		}

		for name, value := range map[string]string{"session_id": base64.RawURLEncoding.EncodeToString(session), "csrf_token": base64.RawURLEncoding.EncodeToString(csrf)} {
			http.SetCookie(w, &http.Cookie{Name: name, Value: value, Path: "/", Secure: secure, HttpOnly: name == "session_id", SameSite: http.SameSiteLaxMode, Expires: expires, MaxAge: 8 * 60 * 60})
		}
		httpx.WriteJSON(w, 200, object{"user_id": user, "expires_at": expires})
	})
}
func (h productHandler) logout(ctx context.Context, user string, r *http.Request) (any, int, error) {
	if e := query(r); e != nil {
		return nil, 0, e
	}
	var input struct{}
	if e := readInput(r, &input); e != nil {
		return nil, 0, e
	}
	p, _ := authn.PrincipalFromContext(ctx)
	_, e := h.store.pool.Exec(ctx, `UPDATE user_sessions SET revoked_at=now() WHERE id=$1::uuid AND user_id=$2::uuid`, p.SessionID, user)
	return object{"signed_out": true}, 200, e
}
