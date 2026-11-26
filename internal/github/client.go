// Package github is a minimal GitHub App client for the task and EOD
// features: App JWT → installation tokens, REST and GraphQL calls with
// timeouts, retries and rate-limit awareness. Tokens and the private key
// are never logged.
package github

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
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
)

// App authenticates as a GitHub App and calls the API per organization.
type App struct {
	apiURL string
	appID  int64
	key    *rsa.PrivateKey
	http   *http.Client
	log    *slog.Logger
	now    func() time.Time
	// retryDelays are the waits between attempts of retryable calls.
	retryDelays []time.Duration

	mu       sync.Mutex
	installs map[string]int64 // lower(org) → installation id
	tokens   map[int64]installToken
}

type installToken struct {
	value   string
	expires time.Time
}

// NewApp builds an App client. apiURL is e.g. https://api.github.com.
func NewApp(apiURL string, appID int64, key *rsa.PrivateKey, log *slog.Logger) *App {
	return &App{
		apiURL: strings.TrimRight(apiURL, "/"),
		appID:  appID,
		key:    key,
		http: &http.Client{
			Timeout: 15 * time.Second,
			// GET /orgs/{org}/members/{user} answers 302 when the App lacks
			// the Members permission; following it would silently check
			// public membership instead.
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		},
		log:         log,
		now:         time.Now,
		retryDelays: []time.Duration{500 * time.Millisecond, 2 * time.Second},
		installs:    map[string]int64{},
		tokens:      map[int64]installToken{},
	}
}

// LoadPrivateKey reads the App's PEM private key (PKCS#1 or PKCS#8). It
// returns a warning when the file is readable by group or others.
func LoadPrivateKey(path string) (*rsa.PrivateKey, string, error) {
	st, err := os.Stat(path)
	if err != nil {
		return nil, "", fmt.Errorf("github private key: %w", err)
	}
	warn := ""
	if st.Mode().Perm()&0o077 != 0 {
		warn = fmt.Sprintf("github private key %s is readable by group/others (mode %o); use chmod 400", path, st.Mode().Perm())
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, "", fmt.Errorf("github private key: %w", err)
	}
	key, err := ParsePrivateKey(raw)
	return key, warn, err
}

// ParsePrivateKey parses a PEM RSA private key.
func ParsePrivateKey(raw []byte) (*rsa.PrivateKey, error) {
	block, _ := pem.Decode(raw)
	if block == nil {
		return nil, errors.New("github private key: no PEM block found")
	}
	if k, err := x509.ParsePKCS1PrivateKey(block.Bytes); err == nil {
		return k, nil
	}
	k, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return nil, errors.New("github private key: not an RSA private key in PKCS#1 or PKCS#8 form")
	}
	rk, ok := k.(*rsa.PrivateKey)
	if !ok {
		return nil, errors.New("github private key: not an RSA key")
	}
	return rk, nil
}

// appJWT is the short-lived RS256 JWT that authenticates as the App.
func (a *App) appJWT() (string, error) {
	now := a.now()
	enc := base64.RawURLEncoding
	header := enc.EncodeToString([]byte(`{"alg":"RS256","typ":"JWT"}`))
	claims, err := json.Marshal(map[string]int64{
		"iat": now.Add(-60 * time.Second).Unix(), // allow for clock drift
		"exp": now.Add(9 * time.Minute).Unix(),   // GitHub max is 10 min
		"iss": a.appID,
	})
	if err != nil {
		return "", err
	}
	signing := header + "." + enc.EncodeToString(claims)
	sum := sha256.Sum256([]byte(signing))
	sig, err := rsa.SignPKCS1v15(rand.Reader, a.key, crypto.SHA256, sum[:])
	if err != nil {
		return "", err
	}
	return signing + "." + enc.EncodeToString(sig), nil
}

// APIError is a non-2xx response from GitHub.
type APIError struct {
	Status  int
	Path    string
	Message string
}

