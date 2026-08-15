package main

import (
	"context"
	"strconv"

	"kandev-plugin-redmine/internal/issues"

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
	results, err := client.SearchIssues(ctx, req.Query, int(limit))
	if err != nil {
		return &pluginsdk.SearchEntityReferencesResponse{}, nil
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
		id, ok := issueIDFromReference(req.Reference)
		if !ok {
			return &pluginsdk.AuthorizeEntityReferenceResponse{Allowed: false, Reason: "reference is missing an issue id"}, nil
		}
		if _, err := issues.New(client).GetIssue(ctx, id); err != nil {
			return &pluginsdk.AuthorizeEntityReferenceResponse{Allowed: false, Reason: "issue is no longer available"}, nil
		}
		return &pluginsdk.AuthorizeEntityReferenceResponse{Allowed: true}, nil
	default:
		return &pluginsdk.AuthorizeEntityReferenceResponse{Allowed: false, Reason: "reference purpose is unsupported"}, nil
	}
}

func issueIDFromReference(reference map[string]any) (int, bool) {
	switch v := reference["id"].(type) {
	case string:
		id, err := strconv.Atoi(v)
		return id, err == nil
	case float64:
		return int(v), true
	default:
		return 0, false
	}
}
