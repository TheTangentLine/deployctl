// Package githubapp gets GitHub App installation tokens.
//
// A GitHub App proves who it is with a private key. To act on a repository
// it needs an installation token, obtained in two hops:
//
//  1. Sign a short-lived JWT (RS256, at most 10 minutes) with the App's
//     private key, with the App ID as issuer.
//  2. POST /app/installations/{id}/access_tokens with that JWT. GitHub
//     returns a token valid for one hour, scoped to the repositories and
//     permissions the installation has.
//
// TokenSource does both, caches the token, and fetches a new one shortly
// before it expires. It is safe for concurrent use.
package githubapp

import (
	"bytes"
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
	"io"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"sync"
	"time"

	"github.com/TheTangentLine/deployctl/internal/steps"
)

// refreshBefore is how long before expiry a cached token is replaced, so a
// clone or push that starts with a token never finishes with an expired one.
const refreshBefore = 5 * time.Minute

// TokenSource returns installation tokens for one GitHub App installation.
type TokenSource struct {
	appID          string
	installationID int64
	key            *rsa.PrivateKey

	// BaseURL is the GitHub API, https://api.github.com by default.
	// GitHub Enterprise uses https://<host>/api/v3. Tests use a fake server.
	BaseURL string
	// HTTPClient is used for API calls; it has a 30-second timeout by default.
	HTTPClient *http.Client
	// Now returns the current time; tests replace it.
	Now func() time.Time

	mu      sync.Mutex
	token   string
	expires time.Time
}

// New returns a TokenSource for the App with appID (the numeric App ID or
// the App's client ID) and the installation with installationID, using the
// App's private key in PEM form, as downloaded from GitHub.
func New(appID string, installationID int64, keyPEM []byte) (*TokenSource, error) {
	if appID == "" || installationID <= 0 {
		return nil, errors.New("githubapp: app ID and installation ID are required")
	}
	key, err := parseKey(keyPEM)
	if err != nil {
		return nil, err
	}
	return &TokenSource{
		appID:          appID,
		installationID: installationID,
		key:            key,
		BaseURL:        "https://api.github.com",
		HTTPClient:     &http.Client{Timeout: 30 * time.Second},
		Now:            time.Now,
	}, nil
}

// NewFromFile is New with the private key read from a file.
func NewFromFile(appID string, installationID int64, keyPath string) (*TokenSource, error) {
	keyPEM, err := os.ReadFile(keyPath)
	if err != nil {
		return nil, fmt.Errorf("githubapp: read private key: %w", err)
	}
	return New(appID, installationID, keyPEM)
}

// Token returns a valid installation token, fetching a new one if there is
// none cached or the cached one expires within refreshBefore.
//
// Errors follow internal/steps: network trouble, rate limits and GitHub
// 5xx are retryable; a rejected key, a wrong App ID or installation ID, or
// an uninstalled App are permanent.
func (s *TokenSource) Token(ctx context.Context) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock() // one refresh at a time; others wait and reuse it

	now := s.Now()
	if s.token != "" && now.Before(s.expires.Add(-refreshBefore)) {
		return s.token, nil
	}

	jwt, err := s.jwt(now)
	if err != nil {
		return "", err
	}
	token, expires, err := s.exchange(ctx, jwt)
	if err != nil {
		return "", err
	}
	s.token, s.expires = token, expires
	return token, nil
}

// Installation is one place the App is installed.
type Installation struct {
	ID                  int64  `json:"id"`
	Account             string `json:"-"`
	RepositorySelection string `json:"repository_selection"` // "all" or "selected"
}

// Installations lists where the App is installed. It authenticates as the
// App itself (JWT only), so it works even with a wrong installation ID,
// which makes it the way to find the right one.
func (s *TokenSource) Installations(ctx context.Context) ([]Installation, error) {
	jwt, err := s.jwt(s.Now())
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, s.BaseURL+"/app/installations", nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+jwt)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	req.Header.Set("User-Agent", "deployctl")

	resp, err := s.HTTPClient.Do(req)
	if err != nil {
		return nil, steps.Retryable(fmt.Errorf("githubapp: list installations: %w", err))
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode != http.StatusOK {
		return nil, statusError(resp.StatusCode, body)
	}
	var raw []struct {
		Installation
		Account struct {
			Login string `json:"login"`
		} `json:"account"`
	}
	if err := json.Unmarshal(body, &raw); err != nil {
		return nil, fmt.Errorf("githubapp: unexpected installations response: %w", err)
	}
	out := make([]Installation, len(raw))
	for i, r := range raw {
		out[i] = r.Installation
		out[i].Account = r.Account.Login
	}
	return out, nil
}

// BotIdentity returns the name and email GitHub associates with the App's
// bot user, e.g. "deployctl-x[bot]" and
// "123456+deployctl-x[bot]@users.noreply.github.com". Commits with this
// identity show the App's name and avatar on GitHub.
func (s *TokenSource) BotIdentity(ctx context.Context) (name, email string, err error) {
	jwt, err := s.jwt(s.Now())
	if err != nil {
		return "", "", err
	}
	var app struct {
		Slug string `json:"slug"`
	}
	if err := s.getJSON(ctx, "/app", jwt, &app); err != nil {
		return "", "", err
	}
	if app.Slug == "" {
		return "", "", errors.New("githubapp: GitHub returned no App slug")
	}
	name = app.Slug + "[bot]"

	token, err := s.Token(ctx)
	if err != nil {
		return "", "", err
	}
	var user struct {
		ID int64 `json:"id"`
	}
	if err := s.getJSON(ctx, "/users/"+url.PathEscape(name), token, &user); err != nil {
		return "", "", err
	}
	return name, fmt.Sprintf("%d+%s@users.noreply.github.com", user.ID, name), nil
}

