package main

import (
	"context"
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"kandev-plugin-redmine/internal/issues"

	"github.com/kandev/kandev/pkg/pluginsdk"
)

type linkResponse struct {
	Linked   bool   `json:"linked"`
	IssueID  int    `json:"issue_id,omitempty"`
	IssueURL string `json:"issue_url,omitempty"`
}

func (p *redminePlugin) handleLinkGet(ctx context.Context, req *pluginsdk.PluginActionRequest) (*pluginsdk.PluginActionResponse, error) {
	link, found, err := p.tasklinkSvc.Get(ctx, req.Context.TaskID)
	if err != nil {
		return nil, err
	}
	if !found {
		return jsonResponse(linkResponse{Linked: false})
	}
	return jsonResponse(linkResponse{Linked: true, IssueID: link.IssueID, IssueURL: link.IssueURL})
}

type linkSetRequest struct {
	// Reference is the free-text value the user typed into the shared host
	// Link dialog (host.openTaskLinkDialog's onSubmit(reference, ...)) — an
	// issue ID, a "#123" shorthand, or a full issue URL.
	Reference string `json:"reference"`
}

// issueReferencePattern extracts a trailing numeric issue ID from a
// Redmine issue URL (".../issues/123") or a "#123" shorthand.
var issueReferencePattern = regexp.MustCompile(`(\d+)\s*$`)

func parseIssueReference(reference string) (int, error) {
	trimmed := strings.TrimSpace(reference)
	trimmed = strings.TrimPrefix(trimmed, "#")
	trimmed = strings.TrimRight(trimmed, "/")
	match := issueReferencePattern.FindStringSubmatch(trimmed)
	if match == nil {
		return 0, fmt.Errorf("redmine: %q does not look like a Redmine issue ID or URL", reference)
	}
	id, err := strconv.Atoi(match[1])
	if err != nil {
		return 0, fmt.Errorf("redmine: parsing issue id from %q: %w", reference, err)
	}
	return id, nil
}

func (p *redminePlugin) handleLinkSet(ctx context.Context, req *pluginsdk.PluginActionRequest) (*pluginsdk.PluginActionResponse, error) {
	body, err := decodeBody[linkSetRequest](req)
	if err != nil {
		return nil, err
	}
	issueID, err := parseIssueReference(body.Reference)
	if err != nil {
		return classifiedErrorResponse(err)
	}

	client, err := p.connectionSvc.Client(ctx, req.Context.WorkspaceID)
	if err != nil {
		return classifiedErrorResponse(err)
	}
	issue, err := issues.New(client).GetIssue(ctx, issueID)
	if err != nil {
		return classifiedErrorResponse(err)
	}

	if err := p.tasklinkSvc.Set(ctx, req.Context.TaskID, req.Context.WorkspaceID, issue.ID, issue.URL); err != nil {
		return nil, err
	}
	return jsonResponse(linkResponse{Linked: true, IssueID: issue.ID, IssueURL: issue.URL})
}

func (p *redminePlugin) handleLinkUnset(ctx context.Context, req *pluginsdk.PluginActionRequest) (*pluginsdk.PluginActionResponse, error) {
	if err := p.tasklinkSvc.Unset(ctx, req.Context.TaskID); err != nil {
		return nil, err
	}
	return jsonResponse(map[string]bool{"unlinked": true})
}

type linkSetStatusRequest struct {
	StatusID int `json:"status_id"`
}

// handleLinkSetStatus is the manual "Set Redmine status" action: it pushes
// unconditionally (ignoring autoStatusWriteback), for when the operator
// wants to push a status change without enabling automatic write-back.
func (p *redminePlugin) handleLinkSetStatus(ctx context.Context, req *pluginsdk.PluginActionRequest) (*pluginsdk.PluginActionResponse, error) {
	body, err := decodeBody[linkSetStatusRequest](req)
	if err != nil {
		return nil, err
	}

	_, found, err := p.tasklinkSvc.Get(ctx, req.Context.TaskID)
	if err != nil {
		return nil, err
	}
	if !found {
		return classifiedErrorResponse(fmt.Errorf("redmine: task %s is not linked to a Redmine issue", req.Context.TaskID))
	}

	client, err := p.connectionSvc.Client(ctx, req.Context.WorkspaceID)
	if err != nil {
		return classifiedErrorResponse(err)
	}

	if err := p.syncSvc.ForceWriteback(ctx, req.Context.TaskID, body.StatusID, issues.New(client)); err != nil {
		return classifiedErrorResponse(err)
	}
	return jsonResponse(map[string]bool{"pushed": true})
}
