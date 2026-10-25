package sources

import (
	"context"
	"errors"
	"net/http"

	"github.com/alfa-hackaton-team/burmalda/backend/internal/authn"
	"github.com/jackc/pgx/v5"
)

type PermissionQuerier interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}

// NewPostgresAuthorizer checks the exact administrative grant on every request.
// Context grants and other source permissions never imply this permission.
func NewPostgresAuthorizer(db PermissionQuerier) Authorize {
	return func(r *http.Request, permission Permission) int {
		principal, ok := authn.PrincipalFromContext(r.Context())
		if !ok {
			return http.StatusUnauthorized
		}
		if db == nil || (permission != ReadSources && permission != ManageSources && permission != SetTrust) {
			return http.StatusForbidden
		}
		var allowed bool
		err := db.QueryRow(r.Context(), `SELECT EXISTS (
			SELECT 1 FROM admin_permission_grants AS grant_row
			JOIN users AS account ON account.id = grant_row.user_id
			WHERE grant_row.user_id = $1::uuid AND grant_row.permission = $2
			  AND grant_row.revoked_at IS NULL
			  AND (grant_row.expires_at IS NULL OR grant_row.expires_at > now())
			  AND account.disabled_at IS NULL
		)`, principal.UserID, string(permission)).Scan(&allowed)
		if err != nil {
			if errors.Is(r.Context().Err(), context.DeadlineExceeded) {
				return http.StatusGatewayTimeout
			}
			return http.StatusInternalServerError
		}
		if !allowed {
			return http.StatusForbidden
		}
		return 0
	}
}
