package dataset

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

const datasetPath = "/internal/v0.1/datasets/active"

func snapshotID(bundleID string, policyVersion int) string {
	sum := sha256.Sum256([]byte(bundleID + "\x00" + strconv.Itoa(policyVersion)))
	return "dataset:" + hex.EncodeToString(sum[:])
}

type DatasetHandler struct {
	DB    *pgxpool.Pool
	Token string
}

func (h DatasetHandler) Register(mux *http.ServeMux) {
	if h.Token == "" {
		return
	}
	mux.HandleFunc("GET "+datasetPath, h.active)
	mux.HandleFunc("GET "+datasetPath+"/{kind}", h.page)
}

func (h DatasetHandler) authorized(w http.ResponseWriter, r *http.Request) bool {
	provided, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	if !ok || subtle.ConstantTimeCompare([]byte(provided), []byte(h.Token)) != 1 {
		w.Header().Set("WWW-Authenticate", "Bearer")
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return false
	}
	return true
}

func (h DatasetHandler) active(w http.ResponseWriter, r *http.Request) {
	if !h.authorized(w, r) {
		return
	}
	var id, manifestHash string
	var policyVersion int
	err := h.DB.QueryRow(r.Context(), `SELECT b.id,b.manifest_sha256,n.policy_version FROM active_dataset_bundle a
		JOIN dataset_bundles b ON b.id=a.bundle_id
		JOIN dataset_normalization_runs n ON n.bundle_id=b.id
		WHERE a.singleton=true`).Scan(&id, &manifestHash, &policyVersion)
	if errors.Is(err, pgx.ErrNoRows) {
		http.Error(w, "active dataset unavailable", http.StatusServiceUnavailable)
		return
	}
	if err != nil {
		http.Error(w, "database unavailable", http.StatusServiceUnavailable)
		return
	}
	writeDatasetJSON(w, map[string]any{
		"bundle_id": id, "manifest_sha256": manifestHash,
		"normalization_policy_version": policyVersion,
		"snapshot_id":                  snapshotID(id, policyVersion),
	})
}

func (h DatasetHandler) page(w http.ResponseWriter, r *http.Request) {
	if !h.authorized(w, r) {
		return
	}
	queries := map[string]string{
		"companies": `SELECT row_no,to_jsonb(t) FROM active_dataset_company_profiles t
			WHERE bundle_id=$1 AND row_no>$2 ORDER BY row_no LIMIT $3`,
		"payments": `SELECT row_no,jsonb_set(to_jsonb(t),'{pay_amt}',to_jsonb(t.pay_amt::text))
			FROM active_dataset_payments t WHERE bundle_id=$1 AND row_no>$2 ORDER BY row_no LIMIT $3`,
		"person-links": `SELECT row_no,to_jsonb(t) FROM active_dataset_person_company_links t
			WHERE bundle_id=$1 AND row_no>$2 ORDER BY row_no LIMIT $3`,
	}
	query, ok := queries[r.PathValue("kind")]
	if !ok {
		http.NotFound(w, r)
		return
	}
	requestedSnapshot := r.URL.Query().Get("snapshot_id")
	if requestedSnapshot == "" {
		http.Error(w, "snapshot_id is required", http.StatusBadRequest)
		return
	}
	limit := 500
	if raw := r.URL.Query().Get("limit"); raw != "" {
		value, err := strconv.Atoi(raw)
		if err != nil || value < 1 || value > 500 {
			http.Error(w, "limit must be between 1 and 500", http.StatusBadRequest)
			return
		}
		limit = value
	}
	var after int64
	if raw := r.URL.Query().Get("after"); raw != "" {
		value, err := strconv.ParseInt(raw, 10, 64)
		if err != nil || value < 0 {
			http.Error(w, "after must be a nonnegative row number", http.StatusBadRequest)
			return
		}
		after = value
	}
	tx, err := h.DB.BeginTx(r.Context(), pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		http.Error(w, "database unavailable", http.StatusServiceUnavailable)
		return
	}
	defer tx.Rollback(r.Context())
	var activeID string
	var policyVersion int
	err = tx.QueryRow(r.Context(), `SELECT a.bundle_id,n.policy_version FROM active_dataset_bundle a
		JOIN dataset_normalization_runs n ON n.bundle_id=a.bundle_id
		WHERE a.singleton=true`).Scan(&activeID, &policyVersion)
	if err != nil {
		http.Error(w, "active dataset unavailable", http.StatusServiceUnavailable)
		return
	}
	if requestedSnapshot != snapshotID(activeID, policyVersion) {
		http.Error(w, "dataset version changed", http.StatusConflict)
		return
	}
	rows, err := tx.Query(r.Context(), query, activeID, after, limit+1)
	if err != nil {
		http.Error(w, "database unavailable", http.StatusServiceUnavailable)
		return
	}
	records := make([]json.RawMessage, 0, limit)
	last := after
	more := false
	for rows.Next() {
		var rowNo int64
		var record json.RawMessage
		if err := rows.Scan(&rowNo, &record); err != nil {
			rows.Close()
			http.Error(w, "database unavailable", http.StatusServiceUnavailable)
			return
		}
		if len(records) == limit {
			more = true
			break
		}
		records = append(records, record)
		last = rowNo
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		http.Error(w, "database unavailable", http.StatusServiceUnavailable)
		return
	}
	if err := tx.Commit(r.Context()); err != nil {
		http.Error(w, "database unavailable", http.StatusServiceUnavailable)
		return
	}
	response := struct {
		SnapshotID string            `json:"snapshot_id"`
		BundleID   string            `json:"bundle_id"`
		Records    []json.RawMessage `json:"records"`
		NextCursor *string           `json:"next_cursor"`
	}{SnapshotID: requestedSnapshot, BundleID: activeID, Records: records}
	if more {
		cursor := strconv.FormatInt(last, 10)
		response.NextCursor = &cursor
	}
	writeDatasetJSON(w, response)
}

func writeDatasetJSON(w http.ResponseWriter, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	_ = json.NewEncoder(w).Encode(value)
}