// getJSON GETs an API path with a bearer credential and decodes the JSON.
func (s *TokenSource) getJSON(ctx context.Context, path, bearer string, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, s.BaseURL+path, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+bearer)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	req.Header.Set("User-Agent", "deployctl")

	resp, err := s.HTTPClient.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return steps.Retryable(fmt.Errorf("githubapp: GET %s: %w", path, err))
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode != http.StatusOK {
		return statusError(resp.StatusCode, body)
	}
	if err := json.Unmarshal(body, out); err != nil {
		return fmt.Errorf("githubapp: unexpected response from GET %s: %w", path, err)
	}
	return nil
}

// jwt returns the App's signed JWT. iat is backdated 60 seconds to allow
// for clock drift, and exp is 9 minutes ahead (GitHub allows at most 10).
func (s *TokenSource) jwt(now time.Time) (string, error) {
	enc := base64.RawURLEncoding
	header := enc.EncodeToString([]byte(`{"alg":"RS256","typ":"JWT"}`))
	claims, err := json.Marshal(map[string]any{
		"iat": now.Add(-60 * time.Second).Unix(),
		"exp": now.Add(9 * time.Minute).Unix(),
		"iss": s.appID,
	})
	if err != nil {
		return "", err
	}
	signed := header + "." + enc.EncodeToString(claims)
	sum := sha256.Sum256([]byte(signed))
	sig, err := rsa.SignPKCS1v15(rand.Reader, s.key, crypto.SHA256, sum[:])
	if err != nil {
		return "", fmt.Errorf("githubapp: sign JWT: %w", err)
	}
	return signed + "." + enc.EncodeToString(sig), nil
}

// exchange trades the App JWT for an installation token.
func (s *TokenSource) exchange(ctx context.Context, jwt string) (string, time.Time, error) {
	url := s.BaseURL + "/app/installations/" + strconv.FormatInt(s.installationID, 10) + "/access_tokens"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, nil)
	if err != nil {
		return "", time.Time{}, err
	}
	req.Header.Set("Authorization", "Bearer "+jwt)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	req.Header.Set("User-Agent", "deployctl")

	resp, err := s.HTTPClient.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return "", time.Time{}, ctx.Err()
		}
		return "", time.Time{}, steps.Retryable(fmt.Errorf("githubapp: request installation token: %w", err))
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))

	if resp.StatusCode != http.StatusCreated {
		return "", time.Time{}, statusError(resp.StatusCode, body)
	}
	var out struct {
		Token     string    `json:"token"`
		ExpiresAt time.Time `json:"expires_at"`
	}
	if err := json.Unmarshal(body, &out); err != nil || out.Token == "" {
		return "", time.Time{}, steps.Retryable(errors.New("githubapp: unexpected token response from GitHub"))
	}
	return out.Token, out.ExpiresAt, nil
}

// statusError explains a failed token request. GitHub's message is
// included; it never contains the token or the key.
func statusError(code int, body []byte) error {
	var gh struct {
		Message string `json:"message"`
	}
	_ = json.Unmarshal(body, &gh)
	msg := fmt.Sprintf("githubapp: GitHub returned %d %s", code, http.StatusText(code))
	if gh.Message != "" {
		msg += ": " + gh.Message
	}
	err := errors.New(msg)

	switch {
	case code == http.StatusTooManyRequests || code >= 500:
		return steps.Retryable(err)
	case code == http.StatusUnauthorized:
		return fmt.Errorf("%w (check the App ID and private key, and that the server clock is correct)", err)
	case code == http.StatusNotFound:
		return fmt.Errorf("%w (check the installation ID, and that the App is still installed)", err)
	case code == http.StatusForbidden && bytes.Contains(bytes.ToLower(body), []byte("rate limit")):
		return steps.Retryable(err)
	default:
		return err
	}
}

// parseKey reads an RSA private key in PEM form. GitHub issues PKCS#1
// ("RSA PRIVATE KEY"); PKCS#8 ("PRIVATE KEY") is accepted too.
func parseKey(keyPEM []byte) (*rsa.PrivateKey, error) {
	block, _ := pem.Decode(keyPEM)
	if block == nil {
		return nil, errors.New("githubapp: private key is not PEM")
	}
	switch block.Type {
	case "RSA PRIVATE KEY":
		key, err := x509.ParsePKCS1PrivateKey(block.Bytes)
		if err != nil {
			return nil, fmt.Errorf("githubapp: parse private key: %w", err)
		}
		return key, nil
	case "PRIVATE KEY":
		k, err := x509.ParsePKCS8PrivateKey(block.Bytes)
		if err != nil {
			return nil, fmt.Errorf("githubapp: parse private key: %w", err)
		}
		key, ok := k.(*rsa.PrivateKey)
		if !ok {
			return nil, errors.New("githubapp: private key is not RSA")
		}
		return key, nil
	default:
		return nil, fmt.Errorf("githubapp: unexpected PEM block %q", block.Type)
	}
}
