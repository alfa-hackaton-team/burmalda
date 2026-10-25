package userapi

import (
	"context"
	"net/http"
	"strconv"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

func (s *PostgresStore) insight(ctx context.Context, user, insightID string) (object, object, error) {
	var payload, snapshot []byte
	var contextID string
	err := s.pool.QueryRow(ctx, `SELECT payload,ml_snapshot,business_context_id::text FROM insights WHERE id=$1::uuid AND user_id=$2::uuid`, insightID, user).Scan(&payload, &snapshot, &contextID)
	if err != nil {
		return nil, nil, err
	}
	if _, err = s.context(ctx, user, contextID); err != nil {
		return nil, nil, err
	}
	return decode(payload), decode(snapshot), nil
}
func (h productHandler) insight(ctx context.Context, user string, r *http.Request) (any, int, error) {
	if e := query(r); e != nil {
		return nil, 0, e
	}
	i, e := pathID(r, "insight_id")
	if e != nil {
		return nil, 0, e
	}
	v, _, e := h.store.insight(ctx, user, i)
	return v, 200, e
}
func (s *PostgresStore) conversation(ctx context.Context, user, conversationID string) (object, object, error) {
	var insightID string
	var created time.Time
	err := s.pool.QueryRow(ctx, `SELECT insight_id::text,created_at FROM conversations WHERE id=$1::uuid AND user_id=$2::uuid`, conversationID, user).Scan(&insightID, &created)
	if err != nil {
		return nil, nil, err
	}
	insight, snapshot, err := s.insight(ctx, user, insightID)
	if err != nil {
		return nil, nil, err
	}
	return object{"conversation_id": conversationID, "insight_id": insightID, "insight_revision": insight["revision"], "context": insight["context"], "role_id": insight["role_id"], "created_at": created}, snapshot, nil
}
func (h productHandler) conversations(ctx context.Context, user string, r *http.Request) (any, int, error) {
	if e := query(r, "cursor", "limit"); e != nil {
		return nil, 0, e
	}
	i, e := pathID(r, "insight_id")
	if e != nil {
		return nil, 0, e
	}
	insight, _, e := h.store.insight(ctx, user, i)
	if e != nil {
		return nil, 0, e
	}
	limit, e := pageLimit(r)
	if e != nil {
		return nil, 0, e
	}
	cursor, e := h.cursor(r, "conversations/"+i, "", limit)
	if e != nil {
		return nil, 0, e
	}
	rows, e := h.store.pool.Query(ctx, `SELECT id::text,created_at FROM conversations WHERE insight_id=$1::uuid AND user_id=$2::uuid ORDER BY created_at,id LIMIT $3 OFFSET $4`, i, user, limit+1, cursor.Offset)
	if e != nil {
		return nil, 0, e
	}
	defer rows.Close()
	items := []any{}
	for rows.Next() {
		var cid string
		var created time.Time
		if e = rows.Scan(&cid, &created); e != nil {
			return nil, 0, e
		}
		items = append(items, object{"conversation_id": cid, "insight_id": i, "insight_revision": insight["revision"], "context": insight["context"], "role_id": insight["role_id"], "created_at": created})
	}
	if e = rows.Err(); e != nil {
		return nil, 0, e
	}
	return h.pageResult(cursor, items), 200, nil
}
func (h productHandler) createConversation(ctx context.Context, user string, r *http.Request) (any, int, error) {
	if e := query(r); e != nil {
		return nil, 0, e
	}
	i, e := pathID(r, "insight_id")
	if e != nil {
		return nil, 0, e
	}
	var input struct{}
	if e = readInput(r, &input); e != nil {
		return nil, 0, e
	}
	key, e := key(r, false)
	if e != nil {
		return nil, 0, e
	}
	if _, _, e = h.store.insight(ctx, user, i); e != nil {
		return nil, 0, e
	}
	if e = h.store.limit(ctx, user, "conversation", 120); e != nil {
		return nil, 0, e
	}
	cid := uuid.NewString()
	e = h.store.pool.QueryRow(ctx, `INSERT INTO conversations(id,user_id,insight_id,idempotency_key) VALUES($1,$2,$3,NULLIF($4,'')) ON CONFLICT(user_id,insight_id,idempotency_key) DO UPDATE SET idempotency_key=EXCLUDED.idempotency_key RETURNING id::text`, cid, user, i, key).Scan(&cid)
	if e != nil {
		return nil, 0, e
	}
	v, _, e := h.store.conversation(ctx, user, cid)
	return v, 201, e
}
func (h productHandler) messages(ctx context.Context, user string, r *http.Request) (any, int, error) {
	if e := query(r, "cursor", "limit"); e != nil {
		return nil, 0, e
	}
	cid, e := pathID(r, "conversation_id")
	if e != nil {
		return nil, 0, e
	}
	if _, _, e = h.store.conversation(ctx, user, cid); e != nil {
		return nil, 0, e
	}
	limit, e := pageLimit(r)
	if e != nil {
		return nil, 0, e
	}
	cursor, e := h.cursor(r, "messages/"+cid, "", limit)
	if e != nil {
		return nil, 0, e
	}
	rows, e := h.store.pool.Query(ctx, `SELECT payload FROM messages WHERE conversation_id=$1::uuid ORDER BY sequence LIMIT $2 OFFSET $3`, cid, limit+1, cursor.Offset)
	if e != nil {
		return nil, 0, e
	}
	defer rows.Close()
	items := []any{}
	for rows.Next() {
		var b []byte
		if e = rows.Scan(&b); e != nil {
			return nil, 0, e
		}
		items = append(items, decode(b))
	}
	if e = rows.Err(); e != nil {
		return nil, 0, e
	}
	return h.pageResult(cursor, items), 200, nil
}
func (h productHandler) feedback(ctx context.Context, user string, r *http.Request) (any, int, error) {
	if e := query(r); e != nil {
		return nil, 0, e
	}
	iid, e := pathID(r, "insight_id")
	if e != nil {
		return nil, 0, e
	}
	var input object
	if e = readInput(r, &input); e != nil {
		return nil, 0, e
	}
	if e = checked("public", "FeedbackInput", input); e != nil {
		return nil, 0, fail(400, "VALIDATION_ERROR", "invalid feedback")
	}
	if len(str(input["comment"])) > 4000 {
		return nil, 0, fail(400, "VALIDATION_ERROR", "comment is too long")
	}
	insight, _, e := h.store.insight(ctx, user, iid)
	if e != nil {
		return nil, 0, e
	}
	if input["insight_revision"] != insight["revision"] {
		return nil, 0, fail(409, "CONFLICT", "insight revision changed")
	}
	k, e := key(r, false)
	if e != nil {
		return nil, 0, e
	}
	if e = h.store.limit(ctx, user, "feedback", 600); e != nil {
		return nil, 0, e
	}
	fid := uuid.NewString()
	created := time.Now().UTC()
	payload := object{"feedback_id": fid, "insight_id": iid, "created_at": created, "input": input}
	var stored []byte
	e = h.store.pool.QueryRow(ctx, `INSERT INTO feedback(id,user_id,insight_id,insight_version,idempotency_key,payload) VALUES($1,$2,$3,$4,NULLIF($5,''),$6) ON CONFLICT(user_id,insight_id,idempotency_key) DO UPDATE SET idempotency_key=EXCLUDED.idempotency_key RETURNING payload`, fid, user, iid, mustInt(str(input["insight_revision"])), k, raw(payload)).Scan(&stored)
	if e != nil {
		return nil, 0, e
	}
	out := decode(stored)
	if string(raw(out["input"])) != string(raw(input)) {
		return nil, 0, fail(409, "CONFLICT", "idempotency key has different parameters")
	}
	return out, 201, nil
}
func mustInt(s string) int { n, _ := strconv.Atoi(s); return n }
func (h productHandler) generation(ctx context.Context, user string, r *http.Request) (any, int, error) {
	if e := query(r); e != nil {
		return nil, 0, e
	}
	gid, e := pathID(r, "generation_id")
	if e != nil {
		return nil, 0, e
	}
	v, e := h.store.generation(ctx, user, gid)
	return v, 200, e
}
func (s *PostgresStore) generation(ctx context.Context, user, gid string) (object, error) {
	var contextID, state, trace string
	var created time.Time
	var iid, mid *string
	var errData []byte
	e := s.pool.QueryRow(ctx, `SELECT business_context_id::text,state,trace_id::text,created_at,insight_id::text,message_id::text,error FROM request_operations WHERE id=$1::uuid AND user_id=$2::uuid`, gid, user).Scan(&contextID, &state, &trace, &created, &iid, &mid, &errData)
	if e != nil {
		return nil, e
	}
	if _, e = s.context(ctx, user, contextID); e != nil {
		return nil, e
	}
	out := object{"generation_id": gid, "state": state, "trace_id": trace, "created_at": created}
	if iid != nil {
		out["insight_id"] = *iid
	}
	if mid != nil {
		out["message_id"] = *mid
	}
	if len(errData) > 0 {
		out["error"] = decode(errData)
	}
	return out, nil
}
func (h productHandler) trace(ctx context.Context, user string, r *http.Request) (any, int, error) {
	if e := query(r); e != nil {
		return nil, 0, e
	}
	tid, e := pathID(r, "trace_id")
	if e != nil {
		return nil, 0, e
	}
	var gid string
	var updated time.Time
	e = h.store.pool.QueryRow(ctx, `SELECT id::text,updated_at FROM request_operations WHERE trace_id=$1::uuid AND user_id=$2::uuid`, tid, user).Scan(&gid, &updated)
	if e != nil {
		return nil, 0, e
	}
	g, e := h.store.generation(ctx, user, gid)
	if e != nil {
		return nil, 0, e
	}
	var finished any
	if g["state"] != "RUNNING" && g["state"] != "QUEUED" {
		finished = updated
	}
	return object{"trace_id": tid, "stages": []any{object{"stage_id": gid, "stage_type": "ANALYSIS", "status": g["state"], "started_at": g["created_at"], "finished_at": finished, "evidence_ids": []any{}, "feature_refs": []any{}, "summary": "Processing the saved analysis request"}}}, 200, nil
}
func (h productHandler) analytics(ctx context.Context, user string, r *http.Request) (any, int, error) {
	if e := query(r); e != nil {
		return nil, 0, e
	}
	var input struct {
		Events []object `json:"events"`
	}
	if e := readInput(r, &input); e != nil {
		return nil, 0, e
	}
	if input.Events == nil || len(input.Events) < 1 || len(input.Events) > 100 {
		return nil, 0, fail(400, "VALIDATION_ERROR", "send 1 to 100 events")
	}
	if e := h.store.limit(ctx, user, "analytics", 600); e != nil {
		return nil, 0, e
	}
	tx, e := h.store.pool.Begin(ctx)
	if e != nil {
		return nil, 0, e
	}
	defer tx.Rollback(ctx)
	for _, v := range input.Events {
		if e = checked("public", "ClientAnalyticsEvent", v); e != nil {
			return nil, 0, fail(400, "VALIDATION_ERROR", "invalid analytics event")
		}
		if len(str(v["analytics_event_id"])) > 200 {
			return nil, 0, fail(400, "VALIDATION_ERROR", "event id is too long")
		}
		cid, err := id(str(v["context_id"]))
		if err != nil {
			return nil, 0, err
		}
		if _, e = contextFrom(ctx, tx, user, cid); e != nil {
			return nil, 0, e
		}
		when, e := time.Parse(time.RFC3339Nano, str(v["occurred_at"]))
		if e != nil || when.After(time.Now().Add(5*time.Minute)) {
			return nil, 0, fail(400, "VALIDATION_ERROR", "invalid event time")
		}
		switch v["event_type"] {
		case "INSIGHT_OPENED":
			i, e := id(str(v["insight_id"]))
			if e != nil {
				return nil, 0, e
			}
			var savedContext string
			if e = tx.QueryRow(ctx, `SELECT business_context_id::text FROM insights WHERE id=$1::uuid AND user_id=$2::uuid`, i, user).Scan(&savedContext); e != nil {
				return nil, 0, e
			}
			if savedContext != cid {
				return nil, 0, fail(403, "FORBIDDEN_CONTEXT", "context does not match")
			}
		case "SOURCE_CLICKED", "ALERT_OPENED":
			field := "source_id"
			if v["event_type"] == "ALERT_OPENED" {
				field = "alert_id"
			}
			target, e := id(str(v[field]))
			if e != nil {
				return nil, 0, e
			}
			var found bool
			e = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM feed_snapshots WHERE user_id=$1::uuid AND business_context_id=$2::uuid AND expires_at>now() AND CASE WHEN $3='source_id' THEN jsonb_path_exists(items,'$[*].sources[*] ? (@.source_id == $id)',jsonb_build_object('id',$4::text)) ELSE jsonb_path_exists(alerts,'$[*] ? (@.alert_id == $id)',jsonb_build_object('id',$4::text)) END)`, user, cid, field, target).Scan(&found)
			if e != nil {
				return nil, 0, e
			}
			if !found {
				return nil, 0, pgx.ErrNoRows
			}
		}
		var stored []byte
		e = tx.QueryRow(ctx, `INSERT INTO client_analytics_events(analytics_event_id,user_id,business_context_id,schema_version,event_type,occurred_at,payload) VALUES($1,$2,$3,$4,$5,$6,$7) ON CONFLICT(analytics_event_id) DO UPDATE SET analytics_event_id=EXCLUDED.analytics_event_id RETURNING payload`, v["analytics_event_id"], user, cid, v["schema_version"], v["event_type"], when, raw(object{"user_id": user, "event": v})).Scan(&stored)
		if e != nil {
			return nil, 0, e
		}
		if string(raw(decode(stored))) != string(raw(object{"user_id": user, "event": v})) {
			return nil, 0, fail(409, "CONFLICT", "event id already used")
		}
	}
	if e = tx.Commit(ctx); e != nil {
		return nil, 0, e
	}
	return object{"accepted": len(input.Events)}, 202, nil
}
