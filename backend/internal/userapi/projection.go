package userapi

import (
	"context"
	"crypto/sha256"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
)

func (s *PostgresStore) chunk(ctx context.Context, chunkID string) (object, error) {
	var version, text string
	var snapshot []byte
	e := s.corpusPool().QueryRow(ctx, `SELECT c.document_version_id::text,c.chunk_text,d.snapshot FROM ml.document_chunks c JOIN ml.document_versions d ON d.id=c.document_version_id WHERE c.id=$1::uuid`, chunkID).Scan(&version, &text, &snapshot)
	if e != nil {
		return nil, fail(502, "INVALID_ML_RESPONSE", "referenced evidence is unavailable")
	}
	doc := decode(snapshot)
	if strings.TrimSpace(str(doc["title"])) == "" {
		doc["title"] = "Без заголовка"
	}
	source := obj(doc["source"])
	out := object{"chunk_id": chunkID, "document_id": doc["document_id"], "document_version_id": version, "chunk_text": text, "title": doc["title"], "canonical_url": doc["canonical_url"], "published_at": doc["published_at"], "retrieved_at": doc["retrieved_at"], "source_id": source["source_id"], "source_name": source["name"], "source_type": source["source_type"], "source_role": source["source_role"], "trust_level": source["trust_level"]}
	return out, nil
}
func (s *PostgresStore) project(ctx context.Context, gid string, request, response object) (object, []any, error) {
	return s.projectEvidence(ctx, gid, request, response, nil)
}

