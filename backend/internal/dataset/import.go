package dataset

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/csv"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/alfa-hackaton-team/burmalda/backend/internal/rawartifacts"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/ledongthuc/pdf"
)

type File struct {
	Name       string `json:"name"`
	Kind       string `json:"kind"`
	SHA256     string `json:"sha256"`
	SizeBytes  int64  `json:"size_bytes"`
	TextSHA256 string `json:"text_sha256,omitempty"`
}

type Manifest struct {
	Version string `json:"version"`
	Files   []File `json:"files"`
}

type Result struct {
	BundleID   string
	Skipped    bool
	Normalized bool
	Rows       map[string]int64
}

var requiredCSV = map[string][]string{
	"df_Uid_data_hack.csv":     {"SYNTHETIC_U", "ORG_TYPE", "OKFS_NAME", "COMPANY_AGE_YEARS_GRADATION", "ECO_MARK_LABEL", "MAINOKVED_CODE", "MAINOKVED_NAME", "NEGATIVEMARK", "AMOUNT_WORKERS_FNS_GRADATION", "AMOUNT_WORKERS_SPARK_GRADATION", "CL_TYPE", "CL_PRODUCT_RKO_MARK", "CL_PRODUCT_GAR_MARK", "CL_PRODUCT_LOA_MARK", "CL_PRODUCT_LEA_MARK", "CL_PRODUCT_DEP_MARK", "CL_PRODUCT_ZPP_MARK", "FTS_FLAG", "FTS_COUNTRY_NAME", "FACTORING_MARK", "REVENUE_COMPANY", "GROUP_ID", "CITY", "REGION"},
	"df_Uid_payment_hack.csv":  {"BENEFICIARY", "PAYER", "PAY_AMT", "PAY_CNT"},
	"df_Fid_Uid_link_hack.csv": {"SYNTHETIC_F", "CITY_NAME", "RC_NAME", "SYNTHETIC_U"},
}

var profileColumns = []string{"bundle_id", "row_no", "synthetic_u", "org_type", "okfs_name", "company_age_years_gradation", "eco_mark_label", "mainokved_code", "mainokved_name", "negativemark", "amount_workers_fns_gradation", "amount_workers_spark_gradation", "cl_type", "cl_product_rko_mark", "cl_product_gar_mark", "cl_product_loa_mark", "cl_product_lea_mark", "cl_product_dep_mark", "cl_product_zpp_mark", "fts_flag", "fts_country_name", "factoring_mark", "revenue_company", "group_id", "city", "region"}

var hashPattern = regexp.MustCompile(`^[0-9a-f]{64}$`)

