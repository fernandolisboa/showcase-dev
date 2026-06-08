package auth

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestGitHubProviderAuthURL(t *testing.T) {
	p := NewGitHubProvider("client123", "secret", "https://showcase.dev/auth/github/callback")
	got := p.AuthURL("st4te")
	for _, want := range []string{
		"https://github.com/login/oauth/authorize?",
		"client_id=client123",
		"redirect_uri=https%3A%2F%2Fshowcase.dev%2Fauth%2Fgithub%2Fcallback",
		"state=st4te",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("AuthURL missing %q in %q", want, got)
		}
	}
}

func TestGitHubProviderExchange(t *testing.T) {
	var gotCode, gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/login/oauth/access_token":
			_ = r.ParseForm()
			gotCode = r.Form.Get("code")
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"access_token":"tok-abc","token_type":"bearer"}`))
		case "/user":
			gotAuth = r.Header.Get("Authorization")
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"id":4242,"login":"octocat"}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	p := NewGitHubProvider("id", "secret", "cb")
	p.authBase = srv.URL
	p.apiBase = srv.URL

	user, err := p.Exchange(context.Background(), "the-code")
	if err != nil {
		t.Fatalf("Exchange: %v", err)
	}
	if user.ID != 4242 || user.Login != "octocat" {
		t.Errorf("user = %+v, want {4242 octocat}", user)
	}
	if gotCode != "the-code" {
		t.Errorf("token endpoint got code %q, want the-code", gotCode)
	}
	if gotAuth != "Bearer tok-abc" {
		t.Errorf("user endpoint Authorization = %q, want Bearer tok-abc", gotAuth)
	}
}

func TestGitHubProviderExchangeRejectsOAuthError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"error":"bad_verification_code","error_description":"expired"}`))
	}))
	defer srv.Close()

	p := NewGitHubProvider("id", "secret", "cb")
	p.authBase = srv.URL
	p.apiBase = srv.URL

	if _, err := p.Exchange(context.Background(), "stale"); err == nil {
		t.Fatal("expected an error when GitHub returns an OAuth error")
	}
}
