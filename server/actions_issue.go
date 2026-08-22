package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"fmt"
	"mime"
	"strings"

	"kandev-plugin-redmine/internal/issues"
	"kandev-plugin-redmine/internal/redmineclient"

	"github.com/kandev/kandev/pkg/pluginsdk"
)

const maxAttachmentBytes = 700 * 1024
const maxSafeActionID int64 = 1<<53 - 1

type issueWriteRequest struct {
	ProjectID    *int                      `json:"project_id"`
	TrackerID    *int                      `json:"tracker_id"`
	StatusID     *int                      `json:"status_id"`
	PriorityID   *int                      `json:"priority_id"`
	Subject      string                    `json:"subject"`
	Description  string                    `json:"description"`
	CustomFields []issues.CustomFieldValue `json:"custom_fields"`
	Uploads      []issues.Upload           `json:"uploads"`
}

func (r issueWriteRequest) write() issues.IssueWrite {
	return issues.IssueWrite{ProjectID: derefInt(r.ProjectID), TrackerID: derefInt(r.TrackerID), StatusID: derefInt(r.StatusID), PriorityID: derefInt(r.PriorityID), Subject: r.Subject, Description: r.Description, CustomFields: r.CustomFields, Uploads: r.Uploads}
}

// issueUpdateRequest uses pointers so omitted JSON fields stay omitted in the
// Redmine PUT payload, while an explicit empty string still clears a field.
type issueUpdateRequest struct {
	IssueID      int                        `json:"issue_id"`
	ProjectID    *int                       `json:"project_id"`
	TrackerID    *int                       `json:"tracker_id"`
	StatusID     *int                       `json:"status_id"`
	PriorityID   *int                       `json:"priority_id"`
	Subject      *string                    `json:"subject"`
	Description  *string                    `json:"description"`
	CustomFields *[]issues.CustomFieldValue `json:"custom_fields"`
	Uploads      *[]issues.Upload           `json:"uploads"`
}

func (r issueUpdateRequest) update() issues.IssueUpdate {
	return issues.IssueUpdate{ProjectID: r.ProjectID, TrackerID: r.TrackerID, StatusID: r.StatusID, PriorityID: r.PriorityID, Subject: r.Subject, Description: r.Description, CustomFields: r.CustomFields, Uploads: r.Uploads}
}

func (r issueUpdateRequest) empty() bool {
	return r.ProjectID == nil && r.TrackerID == nil && r.StatusID == nil && r.PriorityID == nil && r.Subject == nil && r.Description == nil && r.CustomFields == nil && r.Uploads == nil
}

func (p *redminePlugin) handleIssueCreate(ctx context.Context, req *pluginsdk.PluginActionRequest) (*pluginsdk.PluginActionResponse, error) {
	body, err := decodeBody[issueWriteRequest](req)
	if err != nil {
		return nil, err
	}
	body.Subject = strings.TrimSpace(body.Subject)
	if body.Subject == "" {
		return classifiedErrorResponse(fmt.Errorf("redmine: subject is required"))
	}
	client, err := p.connectionSvc.Client(ctx, req.Context.WorkspaceID)
	if err != nil {
		return classifiedErrorResponse(err)
	}
	if err := p.validateIssueCreate(ctx, req.Context.WorkspaceID, client, body); err != nil {
		return classifiedErrorResponse(err)
	}
	issue, err := issues.New(client).CreateIssue(ctx, body.write())
	if err != nil {
		return classifiedErrorResponse(err)
	}
	return jsonResponse(map[string]any{"id": issue.ID, "url": issue.URL})
}

func (p *redminePlugin) handleIssueUpdate(ctx context.Context, req *pluginsdk.PluginActionRequest) (*pluginsdk.PluginActionResponse, error) {
	body, err := decodeBody[issueUpdateRequest](req)
	if err != nil {
		return nil, err
	}
	if !validPositiveActionID(body.IssueID) {
		return classifiedErrorResponse(fmt.Errorf("redmine: issue_id must be a positive safe integer"))
	}
	if body.empty() {
		return classifiedErrorResponse(fmt.Errorf("redmine: update must include at least one field"))
	}
	if body.Subject != nil {
		trimmed := strings.TrimSpace(*body.Subject)
		if trimmed == "" {
			return classifiedErrorResponse(fmt.Errorf("redmine: subject must not be blank"))
		}
		body.Subject = &trimmed
	}
	client, err := p.connectionSvc.Client(ctx, req.Context.WorkspaceID)
	if err != nil {
		return classifiedErrorResponse(err)
	}
	current, err := issues.New(client).GetIssue(ctx, body.IssueID)
	if err != nil {
		return classifiedErrorResponse(err)
	}
	if err := p.requireSelectedProject(ctx, req.Context.WorkspaceID, current.ProjectID); err != nil {
		return classifiedErrorResponse(err)
	}
	if err := p.validateIssueUpdate(ctx, req.Context.WorkspaceID, client, body); err != nil {
		return classifiedErrorResponse(err)
	}
	if err := issues.New(client).UpdateIssueFields(ctx, body.IssueID, body.update()); err != nil {
		return classifiedErrorResponse(err)
	}
	return jsonResponse(map[string]bool{"updated": true})
}

