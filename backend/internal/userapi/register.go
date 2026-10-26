package userapi

import (
	"crypto/pbkdf2"
	"crypto/rand"
	"crypto/sha256"
	"errors"
	"net"
	"net/http"
	"regexp"
	"strings"
	"unicode/utf8"

	"github.com/alfa-hackaton-team/burmalda/backend/internal/httpx"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

var localLoginPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9._@-]{2,199}$`)
var syntheticCompanyPattern = regexp.MustCompile(`^U_[A-Z0-9_-]{1,60}$`)

type registrationInput struct {
	Login       string `json:"login"`
	Password    string `json:"password"`
	DisplayName string `json:"display_name"`
	CompanyUID  string `json:"company_uid"`
	RoleCode    string `json:"role_code"`
}

func (input *registrationInput) normalize() error {
	input.Login = strings.ToLower(strings.TrimSpace(input.Login))
	input.DisplayName = strings.TrimSpace(input.DisplayName)
	input.CompanyUID = strings.ToUpper(strings.TrimSpace(input.CompanyUID))
	input.RoleCode = strings.TrimSpace(input.RoleCode)
	if !localLoginPattern.MatchString(input.Login) || utf8.RuneCountInString(input.DisplayName) < 2 || utf8.RuneCountInString(input.DisplayName) > 200 || len(input.Password) < 12 || len(input.Password) > 1024 || !syntheticCompanyPattern.MatchString(input.CompanyUID) {
		return fail(400, "VALIDATION_ERROR", "invalid registration fields")
	}
	switch input.RoleCode {
	case "owner", "ceo", "cfo", "chief_accountant", "treasury", "other":
		return nil
	default:
		return fail(400, "VALIDATION_ERROR", "invalid role")
	}
}

func (s *PostgresStore) RegistrationCompanyHandler(enabled bool) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			methodNotAllowed(http.MethodGet+", "+http.MethodHead)(w, r)
			return
		}
		if !enabled {
			httpx.WriteError(w, 404, "NOT_FOUND", "resource not found", false)
			return
		}
		if err := query(r, "uid"); err != nil {
			productError(w, r, err)
			return
		}
		uid := strings.ToUpper(strings.TrimSpace(r.URL.Query().Get("uid")))
		if !syntheticCompanyPattern.MatchString(uid) {
			httpx.WriteError(w, 400, "VALIDATION_ERROR", "invalid company ID", false)
			return
		}
		host, _, _ := net.SplitHostPort(r.RemoteAddr)
		if err := s.limit(r.Context(), host, "registration-lookup-ip", 120); err != nil {
			productError(w, r, err)
			return
		}
		var industry, city, region string
		err := s.pool.QueryRow(r.Context(), `SELECT COALESCE(mainokved_name,''),COALESCE(city,''),COALESCE(region,'') FROM active_dataset_company_profiles WHERE synthetic_u=$1`, uid).Scan(&industry, &city, &region)
		if errors.Is(err, pgx.ErrNoRows) {
			httpx.WriteError(w, 404, "NOT_FOUND", "company is not in the active dataset", false)
			return
		}
		if err != nil {
			productError(w, r, err)
			return
		}
		httpx.WriteJSON(w, 200, object{"company_uid": uid, "display_name": "Компания " + uid, "industry": industry, "city": city, "region": region})
	})
}

func (s *PostgresStore) RegisterHandler(origins map[string]struct{}, enabled bool) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			methodNotAllowed(http.MethodPost)(w, r)
			return
		}
		if !enabled {
			httpx.WriteError(w, 404, "NOT_FOUND", "resource not found", false)
			return
		}
		origin := r.Header.Values("Origin")
		if len(origin) != 1 {
			httpx.WriteError(w, 403, "FORBIDDEN", "origin is required", false)
			return
		}
		if _, ok := origins[origin[0]]; !ok {
			httpx.WriteError(w, 403, "FORBIDDEN", "origin is not allowed", false)
			return
		}
		if err := query(r); err != nil {
			productError(w, r, err)
			return
		}
		var input registrationInput
		if err := readInput(r, &input); err != nil {
			productError(w, r, err)
			return
		}
		if err := input.normalize(); err != nil {
			productError(w, r, err)
			return
		}
		host, _, _ := net.SplitHostPort(r.RemoteAddr)
		if err := s.limit(r.Context(), host, "registration-ip", 10); err != nil {
			productError(w, r, err)
			return
		}
		if err := s.limit(r.Context(), input.Login, "registration-login", 3); err != nil {
			productError(w, r, err)
			return
		}
		salt := make([]byte, 32)
		if _, err := rand.Read(salt); err != nil {
			productError(w, r, err)
			return
		}
		hash, err := pbkdf2.Key(sha256.New, input.Password, salt, 600000, 32)
		if err != nil {
			productError(w, r, err)
			return
		}
		tx, err := s.pool.Begin(r.Context())
		if err != nil {
			productError(w, r, err)
			return
		}
		defer tx.Rollback(r.Context())
		var companyID, profileID, userID string
		var roleEnabled bool
		err = tx.QueryRow(r.Context(), `SELECT EXISTS(SELECT 1 FROM roles WHERE code=$1 AND enabled)`, input.RoleCode).Scan(&roleEnabled)
		if err != nil {
			productError(w, r, err)
			return
		}
		if !roleEnabled {
			httpx.WriteError(w, 422, "VALIDATION_ERROR", "role is not available", false)
			return
		}
		err = tx.QueryRow(r.Context(), `INSERT INTO companies(synthetic_uid,org_type,main_okved_code,main_okved_name,region,city,raw_profile) SELECT synthetic_u,org_type,mainokved_code,mainokved_name,region,city,to_jsonb(p) FROM active_dataset_company_profiles p WHERE synthetic_u=$1 ON CONFLICT(synthetic_uid) DO UPDATE SET synthetic_uid=EXCLUDED.synthetic_uid RETURNING id::text`, input.CompanyUID).Scan(&companyID)
		if errors.Is(err, pgx.ErrNoRows) {
			httpx.WriteError(w, 422, "VALIDATION_ERROR", "company is not in the active dataset", false)
			return
		}
		if err == nil {
			err = tx.QueryRow(r.Context(), `SELECT id::text FROM business_profiles WHERE company_id=$1 AND status IN ('active','draft') ORDER BY (status='active') DESC,version DESC LIMIT 1`, companyID).Scan(&profileID)
			if errors.Is(err, pgx.ErrNoRows) {
				err = tx.QueryRow(r.Context(), `INSERT INTO business_profiles(subject_type,company_id,version,status,profile_data) SELECT 'company',id,COALESCE((SELECT max(version)+1 FROM business_profiles WHERE company_id=$1),1),'draft',raw_profile FROM companies WHERE id=$1 RETURNING id::text`, companyID).Scan(&profileID)
			}
		}
		if err == nil {
			var imported []byte
			err = tx.QueryRow(r.Context(), `SELECT raw_profile FROM companies WHERE id=$1`, companyID).Scan(&imported)
			if err == nil {
				var data object
				data, err = importedProfile(companyID, decode(imported))
				if err == nil {
					_, err = tx.Exec(r.Context(), `UPDATE business_profiles SET status='active',profile_data=$2
 WHERE id=$1 AND status='draft'`, profileID, raw(data))
				}
			}
		}
		if err == nil {
			err = tx.QueryRow(r.Context(), `INSERT INTO users(identity_issuer,identity_subject,display_name) VALUES('local-password',$1,$2) RETURNING id::text`, input.Login, input.DisplayName).Scan(&userID)
		}
		if err == nil {
			_, err = tx.Exec(r.Context(), `INSERT INTO user_passwords(user_id,login,salt,password_hash,iterations) VALUES($1,$2,$3,$4,600000)`, userID, input.Login, salt, hash)
		}
		if err == nil {
			_, err = tx.Exec(r.Context(), `INSERT INTO business_contexts(subject_type,company_id,profile_id,role_code) VALUES('company',$1,$2,$3) ON CONFLICT(company_id,profile_id,role_code) WHERE company_id IS NOT NULL DO NOTHING`, companyID, profileID, input.RoleCode)
		}
		if err == nil {
			_, err = tx.Exec(r.Context(), `INSERT INTO context_access_grants(user_id,company_id,role_code) VALUES($1,$2,$3)`, userID, companyID, input.RoleCode)
		}
		if err == nil {
			err = tx.Commit(r.Context())
		}
		if err != nil {
			var pgErr *pgconn.PgError
			if errors.As(err, &pgErr) && pgErr.Code == "23505" && (pgErr.ConstraintName == "users_identity_unique" || pgErr.ConstraintName == "user_passwords_login_key") {
				httpx.WriteError(w, 409, "ALREADY_EXISTS", "login is already registered", false)
				return
			}
			productError(w, r, err)
			return
		}
		httpx.WriteJSON(w, 201, object{"user_id": userID})
	})
}
