package userapi

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"log/slog"
	"math/rand/v2"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

func (s *PostgresStore) event(ctx context.Context, eventID string) (object, error) {
	var data []byte
	e := s.corpusPool().QueryRow(ctx, `SELECT jsonb_build_object('id',e.id,'event_type',e.event_type,'title',e.title,'summary',e.summary,'occurred_at',e.occurred_at,'geography',e.geography,'status',e.status,'confidence',e.confidence,'document_version_ids',
 (SELECT jsonb_agg(DISTINCT ed.document_version_id ORDER BY ed.document_version_id) FROM ml.event_documents ed JOIN ml.document_version_catalog d ON d.document_version_id=ed.document_version_id AND d.ready WHERE ed.event_id=e.id)) FROM ml.events e WHERE e.id=$1::uuid AND e.status<>'rejected'`, eventID).Scan(&data)
	var pe *pgconn.PgError
	if errors.As(e, &pe) && (pe.Code == "42P01" || pe.Code == "3F000" || pe.Code == "42501") {
		return nil, unavailable()
	}
	if e != nil {
		return nil, e
	}
	o := decode(data)
	if e = checked("ml", "Event", o); e != nil {
		return nil, e
	}
	return o, nil
}
func (h productHandler) createInsight(ctx context.Context, user string, r *http.Request) (any, int, error) {
	if e := query(r); e != nil {
		return nil, 0, e
	}
	var input struct {
		ContextID string `json:"context_id"`
		EventID   string `json:"event_id"`
		ForceNew  bool   `json:"force_new"`
	}
	if e := readInput(r, &input); e != nil {
		return nil, 0, e
	}
	cid, e := id(input.ContextID)
	if e != nil {
		return nil, 0, e
	}
	eid, e := id(input.EventID)
	if e != nil {
		return nil, 0, e
	}
	k, e := key(r, true)
	if e != nil {
		return nil, 0, e
	}
	c, e := h.store.context(ctx, user, cid)
	if e != nil {
		return nil, 0, e
	}
	scope := object{"fingerprint_version": "v1", "operation_type": "insight", "context_id": cid, "event_id": eid}
	if input.ForceNew {
		scope["force_new"] = true
	}
	fingerprint := sha256.Sum256(raw(scope))
	previous, e := h.store.existing(ctx, user, "insight", k, fingerprint[:])
	if e != nil {
		return nil, 0, e
	}
	if previous != "" {
		g, e := h.store.generation(ctx, user, previous)
		return g, 202, e
	}
	if e = requireProfile(c); e != nil {
		return nil, 0, e
	}
	event, e := h.store.event(ctx, eid)
	if e != nil {
		return nil, 0, e
	}
	// Serialize discovery and creation for this user/context/event. The full
	// trusted context and event snapshots below prevent reuse across versions.
	tx, e := h.store.pool.Begin(ctx)
	if e != nil {
		return nil, 0, e
	}
	defer tx.Rollback(ctx)
	if _, e = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1, 20261027))`, user+":insight:"+k); e != nil {
		return nil, 0, e
	}
	previous, e = existingFrom(ctx, tx, user, "insight", k, fingerprint[:])
	if e != nil {
		return nil, 0, e
	}
	if previous != "" {
		if e = tx.Rollback(ctx); e != nil {
			return nil, 0, e
		}
		g, e := h.store.generation(ctx, user, previous)
		return g, 202, e
	}
	if _, e = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1, 20261026))`, user+":"+cid+":"+eid); e != nil {
		return nil, 0, e
	}
	var restored string
	e = pgx.ErrNoRows
	if !input.ForceNew {
		e = tx.QueryRow(ctx, `SELECT id::text FROM request_operations
 WHERE user_id=$1::uuid AND business_context_id=$2::uuid AND event_id=$3::uuid
 AND operation_type='insight' AND request_snapshot->'context'=$4::jsonb
 AND request_snapshot->'event'=$5::jsonb AND request_snapshot->>'processing_config_version'=$6
 ORDER BY CASE WHEN state IN ('QUEUED','RUNNING') THEN 0 WHEN state='SUCCEEDED' THEN 1 ELSE 2 END, created_at DESC,id DESC LIMIT 1`, user, cid, eid, raw(mlContext(c)), raw(event), h.store.analysisVersion).Scan(&restored)
	}
	if e == nil {
		if _, e = tx.Exec(ctx, `INSERT INTO insight_operation_aliases(user_id,idempotency_key,request_fingerprint,request_operation_id) VALUES($1,$2,$3,$4)`, user, k, fingerprint[:], restored); e != nil {
			return nil, 0, e
		}
		if e = tx.Commit(ctx); e != nil {
			return nil, 0, e
		}
		g, e := h.store.generation(ctx, user, restored)
		return g, 202, e
	}
	if !errors.Is(e, pgx.ErrNoRows) {
		return nil, 0, e
	}
	if h.store.analysisVersion == "" || h.store.ml.BaseURL == "" {
		return nil, 0, unavailable()
	}
	if e = limitFrom(ctx, tx, user, "analysis", 30); e != nil {
		return nil, 0, e
	}
	gid, trace := uuid.NewString(), uuid.NewString()
	request := object{"contract_version": "v1", "trace_id": trace, "request_operation_id": gid, "request_type": "initial", "context": mlContext(c), "event": event, "follow_up": nil, "processing_config_version": h.store.analysisVersion, "parent_trace_id": nil}
	if len(raw(request)) > int(h.store.settings().ML.MaxResponseBytes) {
		return nil, 0, fail(413, "PAYLOAD_TOO_LARGE", "analysis request exceeds configured size limit")
	}
	if e = checked("ml", "AnalysisRequest", request); e != nil {
		return nil, 0, e
	}
	e = tx.QueryRow(ctx, `INSERT INTO request_operations(id,user_id,business_context_id,operation_type,idempotency_key,request_fingerprint,request_snapshot,trace_id,event_id) VALUES($1,$2,$3,'insight',$4,$5,$6,$7,$8)
 ON CONFLICT(user_id,operation_type,idempotency_key) DO UPDATE SET idempotency_key=EXCLUDED.idempotency_key RETURNING id::text`, gid, user, cid, k, fingerprint[:], raw(request), trace, eid).Scan(&gid)
	if e != nil {
		return nil, 0, e
	}
	if e = tx.Commit(ctx); e != nil {
		return nil, 0, e
	}
	confirmed, e := h.store.existing(ctx, user, "insight", k, fingerprint[:])
	if e != nil {
		return nil, 0, e
	}
	g, e := h.store.generation(ctx, user, confirmed)
	return g, 202, e
}
func (s *PostgresStore) existing(ctx context.Context, user, kind, key string, fingerprint []byte) (string, error) {
	return existingFrom(ctx, s.pool, user, kind, key, fingerprint)
}
func existingFrom(ctx context.Context, db contextReader, user, kind, key string, fingerprint []byte) (string, error) {
	var gid string
	var stored []byte
	e := db.QueryRow(ctx, `SELECT id::text,request_fingerprint FROM request_operations WHERE user_id=$1::uuid AND operation_type=$2 AND idempotency_key=$3
 UNION ALL SELECT o.id::text,a.request_fingerprint FROM insight_operation_aliases a JOIN request_operations o ON o.id=a.request_operation_id AND o.user_id=a.user_id
 WHERE a.user_id=$1::uuid AND o.operation_type=$2 AND a.idempotency_key=$3`, user, kind, key).Scan(&gid, &stored)
	if errors.Is(e, pgx.ErrNoRows) {
		return "", nil
	}
	if e != nil {
		return "", e
	}
	if string(stored) != string(fingerprint) {
		return "", fail(409, "CONFLICT", "idempotency key has different parameters")
	}
	return gid, nil
}
func (h productHandler) ask(ctx context.Context, user string, r *http.Request) (any, int, error) {
	if e := query(r); e != nil {
		return nil, 0, e
	}
	cid, e := pathID(r, "conversation_id")
	if e != nil {
		return nil, 0, e
	}
	var input struct {
		Text string `json:"text"`
	}
	if e = readInput(r, &input); e != nil {
		return nil, 0, e
	}
	if len(input.Text) > 16000 || len(input.Text) == 0 || blank(input.Text) {
		return nil, 0, fail(400, "VALIDATION_ERROR", "question must contain 1 to 16000 bytes of text")
	}
	k, e := key(r, true)
	if e != nil {
		return nil, 0, e
	}
	conv, snapshot, e := h.store.conversation(ctx, user, cid)
	if e != nil {
		return nil, 0, e
	}
	fingerprint := sha256.Sum256(raw(object{"fingerprint_version": "v1", "operation_type": "follow_up", "conversation_id": cid, "text": input.Text}))
	previous, e := h.store.existing(ctx, user, "follow_up", k, fingerprint[:])
	if e != nil {
		return nil, 0, e
	}
	if previous != "" {
		g, e := h.store.generation(ctx, user, previous)
		return g, 202, e
	}
	if h.store.analysisVersion == "" || h.store.ml.BaseURL == "" {
		return nil, 0, unavailable()
	}
	if e = h.store.limit(ctx, user, "analysis", 30); e != nil {
		return nil, 0, e
	}
	tx, e := h.store.pool.Begin(ctx)
	if e != nil {
		return nil, 0, e
	}
	defer tx.Rollback(ctx)
	var lock string
	if e = tx.QueryRow(ctx, `SELECT id::text FROM conversations WHERE id=$1::uuid FOR UPDATE`, cid).Scan(&lock); e != nil {
		return nil, 0, e
	}
	var prior string
	var priorHash []byte
	e = tx.QueryRow(ctx, `SELECT id::text,request_fingerprint FROM request_operations WHERE user_id=$1::uuid AND operation_type='follow_up' AND idempotency_key=$2`, user, k).Scan(&prior, &priorHash)
	if e == nil {
		if string(priorHash) != string(fingerprint[:]) {
			return nil, 0, fail(409, "CONFLICT", "idempotency key has different parameters")
		}
		if e = tx.Rollback(ctx); e != nil {
			return nil, 0, e
		}
		g, e := h.store.generation(ctx, user, prior)
		return g, 202, e
	}
	if !errors.Is(e, pgx.ErrNoRows) {
		return nil, 0, e
	}
	var busy bool
	if e = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM request_operations WHERE conversation_id=$1::uuid AND state IN ('QUEUED','RUNNING'))`, cid).Scan(&busy); e != nil {
		return nil, 0, e
	}
	if busy {
		return nil, 0, fail(409, "CONFLICT", "an answer is already being prepared")
	}
	rows, e := tx.Query(ctx, `SELECT payload,ml_snapshot FROM messages WHERE conversation_id=$1::uuid ORDER BY sequence`, cid)
	if e != nil {
		return nil, 0, e
	}
	history := []any{}
	seq := 0
	for rows.Next() {
		var p, m []byte
		if e = rows.Scan(&p, &m); e != nil {
			rows.Close()
			return nil, 0, e
		}
		msg := decode(p)
		seq = number(msg["sequence"])
		if msg["author"] == "USER" {
			history = append(history, object{"id": msg["message_id"], "role": "user", "content": msg["text"], "claims": []any{}, "citations": []any{}, "chunks": []any{}})
		} else {
			saved := decode(m)
			history = append(history, saved)
		}
	}
	e = rows.Err()
	rows.Close()
	if e != nil {
		return nil, 0, e
	}
	if seq >= 1000 {
		return nil, 0, fail(429, "BUDGET_EXHAUSTED", "conversation limit reached")
	}
	mid, gid, trace := uuid.NewString(), uuid.NewString(), uuid.NewString()
	message := object{"message_id": mid, "conversation_id": cid, "sequence": seq + 1, "author": "USER", "created_at": time.Now().UTC(), "text": input.Text}
	history = append(history, object{"id": mid, "role": "user", "content": input.Text, "claims": []any{}, "citations": []any{}, "chunks": []any{}})
	request := object{"contract_version": "v1", "trace_id": trace, "request_operation_id": gid, "request_type": "follow_up", "context": snapshot["context"], "event": snapshot["event"], "follow_up": object{"conversation_id": cid, "request_message_id": mid, "question": input.Text, "history_through_message_id": mid, "initial_insight_id": conv["insight_id"], "history": history, "initial_insight": snapshot}, "processing_config_version": h.store.analysisVersion, "parent_trace_id": snapshot["trace_id"]}
	initial := obj(obj(request["follow_up"])["initial_insight"])
	clean := object{}
	for _, field := range []string{"context", "event", "insight", "chunks"} {
		clean[field] = initial[field]
	}
	obj(request["follow_up"])["initial_insight"] = clean
	if len(raw(request)) > int(h.store.settings().ML.MaxResponseBytes) {
		return nil, 0, fail(413, "PAYLOAD_TOO_LARGE", "analysis request exceeds configured size limit")
	}
	if e = checked("ml", "AnalysisRequest", request); e != nil {
		return nil, 0, e
	}
	if _, e = tx.Exec(ctx, `INSERT INTO messages(id,conversation_id,sequence,author,payload) VALUES($1,$2,$3,'USER',$4)`, mid, cid, seq+1, raw(message)); e != nil {
		return nil, 0, e
	}
	if _, e = tx.Exec(ctx, `INSERT INTO request_operations(id,user_id,business_context_id,operation_type,idempotency_key,request_fingerprint,request_snapshot,trace_id,event_id,conversation_id,request_message_id) VALUES($1,$2,$3,'follow_up',$4,$5,$6,$7,$8,$9,$10)`, gid, user, obj(conv["context"])["context_id"], k, fingerprint[:], raw(request), trace, obj(snapshot["event"])["id"], cid, mid); e != nil {
		return nil, 0, e
	}
	if e = tx.Commit(ctx); e != nil {
		return nil, 0, e
	}
	g, e := h.store.generation(ctx, user, gid)
	return g, 202, e
}
func blank(v string) bool {
	for _, c := range v {
		if c != ' ' && c != '\n' && c != '\r' && c != '\t' {
			return false
		}
	}
	return true
}

var errNoAnalysis = errors.New("no pending analysis")

func (s *PostgresStore) RunAnalyses(ctx context.Context) error {
	lastCleanup := time.Time{}
	for ctx.Err() == nil {
		if time.Since(lastCleanup) > time.Minute {
			if _, err := s.pool.Exec(ctx, `DELETE FROM rate_limit_buckets WHERE ctid IN(SELECT ctid FROM rate_limit_buckets WHERE expires_at<now() LIMIT 1000)`); err != nil {
				slog.Error("rate limit cleanup failed")
			}
			if _, err := s.pool.Exec(ctx, `DELETE FROM feed_snapshots WHERE id IN(SELECT id FROM feed_snapshots WHERE expires_at<now() LIMIT 1000)`); err != nil {
				slog.Error("feed snapshot cleanup failed")
			}
			lastCleanup = time.Now()
		}
		err := s.runAnalysis(ctx)
		if err == nil {
			continue
		}
		if !errors.Is(err, errNoAnalysis) && !errors.Is(err, context.Canceled) {
			slog.Error("analysis worker iteration failed", "error_type", fmt.Sprintf("%T", err))
		}
		timer := time.NewTimer(s.settings().ML.Idle.Std())
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil
		case <-timer.C:
		}
	}
	return nil
}

func (s *PostgresStore) runAnalysis(ctx context.Context) error {
	settings := s.settings()
	leaseCtx, leaseCancel := context.WithTimeout(ctx, settings.ML.Lease.Std()-settings.ML.FinishTimeout.Std())
	defer leaseCancel()
	ctx = leaseCtx

	var gid, token string
	var requestData []byte
	var mlID *string
	var created time.Time
	var attempts int
	e := s.pool.QueryRow(ctx, `UPDATE request_operations SET state='RUNNING',lease_token=gen_random_uuid(),lease_until=now()+($1::double precision*interval '1 second'),attempts=attempts+1,updated_at=now()
 WHERE id=(SELECT id FROM request_operations WHERE state IN ('QUEUED','RUNNING') AND next_attempt_at<=now() AND(lease_until IS NULL OR lease_until<now()) ORDER BY next_attempt_at,id FOR UPDATE SKIP LOCKED LIMIT 1)
 RETURNING id::text,lease_token::text,request_snapshot,ml_analysis_id,created_at,attempts`, s.settings().ML.Lease.Std().Seconds()).Scan(&gid, &token, &requestData, &mlID, &created, &attempts)
	if errors.Is(e, pgx.ErrNoRows) {
		return errNoAnalysis
	}
	if e != nil {
		return e
	}
	request := decode(requestData)
	analysisID := ""
	if mlID != nil {
		analysisID = *mlID
	}
	if time.Since(created) > s.settings().ML.MaxProcessingDuration.Std() {
		return s.finishFailure(ctx, gid, token, "TIMEOUT", "analysis exceeded maximum duration")
	}
	var response object
	resultCtx, cancel := context.WithTimeout(ctx, s.settings().ML.RequestTimeout.Std())
	response, analysisID, e = s.ml.analysis(resultCtx, request, analysisID)
	cancel()
	if time.Since(created) > s.settings().ML.MaxProcessingDuration.Std() {
		return s.finishFailure(ctx, gid, token, "TIMEOUT", "analysis exceeded maximum duration")
	}
	if e != nil {
		var f *apiFailure
		if errors.As(e, &f) && !f.retryable {
			return s.finishFailure(ctx, gid, token, f.code, f.message)
		}
		if attempts >= s.settings().ML.MaxAttempts && analysisID == "" {
			return s.finishFailure(ctx, gid, token, "SOURCE_UNAVAILABLE", "analysis service unavailable")
		}
	}
	if e != nil || response == nil {
		delay := s.settings().ML.StatusPollInterval.Std()
		if e != nil {
			delay = s.settings().ML.RetryStep.Std()
			for attempt := 1; attempt < min(attempts, 20) && delay < s.settings().ML.RetryMax.Std(); attempt++ {
				delay *= 2
			}
			delay = min(delay, s.settings().ML.RetryMax.Std())
			delay = time.Duration(float64(delay) * (0.8 + rand.Float64()*0.4))
			var f *apiFailure
			if errors.As(e, &f) && f.retryAfter > delay {
				delay = f.retryAfter
			}
		}
		_, e = s.pool.Exec(ctx, `UPDATE request_operations SET ml_analysis_id=NULLIF($3,''),lease_token=NULL,lease_until=NULL,next_attempt_at=now()+($4::double precision*interval '1 second'),updated_at=now() WHERE id=$1::uuid AND lease_token=$2::uuid`, gid, token, analysisID, delay.Seconds())
		return e
	}
	if response["request_operation_id"] != gid || response["trace_id"] != request["trace_id"] {
		return s.finishFailure(ctx, gid, token, "INVALID_ML_RESPONSE", "analysis identity does not match")
	}
	if response["status"] != "succeeded" {
		state, code, message := "FAILED", "ML_PROCESSING_ERROR", "Не удалось завершить анализ. Обратитесь в поддержку с кодом обращения."
		if response["status"] == "failed" && obj(response["error"]) != nil {
			code = str(obj(response["error"])["code"])
			message = analysisErrorMessage(code)
		}
		if response["status"] == "insufficient_evidence" {
			state = "INSUFFICIENT_EVIDENCE"
			code = "INSUFFICIENT_EVIDENCE"
			message = "not enough evidence to prepare an answer"
			descriptions := []string{}
			for _, v := range array(response["gaps"]) {
				gap := obj(v)
				descriptions = append(descriptions, str(gap["code"])+": "+str(gap["description"]))
			}
			if len(descriptions) > 0 {
				message = strings.Join(descriptions, "; ")
				if len(message) > 4000 {
					message = "not enough evidence to prepare an answer"
				}
			}
		}
		_, e = s.pool.Exec(ctx, `UPDATE request_operations SET state=$3,error=$4,response_snapshot=$5,completed_at=now(),updated_at=now(),lease_token=NULL,lease_until=NULL WHERE id=$1::uuid AND lease_token=$2::uuid`, gid, token, state, raw(object{"code": code, "message": message, "retryable": false, "trace_id": request["trace_id"]}), raw(response))
		return e
	}
	if e = s.saveAnalysis(ctx, gid, token, request, response); e != nil {
		var f *apiFailure
		if errors.As(e, &f) {
			return s.finishFailure(ctx, gid, token, f.code, f.message)
		}
		var pe *pgconn.PgError
		if errors.As(e, &pe) && pe.Code == "23505" {
			return s.finishFailure(ctx, gid, token, "INVALID_ML_RESPONSE", "analysis result identity conflicts with an existing result")
		}
		return e
	}
	return nil
}
func (s *PostgresStore) finishFailure(ctx context.Context, gid, token, code, message string) error {
	_, e := s.pool.Exec(ctx, `UPDATE request_operations SET state='FAILED',error=$3::jsonb || jsonb_build_object('trace_id',trace_id),completed_at=now(),updated_at=now(),lease_token=NULL,lease_until=NULL WHERE id=$1::uuid AND lease_token=$2::uuid`, gid, token, raw(object{"code": code, "message": analysisErrorMessage(code), "retryable": false}))
	return e
}

// Never expose provider output, validation payloads or internal credentials.
func analysisErrorMessage(code string) string {
	switch code {
	case "VERIFICATION_FAILED", "INVALID_ML_RESPONSE":
		return "Не удалось подтвердить выводы по источникам. Разбор не опубликован."
	case "TIMEOUT":
		return "Сервис анализа не успел подготовить ответ. Сохранённый статус можно проверить позже."
	case "MODEL_UNAVAILABLE", "SOURCE_UNAVAILABLE":
		return "Сервис анализа временно недоступен. Обратитесь в поддержку с кодом обращения."
	case "BUDGET_EXHAUSTED":
		return "Достигнут лимит запросов к сервису анализа."
	default:
		return "Не удалось завершить анализ. Обратитесь в поддержку с кодом обращения."
	}
}