func (p *redminePlugin) validateIssueCreate(ctx context.Context, workspaceID string, client *redmineclient.Client, body issueWriteRequest) error {
	if body.ProjectID == nil {
		return fmt.Errorf("redmine: project_id is required")
	}
	if err := p.requireSelectedProject(ctx, workspaceID, *body.ProjectID); err != nil {
		return err
	}
	return validateIssueFields(ctx, client, body.TrackerID, body.StatusID, body.PriorityID, body.CustomFields, body.Uploads)
}

func (p *redminePlugin) validateIssueUpdate(ctx context.Context, workspaceID string, client *redmineclient.Client, body issueUpdateRequest) error {
	if body.ProjectID != nil {
		if err := p.requireSelectedProject(ctx, workspaceID, *body.ProjectID); err != nil {
			return err
		}
	}
	return validateIssueFields(ctx, client, body.TrackerID, body.StatusID, body.PriorityID, derefCustomFields(body.CustomFields), derefUploads(body.Uploads))
}

func derefInt(value *int) int {
	if value == nil {
		return 0
	}
	return *value
}

func derefCustomFields(value *[]issues.CustomFieldValue) []issues.CustomFieldValue {
	if value == nil {
		return nil
	}
	return *value
}

func derefUploads(value *[]issues.Upload) []issues.Upload {
	if value == nil {
		return nil
	}
	return *value
}

func (p *redminePlugin) requireSelectedProject(ctx context.Context, workspaceID string, projectID int) error {
	if !validPositiveActionID(projectID) {
		return fmt.Errorf("redmine: project_id must be a positive safe integer")
	}
	selected, err := p.projectsSvc.GetSelection(ctx, workspaceID)
	if err != nil {
		return err
	}
	for _, selectedID := range selected {
		if selectedID == projectID {
			return nil
		}
	}
	return fmt.Errorf("redmine: project_id %d is not selected for this workspace", projectID)
}

func validateIssueFields(ctx context.Context, client *redmineclient.Client, trackerID, statusID, priorityID *int, customFields []issues.CustomFieldValue, uploads []issues.Upload) error {
	if err := validateLiveID(ctx, "tracker_id", trackerID, client.ListTrackers, func(item redmineclient.Tracker) int { return item.ID }); err != nil {
		return err
	}
	if err := validateLiveID(ctx, "status_id", statusID, client.ListIssueStatuses, func(item redmineclient.IssueStatus) int { return item.ID }); err != nil {
		return err
	}
	if err := validateLiveID(ctx, "priority_id", priorityID, client.ListIssuePriorities, func(item redmineclient.Priority) int { return item.ID }); err != nil {
		return err
	}
	seenCustomFields := make(map[int]struct{}, len(customFields))
	for _, field := range customFields {
		if !validPositiveActionID(field.ID) {
			return fmt.Errorf("redmine: custom field id must be a positive safe integer")
		}
		if _, found := seenCustomFields[field.ID]; found {
			return fmt.Errorf("redmine: custom field id %d is duplicated", field.ID)
		}
		seenCustomFields[field.ID] = struct{}{}
	}
	for _, upload := range uploads {
		if strings.TrimSpace(upload.Token) == "" || strings.TrimSpace(upload.Filename) == "" {
			return fmt.Errorf("redmine: uploads require token and filename")
		}
		if strings.ContainsAny(upload.Token, "\r\n") || upload.Token != strings.TrimSpace(upload.Token) || strings.ContainsAny(upload.Filename, "\r\n") || upload.Filename != strings.TrimSpace(upload.Filename) {
			return fmt.Errorf("redmine: upload filename or token is invalid")
		}
		contentType := upload.ContentType
		if contentType == "" || contentType != strings.TrimSpace(contentType) || len(contentType) > 255 || strings.ContainsAny(contentType, "\r\n") {
			return fmt.Errorf("redmine: upload content_type is invalid")
		}
		if _, _, err := mime.ParseMediaType(contentType); err != nil {
			return fmt.Errorf("redmine: upload content_type is invalid")
		}
	}
	return nil
}

func validateLiveID[T any](ctx context.Context, name string, id *int, list func(context.Context) ([]T, error), itemID func(T) int) error {
	if id == nil {
		return nil
	}
	if !validPositiveActionID(*id) {
		return fmt.Errorf("redmine: %s must be a positive safe integer", name)
	}
	items, err := list(ctx)
	if err != nil {
		return err
	}
	for _, item := range items {
		if itemID(item) == *id {
			return nil
		}
	}
	return fmt.Errorf("redmine: %s %d is not available", name, *id)
}

func validPositiveActionID(id int) bool {
	return id > 0 && int64(id) <= maxSafeActionID
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
