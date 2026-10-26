package userapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"
)

func TestRegistrationInputValidation(t *testing.T) {
	valid := registrationInput{Login: " User.Test ", Password: "long-enough-password", DisplayName: " Мария Тестовая ", CompanyUID: "u_80", RoleCode: "cfo"}
	if err := valid.normalize(); err != nil || valid.Login != "user.test" || valid.CompanyUID != "U_80" {
		t.Fatalf("normalize: %#v %v", valid, err)
	}
	for _, change := range []func(*registrationInput){
		func(v *registrationInput) { v.Password = "short" },
		func(v *registrationInput) { v.CompanyUID = "../../../etc" },
		func(v *registrationInput) { v.RoleCode = "admin" },
		func(v *registrationInput) { v.Login = "x" },
	} {
		input := valid
		change(&input)
		if err := input.normalize(); err == nil {
			t.Fatalf("accepted invalid input: %#v", input)
		}
	}
}

func TestSyntheticRegistrationPostgres(t *testing.T) {
	pool := integrationPool(t)
	ctx := context.Background()
	for _, statement := range []string{
		`INSERT INTO dataset_bundles(id,manifest_sha256) VALUES('registration-test',repeat('a',64))`,
		`INSERT INTO active_dataset_bundle(singleton,bundle_id) VALUES(true,'registration-test')`,
		`INSERT INTO dataset_normalization_runs(bundle_id,policy_version,profile_conflict_keys,profile_duplicate_rows,payment_duplicate_rows,person_link_conflict_pairs,person_link_duplicate_rows) VALUES('registration-test',1,0,0,0,0,0)`,
		`INSERT INTO dataset_company_profiles(bundle_id,row_no,synthetic_u,mainokved_name,city) VALUES('registration-test',1,'U_80','Обработка металлов','Екатеринбург')`,
	} {
		if _, err := pool.Exec(ctx, statement); err != nil {
			t.Fatal(err)
		}
	}
	store := NewPostgresStore(pool)
	lookup := store.RegistrationCompanyHandler(true)
	lookupResponse := httptest.NewRecorder()
	lookup.ServeHTTP(lookupResponse, httptest.NewRequest(http.MethodGet, "/v0.1/auth/registration-company?uid=U_80", nil))
	if lookupResponse.Code != 200 || !strings.Contains(lookupResponse.Body.String(), "Екатеринбург") {
		t.Fatalf("lookup: %d %s", lookupResponse.Code, lookupResponse.Body.String())
	}
	register := store.RegisterHandler(testAllowedOrigins, true)
	input := registrationInput{Login: "new-user", Password: "strong-password-for-test", DisplayName: "Мария Тестовая", CompanyUID: "U_80", RoleCode: "cfo"}
	request := func(origin string) *httptest.ResponseRecorder {
		body, _ := json.Marshal(input)
		r := httptest.NewRequest(http.MethodPost, "/v0.1/auth/register", strings.NewReader(string(body)))
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("Origin", origin)
		w := httptest.NewRecorder()
		register.ServeHTTP(w, r)
		return w
	}
	if w := request("https://evil.example"); w.Code != 403 {
		t.Fatalf("untrusted origin: %d", w.Code)
	}
	w := request("https://app.example")
	if w.Code != 201 {
		t.Fatalf("register: %d %s", w.Code, w.Body.String())
	}
	var result struct {
		UserID string `json:"user_id"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil || uuid.Validate(result.UserID) != nil {
		t.Fatalf("invalid registration result: %s %v", w.Body.String(), err)
	}
	if w = request("https://app.example"); w.Code != 409 {
		t.Fatalf("duplicate login: %d %s", w.Code, w.Body.String())
	}
	login := store.LoginHandler(testAllowedOrigins, true, false)
	lr := httptest.NewRequest(http.MethodPost, "/v0.1/auth/login", strings.NewReader(`{"login":"new-user","password":"strong-password-for-test"}`))
	lr.Header.Set("Content-Type", "application/json")
	lr.Header.Set("Origin", "https://app.example")
	lw := httptest.NewRecorder()
	login.ServeHTTP(lw, lr)
	if lw.Code != 200 {
		t.Fatalf("login: %d %s", lw.Code, lw.Body.String())
	}
	contexts, err := store.ListContexts(ctx, result.UserID, "", 10)
	if err != nil || len(contexts) != 1 || contexts[0].RoleID != "cfo" {
		t.Fatalf("contexts: %+v %v", contexts, err)
	}
	c, err := store.context(ctx, result.UserID, contexts[0].Context.ContextID)
	if err != nil {
		t.Fatal(err)
	}
	if err := requireProfile(c); err != nil {
		t.Fatalf("registration must create a usable imported profile: %v", err)
	}
	if len(array(c["profile_features"])) == 0 {
		t.Fatal("imported profile must retain field provenance")
	}
}