func (s *PostgresStore) projectEvidence(ctx context.Context, gid string, request, response object, exact []any) (object, []any, error) {
	result := obj(response["insight"])
	if request["request_type"] == "follow_up" {
		result = obj(response["message"])
	}
	if result == nil {
		return nil, nil, fail(502, "INVALID_ML_RESPONSE", "missing analysis result")
	}
	contextData := obj(request["context"])
	features := map[string]object{}
	for _, v := range array(contextData["profile_features"]) {
		f := obj(v)
		features[str(f["id"])] = f
	}
	knownChunks := map[string]object{}
	for _, v := range exact {
		c := obj(v)
		knownChunks[str(c["chunk_id"])] = c
	}
	if follow := obj(request["follow_up"]); follow != nil {
		for _, v := range array(obj(follow["initial_insight"])["chunks"]) {
			c := obj(v)
			knownChunks[str(c["chunk_id"])] = c
		}
		for _, v := range array(follow["history"]) {
			for _, ch := range array(obj(v)["chunks"]) {
				c := obj(ch)
				knownChunks[str(c["chunk_id"])] = c
			}
		}
	}
	claims, evidence, sections, limitations, chunks := []any{}, []any{}, []any{}, []any{}, []any{}
	bySection := map[string][]any{}
	seenClaims := map[string]bool{}
	usedChunks := map[string]bool{}
	priorClaims := map[string]bool{}
	if follow := obj(request["follow_up"]); follow != nil {
		for _, v := range array(obj(obj(follow["initial_insight"])["insight"])["claims"]) {
			priorClaims[str(obj(v)["id"])] = true
		}
		for _, v := range array(follow["history"]) {
			for _, claim := range array(obj(v)["claims"]) {
				priorClaims[str(obj(claim)["id"])] = true
			}
		}
	}
	for _, v := range array(result["claims"]) {
		claim := obj(v)
		claimID := str(claim["id"])
		if seenClaims[claimID] || priorClaims[claimID] {
			return nil, nil, fail(502, "INVALID_ML_RESPONSE", "duplicate claim identifier")
		}
		seenClaims[claimID] = true
		links := []any{}
		support, bankSupport := false, false
		for index, v := range array(claim["evidence"]) {
			ev := obj(v)
			evid := uuid.NewSHA1(uuid.MustParse(gid), []byte(fmt.Sprintf("%s/%d", claimID, index))).String()
			role := str(ev["evidence_role"])
			links = append(links, object{"evidence_id": evid, "relation": strings.ToUpper(role)})
			if role == "support" {
				support = true
			}
			if ev["evidence_type"] == "document_chunk" {
				chunkID := str(ev["chunk_id"])
				c := knownChunks[chunkID]
				if c == nil {
					if exact != nil {
						return nil, nil, fail(502, "INVALID_ML_RESPONSE", "evidence is outside the analysis snapshot")
					}
					var e error
					c, e = s.chunk(ctx, chunkID)
					if e != nil {
						return nil, nil, e
					}
				}
				quote := str(ev["quote_text"])
				if quote == "" || !strings.Contains(str(c["chunk_text"]), quote) {
					return nil, nil, fail(502, "INVALID_ML_RESPONSE", "evidence quote is not present in the saved chunk")
				}
				typ := "EXTERNAL_NEWS"
				if c["source_role"] == "bank" {
					typ = "ALFA_ANALYTICS"
					if role == "support" {
						bankSupport = true
					}
				}
				evidence = append(evidence, object{"evidence_id": evid, "evidence_type": typ, "reference": object{"source_id": c["source_id"], "document_version_id": c["document_version_id"], "chunk_id": chunkID}, "excerpt": quote, "published_at": c["published_at"], "fetched_at": c["retrieved_at"], "canonical_url": c["canonical_url"], "title": c["title"], "source_name": c["source_name"], "source_role": c["source_role"], "source_type": c["source_type"], "trust_level": c["trust_level"]})
				if !usedChunks[chunkID] {
					chunks = append(chunks, c)
					usedChunks[chunkID] = true
				}
			} else {
				f := features[str(ev["profile_feature_id"])]
				if f == nil {
					return nil, nil, fail(502, "INVALID_ML_RESPONSE", "profile feature is outside the saved profile")
				}
				ref := obj(f["source_ref"])
				snapshot := str(ref["source_snapshot_id"])
				records := array(ref["record_refs"])
				derivation := str(ref["derivation_version"])
				if snapshot == "" || len(records) == 0 || derivation == "" {
					return nil, nil, fail(502, "INVALID_ML_RESPONSE", "profile feature provenance is missing")
				}
				evidence = append(evidence, object{"evidence_id": evid, "evidence_type": "COMPANY_DATA", "reference": object{"profile": profileRef(contextData), "feature_id": f["id"], "contributing_company_ids": array(ref["contributing_company_ids"])}, "content": string(raw(f["feature_value"])), "provenance": object{"source_snapshot_id": snapshot, "record_refs": records, "derivation_version": derivation}})
			}
		}
		if !support {
			return nil, nil, fail(502, "INVALID_ML_RESPONSE", "claim has no supporting evidence")
		}
		if (claim["knowledge_type"] == "ALFA_ANALYTICS" || claim["claim_type"] == "bank_view") && !bankSupport {
			return nil, nil, fail(502, "INVALID_ML_RESPONSE", "bank claim has no bank evidence")
		}
		if claim["verification_status"] == "PARTIALLY_SUPPORTED" && len(array(claim["assumptions"])) == 0 {
			return nil, nil, fail(502, "INVALID_ML_RESPONSE", "partial claim requires assumptions")
		}
		pub := object{"claim_id": claimID, "text": claim["claim_text"], "claim_type": claim["knowledge_type"], "verification_status": claim["verification_status"], "evidence_links": links, "assumptions": claim["assumptions"]}
		if claim["time_horizon"] != nil {
			pub["time_horizon"] = claim["time_horizon"]
		}
		claims = append(claims, pub)
		section := "AI_ANALYSIS"
		switch claim["claim_type"] {
		case "action":
			section = "SCENARIOS_ACTIONS"
		case "relevance":
			section = "WHY_RELEVANT"
		case "bank_view":
			section = "BANK_VIEW"
		default:
			if claim["knowledge_type"] == "FACT" {
				section = "WHAT_HAPPENED"
			} else if claim["knowledge_type"] == "ALFA_ANALYTICS" {
				section = "BANK_VIEW"
			} else if result["impact_kind"] == "risk" {
				section = "RISKS"
			} else if result["impact_kind"] == "opportunity" {
				section = "OPPORTUNITIES"
			}
		}
		bySection[section] = append(bySection[section], claimID)
	}

	if request["request_type"] == "follow_up" {
		for _, v := range array(result["citations"]) {
			citation := obj(v)
			chunkID, claimID := str(citation["chunk_id"]), str(citation["claim_id"])
			if chunkID == "" && claimID == "" {
				return nil, nil, fail(502, "INVALID_ML_RESPONSE", "citation has no target")
			}
			if claimID != "" && !seenClaims[claimID] && !priorClaims[claimID] {
				return nil, nil, fail(502, "INVALID_ML_RESPONSE", "citation references an unknown claim")
			}
			if chunkID != "" && !usedChunks[chunkID] {
				c := knownChunks[chunkID]
				if c == nil {
					return nil, nil, fail(502, "INVALID_ML_RESPONSE", "citation references a chunk outside the response evidence")
				}
				chunks = append(chunks, c)
				usedChunks[chunkID] = true
			}
		}
	}
	for _, name := range []string{"WHAT_HAPPENED", "WHY_RELEVANT", "RISKS", "OPPORTUNITIES", "SCENARIOS_ACTIONS", "BANK_VIEW", "AI_ANALYSIS"} {
		if ids := bySection[name]; len(ids) > 0 {
			sections = append(sections, object{"section_type": name, "claim_ids": ids})
		}
	}
	for _, w := range array(response["warnings"]) {
		limitations = append(limitations, object{"code": "ML_WARNING", "message": w, "related_claim_ids": []any{}})
	}
	for _, g := range array(response["gaps"]) {
		gap := obj(g)
		limitations = append(limitations, object{"code": gap["code"], "message": gap["description"], "related_claim_ids": []any{}})
	}
	out := object{"trace_id": request["trace_id"], "claims": claims, "evidence": evidence, "sections": sections, "limitations": limitations, "model_config_ref": request["processing_config_version"]}
	if e := checked("public", "AgentResponse", out); e != nil {
		return nil, nil, e
	}
	return out, chunks, nil
}
func (s *PostgresStore) saveAnalysis(ctx context.Context, gid, token string, request, response object) error {
	return s.saveAnalysisEvidence(ctx, gid, token, request, response, nil)
}

