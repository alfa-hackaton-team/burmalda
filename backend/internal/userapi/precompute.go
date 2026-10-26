package userapi

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// AnalysisExecutor runs the existing production Python service outside the API.
// It must not retry an unknown model outcome.
type AnalysisExecutor func(context.Context, map[string]any) (map[string]any, error)

func (s *PostgresStore) ConfigureDemo(version string) { s.demoVersion = strings.TrimSpace(version) }

// Precompute uses only trusted database context/event data. The operator selects
// the exact user/context/event; no synthetic identity or profile is provisioned.
func (s *PostgresStore) Precompute(ctx context.Context, user, cid, eid, set string, manifest map[string]any, execute AnalysisExecutor) (map[string]any, error) {
	for _, v := range []string{user, cid, eid} {
		if _, e := id(v); e != nil {
			return nil, e
		}
	}
	if set == "" || len(set) > 100 || s.analysisVersion == "" || len(manifest) == 0 {
		return nil, fmt.Errorf("demo set, processing config and runtime manifest are required")
	}
	c, e := s.context(ctx, user, cid)
	if e != nil {
		return nil, e
	}
	if e = requireProfile(c); e != nil {
		return nil, e
	}
	event, e := s.event(ctx, eid)
	if e != nil {
		return nil, e
	}
	versions := array(event["document_version_ids"])
	if len(versions) == 0 {
		return nil, fmt.Errorf("event has no ready document versions")
	}
	// Capture a deterministic corpus boundary before reserving the paid operation.
	// Only this event's indexed versions are eligible; the fingerprint includes
	// content and metadata, not merely event/company IDs.
	rows, e := s.corpusPool().Query(ctx, `SELECT c.id::text,c.document_version_id::text,c.chunk_text,d.snapshot FROM ml.document_chunks c JOIN ml.document_versions d ON d.id=c.document_version_id WHERE c.document_version_id IN (SELECT jsonb_array_elements_text($1::jsonb)::uuid) ORDER BY c.id`, raw(versions))
	if e != nil {
		return nil, e
	}
	expectedChunks := map[string]object{}
	corpus := []any{}
	ids := []any{}
	for rows.Next() {
		var id, version, text string
		var data []byte
		if e = rows.Scan(&id, &version, &text, &data); e != nil {
			rows.Close()
			return nil, e
		}
		ids = append(ids, id)
		doc := decode(data)
		source := obj(doc["source"])
		title := str(doc["title"])
		if strings.TrimSpace(title) == "" {
			title = "Без заголовка"
		}
		expectedChunks[id] = object{"chunk_id": id, "document_version_id": version, "document_id": doc["document_id"], "chunk_text": text, "title": title, "canonical_url": doc["canonical_url"], "published_at": doc["published_at"], "retrieved_at": doc["retrieved_at"], "source_id": source["source_id"], "source_name": source["name"], "source_type": source["source_type"], "source_role": source["source_role"], "trust_level": source["trust_level"]}
		corpus = append(corpus, object{"id": id, "text": text, "document": doc})
	}
	e = rows.Err()
	rows.Close()
	if e != nil {
		return nil, e
	}
	if len(ids) == 0 {
		return nil, fmt.Errorf("event has no indexed evidence")
	}
	corpusHash := sha256.Sum256(raw(corpus))
	scope := object{"version": "demo-scope-v1", "user_id": user, "context": mlContext(c), "event": event, "demo_set_version": set, "processing_config_version": s.analysisVersion, "manifest": manifest, "corpus_sha256": hex.EncodeToString(corpusHash[:])}
	fingerprint := sha256.Sum256(raw(scope))
	key := "demo:" + hex.EncodeToString(fingerprint[:])
	gid, trace, token := uuid.NewString(), uuid.NewString(), uuid.NewString()
	request := object{"contract_version": "v1", "trace_id": trace, "request_operation_id": gid, "request_type": "initial", "context": mlContext(c), "event": event, "follow_up": nil, "processing_config_version": s.analysisVersion, "parent_trace_id": nil}
	if e = checked("ml", "AnalysisRequest", request); e != nil {
		return nil, e
	}
	// Reserve RUNNING once. A crash leaves RUNNING (unknown outcome); neither the
	// normal worker nor a repeated invocation re-executes it automatically.
	tag, e := s.pool.Exec(ctx, `INSERT INTO request_operations(id,user_id,business_context_id,operation_type,idempotency_key,request_fingerprint,request_snapshot,trace_id,event_id,state,lease_token,lease_until,attempts,demo_set_version,runtime_manifest)
 VALUES($1,$2,$3,'insight',$4,$5,$6,$7,$8,'RUNNING',$9,now()+interval '1 day',1,$10,$11) ON CONFLICT DO NOTHING`, gid, user, cid, key, fingerprint[:], raw(request), trace, eid, token, set, raw(manifest))
	if e != nil {
		return nil, e
	}
	if tag.RowsAffected() == 0 {
		var state string
		var iid *string
		e = s.pool.QueryRow(ctx, `SELECT id::text,state,insight_id::text FROM request_operations WHERE user_id=$1::uuid AND operation_type='insight' AND idempotency_key=$2 AND request_fingerprint=$3`, user, key, fingerprint[:]).Scan(&gid, &state, &iid)
		if errors.Is(e, pgx.ErrNoRows) {
			return nil, fail(409, "CONFLICT", "frozen demo slot changed; use a new set version")
		}
		if e != nil {
			return nil, e
		}
		if state == "SUCCEEDED" && iid != nil {
			if _, _, e = s.insight(ctx, user, *iid); e != nil {
				return nil, e
			}
		}
		return object{"generation_id": gid, "state": state, "insight_id": iid, "reused": true, "unknown_outcome": state == "RUNNING"}, nil
	}
	started := time.Now()
	bundle, e := execute(ctx, object{"request": request, "manifest": manifest, "chunk_ids": ids})
	if e != nil {
		finishCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		finishErr := s.finishFailure(finishCtx, gid, token, "UNKNOWN_OUTCOME", "execution did not return a durable result; operator review required, no automatic retry")
		e = errors.Join(e, finishErr)
		return nil, fmt.Errorf("precompute %s has unknown outcome; no retry: %w", gid, e)
	}
	response := obj(bundle["response"])
	if e = checked("ml", "AnalysisResponse", response); e != nil {
		return nil, s.precomputeFailure(ctx, gid, token, e)
	}
	if response["request_operation_id"] != gid || response["trace_id"] != trace || string(raw(bundle["manifest"])) != string(raw(manifest)) {
		return nil, s.precomputeFailure(ctx, gid, token, fmt.Errorf("runtime/analysis identity mismatch"))
	}
	// Retain the evidence and contracted model/verifier outputs even on rejection.
	// This is internal diagnostic data, never a published or verified Insight.
	chunks := array(bundle["chunks"])
	evidenceHash := sha256.Sum256(raw(chunks))
	var diagnostics any
	if snapshot := obj(bundle["diagnostics"]); snapshot != nil {
		diagnostics = raw(snapshot)
	}
	_, e = s.pool.Exec(ctx, `UPDATE request_operations SET analysis_diagnostics=$3,evidence_snapshot=$4,evidence_fingerprint=$5 WHERE id=$1::uuid AND lease_token=$2::uuid AND state='RUNNING'`, gid, token, diagnostics, raw(chunks), evidenceHash[:])
	if e != nil {
		return nil, e
	}
	state := "FAILED"
	switch response["status"] {
	case "succeeded":
		allowed := map[string]bool{}
		for _, id := range ids {
			allowed[str(id)] = true
		}
		for _, v := range chunks {
			if !allowed[str(obj(v)["chunk_id"])] || string(raw(obj(v))) != string(raw(expectedChunks[str(obj(v)["chunk_id"])])) {
				return nil, s.precomputeFailure(ctx, gid, token, fmt.Errorf("evidence outside frozen scope"))
			}
		}
		if e = s.saveAnalysisEvidence(ctx, gid, token, request, response, chunks); e != nil {
			return nil, s.precomputeFailure(ctx, gid, token, e)
		}
		state = "SUCCEEDED"
	case "insufficient_evidence":
		state = "INSUFFICIENT_EVIDENCE"
	}
	if state != "SUCCEEDED" {
		_, e = s.pool.Exec(ctx, `UPDATE request_operations SET state=$3,response_snapshot=$4,error=$5,completed_at=now(),updated_at=now(),lease_token=NULL,lease_until=NULL WHERE id=$1::uuid AND lease_token=$2::uuid`, gid, token, state, raw(response), raw(object{"code": state, "message": "No verified Insight was published", "retryable": false}))
		if e != nil {
			return nil, e
		}
	}
	result, e := s.generation(ctx, user, gid)
	if e != nil {
		return nil, e
	}
	result["elapsed_seconds"] = time.Since(started).Seconds()
	result["reused"] = false
	result["runtime_metrics"] = bundle["metrics"]
	return result, nil
}

