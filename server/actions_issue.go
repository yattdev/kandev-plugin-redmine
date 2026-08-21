package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"fmt"
	"strings"

	"kandev-plugin-redmine/internal/issues"

	"github.com/kandev/kandev/pkg/pluginsdk"
)

// JSON/base64 action bodies are capped at 1 MiB by the host. Keep a little
// headroom for base64 expansion and JSON framing, while allowing ordinary
// screenshots. Larger files need a future binary/chunked host action API.
const maxAttachmentBytes = 700 * 1024

type issueWriteRequest struct {
	IssueID      int                       `json:"issue_id"`
	ProjectID    int                       `json:"project_id"`
	TrackerID    int                       `json:"tracker_id"`
	StatusID     int                       `json:"status_id"`
	PriorityID   int                       `json:"priority_id"`
	Subject      string                    `json:"subject"`
	Description  string                    `json:"description"`
	CustomFields []issues.CustomFieldValue `json:"custom_fields"`
	Uploads      []issues.Upload           `json:"uploads"`
}

func (r issueWriteRequest) write() issues.IssueWrite {
	return issues.IssueWrite{ProjectID: r.ProjectID, TrackerID: r.TrackerID, StatusID: r.StatusID, PriorityID: r.PriorityID, Subject: r.Subject, Description: r.Description, CustomFields: r.CustomFields, Uploads: r.Uploads}
}

func (p *redminePlugin) handleIssueCreate(ctx context.Context, req *pluginsdk.PluginActionRequest) (*pluginsdk.PluginActionResponse, error) {
	body, err := decodeBody[issueWriteRequest](req)
	if err != nil {
		return nil, err
	}
	client, err := p.connectionSvc.Client(ctx, req.Context.WorkspaceID)
	if err != nil {
		return classifiedErrorResponse(err)
	}
	issue, err := issues.New(client).CreateIssue(ctx, body.write())
	if err != nil {
		return classifiedErrorResponse(err)
	}
	return jsonResponse(map[string]any{"id": issue.ID, "url": issue.URL})
}

func (p *redminePlugin) handleIssueUpdate(ctx context.Context, req *pluginsdk.PluginActionRequest) (*pluginsdk.PluginActionResponse, error) {
	body, err := decodeBody[issueWriteRequest](req)
	if err != nil {
		return nil, err
	}
	if body.IssueID <= 0 {
		return classifiedErrorResponse(fmt.Errorf("redmine: issue_id is required"))
	}
	client, err := p.connectionSvc.Client(ctx, req.Context.WorkspaceID)
	if err != nil {
		return classifiedErrorResponse(err)
	}
	if err := issues.New(client).UpdateIssue(ctx, body.IssueID, body.write()); err != nil {
		return classifiedErrorResponse(err)
	}
	return jsonResponse(map[string]bool{"updated": true})
}

type issueUploadRequest struct {
	Filename      string `json:"filename"`
	ContentType   string `json:"content_type"`
	ContentBase64 string `json:"content_base64"`
}

func (p *redminePlugin) handleIssueUpload(ctx context.Context, req *pluginsdk.PluginActionRequest) (*pluginsdk.PluginActionResponse, error) {
	body, err := decodeBody[issueUploadRequest](req)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(body.Filename) == "" {
		return classifiedErrorResponse(fmt.Errorf("redmine: attachment filename is required"))
	}
	if body.ContentBase64 == "" {
		return classifiedErrorResponse(fmt.Errorf("redmine: attachment content_base64 is required"))
	}
	content, err := base64.StdEncoding.DecodeString(body.ContentBase64)
	if err != nil {
		return classifiedErrorResponse(fmt.Errorf("redmine: attachment content_base64 is invalid"))
	}
	if len(content) > maxAttachmentBytes {
		return classifiedErrorResponse(fmt.Errorf("redmine: attachment exceeds %d bytes", maxAttachmentBytes))
	}
	client, err := p.connectionSvc.Client(ctx, req.Context.WorkspaceID)
	if err != nil {
		return classifiedErrorResponse(err)
	}
	upload, err := issues.New(client).UploadAttachment(ctx, body.Filename, body.ContentType, bytes.NewReader(content))
	if err != nil {
		return classifiedErrorResponse(err)
	}
	return jsonResponse(map[string]any{"token": upload.Token, "filename": upload.Filename, "content_type": upload.ContentType})
}
