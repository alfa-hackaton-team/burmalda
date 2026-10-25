package authn

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/alfa-hackaton-team/burmalda/backend/internal/httpx"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

const sessionCookieName = "session_id"

var ErrUnauthenticated = errors.New("unauthenticated")

type Principal struct {
	SessionID     string
	UserID        string
	DisplayName   *string
	CSRFTokenHash [sha256.Size]byte
}

type Store interface {
	Authenticate(context.Context, [sha256.Size]byte) (Principal, error)
}

type PostgresStore struct {
	pool *pgxpool.Pool
}

func NewPostgresStore(pool *pgxpool.Pool) *PostgresStore {
	return &PostgresStore{pool: pool}
}

func (s *PostgresStore) Authenticate(ctx context.Context, tokenHash [sha256.Size]byte) (Principal, error) {
	var principal Principal
	var csrfHash []byte
	err := s.pool.QueryRow(ctx, `
		SELECT session.id::text, user_account.id::text, user_account.display_name,
		       session.csrf_token_hash
		FROM user_sessions AS session
		JOIN users AS user_account ON user_account.id = session.user_id
		WHERE session.session_token_hash = $1
		  AND session.revoked_at IS NULL
		  AND session.expires_at > now()
		  AND user_account.disabled_at IS NULL
	`, tokenHash[:]).Scan(
		&principal.SessionID,
		&principal.UserID,
		&principal.DisplayName,
		&csrfHash,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return Principal{}, ErrUnauthenticated
	}
	if err != nil {
		return Principal{}, err
	}
	if len(csrfHash) != sha256.Size {
		return Principal{}, errors.New("invalid csrf token hash in session")
	}
	copy(principal.CSRFTokenHash[:], csrfHash)
	return principal, nil
}

type principalContextKey struct{}

func PrincipalFromContext(ctx context.Context) (Principal, bool) {
	principal, ok := ctx.Value(principalContextKey{}).(Principal)
	return principal, ok
}

func Middleware(store Store, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cookies := r.CookiesNamed(sessionCookieName)
		if len(cookies) != 1 {
			unauthenticated(w)
			return
		}
		token, err := base64.RawURLEncoding.DecodeString(cookies[0].Value)
		if err != nil || len(token) != sha256.Size {
			unauthenticated(w)
			return
		}
		tokenHash := sha256.Sum256(token)
		started := time.Now()
		principal, err := store.Authenticate(r.Context(), tokenHash)
		if err != nil {
			if errors.Is(r.Context().Err(), context.DeadlineExceeded) {
				httpx.WriteError(w, http.StatusGatewayTimeout, httpx.ErrorCodeTimeout, "request timed out", true)
				return
			}
			if errors.Is(err, ErrUnauthenticated) {
				unauthenticated(w)
				return
			}
			httpx.WriteError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "internal error", false)
			return
		}
		w.Header().Add("Server-Timing", fmt.Sprintf("session;dur=%.3f", float64(time.Since(started).Microseconds())/1000))
		ctx := context.WithValue(r.Context(), principalContextKey{}, principal)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

func unauthenticated(w http.ResponseWriter) {
	httpx.WriteError(w, http.StatusUnauthorized, "UNAUTHENTICATED", "valid session is required", false)
}
