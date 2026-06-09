package githubapp

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// testKeyPEM generates a throwaway RSA key (never a real secret) as a PKCS#1 PEM.
func testKeyPEM(t *testing.T) string {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	der := x509.MarshalPKCS1PrivateKey(key)
	return string(pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: der}))
}

func TestNewRejectsBadInput(t *testing.T) {
	if _, err := New("123", "not a pem"); err == nil {
		t.Error("expected error for non-PEM key")
	}
	if _, err := New("", testKeyPEM(t)); err == nil {
		t.Error("expected error for empty app id")
	}
	// The PEM-parse error must be static and never echo the key bytes.
	_, err := New("123", "-----BEGIN RSA PRIVATE KEY-----\nSECRETKEYBYTES\n-----END RSA PRIVATE KEY-----")
	if err == nil || strings.Contains(err.Error(), "SECRETKEYBYTES") {
		t.Errorf("PEM error must be static and not echo key bytes: %v", err)
	}
}

func TestInstallationTokenHappyPath(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/repos/octo/app/installation":
			auth := r.Header.Get("Authorization")
			if !strings.HasPrefix(auth, "Bearer ") {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			assertJWTBounds(t, strings.TrimPrefix(auth, "Bearer "))
			_ = json.NewEncoder(w).Encode(map[string]any{"id": 42})
		case r.Method == http.MethodPost && r.URL.Path == "/app/installations/42/access_tokens":
			var body struct {
				Repositories []string          `json:"repositories"`
				Permissions  map[string]string `json:"permissions"`
			}
			_ = json.NewDecoder(r.Body).Decode(&body)
			if len(body.Repositories) != 1 || body.Repositories[0] != "app" {
				t.Errorf("token not scoped to the repo: %+v", body.Repositories)
			}
			if body.Permissions["contents"] != "read" || len(body.Permissions) != 1 {
				t.Errorf("token not least-privilege (want only contents:read): %+v", body.Permissions)
			}
			w.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(w).Encode(map[string]any{"token": "ghs_secret", "expires_at": "2099-01-01T00:00:00Z"})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	c, err := New("123456", testKeyPEM(t))
	if err != nil {
		t.Fatal(err)
	}
	c.apiBase = srv.URL

	tok, exp, err := c.InstallationToken(context.Background(), "octo", "app")
	if err != nil {
		t.Fatalf("InstallationToken: %v", err)
	}
	if tok != "ghs_secret" {
		t.Errorf("token = %q, want ghs_secret", tok)
	}
	if exp.IsZero() {
		t.Error("expiry not parsed")
	}
}

// accessServer mocks the App API for HasRepoAccess: installation lookup, a metadata:read
// token mint, and the collaborator-permission endpoint returning perm (or 404 if perm=="").
func accessServer(t *testing.T, perm string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/repos/acme/app/installation":
			_ = json.NewEncoder(w).Encode(map[string]any{"id": 7})
		case r.URL.Path == "/app/installations/7/access_tokens":
			var body struct {
				Permissions map[string]string `json:"permissions"`
			}
			_ = json.NewDecoder(r.Body).Decode(&body)
			if body.Permissions["metadata"] != "read" || len(body.Permissions) != 1 {
				t.Errorf("access check token not scoped to metadata:read, got %+v", body.Permissions)
			}
			w.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(w).Encode(map[string]any{"token": "ghs_meta", "expires_at": "2099-01-01T00:00:00Z"})
		case r.URL.Path == "/repos/acme/app/collaborators/alice/permission":
			if perm == "" {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"permission": perm})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
}

func TestHasRepoAccess(t *testing.T) {
	cases := []struct {
		name string
		perm string
		want bool
	}{
		{"admin", "admin", true},
		{"write", "write", true},
		{"read", "read", true},
		{"none", "none", false},
		{"not a collaborator (404)", "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv := accessServer(t, tc.perm)
			defer srv.Close()
			c, err := New("123456", testKeyPEM(t))
			if err != nil {
				t.Fatal(err)
			}
			c.apiBase = srv.URL

			got, err := c.HasRepoAccess(context.Background(), "acme", "app", "alice")
			if err != nil {
				t.Fatalf("HasRepoAccess: %v", err)
			}
			if got != tc.want {
				t.Errorf("HasRepoAccess(perm=%q) = %v, want %v", tc.perm, got, tc.want)
			}
		})
	}
}

func TestHasRepoAccessNoInstallation(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound) // installation lookup 404s
	}))
	defer srv.Close()
	c, err := New("123456", testKeyPEM(t))
	if err != nil {
		t.Fatal(err)
	}
	c.apiBase = srv.URL

	if _, err := c.HasRepoAccess(context.Background(), "acme", "app", "alice"); !errors.Is(err, ErrNoInstallation) {
		t.Fatalf("HasRepoAccess without an installation = %v, want ErrNoInstallation", err)
	}
}

func TestInstallationTokenNoInstallation(t *testing.T) {
	// A repo the App is not installed on returns 404 on discovery.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()
	c, _ := New("123456", testKeyPEM(t))
	c.apiBase = srv.URL
	if _, _, err := c.InstallationToken(context.Background(), "victim", "private"); !errors.Is(err, ErrNoInstallation) {
		t.Fatalf("err = %v, want ErrNoInstallation", err)
	}
}

// assertJWTBounds checks the App JWT issuer + that exp-iat stays within GitHub's
// 10-minute cap (a future edit that drifts past it would break token minting).
func assertJWTBounds(t *testing.T, jwt string) {
	t.Helper()
	parts := strings.Split(jwt, ".")
	if len(parts) != 3 {
		t.Fatalf("malformed jwt (%d parts)", len(parts))
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		t.Fatalf("decode jwt payload: %v", err)
	}
	var claims struct {
		Iat int64  `json:"iat"`
		Exp int64  `json:"exp"`
		Iss string `json:"iss"`
	}
	if err := json.Unmarshal(payload, &claims); err != nil {
		t.Fatalf("unmarshal claims: %v", err)
	}
	if claims.Iss != "123456" {
		t.Errorf("iss = %q, want 123456", claims.Iss)
	}
	if claims.Exp <= claims.Iat {
		t.Error("exp must be after iat")
	}
	if claims.Exp-claims.Iat > 600 {
		t.Errorf("exp-iat = %ds, must be <= 600 (GitHub caps App JWTs at 10 min)", claims.Exp-claims.Iat)
	}
}
