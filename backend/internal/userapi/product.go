package userapi

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/alfa-hackaton-team/burmalda/backend/internal/apicontract"
	"github.com/alfa-hackaton-team/burmalda/backend/internal/authn"
	"github.com/alfa-hackaton-team/burmalda/backend/internal/httpx"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

type object = map[string]any

type apiFailure struct {
	status        int
	code, message string
	retryable     bool
	retryAfter    time.Duration
}

func (e *apiFailure) Error() string { return e.code }
func fail(status int, code, message string) error {
	return &apiFailure{status: status, code: code, message: message}
}
func unavailable() error {
	return &apiFailure{status: 503, code: "SOURCE_UNAVAILABLE", message: "required service is unavailable", retryable: true}
}
func productError(w http.ResponseWriter, r *http.Request, err error) {
	var f *apiFailure
	switch {
	case errors.As(err, &f):
		if f.retryAfter > 0 {
			w.Header().Set("Retry-After", strconv.Itoa(int(f.retryAfter.Seconds())+1))
		} else if f.status == 429 {
			w.Header().Set("Retry-After", strconv.Itoa(int(time.Until(time.Now().Truncate(time.Hour).Add(time.Hour)).Seconds())+1))
		}
		httpx.WriteError(w, f.status, f.code, f.message, f.retryable)
	case errors.Is(err, context.DeadlineExceeded), errors.Is(r.Context().Err(), context.DeadlineExceeded):
		httpx.WriteError(w, 504, "TIMEOUT", "request timed out", true)
	case errors.Is(err, pgx.ErrNoRows):
		httpx.WriteError(w, 404, "NOT_FOUND", "resource not found", false)
	default:
		httpx.WriteError(w, 500, "INTERNAL_ERROR", "internal error", false)
	}
}
func str(v any) string { s, _ := v.(string); return s }
func obj(v any) object { o, _ := v.(map[string]any); return o }
func array(v any) []any {
	a, _ := v.([]any)
	if a == nil {
		return []any{}
	}
	return a
}
func decode(raw []byte) object { var o object; _ = json.Unmarshal(raw, &o); return o }
func raw(v any) []byte         { b, _ := json.Marshal(v); return b }
func id(v string) (string, error) {
	u, e := uuid.Parse(v)
	if e != nil {
		return "", fail(400, "VALIDATION_ERROR", "invalid identifier")
	}
	return u.String(), nil
}
func number(v any) int { f, _ := v.(float64); return int(f) }
func (s *PostgresStore) ConfigureML(baseURL, token, version string) {
	s.ml = MLClient{BaseURL: strings.TrimRight(baseURL, "/"), Token: token, Settings: s.settings}
	s.analysisVersion = strings.TrimSpace(version)
}

type productHandler struct {
	store *PostgresStore
	key   [32]byte
}

func registerProduct(mux *http.ServeMux, store Store, key [32]byte) {
	s, _ := store.(*PostgresStore)
	h := productHandler{s, key}
	routes := []struct {
		method, path string
		handler      func(context.Context, string, *http.Request) (any, int, error)
	}{
		{"POST", "/v0.1/auth/logout", h.logout}, {"GET", "/v0.1/news", h.news}, {"GET", "/v0.1/feed", h.feed}, {"GET", "/v0.1/alerts", h.alerts},
		{"POST", "/v0.1/insights", h.createInsight}, {"GET", "/v0.1/insights/{insight_id}", h.insight},
		{"GET", "/v0.1/generations/{generation_id}", h.generation},
		{"GET", "/v0.1/insights/{insight_id}/conversations", h.conversations},
		{"POST", "/v0.1/insights/{insight_id}/conversations", h.createConversation},
		{"GET", "/v0.1/conversations/{conversation_id}/messages", h.messages},
		{"POST", "/v0.1/conversations/{conversation_id}/messages", h.ask},
		{"POST", "/v0.1/insights/{insight_id}/feedback", h.feedback},
		{"GET", "/v0.1/traces/{trace_id}", h.trace}, {"POST", "/v0.1/analytics/events", h.analytics},
	}
	paths := map[string]string{}
	for _, route := range routes {
		fn := route.handler
		path, method := route.path, route.method
		paths[path] += method + ", "
		mux.HandleFunc(method+" "+path, func(w http.ResponseWriter, r *http.Request) {
			p, ok := authn.PrincipalFromContext(r.Context())
			if !ok {
				httpx.WriteError(w, 401, "UNAUTHENTICATED", "valid session is required", false)
				return
			}
			if h.store == nil {
				productError(w, r, unavailable())
				return
			}
			result, status, err := fn(r.Context(), p.UserID, r)
			if err != nil {
				productError(w, r, err)
				return
			}
			if status == 202 {
				w.Header().Set("Retry-After", "5")
				if o := obj(result); o != nil && o["generation_id"] != nil {
					w.Header().Set("Location", "/v0.1/generations/"+str(o["generation_id"]))
				}
			}
			if path == "/v0.1/auth/logout" {
				for _, name := range []string{"session_id", "csrf_token"} {
					http.SetCookie(w, &http.Cookie{Name: name, Value: "", Path: "/", HttpOnly: name == "session_id", SameSite: http.SameSiteLaxMode, MaxAge: -1, Expires: time.Unix(1, 0)})
				}
			}
			httpx.WriteJSON(w, status, result)
		})
	}
	for path, methods := range paths {
		if strings.Contains(methods, "GET") {
			methods += "HEAD, "
		}
		mux.HandleFunc(path, methodNotAllowed(strings.TrimSuffix(methods, ", ")))
	}
}
func query(r *http.Request, allowed ...string) error {
	values, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil {
		return fail(400, "VALIDATION_ERROR", "invalid query")
	}
	for k, v := range values {
		found := false
		for _, name := range allowed {
			if k == name {
				found = true
			}
		}
		if !found || len(v) != 1 || v[0] == "" {
			return fail(400, "VALIDATION_ERROR", "invalid query parameter")
		}
	}
	if (r.Method == "GET" || r.Method == "HEAD") && (r.ContentLength != 0 || len(r.TransferEncoding) > 0) {
		return fail(400, "VALIDATION_ERROR", "request body must be empty")
	}
	return nil
}
func readInput(r *http.Request, dst any) error {
	if e := httpx.ReadJSONObject(r, dst); e != nil {
		return fail(e.Status, e.Code, e.Message)
	}
	return nil
}

