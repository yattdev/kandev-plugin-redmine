// Package redmineclient is a minimal REST client for the Redmine issue
// tracker's JSON API. It owns HTTP plumbing (auth header, error
// classification) shared by every endpoint method; auth (this file),
// project/field listing, and issue read/write live in sibling files added by
// later plan tasks.
package redmineclient

import (
	"context"
	"encoding/json"
	"fmt"
	"math/rand"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Client talks to one Redmine instance under one API key. It holds no
// per-workspace state; callers (internal/connection) own composing one
// Client per connected workspace.
type Client struct {
	baseURL    string
	apiKey     string
	httpClient *http.Client
}

const defaultHTTPTimeout = 15 * time.Second

const (
	maxRetryAttempts = 3
	retryBaseDelay   = 50 * time.Millisecond
	maxRetryDelay    = 1 * time.Second
)

// NormalizeBaseURL accepts only an HTTP(S) origin or an HTTP(S) origin with a
// Redmine subpath. Credentials, queries and fragments are never meaningful
// for an API base URL and accepting them risks credential confusion.
func NormalizeBaseURL(raw string) (string, error) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Scheme == "" || u.Host == "" {
		return "", fmt.Errorf("redmineclient: base URL must be an absolute http(s) URL")
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return "", fmt.Errorf("redmineclient: base URL scheme must be http or https")
	}
	if u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return "", fmt.Errorf("redmineclient: base URL must not include credentials, query, or fragment")
	}
	u.Path = strings.TrimRight(u.Path, "/")
	u.RawPath = ""
	return strings.TrimRight(u.String(), "/"), nil
}

func defaultHTTPClient() *http.Client {
	client := &http.Client{Timeout: defaultHTTPTimeout}
	client.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		if len(via) == 0 || req.URL.Scheme != via[0].URL.Scheme || req.URL.Host != via[0].URL.Host {
			return http.ErrUseLastResponse
		}
		return nil
	}
	return client
}

// New builds a Client. httpClient must not be nil; callers own its timeout
// and transport configuration (proxy env vars are honored for free by Go's
// default transport, per the spec's Network section).
func New(baseURL, apiKey string, httpClient *http.Client) *Client {
	if httpClient == nil {
		httpClient = defaultHTTPClient()
	}
	return &Client{
		baseURL:    strings.TrimRight(baseURL, "/"),
		apiKey:     apiKey,
		httpClient: httpClient,
	}
}

// ErrorKind distinguishes the failure taxonomy the spec requires: a plugin
// action error code, never a bare host-level 401 (see spec "Failure modes").
type ErrorKind string

const (
	ErrKindInvalidCredentials ErrorKind = "invalid_credentials"
	ErrKindAPIDisabled        ErrorKind = "api_disabled"
	ErrKindPermissionDenied   ErrorKind = "permission_denied"
	ErrKindUnreachable        ErrorKind = "unreachable"
	ErrKindUnexpected         ErrorKind = "unexpected"
)

// APIError is the distinct, typed error every redmineclient call returns on
// failure. Callers switch on Kind rather than inspecting StatusCode/text.
type APIError struct {
	Kind       ErrorKind
	StatusCode int
	Message    string
}

func (e *APIError) Error() string { return e.Message }

// newRequest builds an authenticated GET request against path (which must
// start with "/"), with query parameters merged in.
func (c *Client) newRequest(ctx context.Context, method, path string, query map[string]string) (*http.Request, error) {
	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, nil)
	if err != nil {
		return nil, fmt.Errorf("redmineclient: building request: %w", err)
	}
	req.Header.Set("X-Redmine-API-Key", c.apiKey)
	req.Header.Set("Accept", "application/json")

	q := req.URL.Query()
	for k, v := range query {
		q.Set(k, v)
	}
	req.URL.RawQuery = q.Encode()
	return req, nil
}

