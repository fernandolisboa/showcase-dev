package auth

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestCanonicalUsername(t *testing.T) {
	ok := map[string]string{
		"octocat":     "octocat",
		"OctoCat":     "octocat", // lowercased
		"  spaced  ":  "spaced",  // trimmed
		"a1":          "a1",
		"my-handle-2": "my-handle-2",
	}
	for in, want := range ok {
		got, err := CanonicalUsername(in)
		if err != nil || got != want {
			t.Errorf("CanonicalUsername(%q) = (%q, %v), want (%q, nil)", in, got, err, want)
		}
	}
	bad := []string{
		"a",                     // too short
		"-lead",                 // leading hyphen
		"trail-",                // trailing hyphen
		"dou--ble",              // double hyphen
		"has space",             // space
		"under_score",           // underscore
		"café",                  // non-ascii
		"login",                 // reserved
		"API",                   // reserved (case-insensitive)
		"dashboard",             // reserved
		strings.Repeat("a", 40), // too long
	}
	for _, in := range bad {
		if got, err := CanonicalUsername(in); err == nil {
			t.Errorf("CanonicalUsername(%q) = %q, want error", in, got)
		}
	}
}

// claim drives POST /api/owner/username through RequireOwner with a session cookie.
func claim(t *testing.T, a *Authenticator, sid *http.Cookie, body string) *httptest.ResponseRecorder {
	t.Helper()
	h := a.RequireOwner(http.HandlerFunc(a.ClaimUsername))
	req := httptest.NewRequest(http.MethodPost, "/api/owner/username", strings.NewReader(body))
	req.AddCookie(sid)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestClaimUsernameHappyPath(t *testing.T) {
	fs := newFakeStore()
	a := New(fakeProvider{user: GitHubUser{ID: 1, Login: "dev"}}, fs, false)
	sid := signIn(t, a)

	rec := claim(t, a, sid, `{"username":"DevHandle"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("claim status = %d, want 200", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "devhandle") {
		t.Errorf("response %q should carry the canonical username", rec.Body.String())
	}
	// Me now reflects it.
	meReq := httptest.NewRequest(http.MethodGet, "/api/owner/me", nil)
	meReq.AddCookie(sid)
	meRec := httptest.NewRecorder()
	a.Me(meRec, meReq)
	if !strings.Contains(meRec.Body.String(), `"username":"devhandle"`) {
		t.Errorf("Me = %q, want canonical username", meRec.Body.String())
	}
}

func TestClaimUsernameRejectsInvalidAndReserved(t *testing.T) {
	fs := newFakeStore()
	a := New(fakeProvider{user: GitHubUser{ID: 2, Login: "dev"}}, fs, false)
	sid := signIn(t, a)

	for _, body := range []string{`{"username":"-bad"}`, `{"username":"login"}`, `{"username":"a"}`} {
		if rec := claim(t, a, sid, body); rec.Code != http.StatusBadRequest {
			t.Errorf("claim %s = %d, want 400", body, rec.Code)
		}
	}
}

func TestClaimUsernameConflict(t *testing.T) {
	fs := newFakeStore()
	a := New(fakeProvider{user: GitHubUser{ID: 10, Login: "first"}}, fs, false)
	first := signIn(t, a)
	if rec := claim(t, a, first, `{"username":"shared"}`); rec.Code != http.StatusOK {
		t.Fatalf("first claim = %d, want 200", rec.Code)
	}

	// A second Owner signs in and tries the same name → 409.
	a2 := New(fakeProvider{user: GitHubUser{ID: 20, Login: "second"}}, fs, false)
	second := signIn(t, a2)
	if rec := claim(t, a2, second, `{"username":"shared"}`); rec.Code != http.StatusConflict {
		t.Errorf("conflicting claim = %d, want 409", rec.Code)
	}
}
