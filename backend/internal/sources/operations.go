package sources

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/alfa-hackaton-team/burmalda/backend/internal/authn"
	"github.com/alfa-hackaton-team/burmalda/backend/internal/httpx"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

type sourceOperations interface {
	RequestSync(context.Context, string, string, string) (map[string]any, error)
	GetSync(context.Context, string) (map[string]any, error)
	Quality(context.Context, time.Time, time.Time) (map[string]any, error)
}

func (h adminHandler) syncNow(w http.ResponseWriter, r *http.Request) {
	if !h.allowed(w, r, ManageSources) || !validSourceRequest(w, r, nil) || !validSourceID(w, r) {
		return
	}
	var input struct{}
	if !httpx.DecodeJSONObject(w, r, &input) {
		return
	}
	values := r.Header.Values("Idempotency-Key")
	k := ""
	if len(values) > 1 || (len(values) == 1 && (len(values[0]) < 1 || len(values[0]) > 200)) {
		writeError(w, 400, "VALIDATION_ERROR", "invalid Idempotency-Key")
		return
	}
	if len(values) == 1 {
		k = values[0]
	}
	store, ok := h.registry.(sourceOperations)
	if !ok {
		writeError(w, 503, "SOURCE_UNAVAILABLE", "source operations are unavailable")
		return
	}
	p, ok := authn.PrincipalFromContext(r.Context())
	if !ok {
		writeError(w, 401, "UNAUTHENTICATED", "valid session is required")
		return
	}
	result, e := store.RequestSync(r.Context(), p.UserID, r.PathValue("source_id"), k)
	if e != nil {
		respondError(w, r.Context(), e)
		return
	}
	w.Header().Set("Location", "/v0.1/admin/source-syncs/"+result["sync_id"].(string))
	w.Header().Set("Retry-After", "5")
	writeJSON(w, 202, result)
}
func (h adminHandler) getSync(w http.ResponseWriter, r *http.Request) {
	if !h.allowed(w, r, ReadSources) || !validSourceRequest(w, r, nil) {
		return
	}
	if !uuidPattern.MatchString(r.PathValue("sync_id")) {
		writeError(w, 400, "VALIDATION_ERROR", "invalid sync_id")
		return
	}
	store, ok := h.registry.(sourceOperations)
	if !ok {
		writeError(w, 503, "SOURCE_UNAVAILABLE", "source operations are unavailable")
		return
	}
	v, e := store.GetSync(r.Context(), r.PathValue("sync_id"))
	if e != nil {
		respondError(w, r.Context(), e)
		return
	}
	writeJSON(w, 200, v)
}
func (h adminHandler) quality(w http.ResponseWriter, r *http.Request) {
	if !h.allowed(w, r, ReadSources) || !validSourceRequest(w, r, map[string]bool{"from": true, "to": true}) {
		return
	}
	from, e := time.Parse(time.RFC3339Nano, r.URL.Query().Get("from"))
	to, e2 := time.Parse(time.RFC3339Nano, r.URL.Query().Get("to"))
	if e != nil || e2 != nil || !to.After(from) || to.Sub(from) > 31*24*time.Hour {
		writeError(w, 400, "VALIDATION_ERROR", "invalid time range; maximum is 31 days")
		return
	}
	store, ok := h.registry.(sourceOperations)
	if !ok {
		writeError(w, 503, "SOURCE_UNAVAILABLE", "quality metrics are unavailable")
		return
	}
	v, e := store.Quality(r.Context(), from, to)
	if e != nil {
		respondError(w, r.Context(), e)
		return
	}
	writeJSON(w, 200, v)
}
func (s *Store) RequestSync(ctx context.Context, user, sourceID, key string) (map[string]any, error) {
	tx, e := s.db.Begin(ctx)
	if e != nil {
		return nil, e
	}
	defer tx.Rollback(ctx)
	var enabled bool
	var adapter string
	e = tx.QueryRow(ctx, `SELECT enabled,adapter_type FROM sources WHERE id=$1::uuid FOR UPDATE`, sourceID).Scan(&enabled, &adapter)
	if errors.Is(e, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if e != nil {
		return nil, e
	}

	if key != "" {
		var previous string
		err := tx.QueryRow(ctx, `SELECT id::text FROM source_sync_requests WHERE user_id=$1::uuid AND source_id=$2::uuid AND idempotency_key=$3`, user, sourceID, key).Scan(&previous)
		if err == nil {
			if err = tx.Commit(ctx); err != nil {
				return nil, err
			}
			return s.GetSync(ctx, previous)
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return nil, err
		}
	}
	if !enabled || (adapter != "rss" && adapter != "atom") {
		return nil, ErrConflict
	}
	syncID := uuid.NewString()
	e = tx.QueryRow(ctx, `INSERT INTO source_sync_requests(id,source_id,user_id,idempotency_key,trace_id,poll_id) VALUES($1,$2,$3,NULLIF($4,''),$5,(SELECT p.id FROM source_polls p JOIN sources s ON s.id=p.source_id WHERE p.source_id=$2 AND p.status='RUNNING' AND s.lease_until>now() ORDER BY p.started_at DESC LIMIT 1))
 ON CONFLICT(user_id,source_id,idempotency_key) DO UPDATE SET idempotency_key=EXCLUDED.idempotency_key RETURNING id::text`, syncID, sourceID, user, key, uuid.NewString()).Scan(&syncID)
	if e != nil {
		return nil, e
	}
	_, e = tx.Exec(ctx, `UPDATE sources SET next_poll_at=now() WHERE id=$1::uuid AND (lease_until IS NULL OR lease_until<=now())`, sourceID)
	if e != nil {
		return nil, e
	}
	if e = tx.Commit(ctx); e != nil {
		return nil, e
	}
	return s.GetSync(ctx, syncID)
}
func (s *Store) GetSync(ctx context.Context, syncID string) (map[string]any, error) {
	var sourceID, trace, state string
	var created time.Time
	e := s.db.QueryRow(ctx, `SELECT r.source_id::text,r.trace_id::text,r.created_at,CASE WHEN p.id IS NOT NULL THEN p.status WHEN r.state IN ('SUCCEEDED','PARTIAL','FAILED') THEN r.state WHEN NOT s.enabled THEN 'FAILED' WHEN r.created_at<now()-interval '1 hour' THEN 'FAILED' ELSE 'QUEUED' END FROM source_sync_requests r JOIN sources s ON s.id=r.source_id LEFT JOIN source_polls p ON p.id=r.poll_id WHERE r.id=$1::uuid`, syncID).Scan(&sourceID, &trace, &created, &state)
	if errors.Is(e, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if e != nil {
		return nil, e
	}
	out := map[string]any{"sync_id": syncID, "source_id": sourceID, "trace_id": trace, "created_at": created, "state": state}
	if state == "FAILED" || state == "PARTIAL" {
		out["error"] = map[string]any{"code": "SOURCE_UNAVAILABLE", "message": "source synchronization did not fully complete", "retryable": false}
	}
	return out, nil
}
func (s *Store) Quality(ctx context.Context, from, to time.Time) (map[string]any, error) {
	var total, failed, partial int
	var avg *float64
	e := s.db.QueryRow(ctx, `SELECT count(*),count(*) FILTER(WHERE status='FAILED'),count(*) FILTER(WHERE status='PARTIAL'),avg(extract(epoch FROM finished_at-started_at)) FROM source_polls WHERE created_at>=$1 AND created_at<$2 AND finished_at IS NOT NULL`, from, to).Scan(&total, &failed, &partial, &avg)
	if e != nil {
		return nil, e
	}
	metric := func(name, unit string, value any) map[string]any {
		status := "AVAILABLE"
		if total == 0 {
			status = "NO_DATA"
			value = nil
		}
		return map[string]any{"name": name, "unit": unit, "value": value, "sample_size": total, "status": status}
	}
	var average any
	if avg != nil {
		average = *avg
	}
	return map[string]any{"from": from, "to": to, "metrics": []any{metric("source_polls_failed", "count", failed), metric("source_polls_partial", "count", partial), metric("source_poll_duration", "seconds", average)}}, nil
}