func ReadManifest(dir string) (Manifest, string, error) {
	data, err := os.ReadFile(filepath.Join(dir, "manifest.json"))
	if err != nil {
		return Manifest{}, "", fmt.Errorf("read dataset manifest: %w", err)
	}
	var manifest Manifest
	decoder := json.NewDecoder(strings.NewReader(string(data)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&manifest); err != nil {
		return Manifest{}, "", fmt.Errorf("parse dataset manifest: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return Manifest{}, "", fmt.Errorf("dataset manifest contains trailing data")
	}
	if manifest.Version == "" || len(manifest.Files) != 12 {
		return Manifest{}, "", fmt.Errorf("dataset manifest needs a version and exactly 12 files")
	}
	seen := make(map[string]bool)
	for _, file := range manifest.Files {
		if file.Name != filepath.Base(file.Name) || file.Name == "." || file.Name == ".." || seen[file.Name] || !hashPattern.MatchString(file.SHA256) || file.SizeBytes <= 0 {
			return Manifest{}, "", fmt.Errorf("invalid dataset file entry %q", file.Name)
		}
		seen[file.Name] = true
		if _, ok := requiredCSV[file.Name]; ok {
			if file.Kind != "csv" || file.TextSHA256 != "" {
				return Manifest{}, "", fmt.Errorf("%s must be CSV", file.Name)
			}
		} else if file.Kind != "pdf" || !strings.HasSuffix(strings.ToLower(file.Name), ".pdf") || !hashPattern.MatchString(file.TextSHA256) {
			return Manifest{}, "", fmt.Errorf("unsupported dataset file %q", file.Name)
		}
	}
	for name := range requiredCSV {
		if !seen[name] {
			return Manifest{}, "", fmt.Errorf("dataset is missing %s", name)
		}
	}
	for _, file := range manifest.Files {
		if err := verifyFile(filepath.Join(dir, file.Name), file.SHA256, file.SizeBytes); err != nil {
			return Manifest{}, "", fmt.Errorf("SHA-256 mismatch for %s", file.Name)
		}
		if file.Kind == "pdf" {
			if err := verifyFile(textPath(filepath.Join(dir, file.Name)), file.TextSHA256, 0); err != nil {
				return Manifest{}, "", fmt.Errorf("provided text mismatch for %s: %w", file.Name, err)
			}
		}
	}
	hash := sha256.Sum256(data)
	return manifest, hex.EncodeToString(hash[:]), nil
}

func verifyFile(path, expectedHash string, expectedSize int64) error {
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Size() == 0 || (expectedSize > 0 && info.Size() != expectedSize) {
		return fmt.Errorf("missing or changed file")
	}
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	sum := sha256.New()
	if _, err := io.Copy(sum, f); err != nil {
		return err
	}
	if hex.EncodeToString(sum.Sum(nil)) != expectedHash {
		return fmt.Errorf("hash changed")
	}
	return nil
}

func Ensure(ctx context.Context, db *pgxpool.Pool, dir string, store rawartifacts.Store, s3 *rawartifacts.S3) (Result, error) {
	manifest, manifestHash, err := ReadManifest(dir)
	if err != nil {
		return Result{}, err
	}
	id := manifest.Version + ":" + manifestHash[:16]
	conn, err := db.Acquire(ctx)
	if err != nil {
		return Result{}, err
	}
	defer conn.Release()
	if _, err := conn.Exec(ctx, `SELECT pg_advisory_lock(748294679294)`); err != nil {
		return Result{}, fmt.Errorf("lock dataset import: %w", err)
	}
	defer func() {
		unlockCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_, _ = conn.Exec(unlockCtx, `SELECT pg_advisory_unlock(748294679294)`)
	}()
	result := Result{BundleID: id, Rows: map[string]int64{}}
	ready, err := isReady(ctx, conn.Conn(), id, manifest, s3)
	if err != nil {
		return result, err
	}
	if ready {
		ok, err := normalized(ctx, conn.Conn(), id)
		if err != nil {
			return result, err
		}
		if !ok {
			tx, err := conn.Begin(ctx)
			if err != nil {
				return result, err
			}
			defer tx.Rollback(ctx)
			if err := normalize(ctx, tx, id); err != nil {
				return result, err
			}
			if err := tx.Commit(ctx); err != nil {
				return result, err
			}
			result.Normalized = true
		}
		result.Skipped = true
		return result, nil
	}
	if store == nil {
		return result, errors.New("raw artifact store is required")
	}
	tx, err := conn.Begin(ctx)
	if err != nil {
		return result, err
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, `INSERT INTO dataset_bundles (id, manifest_sha256) VALUES ($1,$2) ON CONFLICT (id) DO NOTHING`, id, manifestHash); err != nil {
		return result, err
	}
	for _, table := range []string{
		"dataset_profile_duplicate_rows", "dataset_payment_duplicate_rows", "dataset_person_link_duplicate_rows",
		"dataset_profile_conflicts", "dataset_person_link_conflicts", "dataset_normalization_runs",
		"dataset_files", "dataset_company_profiles", "dataset_payments", "dataset_person_company_links", "dataset_bank_reports",
	} {
		if _, err := tx.Exec(ctx, "DELETE FROM "+table+" WHERE bundle_id=$1", id); err != nil {
			return result, err
		}
	}
	for _, file := range manifest.Files {
		path := filepath.Join(dir, file.Name)
		var rows *int64
		if file.Kind == "csv" {
			count, err := copyCSV(ctx, tx, id, path, file.Name)
			if err != nil {
				return result, fmt.Errorf("import %s: %w", file.Name, err)
			}
			rows = &count
			result.Rows[file.Name] = count
		} else {
			if err := importPDF(ctx, tx, id, path, file, store); err != nil {
				return result, fmt.Errorf("import %s: %w", file.Name, err)
			}
		}
		if _, err := tx.Exec(ctx, `INSERT INTO dataset_files (bundle_id,name,sha256,size_bytes,row_count) VALUES ($1,$2,$3,$4,$5)`, id, file.Name, file.SHA256, file.SizeBytes, rows); err != nil {
			return result, err
		}
	}
	if err := normalize(ctx, tx, id); err != nil {
		return result, err
	}
	result.Normalized = true
	if _, err := tx.Exec(ctx, `INSERT INTO active_dataset_bundle (singleton,bundle_id) VALUES (true,$1)
		ON CONFLICT (singleton) DO UPDATE SET bundle_id=EXCLUDED.bundle_id`, id); err != nil {
		return result, err
	}
	if err := tx.Commit(ctx); err != nil {
		return result, fmt.Errorf("commit dataset import: %w", err)
	}
	return result, nil
}

func RequireActive(ctx context.Context, db *pgxpool.Pool) error {
	var id string
	if err := db.QueryRow(ctx, `SELECT a.bundle_id FROM active_dataset_bundle a
		JOIN dataset_normalization_runs n ON n.bundle_id=a.bundle_id AND n.policy_version=$1
		WHERE a.singleton=true`, normalizationPolicyVersion).Scan(&id); err != nil {
		return fmt.Errorf("active reference dataset is unavailable: %w", err)
	}
	return nil
}

func isReady(ctx context.Context, db *pgx.Conn, id string, manifest Manifest, s3 *rawartifacts.S3) (bool, error) {
	var active string
	err := db.QueryRow(ctx, `SELECT bundle_id FROM active_dataset_bundle WHERE singleton=true`).Scan(&active)
	if errors.Is(err, pgx.ErrNoRows) || active != id {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	for _, file := range manifest.Files {
		var hash string
		var size int64
		var rows *int64
		err := db.QueryRow(ctx, `SELECT sha256,size_bytes,row_count FROM dataset_files WHERE bundle_id=$1 AND name=$2`, id, file.Name).Scan(&hash, &size, &rows)
		if errors.Is(err, pgx.ErrNoRows) || hash != file.SHA256 || size != file.SizeBytes {
			return false, nil
		}
		if err != nil {
			return false, err
		}
		if file.Kind == "csv" {
			var count int64
			table := csvTable(file.Name)
			if err := db.QueryRow(ctx, "SELECT count(*) FROM "+table+" WHERE bundle_id=$1", id).Scan(&count); err != nil {
				return false, err
			}
			if rows == nil || count != *rows || count == 0 {
				return false, nil
			}
			continue
		}
		var objectURI *string
		var content []byte
		var reportHash, textHash, status string
		var reportSize int64
		err = db.QueryRow(ctx, `SELECT object_uri,content,sha256,size_bytes,text_sha256,text_status FROM dataset_bank_reports WHERE bundle_id=$1 AND name=$2`, id, file.Name).Scan(&objectURI, &content, &reportHash, &reportSize, &textHash, &status)
		if errors.Is(err, pgx.ErrNoRows) || reportHash != hash || reportSize != size || textHash != file.TextSHA256 || status != "PROVIDED" {
			return false, nil
		}
		if err != nil {
			return false, err
		}
		if objectURI != nil {
			if s3 == nil || s3.VerifyMetadata(ctx, rawartifacts.Artifact{ObjectURI: *objectURI, SHA256: hash, Size: size}) != nil {
				return false, nil
			}
		} else if int64(len(content)) != size {
			return false, nil
		}
	}
	return true, nil
}

func csvTable(name string) string {
	switch name {
	case "df_Uid_data_hack.csv":
		return "dataset_company_profiles"
	case "df_Uid_payment_hack.csv":
		return "dataset_payments"
	default:
		return "dataset_person_company_links"
	}
}

func copyCSV(ctx context.Context, tx pgx.Tx, id, path, name string) (int64, error) {
	f, err := os.Open(path)
	if err != nil {
		return 0, err
	}
	defer f.Close()
	r := csv.NewReader(f)
	r.Comma = ';'
	r.FieldsPerRecord = len(requiredCSV[name])
	header, err := r.Read()
	if err != nil || !equal(header, requiredCSV[name]) {
		return 0, fmt.Errorf("unexpected CSV header")
	}
	var columns []string
	switch name {
	case "df_Uid_data_hack.csv":
		columns = profileColumns
	case "df_Uid_payment_hack.csv":
		columns = []string{"bundle_id", "row_no", "beneficiary", "payer", "pay_amt", "pay_cnt"}
	default:
		columns = []string{"bundle_id", "row_no", "synthetic_f", "city_name", "rc_name", "synthetic_u"}
	}
	var count int64
	_, err = tx.CopyFrom(ctx, pgx.Identifier{csvTable(name)}, columns, pgx.CopyFromFunc(func() ([]any, error) {
		record, err := r.Read()
		if errors.Is(err, io.EOF) {
			return nil, nil
		}
		if err != nil {
			return nil, fmt.Errorf("CSV row %d: %w", count+2, err)
		}
		count++
		for i := range record {
			record[i] = strings.TrimSpace(record[i])
		}
		if name == "df_Uid_data_hack.csv" {
			if !strings.HasPrefix(record[0], "U_") {
				return nil, fmt.Errorf("CSV row %d: invalid SYNTHETIC_U", count+1)
			}
			out := make([]any, 0, 26)
			out = append(out, id, count, record[0])
			for _, value := range record[1:] {
				out = append(out, nullable(value))
			}
			return out, nil
		}
		if name == "df_Uid_payment_hack.csv" {
			if !strings.HasPrefix(record[0], "U_") || !strings.HasPrefix(record[1], "U_") {
				return nil, fmt.Errorf("CSV row %d: invalid company ID", count+1)
			}
			var amount pgtype.Numeric
			if err := amount.Scan(record[2]); err != nil || !amount.Valid || amount.NaN || amount.InfinityModifier != 0 {
				return nil, fmt.Errorf("CSV row %d: invalid PAY_AMT", count+1)
			}
			payCount, err := strconv.ParseInt(record[3], 10, 64)
			if err != nil || payCount < 0 {
				return nil, fmt.Errorf("CSV row %d: invalid PAY_CNT", count+1)
			}
			return []any{id, count, record[0], record[1], amount, payCount}, nil
		}
		if !strings.HasPrefix(record[0], "F_") || !strings.HasPrefix(record[3], "U_") {
			return nil, fmt.Errorf("CSV row %d: invalid person/company ID", count+1)
		}
		return []any{id, count, record[0], nullable(record[1]), nullable(record[2]), record[3]}, nil
	}))
	if err != nil {
		return 0, err
	}
	if count == 0 {
		return 0, fmt.Errorf("CSV contains no rows")
	}
	return count, nil
}

func importPDF(ctx context.Context, tx pgx.Tx, id, path string, file File, store rawartifacts.Store) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	storageID := uuid.NewSHA1(uuid.NameSpaceURL, []byte("alfa-hackathon-reference-pdfs")).String()
	artifact, err := store.Prepare(ctx, storageID, "application/pdf", data)
	if err != nil {
		return err
	}
	if artifact.SHA256 != file.SHA256 || artifact.Size != file.SizeBytes {
		return fmt.Errorf("PDF changed during import")
	}
	textBytes, err := os.ReadFile(textPath(path))
	if err != nil || !utf8.Valid(textBytes) || len(strings.TrimSpace(string(textBytes))) == 0 {
		return fmt.Errorf("provided TXT is missing, empty or not UTF-8")
	}
	textSum := sha256.Sum256(textBytes)
	if hex.EncodeToString(textSum[:]) != file.TextSHA256 {
		return fmt.Errorf("provided TXT changed during import")
	}
	pages, err := reportPages(string(textBytes))
	if err != nil {
		return err
	}
	issues := []string{}
	count, err := pdfPageCount(data)
	if err != nil {
		return err
	}
	if len(pages) != count {
		issues = append(issues, fmt.Sprintf("PAGE_COUNT_MISMATCH: TXT contains %d pages; PDF contains %d", len(pages), count))
	}
	issueJSON, err := json.Marshal(issues)
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `INSERT INTO dataset_bank_reports
		(bundle_id,name,sha256,size_bytes,content,object_uri,body_text,text_status,text_sha256,text_issues)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10::jsonb)`, id, file.Name, file.SHA256, file.SizeBytes,
		artifact.Content, nullable(artifact.ObjectURI), string(textBytes), "PROVIDED", file.TextSHA256, issueJSON)
	return err
}

func nullable(value string) any {
	if value == "" {
		return nil
	}
	return value
}

func equal(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func textPath(path string) string { return strings.TrimSuffix(path, filepath.Ext(path)) + ".txt" }

func pdfPageCount(data []byte) (count int, err error) {
	defer func() {
		if recover() != nil {
			count = 0
			err = fmt.Errorf("invalid reference PDF")
		}
	}()
	reader, err := pdf.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return 0, err
	}
	count = reader.NumPage()
	if count < 1 {
		return 0, fmt.Errorf("reference PDF has no pages")
	}
	return count, nil
}
