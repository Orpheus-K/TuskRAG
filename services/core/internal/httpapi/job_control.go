package httpapi

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/danielgtaylor/huma/v2"

	"tuskrag.local/core/internal/kb"
)

// Both operations use a full-segment wildcard because net/http does not allow
// a suffix outside a wildcard. The POST parameter includes ":retry".
type JobPath struct {
	Job string `path:"job" pattern:"^[1-9][0-9]{0,18}$"`
}

type RetryJobInput struct {
	Job  string `path:"job" pattern:"^[1-9][0-9]{0,18}:retry$"`
	CSRF string `header:"X-CSRF-Token" required:"true"`
}

type JobBody struct {
	ID              int64   `json:"id"`
	KnowledgeBaseID int64   `json:"kb_id"`
	DocumentID      int64   `json:"document_id"`
	VersionID       int64   `json:"version_id"`
	Status          string  `json:"status"`
	Attempts        int32   `json:"attempts"`
	MaxAttempts     int32   `json:"max_attempts"`
	FailureCode     *string `json:"failure_code" nullable:"true"`
	Retryable       bool    `json:"retryable"`
}

type RetryJobOutput struct {
	Body struct {
		JobID  int64  `json:"job_id"`
		Status string `json:"status" enum:"queued"`
	}
}

func registerJobControl(api huma.API, deps Dependencies) {
	huma.Register(api, huma.Operation{OperationID: "get-job", Method: http.MethodGet, Path: "/jobs/{job}"},
		func(ctx context.Context, input *JobPath) (*struct{ Body JobBody }, error) {
			if deps.Jobs == nil {
				return nil, statusError(ctx, http.StatusServiceUnavailable, "dependency_unavailable", "job service unavailable", true)
			}
			id, err := strconv.ParseInt(input.Job, 10, 64)
			if err != nil || id <= 0 {
				return nil, statusError(ctx, http.StatusBadRequest, "invalid_request", "invalid job ID", false)
			}
			job, err := deps.Jobs.GetJob(ctx, currentActor(ctx), id)
			if err != nil {
				return nil, knowledgeError(ctx, "get job", err)
			}
			return &struct{ Body JobBody }{Body: JobBody{ID: job.ID, KnowledgeBaseID: job.KnowledgeBaseID,
				DocumentID: job.DocumentID, VersionID: job.VersionID, Status: job.Status, Attempts: job.Attempts,
				MaxAttempts: job.MaxAttempts, FailureCode: job.FailureCode, Retryable: job.Retryable}}, nil
		})
	huma.Register(api, huma.Operation{OperationID: "retry-job", Method: http.MethodPost, Path: "/jobs/{job}", DefaultStatus: http.StatusAccepted},
		func(ctx context.Context, input *RetryJobInput) (*RetryJobOutput, error) {
			if deps.Jobs == nil {
				return nil, statusError(ctx, http.StatusServiceUnavailable, "dependency_unavailable", "job service unavailable", true)
			}
			if !strings.HasSuffix(input.Job, ":retry") {
				return nil, statusError(ctx, http.StatusBadRequest, "invalid_request", "invalid job action", false)
			}
			id, err := strconv.ParseInt(strings.TrimSuffix(input.Job, ":retry"), 10, 64)
			if err != nil || id <= 0 {
				return nil, statusError(ctx, http.StatusBadRequest, "invalid_request", "invalid job ID", false)
			}
			newID, err := deps.Jobs.RetryJob(ctx, currentActor(ctx), id)
			if errors.Is(err, kb.ErrJobNotRetryable) {
				return nil, statusError(ctx, http.StatusConflict, "job_not_retryable", "job cannot be retried", false)
			}
			if errors.Is(err, kb.ErrArchived) {
				return nil, statusError(ctx, http.StatusConflict, "knowledge_base_archived", "knowledge base is archived", false)
			}
			if err != nil {
				return nil, knowledgeError(ctx, "retry job", err)
			}
			output := &RetryJobOutput{}
			output.Body.JobID, output.Body.Status = newID, "queued"
			return output, nil
		})
}