func pathID(r *http.Request, name string) (string, error) { return id(r.PathValue(name)) }
func key(r *http.Request, required bool) (string, error) {
	v := r.Header.Values("Idempotency-Key")
	if len(v) == 0 && !required {
		return "", nil
	}
	if len(v) != 1 || len(v[0]) < 1 || len(v[0]) > 200 || strings.TrimSpace(v[0]) != v[0] {
		return "", fail(400, "VALIDATION_ERROR", "invalid Idempotency-Key")
	}
	return v[0], nil
}
func checked(family, name string, v any) error {
	if err := apicontract.Validate(family, name, v); err != nil {
		return fail(502, "INVALID_ML_RESPONSE", "service returned an invalid response")
	}
	return nil
}
func (s *PostgresStore) context(ctx context.Context, user, contextID string) (object, error) {
	return contextFrom(ctx, s.pool, user, contextID)
}

type contextReader interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}

func contextFrom(ctx context.Context, db contextReader, user, contextID string) (object, error) {
	var kind, company, group, profile, role string
	var version int
	var data []byte
	var status string
	err := db.QueryRow(ctx, `SELECT c.subject_type,COALESCE(c.company_id::text,''),COALESCE(c.group_id::text,''),c.profile_id::text,c.role_code,p.version,p.profile_data,p.status
 FROM business_contexts c JOIN business_profiles p ON p.id=c.profile_id JOIN roles r ON r.code=c.role_code AND r.enabled
 WHERE c.id=$2::uuid AND EXISTS(SELECT 1 FROM context_access_grants g WHERE g.user_id=$1::uuid AND g.role_code=c.role_code AND g.revoked_at IS NULL AND(g.expires_at IS NULL OR g.expires_at>now()) AND((g.company_id=c.company_id AND c.subject_type='company') OR(g.group_id=c.group_id AND c.subject_type='group')))`, user, contextID).Scan(&kind, &company, &group, &profile, &role, &version, &data, &status)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, fail(403, "FORBIDDEN_CONTEXT", "context is not available")
	}
	if err != nil {
		return nil, err
	}
	profileData := decode(data)
	features := array(profileData["profile_features"])
	delete(profileData, "profile_features")
	var companyID, groupID any
	if company != "" {
		companyID = company
	}
	if group != "" {
		groupID = group
	}
	return object{"business_context_id": contextID, "subject_type": kind, "company_id": companyID, "group_id": groupID, "profile_id": profile, "profile_version": version, "role_code": role, "profile_data": profileData, "profile_features": features, "_status": status}, nil
}
func publicContext(c object) object {
	kind := "COMPANY"
	field := "company_id"
	if c["subject_type"] == "group" {
		kind = "GROUP"
		field = "group_id"
	}
	return object{"context_id": c["business_context_id"], "kind": kind, field: c[field]}
}
func profileRef(c object) object {
	return object{"profile_id": c["profile_id"], "version": fmt.Sprint(c["profile_version"])}
}
func mlContext(c object) object {
	out := object{}
	for k, v := range c {
		if !strings.HasPrefix(k, "_") {
			out[k] = v
		}
	}
	return out
}
func requireProfile(c object) error {
	if c["_status"] != "active" {
		return fail(409, "PROFILE_NOT_READY", "company profile is not ready")
	}
	if err := apicontract.Validate("ml", "Context", mlContext(c)); err != nil {
		return fail(409, "PROFILE_NOT_READY", "company profile is invalid or incomplete")
	}
	return nil
}
func (s *PostgresStore) limit(ctx context.Context, user, name string, max int) error {
	hash := sha256.Sum256([]byte(user))
	var used int
	err := s.pool.QueryRow(ctx, `INSERT INTO rate_limit_buckets(subject_hash,limit_name,window_started_at,used,expires_at) VALUES($1,$2,date_trunc('hour',now()),1,date_trunc('hour',now())+interval '2 hours')
 ON CONFLICT(subject_hash,limit_name,window_started_at) DO UPDATE SET used=rate_limit_buckets.used+1 WHERE rate_limit_buckets.used<$3 RETURNING used`, hash[:], name, max).Scan(&used)
	if errors.Is(err, pgx.ErrNoRows) {
		return fail(429, "BUDGET_EXHAUSTED", "request limit reached")
	}
	return err
}
func pageLimit(r *http.Request) (int, error) {
	if r.URL.Query().Get("limit") == "" {
		return 50, nil
	}
	n, e := strconv.Atoi(r.URL.Query().Get("limit"))
	if e != nil || n < 1 || n > 100 {
		return 0, fail(400, "VALIDATION_ERROR", "invalid limit")
	}
	return n, nil
}
