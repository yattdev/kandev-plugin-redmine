package issues_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"kandev-plugin-redmine/internal/issues"
	"kandev-plugin-redmine/internal/redmineclient"

	"github.com/stretchr/testify/require"
)

func newService(t *testing.T, handler http.HandlerFunc) *issues.Service {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	client := redmineclient.New(srv.URL, "key", srv.Client())
	return issues.New(client)
}

func TestListIssues_AlwaysSendsStatusIDWildcard(t *testing.T) {
	var sawStatusID string
	svc := newService(t, func(w http.ResponseWriter, r *http.Request) {
		sawStatusID = r.URL.Query().Get("status_id")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"issues":[],"total_count":0}`))
	})

	_, err := svc.ListIssues(context.Background(), issues.ListIssuesParams{ProjectID: "demo"})
	require.NoError(t, err)
	require.Equal(t, "*", sawStatusID)
}

func TestListIssues_ClosedIssueIncludedInResults(t *testing.T) {
	svc := newService(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"issues":[{"id":1,"subject":"closed one","status":{"id":5,"name":"Shipped"}}],"total_count":1}`))
	})

	result, err := svc.ListIssues(context.Background(), issues.ListIssuesParams{})
	require.NoError(t, err)
	require.Len(t, result.Issues, 1)
	require.Equal(t, 5, result.Issues[0].StatusID)
}

func TestGetIssue_RequestsFullDetail(t *testing.T) {
	var gotPath, gotInclude string
	svc := newService(t, func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotInclude = r.URL.Query().Get("include")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"issue":{"id":42,"subject":"Full detail",
			"journals":[{"id":1,"notes":"a note"}],
			"attachments":[{"id":2,"filename":"log.txt"}],
			"relations":[{"id":3,"issue_to_id":99,"relation_type":"relates"}]}}`))
	})

	issue, err := svc.GetIssue(context.Background(), 42)
	require.NoError(t, err)
	require.Equal(t, "/issues/42.json", gotPath)
	require.Equal(t, "journals,attachments,relations", gotInclude)
	require.Equal(t, 42, issue.ID)
	require.Len(t, issue.Journals, 1)
	require.Len(t, issue.Attachments, 1)
	require.Len(t, issue.Relations, 1)
}

func TestCreateIssue_SendsFullFieldSetAndReturnsIDAndURL(t *testing.T) {
	var gotBody map[string]any
	svc := newService(t, func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, http.MethodPost, r.Method)
		require.Equal(t, "/issues.json", r.URL.Path)
		body, _ := io.ReadAll(r.Body)
		require.NoError(t, json.Unmarshal(body, &gotBody))
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"issue":{"id":7,"subject":"New issue"}}`))
	})

	created, err := svc.CreateIssue(context.Background(), issues.IssueWrite{
		ProjectID:   1,
		TrackerID:   2,
		StatusID:    3,
		PriorityID:  4,
		Subject:     "New issue",
		Description: "body",
		CustomFields: []issues.CustomFieldValue{
			{ID: 10, Value: "high"},
		},
	})
	require.NoError(t, err)
	require.Equal(t, 7, created.ID)
	require.Contains(t, created.URL, "/issues/7")

	issueBody, ok := gotBody["issue"].(map[string]any)
	require.True(t, ok)
	require.EqualValues(t, 1, issueBody["project_id"])
	require.EqualValues(t, 2, issueBody["tracker_id"])
	require.EqualValues(t, 3, issueBody["status_id"])
	require.EqualValues(t, 4, issueBody["priority_id"])
	require.Equal(t, "New issue", issueBody["subject"])
	require.Equal(t, "body", issueBody["description"])
	require.NotEmpty(t, issueBody["custom_fields"])
}

func TestUpdateIssue_SendsFullFieldSet(t *testing.T) {
	var gotMethod, gotPath string
	var gotBody map[string]any
	svc := newService(t, func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotPath = r.URL.Path
		body, _ := io.ReadAll(r.Body)
		require.NoError(t, json.Unmarshal(body, &gotBody))
		w.WriteHeader(http.StatusOK)
	})

	err := svc.UpdateIssue(context.Background(), 42, issues.IssueWrite{
		StatusID: 5,
		Subject:  "Updated subject",
	})
	require.NoError(t, err)
	require.Equal(t, http.MethodPut, gotMethod)
	require.Equal(t, "/issues/42.json", gotPath)

	issueBody, ok := gotBody["issue"].(map[string]any)
	require.True(t, ok)
	require.EqualValues(t, 5, issueBody["status_id"])
	require.Equal(t, "Updated subject", issueBody["subject"])
}

func TestUpdateIssueFields_PreservesOmittedAndExplicitEmptyDescription(t *testing.T) {
	var bodies []map[string]any
	svc := newService(t, func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		require.NoError(t, json.NewDecoder(r.Body).Decode(&body))
		bodies = append(bodies, body)
		w.WriteHeader(http.StatusNoContent)
	})

	status := 5
	require.NoError(t, svc.UpdateIssueFields(context.Background(), 42, issues.IssueUpdate{StatusID: &status}))
	empty := ""
	require.NoError(t, svc.UpdateIssueFields(context.Background(), 42, issues.IssueUpdate{Description: &empty}))

	first := bodies[0]["issue"].(map[string]any)
	require.EqualValues(t, 5, first["status_id"])
	_, found := first["description"]
	require.False(t, found, "an omitted description must not clear Redmine")
	second := bodies[1]["issue"].(map[string]any)
	require.Equal(t, "", second["description"], "an explicit empty description clears Redmine")
}

func TestUploadAttachment_ThenCreateIssue_IncludesTokenInUploadsArray(t *testing.T) {
	var gotUploadContentType string
	var gotUploadBody []byte
	var gotUploadFilename string
	var gotCreateBody map[string]any

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/uploads.json":
			gotUploadContentType = r.Header.Get("Content-Type")
			gotUploadFilename = r.URL.Query().Get("filename")
			gotUploadBody, _ = io.ReadAll(r.Body)
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{"upload":{"id":1,"token":"abc123.def456"}}`))
		case "/issues.json":
			body, _ := io.ReadAll(r.Body)
			_ = json.Unmarshal(body, &gotCreateBody)
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{"issue":{"id":9,"subject":"with attachment"}}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	client := redmineclient.New(srv.URL, "key", srv.Client())
	svc := issues.New(client)

	upload, err := svc.UploadAttachment(context.Background(), "evidence.txt", "text/plain", strings.NewReader("file contents"))
	require.NoError(t, err)
	require.Equal(t, "abc123.def456", upload.Token)
	require.Equal(t, "application/octet-stream", gotUploadContentType)
	require.Equal(t, "evidence.txt", gotUploadFilename)
	require.Equal(t, "file contents", string(gotUploadBody))

	_, err = svc.CreateIssue(context.Background(), issues.IssueWrite{
		Subject: "with attachment",
		Uploads: []issues.Upload{upload},
	})
	require.NoError(t, err)

	issueBody, ok := gotCreateBody["issue"].(map[string]any)
	require.True(t, ok)
	uploads, ok := issueBody["uploads"].([]any)
	require.True(t, ok)
	require.Len(t, uploads, 1)
	uploadBody, ok := uploads[0].(map[string]any)
	require.True(t, ok)
	require.Equal(t, "abc123.def456", uploadBody["token"])
	require.Equal(t, "evidence.txt", uploadBody["filename"])
	require.Equal(t, "text/plain", uploadBody["content_type"])
}