func (s *PostgresStore) saveAnalysisEvidence(ctx context.Context, gid, token string, request, response object, exact []any) error {
	if response["status"] != "succeeded" || obj(response["insight"]) == nil && request["request_type"] != "follow_up" {
		return fail(502, "INVALID_ML_RESPONSE", "analysis is not successful")
	}
	if request["request_type"] != "follow_up" && obj(response["insight"])["status"] != "verified" {
		return fail(502, "INVALID_ML_RESPONSE", "insight is not verified")
	}
	projected, chunks, e := s.projectEvidence(ctx, gid, request, response, exact)
	if e != nil {
		return e
	}
	c, event := obj(request["context"]), obj(request["event"])
	now := time.Now().UTC()
	tx, e := s.pool.Begin(ctx)
	if e != nil {
		return e
	}
	defer tx.Rollback(ctx)
	var user string
	e = tx.QueryRow(ctx, `SELECT user_id::text FROM request_operations WHERE id=$1::uuid AND lease_token=$2::uuid AND state='RUNNING' FOR UPDATE`, gid, token).Scan(&user)
	if e != nil {
		return e
	}
	current, e := contextFrom(ctx, tx, user, str(c["business_context_id"]))
	if e != nil {
		return e
	}
	if string(raw(mlContext(current))) != string(raw(c)) || current["_status"] != "active" {
		return fail(409, "CONFLICT", "analysis context changed")
	}
	if exact != nil {
		fingerprint := sha256.Sum256(raw(exact))
		if _, e = tx.Exec(ctx, `UPDATE request_operations SET evidence_snapshot=$3,evidence_fingerprint=$4 WHERE id=$1::uuid AND lease_token=$2::uuid`, gid, token, raw(exact), fingerprint[:]); e != nil {
			return e
		}
	}
	var iid, mid any
	if request["request_type"] == "follow_up" {
		follow := obj(request["follow_up"])
		message := obj(response["message"])
		if message["conversation_id"] != follow["conversation_id"] || message["parent_message_id"] != follow["request_message_id"] {
			return fail(502, "INVALID_ML_RESPONSE", "message identity does not match")
		}
		mid = message["id"]
		var seq int
		var lock string
		if e = tx.QueryRow(ctx, `SELECT id::text FROM conversations WHERE id=$1::uuid FOR UPDATE`, follow["conversation_id"]).Scan(&lock); e != nil {
			return e
		}
		if e = tx.QueryRow(ctx, `SELECT COALESCE(max(sequence),0)+1 FROM messages WHERE conversation_id=$1::uuid`, follow["conversation_id"]).Scan(&seq); e != nil {
			return e
		}
		payload := object{"message_id": mid, "conversation_id": follow["conversation_id"], "sequence": seq, "author": "ASSISTANT", "created_at": now, "response": projected, "text": message["content"]}
		if e = checked("public", "Message", payload); e != nil {
			return e
		}
		history := object{"id": mid, "role": "assistant", "content": message["content"], "claims": message["claims"], "citations": message["citations"], "chunks": chunks}
		_, e = tx.Exec(ctx, `INSERT INTO messages(id,conversation_id,sequence,parent_message_id,author,payload,ml_snapshot) VALUES($1,$2,$3,$4,'ASSISTANT',$5,$6)`, mid, follow["conversation_id"], seq, follow["request_message_id"], raw(payload), raw(history))
		if e != nil {
			return e
		}
	} else {
		insight := obj(response["insight"])
		if insight["business_context_id"] != c["business_context_id"] || insight["event_id"] != event["id"] || insight["analysis_run_id"] != response["analysis_run_id"] {
			return fail(502, "INVALID_ML_RESPONSE", "insight identity does not match")
		}
		iid = insight["id"]
		payload := object{"insight_id": iid, "revision": fmt.Sprint(insight["version"]), "primary_event": object{"event_id": event["id"], "title": event["title"], "summary": event["summary"], "occurred_at": event["occurred_at"]}, "supporting_event_ids": []any{}, "context": publicContext(c), "role_id": c["role_code"], "profile": profileRef(c), "created_at": now, "response": projected}
		if e = checked("public", "Insight", payload); e != nil {
			return e
		}
		snapshot := object{"context": c, "event": event, "insight": insight, "chunks": chunks, "trace_id": request["trace_id"]}
		_, e = tx.Exec(ctx, `INSERT INTO insights(id,user_id,business_context_id,request_operation_id,version,event_id,payload,ml_snapshot) VALUES($1,$2,$3,$4,$5,$6,$7,$8)`, iid, user, c["business_context_id"], gid, number(insight["version"]), event["id"], raw(payload), raw(snapshot))
		if e != nil {
			return e
		}
	}
	_, e = tx.Exec(ctx, `UPDATE request_operations SET state='SUCCEEDED',insight_id=$3,message_id=$4,response_snapshot=$5,completed_at=now(),updated_at=now(),lease_token=NULL,lease_until=NULL WHERE id=$1::uuid AND lease_token=$2::uuid`, gid, token, iid, mid, raw(response))
	if e != nil {
		return e
	}
	return tx.Commit(ctx)
}