func (s *PostgresStore) precomputeFailure(ctx context.Context, gid, token string, cause error) error {
	e := s.finishFailure(ctx, gid, token, "INVALID_ML_RESPONSE", "precompute did not publish a verified Insight")
	return errors.Join(cause, e)
}

func sameContext(current, saved object) bool {
	return current["_status"] == "active" && string(raw(mlContext(current))) == string(raw(saved))
}

func (s *PostgresStore) readyInsights(ctx context.Context, user, cid string) (object, error) {
	c, e := s.context(ctx, user, cid)
	if e != nil {
		return nil, e
	}
	if e = requireProfile(c); e != nil {
		return nil, e
	}
	rows, e := s.pool.Query(ctx, `SELECT i.payload,i.ml_snapshot FROM insights i JOIN request_operations o ON o.id=i.request_operation_id WHERE i.user_id=$1::uuid AND i.business_context_id=$2::uuid AND o.state='SUCCEEDED' AND o.demo_set_version=$3 AND $3<>'' ORDER BY i.created_at DESC,i.id LIMIT 100`, user, cid, s.demoVersion)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	items := []any{}
	for rows.Next() {
		var payload, snapshot []byte
		if e = rows.Scan(&payload, &snapshot); e != nil {
			return nil, e
		}
		if sameContext(c, obj(decode(snapshot)["context"])) {
			items = append(items, decode(payload))
		}
	}
	return object{"items": items}, rows.Err()
}
func (h productHandler) readyInsights(ctx context.Context, user string, r *http.Request) (any, int, error) {
	if e := query(r); e != nil {
		return nil, 0, e
	}
	cid, e := pathID(r, "context_id")
	if e != nil {
		return nil, 0, e
	}
	v, e := h.store.readyInsights(ctx, user, cid)
	return v, 200, e
}