func (e *APIError) Error() string {
	return fmt.Sprintf("github %s: HTTP %d: %s", e.Path, e.Status, e.Message)
}

// RateLimitError means GitHub refused the call until Reset.
type RateLimitError struct {
	Path  string
	Reset time.Time
}

func (e *RateLimitError) Error() string {
	return fmt.Sprintf("github %s: rate limited until %s", e.Path, e.Reset.UTC().Format(time.RFC3339))
}

// GraphQLError is an "errors" entry in a GraphQL response.
type GraphQLError struct {
	Messages []string
}

func (e *GraphQLError) Error() string { return "github graphql: " + strings.Join(e.Messages, "; ") }

// response is a finished HTTP exchange.
type response struct {
	status int
	body   []byte
}

// send performs one request, retrying network errors and 5xx when retry
// is set. auth is the full Authorization header value.
func (a *App) send(ctx context.Context, auth, method, path string, body []byte, retry bool) (*response, error) {
	attempts := 1
	if retry {
		attempts += len(a.retryDelays)
	}
	var lastErr error
	for i := 0; i < attempts; i++ {
		if i > 0 {
			a.log.Warn("github retry", "path", path, "attempt", i+1, "err", lastErr)
			select {
			case <-time.After(a.retryDelays[i-1]):
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		}
		r, err := a.once(ctx, auth, method, path, body)
		if err == nil && r.status >= 500 {
			err = &APIError{Status: r.status, Path: path, Message: "server error"}
		}
		if err == nil {
			return r, nil
		}
		var rl *RateLimitError
		if errors.As(err, &rl) || ctx.Err() != nil {
			return nil, err
		}
		lastErr = err
	}
	return nil, lastErr
}

func (a *App) once(ctx context.Context, auth, method, path string, body []byte) (*response, error) {
	var rd io.Reader
	if body != nil {
		rd = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, a.apiURL+path, rd)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", auth)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	req.Header.Set("User-Agent", "mattermost-attendance-bot")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := a.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return nil, err
	}

	remaining := resp.Header.Get("X-RateLimit-Remaining")
	if n, err := strconv.Atoi(remaining); err == nil && n < 100 {
		a.log.Warn("github rate limit low", "path", path, "remaining", n, "reset", resp.Header.Get("X-RateLimit-Reset"))
	}
	if resp.StatusCode == http.StatusForbidden || resp.StatusCode == http.StatusTooManyRequests {
		if remaining == "0" || resp.Header.Get("Retry-After") != "" {
			reset := a.now().Add(time.Minute)
			if s, err := strconv.ParseInt(resp.Header.Get("X-RateLimit-Reset"), 10, 64); err == nil {
				reset = time.Unix(s, 0)
			} else if s, err := strconv.Atoi(resp.Header.Get("Retry-After")); err == nil {
				reset = a.now().Add(time.Duration(s) * time.Second)
			}
			a.log.Warn("github rate limited", "path", path, "reset", reset)
			return nil, &RateLimitError{Path: path, Reset: reset}
		}
	}
	return &response{status: resp.StatusCode, body: data}, nil
}

func apiErr(path string, r *response) error {
	var e struct {
		Message string `json:"message"`
	}
	_ = json.Unmarshal(r.body, &e)
	return &APIError{Status: r.status, Path: path, Message: e.Message}
}

