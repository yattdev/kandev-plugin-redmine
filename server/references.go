package main

import (
	"context"
	"math"
	"strconv"
	"strings"

	"kandev-plugin-redmine/internal/issues"
	"kandev-plugin-redmine/internal/redmineclient"

	"github.com/kandev/kandev/pkg/pluginsdk"
)

// referenceSource must match manifest.yaml's reference_sources[].source.
const referenceSource = "redmine-issues"

const (
	referencePurposeSearch     = "search"
	referencePurposeSubmission = "submission"
)

var (
	_ pluginsdk.EntityReferenceSearcher   = (*redminePlugin)(nil)
	_ pluginsdk.EntityReferenceAuthorizer = (*redminePlugin)(nil)
)

// SearchEntityReferences answers composer `#` mention search. An
// unconnected or errored workspace returns no candidates rather than an
// error — a mention search failing silently is preferable to surfacing a
// connection error mid-composition.
func (p *redminePlugin) SearchEntityReferences(ctx context.Context, req *pluginsdk.SearchEntityReferencesRequest) (*pluginsdk.SearchEntityReferencesResponse, error) {
	if req == nil || req.Source != referenceSource {
		return &pluginsdk.SearchEntityReferencesResponse{}, nil
	}
	client, err := p.connectionSvc.Client(ctx, req.WorkspaceID)
	if err != nil {
		return &pluginsdk.SearchEntityReferencesResponse{}, nil
	}
	limit := req.Limit
	if limit <= 0 {
		limit = 10
	}
	projects, err := p.projectsSvc.GetSelection(ctx, req.WorkspaceID)
	if err != nil || len(projects) == 0 {
		return &pluginsdk.SearchEntityReferencesResponse{}, nil
	}
	seen := map[int]bool{}
	var results []redmineclient.SearchResult
	for _, projectID := range projects {
		items, err := client.SearchIssuesInProject(ctx, req.Query, projectID, int(limit))
		if err != nil {
			return &pluginsdk.SearchEntityReferencesResponse{}, nil
		}
		for _, item := range items {
			if !seen[item.ID] && len(results) < int(limit) {
				seen[item.ID] = true
				results = append(results, item)
			}
		}
	}
	candidates := make([]pluginsdk.EntityReferenceCandidate, len(results))
	for i, r := range results {
		candidates[i] = pluginsdk.EntityReferenceCandidate{
			ProviderLocalID: strconv.Itoa(r.ID),
			Title:           r.Title,
			URL:             r.URL,
		}
	}
	return &pluginsdk.SearchEntityReferencesResponse{Candidates: candidates}, nil
}

// AuthorizeEntityReference is called twice per reference in the real flow:
// once when a candidate is surfaced (purpose "search") and again at
// message-submit time (purpose "submission") — a candidate valid at search
// time can be rejected at submission if the issue was deleted or moved in
// the meantime. Search-time authorization is a light workspace-connectivity
// check; submission-time re-verifies the issue actually still exists.
func (p *redminePlugin) AuthorizeEntityReference(ctx context.Context, req *pluginsdk.AuthorizeEntityReferenceRequest) (*pluginsdk.AuthorizeEntityReferenceResponse, error) {
	if req == nil || req.Source != referenceSource {
		return &pluginsdk.AuthorizeEntityReferenceResponse{Allowed: false, Reason: "reference source unavailable"}, nil
	}

	client, err := p.connectionSvc.Client(ctx, req.WorkspaceID)
	if err != nil {
		return &pluginsdk.AuthorizeEntityReferenceResponse{Allowed: false, Reason: "workspace is not connected to Redmine"}, nil
	}

	switch req.Purpose {
	case referencePurposeSearch:
		return &pluginsdk.AuthorizeEntityReferenceResponse{Allowed: true}, nil
	case referencePurposeSubmission:
		id, ok := issueIDFromReference(req.Reference, client.BaseURL())
		if !ok {
			return &pluginsdk.AuthorizeEntityReferenceResponse{Allowed: false, Reason: "reference is missing an issue id"}, nil
		}
		issue, err := issues.New(client).GetIssue(ctx, id)
		if err != nil {
			return &pluginsdk.AuthorizeEntityReferenceResponse{Allowed: false, Reason: "issue is no longer available"}, nil
		}
		projects, err := p.projectsSvc.GetSelection(ctx, req.WorkspaceID)
		if err != nil {
			return &pluginsdk.AuthorizeEntityReferenceResponse{Allowed: false, Reason: "project selection unavailable"}, nil
		}
		for _, projectID := range projects {
			if projectID == issue.ProjectID {
				return &pluginsdk.AuthorizeEntityReferenceResponse{Allowed: true}, nil
			}
		}
		return &pluginsdk.AuthorizeEntityReferenceResponse{Allowed: false, Reason: "issue project is not selected"}, nil
	default:
		return &pluginsdk.AuthorizeEntityReferenceResponse{Allowed: false, Reason: "reference purpose is unsupported"}, nil
	}
}

// issueIDFromReference accepts only the same canonical forms as the task-link
// action. The host normally supplies Candidate.ProviderLocalID in "id", but
// keeping URL validation here prevents an untrusted caller from smuggling a
// different Redmine origin through the submission authorization boundary.
func issueIDFromReference(reference map[string]any, baseURL string) (int, bool) {
	switch v := reference["id"].(type) {
	case string:
		id, err := parseIssueReference(strings.TrimSpace(v), baseURL)
		return id, err == nil
	case float64:
		// JSON numbers are decoded as float64, so accepting values beyond the
		// exact integer range could silently change an issue ID.
		if v <= 0 || v != math.Trunc(v) || v > math.Min(float64(math.MaxInt), float64(1<<53-1)) {
			return 0, false
		}
		return int(v), true
	default:
		return 0, false
	}
}
