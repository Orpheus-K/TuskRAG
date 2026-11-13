package httpapi

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

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
	Kind            string  `json:"kind" enum:"ingest,purge"`
	KnowledgeBaseID int64   `json:"kb_id"`
	DocumentID      int64   `json:"document_id"`
	VersionID       int64   `json:"version_id"`
	Status          string  `json:"status"`
	Attempts        int32   `json:"attempts"`
	MaxAttempts     int32   `json:"max_attempts"`
	FailureCode     *string `json:"failure_code" nullable:"true"`
	Retryable       bool    `json:"retryable"`
}

type PurgeJobListInput struct {
	ID     int64 `path:"kb" minimum:"1"`
	Cursor int64 `query:"cursor" minimum:"0" default:"0"`
	Limit  int32 `query:"limit" minimum:"1" maximum:"50" default:"20"`
}

type PurgeJobBody struct {
	ID              int64      `json:"id"`
	KnowledgeBaseID int64      `json:"kb_id"`
	DocumentID      int64      `json:"document_id"`
	VersionID       int64      `json:"version_id"`
	DocumentTitle   *string    `json:"document_title" nullable:"true"`
	DocumentDeleted bool       `json:"document_deleted"`
	Status          string     `json:"status" enum:"queued,running,succeeded,failed,dead,cancelled"`
	Attempts        int32      `json:"attempts"`
	MaxAttempts     int32      `json:"max_attempts"`
	FailureCode     *string    `json:"failure_code" nullable:"true"`
	Retryable       bool       `json:"retryable"`
	CreatedAt       time.Time  `json:"created_at"`
	FinishedAt      *time.Time `json:"finished_at" nullable:"true"`
}

type PurgeJobListOutput struct {
	Body struct {
		Items      []PurgeJobBody `json:"items"`
		NextCursor *int64         `json:"next_cursor" nullable:"true"`
	}
}

type RetryJobOutput struct {
	Body struct {
		JobID  int64  `json:"job_id"`
		Status string `json:"status" enum:"queued"`
	}
}

func registerJobControl(api huma.API, deps Dependencies) {
	huma.Register(api, huma.Operation{OperationID: "list-purge-jobs", Method: http.MethodGet, Path: "/kbs/{kb}/purge-jobs"},
		func(ctx context.Context, input *PurgeJobListInput) (*PurgeJobListOutput, error) {
			if deps.Jobs == nil {
				return nil, statusError(ctx, http.StatusServiceUnavailable, "dependency_unavailable", "job service unavailable", true)
			}
			page, err := deps.Jobs.ListPurgeJobs(ctx, currentActor(ctx), input.ID, input.Cursor, input.Limit)
			if err != nil {
				return nil, knowledgeError(ctx, "list purge jobs", err)
			}
			output := &PurgeJobListOutput{}
			output.Body.Items = make([]PurgeJobBody, 0, len(page.Items))
			output.Body.NextCursor = page.NextBefore
			for _, item := range page.Items {
				output.Body.Items = append(output.Body.Items, PurgeJobBody{
					ID: item.ID, KnowledgeBaseID: item.KnowledgeBaseID, DocumentID: item.DocumentID, VersionID: item.VersionID,
					DocumentTitle: item.DocumentTitle, DocumentDeleted: item.DocumentDeleted, Status: item.Status,
					Attempts: item.Attempts, MaxAttempts: item.MaxAttempts, FailureCode: item.FailureCode,
					Retryable: item.Retryable, CreatedAt: item.CreatedAt, FinishedAt: item.FinishedAt,
				})
			}
			return output, nil
		})
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
			return &struct{ Body JobBody }{Body: JobBody{ID: job.ID, Kind: job.Kind, KnowledgeBaseID: job.KnowledgeBaseID,
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
