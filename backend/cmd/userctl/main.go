package main

import (
	"bufio"
	"context"
	"crypto/pbkdf2"
	"crypto/rand"
	"crypto/sha256"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"strings"

	"github.com/alfa-hackaton-team/burmalda/backend/internal/apicontract"
	"github.com/alfa-hackaton-team/burmalda/backend/internal/config"
	"github.com/alfa-hackaton-team/burmalda/backend/internal/database"
	"github.com/google/uuid"
)

func main() {
	if e := run(); e != nil {
		fmt.Fprintln(os.Stderr, e)
		os.Exit(1)
	}
}
func run() error {
	login := flag.String("login", "", "local login")
	display := flag.String("name", "", "display name")
	company := flag.String("company-id", "", "existing company UUID")
	companyUID := flag.String("company-uid", "", "normalized imported company key")
	permissions := flag.String("permissions", "", "comma-separated administrator permissions")
	role := flag.String("role", "owner", "role code")
	profileFile := flag.String("profile-file", "", "flat profile data JSON with optional profile_features")
	flag.Parse()
	if *login == "" || len(*login) > 200 || (*company == "" && *companyUID == "") {
		return fmt.Errorf("--login and either --company-id or --company-uid are required; password is read from stdin")
	}
	if _, e := uuid.Parse(*company); *company != "" && e != nil {
		return fmt.Errorf("invalid company ID")
	}
	scanner := bufio.NewScanner(os.Stdin)
	if !scanner.Scan() {
		return fmt.Errorf("password is required on stdin")
	}
	password := scanner.Text()
	if len(password) < 12 || len(password) > 1024 {
		return fmt.Errorf("password must contain 12 to 1024 bytes")
	}
	cfg, e := config.Load()
	if e != nil {
		return e
	}
	ctx := context.Background()
	pool, e := database.Open(ctx, cfg)
	if e != nil {
		return e
	}
	defer pool.Close()
	salt := make([]byte, 32)
	if _, e = rand.Read(salt); e != nil {
		return e
	}
	hash, e := pbkdf2.Key(sha256.New, password, salt, 600000, 32)
	if e != nil {
		return e
	}
	tx, e := pool.Begin(ctx)
	if e != nil {
		return e
	}
	defer tx.Rollback(ctx)
	if *company == "" {
		e = tx.QueryRow(ctx, `INSERT INTO companies(synthetic_uid,org_type,main_okved_code,main_okved_name,region,city,raw_profile) SELECT synthetic_u,org_type,mainokved_code,mainokved_name,region,city,to_jsonb(p) FROM active_dataset_company_profiles p WHERE synthetic_u=$1 ON CONFLICT(synthetic_uid) DO UPDATE SET synthetic_uid=EXCLUDED.synthetic_uid RETURNING id::text`, *companyUID).Scan(company)
		if e != nil {
			return fmt.Errorf("company must exist in the active normalized dataset: %w", e)
		}
	}
	var user string
	e = tx.QueryRow(ctx, `INSERT INTO users(identity_issuer,identity_subject,display_name) VALUES('local-password',$1,NULLIF($2,'')) ON CONFLICT(identity_issuer,identity_subject) DO UPDATE SET display_name=EXCLUDED.display_name RETURNING id::text`, strings.ToLower(*login), *display).Scan(&user)
	if e != nil {
		return e
	}
	_, e = tx.Exec(ctx, `INSERT INTO user_passwords(user_id,login,salt,password_hash,iterations) VALUES($1,$2,$3,$4,600000) ON CONFLICT(user_id) DO UPDATE SET salt=EXCLUDED.salt,password_hash=EXCLUDED.password_hash,updated_at=now()`, user, strings.ToLower(*login), salt, hash)
	if e != nil {
		return e
	}
	_, e = tx.Exec(ctx, `UPDATE user_sessions SET revoked_at=now() WHERE user_id=$1 AND revoked_at IS NULL`, user)
	if e != nil {
		return e
	}
	var profile string
	if *profileFile != "" {
		b, err := os.ReadFile(*profileFile)
		if err != nil {
			return err
		}
		var data map[string]any
		if err = json.Unmarshal(b, &data); err != nil || data == nil {
			return fmt.Errorf("invalid profile JSON")
		}
		if features, exists := data["profile_features"]; exists {
			list, ok := features.([]any)
			if !ok {
				return fmt.Errorf("profile_features must be an array")
			}
			for _, feature := range list {
				if err := apicontract.Validate("ml", "ProfileFeature", feature); err != nil {
					return fmt.Errorf("invalid profile feature: %w", err)
				}
			}
		}
		if _, e = tx.Exec(ctx, `UPDATE business_profiles SET status='superseded' WHERE company_id=$1 AND status IN ('active','draft')`, *company); e != nil {
			return e
		}
		e = tx.QueryRow(ctx, `INSERT INTO business_profiles(subject_type,company_id,version,status,profile_data) SELECT 'company',id,COALESCE((SELECT max(version)+1 FROM business_profiles WHERE company_id=$1),1),'active',$2 FROM companies WHERE id=$1 RETURNING id::text`, *company, b).Scan(&profile)
		if e != nil {
			return e
		}
	} else {
		e = tx.QueryRow(ctx, `SELECT id::text FROM business_profiles WHERE company_id=$1 AND status IN ('active','draft') ORDER BY (status='active') DESC,version DESC LIMIT 1`, *company).Scan(&profile)
		if e != nil {
			e = tx.QueryRow(ctx, `INSERT INTO business_profiles(subject_type,company_id,version,status,profile_data) SELECT 'company',id,COALESCE((SELECT max(version)+1 FROM business_profiles WHERE company_id=$1),1),'draft',raw_profile FROM companies WHERE id=$1 RETURNING id::text`, *company).Scan(&profile)
			if e != nil {
				return e
			}
		}
	}
	var contextID string
	e = tx.QueryRow(ctx, `INSERT INTO business_contexts(subject_type,company_id,profile_id,role_code) VALUES('company',$1,$2,$3) ON CONFLICT(company_id,profile_id,role_code) WHERE company_id IS NOT NULL DO UPDATE SET role_code=EXCLUDED.role_code RETURNING id::text`, *company, profile, *role).Scan(&contextID)
	if e != nil {
		return e
	}
	_, e = tx.Exec(ctx, `UPDATE context_access_grants SET revoked_at=now() WHERE user_id=$1 AND company_id=$2 AND role_code=$3 AND revoked_at IS NULL AND expires_at<=now()`, user, *company, *role)
	if e != nil {
		return e
	}
	_, e = tx.Exec(ctx, `INSERT INTO context_access_grants(user_id,company_id,role_code) VALUES($1,$2,$3) ON CONFLICT(user_id,company_id,role_code) WHERE revoked_at IS NULL AND company_id IS NOT NULL DO NOTHING`, user, *company, *role)
	if e != nil {
		return e
	}
	for _, permission := range strings.Split(*permissions, ",") {
		if permission == "" {
			continue
		}
		if permission != "sources:read" && permission != "sources:manage" && permission != "sources:trust" {
			return fmt.Errorf("unknown administrator permission")
		}
		if _, e = tx.Exec(ctx, `UPDATE admin_permission_grants SET revoked_at=now() WHERE user_id=$1 AND permission=$2 AND revoked_at IS NULL AND expires_at<=now()`, user, permission); e != nil {
			return e
		}
		if _, e = tx.Exec(ctx, `INSERT INTO admin_permission_grants(user_id,permission) VALUES($1,$2) ON CONFLICT(user_id,permission) WHERE revoked_at IS NULL DO NOTHING`, user, permission); e != nil {
			return e
		}
	}
	if e = tx.Commit(ctx); e != nil {
		return e
	}
	return json.NewEncoder(os.Stdout).Encode(map[string]string{"user_id": user, "context_id": contextID})
}