// installationID finds the App's installation on org (cached).
func (a *App) installationID(ctx context.Context, org string) (int64, error) {
	k := strings.ToLower(org)
	a.mu.Lock()
	id, ok := a.installs[k]
	a.mu.Unlock()
	if ok {
		return id, nil
	}
	jwt, err := a.appJWT()
	if err != nil {
		return 0, err
	}
	path := "/orgs/" + url.PathEscape(org) + "/installation"
	r, err := a.send(ctx, "Bearer "+jwt, http.MethodGet, path, nil, true)
	if err != nil {
		return 0, err
	}
	if r.status == http.StatusNotFound {
		return 0, fmt.Errorf("the GitHub App is not installed on org %q", org)
	}
	if r.status/100 != 2 {
		return 0, apiErr(path, r)
	}
	var out struct {
		ID int64 `json:"id"`
	}
	if err := json.Unmarshal(r.body, &out); err != nil || out.ID == 0 {
		return 0, fmt.Errorf("github %s: bad response", path)
	}
	a.mu.Lock()
	a.installs[k] = out.ID
	a.mu.Unlock()
	return out.ID, nil
}

// token returns an installation access token for org, cached until 5
// minutes before it expires.
func (a *App) token(ctx context.Context, org string) (string, error) {
	id, err := a.installationID(ctx, org)
	if err != nil {
		return "", err
	}
	a.mu.Lock()
	t, ok := a.tokens[id]
	a.mu.Unlock()
	if ok && a.now().Before(t.expires.Add(-5*time.Minute)) {
		return t.value, nil
	}
	jwt, err := a.appJWT()
	if err != nil {
		return "", err
	}
	path := fmt.Sprintf("/app/installations/%d/access_tokens", id)
	r, err := a.send(ctx, "Bearer "+jwt, http.MethodPost, path, nil, true) // creating a token is safe to repeat
	if err != nil {
		return "", err
	}
	if r.status/100 != 2 {
		return "", apiErr(path, r)
	}
	var out struct {
		Token     string    `json:"token"`
		ExpiresAt time.Time `json:"expires_at"`
	}
	if err := json.Unmarshal(r.body, &out); err != nil || out.Token == "" {
		return "", fmt.Errorf("github %s: bad response", path)
	}
	a.mu.Lock()
	a.tokens[id] = installToken{value: out.Token, expires: out.ExpiresAt}
	a.mu.Unlock()
	return out.Token, nil
}

// dropToken forgets org's cached token (after a 401).
func (a *App) dropToken(org string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if id, ok := a.installs[strings.ToLower(org)]; ok {
		delete(a.tokens, id)
	}
}

// rest calls the REST API as the org's installation. It returns the
// response for the caller to interpret its status; a 401 refreshes the
// token once.
func (a *App) rest(ctx context.Context, org, method, path string, body any, retry bool) (*response, error) {
	var raw []byte
	if body != nil {
		var err error
		if raw, err = json.Marshal(body); err != nil {
			return nil, err
		}
	}
	for attempt := 0; ; attempt++ {
		tok, err := a.token(ctx, org)
		if err != nil {
			return nil, err
		}
		r, err := a.send(ctx, "token "+tok, method, path, raw, retry)
		if err != nil {
			return nil, err
		}
		if r.status == http.StatusUnauthorized && attempt == 0 {
			a.dropToken(org)
			continue
		}
		return r, nil
	}
}

// GraphQL runs a query or mutation as the org's installation and decodes
// "data" into out. Queries are retried; pass mutation=true to disable.
func (a *App) GraphQL(ctx context.Context, org, query string, vars map[string]any, out any, mutation bool) error {
	r, err := a.rest(ctx, org, http.MethodPost, "/graphql", map[string]any{"query": query, "variables": vars}, !mutation)
	if err != nil {
		return err
	}
	if r.status/100 != 2 {
		return apiErr("/graphql", r)
	}
	var env struct {
		Data   json.RawMessage `json:"data"`
		Errors []struct {
			Message string `json:"message"`
		} `json:"errors"`
	}
	if err := json.Unmarshal(r.body, &env); err != nil {
		return fmt.Errorf("github graphql: %w", err)
	}
	if len(env.Errors) > 0 {
		ge := &GraphQLError{}
		for _, e := range env.Errors {
			ge.Messages = append(ge.Messages, e.Message)
		}
		return ge
	}
	if out == nil {
		return nil
	}
	return json.Unmarshal(env.Data, out)
}
