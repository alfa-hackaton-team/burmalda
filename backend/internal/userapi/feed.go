package userapi

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"time"

	"github.com/alfa-hackaton-team/burmalda/backend/internal/authn"
	"github.com/google/uuid"
)

type pageCursor struct {
	User, Session, Scope, Snapshot, Filter string
	Offset, Limit                          int
}

func (h productHandler) sign(c pageCursor) string {
	b := raw(c)
	mac := hmac.New(sha256.New, h.key[:])
	mac.Write(b)
	return base64.RawURLEncoding.EncodeToString(append(b, mac.Sum(nil)...))
}
func (h productHandler) cursor(r *http.Request, scope, filter string, limit int) (pageCursor, error) {
	p, _ := authn.PrincipalFromContext(r.Context())
	c := pageCursor{User: p.UserID, Session: p.SessionID, Scope: scope, Filter: filter, Limit: limit}
	v := r.URL.Query().Get("cursor")
	if v == "" {
		return c, nil
	}
	if len(v) > 2048 {
		return c, fail(400, "VALIDATION_ERROR", "invalid cursor")
	}
	b, e := base64.RawURLEncoding.DecodeString(v)
	if e != nil || len(b) <= 32 {
		return c, fail(400, "VALIDATION_ERROR", "invalid cursor")
	}
	payload, signature := b[:len(b)-32], b[len(b)-32:]
	mac := hmac.New(sha256.New, h.key[:])
	mac.Write(payload)
	var previous pageCursor
	if !hmac.Equal(signature, mac.Sum(nil)) || json.Unmarshal(payload, &previous) != nil || previous.User != c.User || previous.Session != c.Session || previous.Scope != scope || previous.Filter != filter || previous.Limit != limit || previous.Offset < 0 {
		return c, fail(400, "VALIDATION_ERROR", "invalid cursor")
	}
	return previous, nil
}
func (h productHandler) feed(ctx context.Context, user string, r *http.Request) (any, int, error) {
	return h.feedPage(ctx, user, r, false)
}
func (h productHandler) alerts(ctx context.Context, user string, r *http.Request) (any, int, error) {
	return h.feedPage(ctx, user, r, true)
}
func (h productHandler) feedPage(ctx context.Context, user string, r *http.Request, alerts bool) (any, int, error) {
	allowed := []string{"context_id", "limit", "cursor"}
	if !alerts {
		allowed = append(allowed, "topic")
	}
	if e := query(r, allowed...); e != nil {
		return nil, 0, e
	}
	contextID, e := id(r.URL.Query().Get("context_id"))
	if e != nil {
		return nil, 0, e
	}
	c, e := h.store.context(ctx, user, contextID)
	if e != nil {
		return nil, 0, e
	}
	if e = requireProfile(c); e != nil {
		return nil, 0, e
	}
	limit, e := pageLimit(r)
	if e != nil {
		return nil, 0, e
	}
	filter := contextID + ":" + r.URL.Query().Get("topic") + ":" + str(c["profile_id"])
	scope := "feed"
	if alerts {
		scope = "alerts"
	}
	cursor, e := h.cursor(r, scope, filter, limit)
	if e != nil {
		return nil, 0, e
	}
	var items []any
	if cursor.Snapshot == "" {
		if e = h.store.limit(ctx, user, "feed", 120); e != nil {
			return nil, 0, e
		}
		trace := uuid.NewString()
		input := object{"request_context": object{"request_id": uuid.NewString(), "trace_id": trace, "user_id": user, "context": publicContext(c), "role_id": c["role_code"], "profile": profileRef(c)}, "limit": 100}
		if topic := r.URL.Query().Get("topic"); topic != "" {
			input["topic"] = topic
		}

		feed := []any{}
		seen := map[string]bool{}
		for page := 0; ; page++ {
			if page >= 50 {
				return nil, 0, fail(502, "INVALID_ML_RESPONSE", "ranking exceeds snapshot limit")
			}
			result, _, err := h.store.ml.call(ctx, "POST", "/internal/v0.1/feed-rankings", "", input)
			if err != nil {
				return nil, 0, err
			}
			if err = checked("public", "FeedPage", result); err != nil {
				return nil, 0, err
			}
			feed = append(feed, array(result["items"])...)
			if len(feed) > 5000 || len(raw(feed)) > 8<<20 {
				return nil, 0, fail(502, "INVALID_ML_RESPONSE", "ranking exceeds snapshot limit")
			}
			next := str(result["next_cursor"])
			if next == "" {
				break
			}
			if seen[next] {
				return nil, 0, fail(502, "INVALID_ML_RESPONSE", "ranking repeated a cursor")
			}
			seen[next] = true
			input["cursor"] = next
		}

		alertItems := []any{}
		for _, v := range feed {
			item := obj(v)
			if item["alert_id"] != nil {
				alertItems = append(alertItems, object{"alert_id": item["alert_id"], "context": publicContext(c), "event": item["event"], "impact": item["impact"], "created_at": time.Now().UTC()})
			}
		}
		cursor.Snapshot = uuid.NewString()
		fingerprint := sha256.Sum256([]byte(filter))
		_, e = h.store.pool.Exec(ctx, `INSERT INTO feed_snapshots(id,user_id,business_context_id,profile_id,filters_fingerprint,ranking_version,items,alerts) VALUES($1,$2,$3,$4,$5,$6,$7,$8)`, cursor.Snapshot, user, contextID, c["profile_id"], fingerprint[:], h.store.analysisVersion, raw(feed), raw(alertItems))
		if e != nil {
			return nil, 0, e
		}
		items = feed
		if alerts {
			items = alertItems
		}
	} else {
		var feed, alertData []byte
		fingerprint := sha256.Sum256([]byte(filter))
		e = h.store.pool.QueryRow(ctx, `SELECT items,alerts FROM feed_snapshots WHERE id=$1::uuid AND user_id=$2::uuid AND business_context_id=$3::uuid AND profile_id=$4::uuid AND filters_fingerprint=$5 AND expires_at>now()`, cursor.Snapshot, user, contextID, c["profile_id"], fingerprint[:]).Scan(&feed, &alertData)
		if e != nil {
			return nil, 0, fail(400, "VALIDATION_ERROR", "cursor expired or invalid")
		}
		if alerts {
			feed = alertData
		}
		if e = json.Unmarshal(feed, &items); e != nil {
			return nil, 0, e
		}
	}
	start := cursor.Offset
	if cursor.Offset > len(items) {
		return nil, 0, fail(400, "VALIDATION_ERROR", "invalid cursor")
	}
	end := min(cursor.Offset+limit, len(items))
	var next any
	if end < len(items) {
		cursor.Offset = end
		next = h.sign(cursor)
	}
	return object{"items": items[start:end], "next_cursor": next}, 200, nil
}
func (h productHandler) pageResult(cursor pageCursor, items []any) object {
	var next any
	if len(items) > cursor.Limit {
		items = items[:cursor.Limit]
		cursor.Offset += cursor.Limit
		next = h.sign(cursor)
	}
	return object{"items": items, "next_cursor": next}
}