// do executes req and classifies a non-2xx response or transport failure
// into a typed *APIError. On success it decodes the JSON body into out (skip
// decoding by passing a nil out, e.g. for 204 responses).
func (c *Client) do(req *http.Request, out any) error {
	for attempt := 0; ; attempt++ {
		resp, err := c.httpClient.Do(req)
		if err != nil {
			if !canRetry(req) || attempt+1 >= maxRetryAttempts {
				return &APIError{Kind: ErrKindUnreachable, Message: fmt.Sprintf("redmineclient: %s %s: %v", req.Method, req.URL.Path, err)}
			}
			if err := waitForRetry(req.Context(), retryDelay(nil, attempt)); err != nil {
				return &APIError{Kind: ErrKindUnreachable, Message: fmt.Sprintf("redmineclient: retry cancelled: %v", err)}
			}
			resetRequestBody(req)
			continue
		}
		if retryableStatus(resp.StatusCode) && canRetry(req) && attempt+1 < maxRetryAttempts {
			delay := retryDelay(resp, attempt)
			_ = resp.Body.Close()
			if err := waitForRetry(req.Context(), delay); err != nil {
				return &APIError{Kind: ErrKindUnexpected, StatusCode: resp.StatusCode, Message: fmt.Sprintf("redmineclient: retry cancelled: %v", err)}
			}
			resetRequestBody(req)
			continue
		}
		defer func() { _ = resp.Body.Close() }()

		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			return classifyErrorStatus(resp.StatusCode, req.URL.Path)
		}
		if out == nil {
			return nil
		}
		if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
			return &APIError{Kind: ErrKindUnexpected, StatusCode: resp.StatusCode, Message: fmt.Sprintf("redmineclient: decoding response from %s: %v", req.URL.Path, err)}
		}
		return nil
	}
}

func canRetry(req *http.Request) bool {
	return req.Method == http.MethodGet || (req.Method == http.MethodPut && req.GetBody != nil)
}

func resetRequestBody(req *http.Request) {
	if req.GetBody == nil {
		return
	}
	body, err := req.GetBody()
	if err == nil {
		req.Body = body
	}
}

func retryableStatus(status int) bool { return status == http.StatusTooManyRequests || status >= 500 }

func retryDelay(resp *http.Response, attempt int) time.Duration {
	if resp != nil {
		retryAfter := strings.TrimSpace(resp.Header.Get("Retry-After"))
		if seconds, err := time.ParseDuration(retryAfter + "s"); err == nil && seconds > 0 {
			return min(seconds, maxRetryDelay)
		}
		if when, err := http.ParseTime(retryAfter); err == nil {
			return min(max(time.Until(when), 0), maxRetryDelay)
		}
	}
	delay := retryBaseDelay << attempt
	return min(delay/2+time.Duration(rand.Int63n(int64(delay/2)+1)), maxRetryDelay) //nolint:gosec // retry jitter is not security-sensitive
}

func waitForRetry(ctx context.Context, delay time.Duration) error {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func classifyErrorStatus(statusCode int, path string) *APIError {
	switch statusCode {
	case http.StatusUnauthorized:
		return &APIError{Kind: ErrKindInvalidCredentials, StatusCode: statusCode, Message: "redmineclient: Redmine rejected the API key"}
	case http.StatusForbidden:
		if path != "/users/current.json" {
			return &APIError{Kind: ErrKindPermissionDenied, StatusCode: statusCode, Message: fmt.Sprintf("redmineclient: permission denied for %s", path)}
		}
		return &APIError{Kind: ErrKindAPIDisabled, StatusCode: statusCode, Message: "redmineclient: Redmine's REST API is disabled (Administration > Settings > API)"}
	default:
		return &APIError{Kind: ErrKindUnexpected, StatusCode: statusCode, Message: fmt.Sprintf("redmineclient: unexpected status %d from %s", statusCode, path)}
	}
}

// User is the subset of Redmine's /users/current.json response this plugin
// uses.
type User struct {
	ID        int    `json:"id"`
	Login     string `json:"login"`
	Firstname string `json:"firstname"`
	Lastname  string `json:"lastname"`
	Admin     bool   `json:"admin"`
}

type currentUserResponse struct {
	User User `json:"user"`
}

// ValidateCredentials calls GET /users/current.json — the spec's chosen
// validation endpoint, distinguishing invalid-credentials (401),
// API-disabled (403), and unreachable-host failures from a generic
// unexpected error.
func (c *Client) ValidateCredentials(ctx context.Context) (*User, error) {
	req, err := c.newRequest(ctx, http.MethodGet, "/users/current.json", nil)
	if err != nil {
		return nil, err
	}
	var out currentUserResponse
	if err := c.do(req, &out); err != nil {
		return nil, err
	}
	return &out.User, nil
}
