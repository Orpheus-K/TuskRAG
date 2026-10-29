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
	get   func(context.Context, kb.Actor, int64) (kb.JobSummary, error)
	retry func(context.Context, kb.Actor, int64) (int64, error)
}

func (c jobTestControl) GetJob(ctx context.Context, actor kb.Actor, id int64) (kb.JobSummary, error) {
	return c.get(ctx, actor, id)
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
			return kb.JobSummary{ID: id, Status: "dead", Retryable: true}, nil
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
	if err := json.Unmarshal(response.Body.Bytes(), &summary); err != nil || response.Code != http.StatusOK || summary.ID != 7 || !summary.Retryable {
		t.Fatalf("job response: status=%d body=%s err=%v", response.Code, response.Body.String(), err)
	}
}
