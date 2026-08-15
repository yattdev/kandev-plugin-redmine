package redmineclient

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
)

// BaseURL returns the configured instance base URL, for callers that need to
// build a browsable issue URL themselves (Redmine's issue JSON does not
// include one).
func (c *Client) BaseURL() string { return c.baseURL }

// GetJSON performs an authenticated GET against path with query parameters,
// decoding the JSON response into out.
func (c *Client) GetJSON(ctx context.Context, path string, query map[string]string, out any) error {
	req, err := c.newRequest(ctx, http.MethodGet, path, query)
	if err != nil {
		return err
	}
	return c.do(req, out)
}

// PostJSON performs an authenticated POST with a JSON-encoded body, decoding
// the JSON response into out (pass nil to skip decoding).
func (c *Client) PostJSON(ctx context.Context, path string, body, out any) error {
	return c.doWithJSONBody(ctx, http.MethodPost, path, body, out)
}

// PutJSON performs an authenticated PUT with a JSON-encoded body, decoding
// the JSON response into out (pass nil to skip decoding — Redmine typically
// answers a successful PUT with an empty body).
func (c *Client) PutJSON(ctx context.Context, path string, body, out any) error {
	return c.doWithJSONBody(ctx, http.MethodPut, path, body, out)
}

func (c *Client) doWithJSONBody(ctx context.Context, method, path string, body, out any) error {
	encoded, err := json.Marshal(body)
	if err != nil {
		return fmt.Errorf("redmineclient: encoding request body: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, bytes.NewReader(encoded))
	if err != nil {
		return fmt.Errorf("redmineclient: building request: %w", err)
	}
	req.Header.Set("X-Redmine-API-Key", c.apiKey)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Content-Type", "application/json")
	return c.do(req, out)
}

// PostBinary uploads raw bytes with an explicit Content-Type — the Redmine
// attachment upload-token flow's first step (POST /uploads.json,
// Content-Type: application/octet-stream).
func (c *Client) PostBinary(ctx context.Context, path, contentType string, body io.Reader, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+path, body)
	if err != nil {
		return fmt.Errorf("redmineclient: building request: %w", err)
	}
	req.Header.Set("X-Redmine-API-Key", c.apiKey)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Content-Type", contentType)
	return c.do(req, out)
}
