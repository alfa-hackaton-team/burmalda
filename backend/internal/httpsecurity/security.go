package httpsecurity

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/alfa-hackaton-team/burmalda/backend/internal/authn"
	"github.com/alfa-hackaton-team/burmalda/backend/internal/httpx"
)

const (
	csrfCookieName = "csrf_token"
	csrfHeaderName = "X-CSRF-Token"
)

var (
	preflightMethods = map[string]bool{
		http.MethodGet:   true,
		http.MethodPost:  true,
		http.MethodPatch: true,
	}
	preflightHeaders = map[string]bool{
		"content-type":    true,
		"idempotency-key": true,
		"if-match":        true,
		"x-csrf-token":    true,
	}
)

type OriginAllowlist map[string]struct{}

type Options struct {
	RequestTimeout time.Duration
	MaxBodyBytes   int64
	AllowedOrigins OriginAllowlist
}

func ParseOriginAllowlist(value string) (OriginAllowlist, error) {
	allowlist := OriginAllowlist{}
	for _, raw := range strings.Split(value, ",") {
		origin := strings.TrimSpace(raw)
		if origin == "" {
			continue
		}
		if origin == "*" || origin == "null" {
			return nil, fmt.Errorf("API_ALLOWED_ORIGINS must contain exact http(s) origins")
		}
		parsed, err := url.Parse(origin)
		if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") ||
			parsed.Host == "" || parsed.User != nil || parsed.Path != "" || parsed.RawPath != "" ||
			parsed.RawQuery != "" || parsed.Fragment != "" || parsed.Opaque != "" ||
			origin != parsed.Scheme+"://"+parsed.Host {
			return nil, fmt.Errorf("API_ALLOWED_ORIGINS contains invalid origin %q", origin)
		}
		if _, duplicate := allowlist[origin]; duplicate {
			return nil, fmt.Errorf("API_ALLOWED_ORIGINS contains duplicate origin %q", origin)
		}
		allowlist[origin] = struct{}{}
	}
	if len(allowlist) == 0 {
		return nil, fmt.Errorf("API_ALLOWED_ORIGINS must contain at least one origin")
	}
	return allowlist, nil
}

// Protect applies limits and CORS before authentication. Preflight requests do
// not carry a session cookie, so they must finish before the auth middleware.
func Protect(next http.Handler, options Options) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		if len(r.Header.Values("Origin")) > 0 {
			addVary(w.Header(), "Origin")
		}
		origin, originAllowed := allowedRequestOrigin(r, options.AllowedOrigins)
		if originAllowed {
			setCORSResponseHeaders(w.Header(), origin)
		}
		if isPreflight(r) {
			addVary(w.Header(), "Access-Control-Request-Method")
			addVary(w.Header(), "Access-Control-Request-Headers")
			handlePreflight(w, r, origin, originAllowed)
			return
		}
		if r.ContentLength > options.MaxBodyBytes {
			httpx.WriteError(w, http.StatusRequestEntityTooLarge, "PAYLOAD_TOO_LARGE", "request body is too large", false)
			return
		}
		if r.Body != nil {
			r.Body = http.MaxBytesReader(w, r.Body, options.MaxBodyBytes)
		}
		ctx, cancel := context.WithTimeout(r.Context(), options.RequestTimeout)
		defer cancel()
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// RequireMutationProof runs after authentication so an absent session remains
// a 401. Mutating requests must prove both their browser origin and possession
// of the CSRF token bound to the authenticated server-side session.
func RequireMutationProof(next http.Handler, allowedOrigins OriginAllowlist) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if isSafeMethod(r.Method) {
			next.ServeHTTP(w, r)
			return
		}
		principal, ok := authn.PrincipalFromContext(r.Context())
		if !ok {
			httpx.WriteError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "internal error", false)
			return
		}
		if _, ok := allowedRequestOrigin(r, allowedOrigins); !ok || !validCSRF(r, principal.CSRFTokenHash) {
			httpx.WriteError(w, http.StatusForbidden, "FORBIDDEN", "request protection check failed", false)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func allowedRequestOrigin(r *http.Request, allowlist OriginAllowlist) (string, bool) {
	values := r.Header.Values("Origin")
	if len(values) != 1 || values[0] == "" {
		return "", false
	}
	_, ok := allowlist[values[0]]
	return values[0], ok
}

func validCSRF(r *http.Request, expectedHash [sha256.Size]byte) bool {
	cookies := r.CookiesNamed(csrfCookieName)
	headers := r.Header.Values(csrfHeaderName)
	if len(cookies) != 1 || len(headers) != 1 || len(cookies[0].Value) != base64.RawURLEncoding.EncodedLen(sha256.Size) || len(headers[0]) != len(cookies[0].Value) {
		return false
	}
	if subtle.ConstantTimeCompare([]byte(cookies[0].Value), []byte(headers[0])) != 1 {
		return false
	}
	token, err := base64.RawURLEncoding.DecodeString(headers[0])
	if err != nil || len(token) != sha256.Size {
		return false
	}
	actualHash := sha256.Sum256(token)
	return subtle.ConstantTimeCompare(actualHash[:], expectedHash[:]) == 1
}

func isSafeMethod(method string) bool {
	return method == http.MethodGet || method == http.MethodHead || method == http.MethodOptions
}

func isPreflight(r *http.Request) bool {
	return r.Method == http.MethodOptions && r.Header.Get("Access-Control-Request-Method") != ""
}

func handlePreflight(w http.ResponseWriter, r *http.Request, origin string, originAllowed bool) {
	methodValues := r.Header.Values("Access-Control-Request-Method")
	if !originAllowed || origin == "" || len(methodValues) != 1 || !preflightMethods[methodValues[0]] || !validPreflightHeaders(r.Header.Values("Access-Control-Request-Headers")) {
		httpx.WriteError(w, http.StatusForbidden, "FORBIDDEN", "cross-origin request is not allowed", false)
		return
	}
	w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PATCH")
	w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Idempotency-Key, If-Match, X-CSRF-Token")
	w.Header().Set("Access-Control-Max-Age", "600")
	w.WriteHeader(http.StatusNoContent)
}

func validPreflightHeaders(values []string) bool {
	if len(values) > 1 {
		return false
	}
	if len(values) == 0 || strings.TrimSpace(values[0]) == "" {
		return true
	}
	for _, value := range strings.Split(values[0], ",") {
		if !preflightHeaders[strings.ToLower(strings.TrimSpace(value))] {
			return false
		}
	}
	return true
}

func setCORSResponseHeaders(header http.Header, origin string) {
	header.Set("Access-Control-Allow-Origin", origin)
	header.Set("Access-Control-Allow-Credentials", "true")
	header.Set("Access-Control-Expose-Headers", "ETag, Location")
	addVary(header, "Origin")
}

func addVary(header http.Header, value string) {
	for _, line := range header.Values("Vary") {
		for _, existing := range strings.Split(line, ",") {
			if strings.EqualFold(strings.TrimSpace(existing), value) {
				return
			}
		}
	}
	header.Add("Vary", value)
}
