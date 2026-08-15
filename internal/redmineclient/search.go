package redmineclient

import (
	"context"
	"net/http"
	"strconv"
)

// SearchResult is one entry from Redmine's generic /search.json endpoint,
// scoped to issues only (issues=1) by SearchIssues.
type SearchResult struct {
	ID    int    `json:"id"`
	Title string `json:"title"`
	URL   string `json:"url"`
}

type searchResponse struct {
	Results []SearchResult `json:"results"`
}

// SearchIssues queries Redmine's full-text search, restricted to issues, for
// composer `#` mention candidates (internal/references' SearchEntityReferences).
func (c *Client) SearchIssues(ctx context.Context, query string, limit int) ([]SearchResult, error) {
	req, err := c.newRequest(ctx, http.MethodGet, "/search.json", map[string]string{
		"q":      query,
		"issues": "1",
		"limit":  strconv.Itoa(limit),
	})
	if err != nil {
		return nil, err
	}
	var out searchResponse
	if err := c.do(req, &out); err != nil {
		return nil, err
	}
	return out.Results, nil
}
