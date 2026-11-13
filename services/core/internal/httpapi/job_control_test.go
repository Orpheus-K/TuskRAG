package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"tuskrag.local/core/internal/kb"
)

type jobTestControl struct {
	get       func(context.Context, kb.Actor, int64) (kb.JobSummary, error)
	listPurge func(context.Context, kb.Actor, int64, int64, int32) (kb.PurgeJobPage, error)
	retry     func(context.Context, kb.Actor, int64) (int64, error)
}

func (c jobTestControl) GetJob(ctx context.Context, actor kb.Actor, id int64) (kb.JobSummary, error) {
	return c.get(ctx, actor, id)
}

func (c jobTestControl) ListPurgeJobs(ctx context.Context, actor kb.Actor, kbID, beforeID int64, limit int32) (kb.PurgeJobPage, error) {
	if c.listPurge == nil {
		return kb.PurgeJobPage{}, nil
	}
	return c.listPurge(ctx, actor, kbID, beforeID, limit)
}

func (c jobTestControl) RetryJob(ctx context.Context, actor kb.Actor, id int64) (int64, error) {
	return c.retry(ctx, actor, id)
}

func TestJobControlRequiresCSRFAndUsesRetrySuffix(t *testing.T) {
	calls := 0
	control := jobTestControl{
		get: func(_ context.Context, actor kb.Actor, id int64) (kb.JobSummary, error) {
			if actor.TenantID != 1 || actor.UserID != 2 || id != 7 {
				t.Fatal("incorrect job read target")
			}
			return kb.JobSummary{ID: id, Kind: "purge", Status: "dead", Retryable: true}, nil
		},
		listPurge: func(_ context.Context, actor kb.Actor, kbID, beforeID int64, limit int32) (kb.PurgeJobPage, error) {
			if actor.TenantID != 1 || actor.UserID != 2 || kbID != 3 || beforeID != 0 || limit != 20 {
				t.Fatalf("incorrect purge list params: actor=%+v kb=%d before=%d limit=%d", actor, kbID, beforeID, limit)
			}
			title := "deleted.md"
			return kb.PurgeJobPage{Items: []kb.PurgeJobSummary{{
				ID: 7, KnowledgeBaseID: 3, DocumentID: 4, VersionID: 5,
				DocumentTitle: &title, DocumentDeleted: true, Status: "dead",
				Attempts: 3, MaxAttempts: 3, Retryable: true,
			}}}, nil
		},
		retry: func(_ context.Context, actor kb.Actor, id int64) (int64, error) {
			if actor.TenantID != 1 || actor.UserID != 2 || id != 7 {
				t.Fatal("incorrect job retry target")
			}
			calls++
			return 8, nil
		},
	}
	app := New(Dependencies{Identity: uploadTestIdentity{}, Jobs: control, InsecureLocalCookies: true})
	request := authenticatedUploadRequest(nil)
	request.Method, request.URL.Path = http.MethodPost, "/api/v1/jobs/7:retry"
	request.Header.Del("X-CSRF-Token")
	response := httptest.NewRecorder()
	app.Handler.ServeHTTP(response, request)
	if response.Code != http.StatusForbidden || calls != 0 {
		t.Fatalf("missing CSRF: status=%d calls=%d", response.Code, calls)
	}
	request = authenticatedUploadRequest(nil)
	request.Method, request.URL.Path = http.MethodPost, "/api/v1/jobs/7"
	response = httptest.NewRecorder()
	app.Handler.ServeHTTP(response, request)
	if response.Code != http.StatusUnprocessableEntity && response.Code != http.StatusBadRequest {
		t.Fatalf("missing action suffix: status=%d", response.Code)
	}
	if calls != 0 {
		t.Fatal("missing action suffix reached the retry service")
	}
	request = authenticatedUploadRequest(nil)
	request.Method, request.URL.Path = http.MethodPost, "/api/v1/jobs/7:retry"
	response = httptest.NewRecorder()
	app.Handler.ServeHTTP(response, request)
	var body struct {
		JobID  int64  `json:"job_id"`
		Status string `json:"status"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil || response.Code != http.StatusAccepted || body.JobID != 8 || body.Status != "queued" || calls != 1 {
		t.Fatalf("retry response: status=%d body=%s calls=%d err=%v", response.Code, response.Body.String(), calls, err)
	}
	request = authenticatedUploadRequest(nil)
	request.Method, request.URL.Path = http.MethodGet, "/api/v1/jobs/7"
	response = httptest.NewRecorder()
	app.Handler.ServeHTTP(response, request)
	var summary JobBody
	if err := json.Unmarshal(response.Body.Bytes(), &summary); err != nil || response.Code != http.StatusOK || summary.ID != 7 || summary.Kind != "purge" || !summary.Retryable {
		t.Fatalf("job response: status=%d body=%s err=%v", response.Code, response.Body.String(), err)
	}
	request = authenticatedUploadRequest(nil)
	request.Method, request.URL.Path = http.MethodGet, "/api/v1/kbs/3/purge-jobs"
	response = httptest.NewRecorder()
	app.Handler.ServeHTTP(response, request)
	var purgeList struct {
		Items []PurgeJobBody `json:"items"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &purgeList); err != nil || response.Code != http.StatusOK || len(purgeList.Items) != 1 || purgeList.Items[0].ID != 7 || !purgeList.Items[0].DocumentDeleted || !purgeList.Items[0].Retryable {
		t.Fatalf("purge list response: status=%d body=%s err=%v", response.Code, response.Body.String(), err)
	}
}
