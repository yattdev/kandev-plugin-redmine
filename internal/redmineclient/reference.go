package redmineclient

import (
	"context"
	"fmt"
	"net/http"
	"strconv"
)

// Project is the subset of Redmine's /projects.json entries this plugin
// uses.
type Project struct {
	ID         int    `json:"id"`
	Name       string `json:"name"`
	Identifier string `json:"identifier"`
}

type projectsPageResponse struct {
	Projects   []Project `json:"projects"`
	TotalCount int       `json:"total_count"`
}

// ListProjectsPage fetches one page of /projects.json. Callers walk pages by
// increasing offset until offset+len(items) >= totalCount (see
// internal/projects, which owns that walk and the selected-project
// persistence).
func (c *Client) ListProjectsPage(ctx context.Context, offset, limit int) ([]Project, int, error) {
	req, err := c.newRequest(ctx, http.MethodGet, "/projects.json", map[string]string{
		"offset": strconv.Itoa(offset),
		"limit":  strconv.Itoa(limit),
	})
	if err != nil {
		return nil, 0, err
	}
	var out projectsPageResponse
	if err := c.do(req, &out); err != nil {
		return nil, 0, err
	}
	return out.Projects, out.TotalCount, nil
}

// IssueStatus is one entry from /issue_statuses.json.
type IssueStatus struct {
	ID       int    `json:"id"`
	Name     string `json:"name"`
	IsClosed bool   `json:"is_closed"`
}

type issueStatusesResponse struct {
	IssueStatuses []IssueStatus `json:"issue_statuses"`
}

// ListIssueStatuses fetches every issue status live — the spec requires
// status names are never hardcoded in this plugin.
func (c *Client) ListIssueStatuses(ctx context.Context) ([]IssueStatus, error) {
	req, err := c.newRequest(ctx, http.MethodGet, "/issue_statuses.json", nil)
	if err != nil {
		return nil, err
	}
	var out issueStatusesResponse
	if err := c.do(req, &out); err != nil {
		return nil, err
	}
	return out.IssueStatuses, nil
}

// Tracker is one entry from /trackers.json.
type Tracker struct {
	ID   int    `json:"id"`
	Name string `json:"name"`
}

type trackersResponse struct {
	Trackers []Tracker `json:"trackers"`
}

func (c *Client) ListTrackers(ctx context.Context) ([]Tracker, error) {
	req, err := c.newRequest(ctx, http.MethodGet, "/trackers.json", nil)
	if err != nil {
		return nil, err
	}
	var out trackersResponse
	if err := c.do(req, &out); err != nil {
		return nil, err
	}
	return out.Trackers, nil
}

// Priority is one entry from /enumerations/issue_priorities.json.
type Priority struct {
	ID   int    `json:"id"`
	Name string `json:"name"`
}

type issuePrioritiesResponse struct {
	IssuePriorities []Priority `json:"issue_priorities"`
}

func (c *Client) ListIssuePriorities(ctx context.Context) ([]Priority, error) {
	req, err := c.newRequest(ctx, http.MethodGet, "/enumerations/issue_priorities.json", nil)
	if err != nil {
		return nil, err
	}
	var out issuePrioritiesResponse
	if err := c.do(req, &out); err != nil {
		return nil, err
	}
	return out.IssuePriorities, nil
}

// CustomFieldDef is one entry from /custom_fields.json — admin-only on
// Redmine; a non-admin key gets a 403 (surfaced here as *APIError with Kind
// ErrKindPermissionDenied while /users/current.json still authenticates,
// since Redmine returns the same status for both — internal/fieldmapping
// distinguishes them by which endpoint it came from and falls back to
// deriving fields from recently fetched issues rather than treating it as a
// connection failure).
type CustomFieldDef struct {
	ID             int      `json:"id"`
	Name           string   `json:"name"`
	PossibleValues []string `json:"possible_values"`
}

type customFieldsResponse struct {
	CustomFields []CustomFieldDef `json:"custom_fields"`
}

func (c *Client) ListCustomFields(ctx context.Context) ([]CustomFieldDef, error) {
	req, err := c.newRequest(ctx, http.MethodGet, "/custom_fields.json", nil)
	if err != nil {
		return nil, err
	}
	var out customFieldsResponse
	if err := c.do(req, &out); err != nil {
		return nil, err
	}
	return out.CustomFields, nil
}

// NamedID is the compact live-option shape used for project-specific issue
// filters such as assignee and category.
type NamedID struct {
	ID   int    `json:"id"`
	Name string `json:"name"`
}

type projectMembersResponse struct {
	Memberships []struct {
		User NamedID `json:"user"`
	} `json:"memberships"`
	TotalCount int `json:"total_count"`
}

// ListProjectMembers returns users visible in a project. Redmine exposes
// assignee choices through memberships, not a global users endpoint.
func (c *Client) ListProjectMembers(ctx context.Context, projectID int) ([]NamedID, error) {
	if projectID <= 0 {
		return nil, fmt.Errorf("redmineclient: project ID must be positive")
	}
	result := make([]NamedID, 0)
	for offset := 0; ; offset += 100 {
		req, err := c.newRequest(ctx, http.MethodGet, "/projects/"+strconv.Itoa(projectID)+"/memberships.json", map[string]string{"offset": strconv.Itoa(offset), "limit": "100"})
		if err != nil {
			return nil, err
		}
		var out projectMembersResponse
		if err := c.do(req, &out); err != nil {
			return nil, err
		}
		for _, membership := range out.Memberships {
			if membership.User.ID > 0 {
				result = append(result, membership.User)
			}
		}
		if len(out.Memberships) == 0 || offset+len(out.Memberships) >= out.TotalCount {
			return result, nil
		}
	}
}

type issueCategoriesResponse struct {
	IssueCategories []NamedID `json:"issue_categories"`
}

func (c *Client) ListIssueCategories(ctx context.Context, projectID int) ([]NamedID, error) {
	if projectID <= 0 {
		return nil, fmt.Errorf("redmineclient: project ID must be positive")
	}
	req, err := c.newRequest(ctx, http.MethodGet, "/projects/"+strconv.Itoa(projectID)+"/issue_categories.json", nil)
	if err != nil {
		return nil, err
	}
	var out issueCategoriesResponse
	if err := c.do(req, &out); err != nil {
		return nil, err
	}
	return out.IssueCategories, nil
}
