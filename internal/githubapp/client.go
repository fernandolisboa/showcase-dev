// Package githubapp mints short-lived GitHub App installation access tokens and
// uses them to clone an Owner's repo at publish (#19, ADR-0008). The App's own
// credentials — a numeric App ID and an RSA private key — sign a short-lived JWT;
// that JWT mints an installation token SCOPED to a single repo with contents:read
// (least privilege). The token lives only in memory, is handed to git via an
// env-provided auth header (never the URL, never argv), and never reaches a build
// context (the clone strips .git). Tokens are never persisted or logged.
package githubapp

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"
)

// ErrNoInstallation is returned when the App is not installed on the requested
// owner/repo. This is the access boundary: a repo an Owner has not granted the App
// (e.g. someone else's private repo) returns this rather than a token.
var ErrNoInstallation = errors.New("githubapp: app not installed on repository")

// Client mints installation tokens for the configured GitHub App.
type Client struct {
	appID   string
	key     *rsa.PrivateKey
	apiBase string // https://api.github.com; overridden by tests in-package
	http    *http.Client
	now     func() time.Time // injectable clock for tests
}

// New parses the App's RSA private key (PKCS#1, falling back to PKCS#8) and returns
// a Client. On a bad key or empty id it returns a STATIC error that never echoes the
// key bytes (the key is a platform secret).
func New(appID, privateKeyPEM string) (*Client, error) {
	if strings.TrimSpace(appID) == "" {
		return nil, errors.New("githubapp: empty GITHUB_APP_ID")
	}
	key, err := parsePrivateKey(privateKeyPEM)
	if err != nil {
		return nil, err
	}
	return &Client{
		appID:   strings.TrimSpace(appID),
		key:     key,
		apiBase: "https://api.github.com",
		http:    &http.Client{Timeout: 15 * time.Second},
		now:     time.Now,
	}, nil
}

func parsePrivateKey(pemStr string) (*rsa.PrivateKey, error) {
	block, _ := pem.Decode([]byte(pemStr))
	if block == nil {
		return nil, errors.New("githubapp: GITHUB_APP_PRIVATE_KEY is not valid PEM")
	}
	if k, err := x509.ParsePKCS1PrivateKey(block.Bytes); err == nil {
		return k, nil
	}
	if k, err := x509.ParsePKCS8PrivateKey(block.Bytes); err == nil {
		if rk, ok := k.(*rsa.PrivateKey); ok {
			return rk, nil
		}
		return nil, errors.New("githubapp: GITHUB_APP_PRIVATE_KEY is not an RSA key")
	}
	return nil, errors.New("githubapp: GITHUB_APP_PRIVATE_KEY could not be parsed")
}

// appJWT mints a short-lived App JWT (RS256). iat is backdated 60s to tolerate clock
// skew; exp is 9 minutes out (GitHub caps App JWTs at 10).
func (c *Client) appJWT() (string, error) {
	now := c.now()
	enc := func(b []byte) string { return base64.RawURLEncoding.EncodeToString(b) }
	header := enc([]byte(`{"alg":"RS256","typ":"JWT"}`))
	payload := enc([]byte(fmt.Sprintf(`{"iat":%d,"exp":%d,"iss":%q}`,
		now.Add(-60*time.Second).Unix(), now.Add(9*time.Minute).Unix(), c.appID)))
	signing := header + "." + payload
	sum := sha256.Sum256([]byte(signing))
	sig, err := rsa.SignPKCS1v15(rand.Reader, c.key, crypto.SHA256, sum[:])
	if err != nil {
		return "", fmt.Errorf("sign app jwt: %w", err)
	}
	return signing + "." + enc(sig), nil
}

// InstallationToken returns a short-lived token scoped to owner/repo with
// contents:read. A repo with no installation for this App yields ErrNoInstallation.
func (c *Client) InstallationToken(ctx context.Context, owner, repo string) (string, time.Time, error) {
	jwt, err := c.appJWT()
	if err != nil {
		return "", time.Time{}, err
	}
	instID, err := c.repoInstallationID(ctx, jwt, owner, repo)
	if err != nil {
		return "", time.Time{}, err
	}
	// Scope the token to just this repo with read-only contents — least privilege.
	body := fmt.Sprintf(`{"repositories":[%q],"permissions":{"contents":"read"}}`, repo)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		fmt.Sprintf("%s/app/installations/%d/access_tokens", c.apiBase, instID), strings.NewReader(body))
	if err != nil {
		return "", time.Time{}, err
	}
	c.setAppHeaders(req, jwt)
	resp, err := c.http.Do(req)
	if err != nil {
		return "", time.Time{}, fmt.Errorf("mint installation token: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		return "", time.Time{}, fmt.Errorf("mint installation token: status %d", resp.StatusCode)
	}
	var out struct {
		Token     string    `json:"token"`
		ExpiresAt time.Time `json:"expires_at"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return "", time.Time{}, fmt.Errorf("decode installation token: %w", err)
	}
	if out.Token == "" {
		return "", time.Time{}, errors.New("mint installation token: empty token")
	}
	return out.Token, out.ExpiresAt, nil
}

// repoInstallationID finds the installation that grants the App access to owner/repo.
func (c *Client) repoInstallationID(ctx context.Context, jwt, owner, repo string) (int64, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		fmt.Sprintf("%s/repos/%s/%s/installation", c.apiBase, owner, repo), nil)
	if err != nil {
		return 0, err
	}
	c.setAppHeaders(req, jwt)
	resp, err := c.http.Do(req)
	if err != nil {
		return 0, fmt.Errorf("find installation: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return 0, ErrNoInstallation
	}
	if resp.StatusCode != http.StatusOK {
		return 0, fmt.Errorf("find installation: status %d", resp.StatusCode)
	}
	var inst struct {
		ID int64 `json:"id"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&inst); err != nil {
		return 0, fmt.Errorf("decode installation: %w", err)
	}
	if inst.ID == 0 {
		return 0, ErrNoInstallation
	}
	return inst.ID, nil
}

func (c *Client) setAppHeaders(req *http.Request, jwt string) {
	req.Header.Set("Authorization", "Bearer "+jwt)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
}
