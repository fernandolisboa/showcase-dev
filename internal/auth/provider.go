// Package auth implements Owner sign-in via the GitHub App's user-to-server
// OAuth flow (ADR-0008) and the web login session that follows it. Identity is
// the only thing taken from GitHub: the user's stable numeric id (which survives
// a handle rename) and current login; the GitHub access token is used once to
// read those and then discarded — it is never stored (ADR-0008). The login
// session itself is a server-side, DB-backed opaque token; only its hash is kept.
package auth

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// GitHubUser is the minimal identity read at sign-in.
type GitHubUser struct {
	ID    int64
	Login string
}

// IdentityProvider abstracts the GitHub OAuth flow so handlers are testable with
// a fake (no real GitHub App needed in tests).
type IdentityProvider interface {
	// AuthURL is the GitHub authorization URL to redirect the user to, carrying an
	// anti-CSRF state the caller will verify on the callback.
	AuthURL(state string) string
	// Exchange swaps the callback code for the authenticated GitHub user. The
	// access token is used only to read the identity and is then discarded.
	Exchange(ctx context.Context, code string) (GitHubUser, error)
}

// GitHubProvider is the real IdentityProvider against github.com. Base URLs are
// fields (not constants) so tests can point them at an httptest server.
type GitHubProvider struct {
	clientID     string
	clientSecret string
	callbackURL  string
	authBase     string // default https://github.com
	apiBase      string // default https://api.github.com
	httpClient   *http.Client
}

// NewGitHubProvider builds the real provider from the GitHub App's user-to-server
// OAuth credentials (ADR-0008): Client ID + Client Secret (NOT the app private
// key, which is for installation tokens, a later slice).
func NewGitHubProvider(clientID, clientSecret, callbackURL string) *GitHubProvider {
	return &GitHubProvider{
		clientID:     clientID,
		clientSecret: clientSecret,
		callbackURL:  callbackURL,
		authBase:     "https://github.com",
		apiBase:      "https://api.github.com",
		httpClient:   &http.Client{Timeout: 10 * time.Second},
	}
}

func (p *GitHubProvider) AuthURL(state string) string {
	v := url.Values{}
	v.Set("client_id", p.clientID)
	v.Set("redirect_uri", p.callbackURL)
	v.Set("state", state)
	return p.authBase + "/login/oauth/authorize?" + v.Encode()
}

func (p *GitHubProvider) Exchange(ctx context.Context, code string) (GitHubUser, error) {
	token, err := p.exchangeCode(ctx, code)
	if err != nil {
		return GitHubUser{}, err
	}
	return p.fetchUser(ctx, token)
}

// exchangeCode trades the authorization code for a user access token.
func (p *GitHubProvider) exchangeCode(ctx context.Context, code string) (string, error) {
	form := url.Values{}
	form.Set("client_id", p.clientID)
	form.Set("client_secret", p.clientSecret)
	form.Set("code", code)
	form.Set("redirect_uri", p.callbackURL)

	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		p.authBase+"/login/oauth/access_token", strings.NewReader(form.Encode()))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")

	resp, err := p.httpClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("github token exchange: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("github token exchange: status %d", resp.StatusCode)
	}
	var body struct {
		AccessToken      string `json:"access_token"`
		Error            string `json:"error"`
		ErrorDescription string `json:"error_description"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return "", fmt.Errorf("decode github token response: %w", err)
	}
	if body.Error != "" {
		return "", fmt.Errorf("github token exchange: %s (%s)", body.Error, body.ErrorDescription)
	}
	if body.AccessToken == "" {
		return "", fmt.Errorf("github token exchange: empty access token")
	}
	return body.AccessToken, nil
}

// fetchUser reads the authenticated user's stable id + login, then drops the token.
func (p *GitHubProvider) fetchUser(ctx context.Context, token string) (GitHubUser, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, p.apiBase+"/user", nil)
	if err != nil {
		return GitHubUser{}, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/vnd.github+json")

	resp, err := p.httpClient.Do(req)
	if err != nil {
		return GitHubUser{}, fmt.Errorf("github user fetch: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return GitHubUser{}, fmt.Errorf("github user fetch: status %d", resp.StatusCode)
	}
	var u struct {
		ID    int64  `json:"id"`
		Login string `json:"login"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&u); err != nil {
		return GitHubUser{}, fmt.Errorf("decode github user: %w", err)
	}
	if u.ID == 0 {
		return GitHubUser{}, fmt.Errorf("github user fetch: missing id")
	}
	return GitHubUser{ID: u.ID, Login: u.Login}, nil
}
