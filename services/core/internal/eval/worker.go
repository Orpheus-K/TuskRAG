package eval

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"slices"
	"time"

	"github.com/jackc/pgx/v5"

	"tuskrag.local/core/internal/dbquery"
	"tuskrag.local/core/internal/jobs"
	"tuskrag.local/core/internal/models"
	"tuskrag.local/core/internal/retrieval"
)

type RunHandler struct {
	store  *RunStore
	search Searcher
}

func NewRunHandler(store *RunStore, search Searcher) (*RunHandler, error) {
	if store == nil || search == nil {
		return nil, errors.New("evaluation store and search service are required")
	}
	return &RunHandler{store: store, search: search}, nil
}

// Prepare holds the lease only while reading metadata. Model work runs outside
// transactions; the returned effect can only commit through this lease's Complete.
func (h *RunHandler) Prepare(ctx context.Context, job jobs.ClaimedJob) (func(context.Context, jobs.EffectsTx) error, error) {
	if ctx == nil || h == nil {
		return nil, ErrInvalidCase
	}
	var payload RunJobPayload
	decoder := json.NewDecoder(bytes.NewReader(job.Payload))
	decoder.DisallowUnknownFields()
	if job.Kind != jobs.EvalRun || len(job.Payload) > 256 || decoder.Decode(&payload) != nil || payload.RunID <= 0 {
		return nil, executionFailure(ErrInvalidCase)
	}
	var extra any
	if decoder.Decode(&extra) != io.EOF {
		return nil, executionFailure(ErrInvalidCase)
	}
	var run StoredRun
	err := h.store.queue.WithLease(ctx, job.Lease, func(ctx context.Context, tx jobs.EffectsTx) error {
		q := dbquery.New(tx)
		row, err := q.GetClaimedEvalRun(ctx, dbquery.GetClaimedEvalRunParams{TenantID: job.Lease.TenantID, ID: payload.RunID, JobID: job.Lease.JobID})
		if errors.Is(err, pgx.ErrNoRows) {
			return executionFailure(ErrRunNotFound)
		}
		if err != nil {
			return err
		}
		run, err = decodeStoredRun(row)
		if err != nil {
			return executionFailure(err)
		}
		return executionFailure(h.store.checkExecution(ctx, q, run.Request.Config.Actor, run, nil, true))
	})
	if err != nil {
		return nil, err
	}
	workCtx, cancel := context.WithTimeout(ctx, 30*time.Minute)
	defer cancel()
	report, err := RunSet(workCtx, h.search, h.store.sets, run.Request.Set, run.Request.Config, run.Request.Mode)
	if err != nil {
		return nil, executionFailure(err)
	}
	data, err := json.Marshal(report)
	if err != nil || len(data) > 8*1024*1024 {
		return nil, executionFailure(ErrReportInvalid)
	}
	hash := sha256.Sum256(data)
	digest := hex.EncodeToString(hash[:])
	return func(ctx context.Context, tx jobs.EffectsTx) error {
		if err := h.store.queue.CheckCompletion(tx, job.Lease); err != nil {
			return err
		}
		q := dbquery.New(tx)
		if err := h.store.checkExecution(ctx, q, run.Request.Config.Actor, run, &report, true); err != nil {
			return executionFailure(err)
		}
		if err := q.InsertEvalRunResult(ctx, dbquery.InsertEvalRunResultParams{RunID: run.ID, TenantID: job.Lease.TenantID, KbID: run.Request.Config.KnowledgeBaseID, Digest: digest, Report: data}); err != nil {
			return err
		}
		return q.AuditEvalRunResult(ctx, dbquery.AuditEvalRunResultParams{TenantID: job.Lease.TenantID, KbID: run.Request.Config.KnowledgeBaseID, UserID: run.Request.Config.Actor.UserID, RunID: run.ID, Digest: digest})
	}, nil
}

func (s *RunStore) checkExecution(ctx context.Context, q *dbquery.Queries, actor retrieval.Actor, run StoredRun, report *RunReport, writing bool) error {
	c := run.Request.Config
	set, err := loadRunSet(ctx, q, actor, c.KnowledgeBaseID, run.Request.Set)
	if err != nil {
		return err
	}
	if set.Digest != run.SetDigest {
		return ErrReportInvalid
	}
	if err := runAccess(ctx, q, actor, c.KnowledgeBaseID, run.Request, set.Definition, writing); err != nil {
		return err
	}
	if writing {
		if err := checkRunConfiguration(ctx, q, actor, c.KnowledgeBaseID, *c.Configuration); err != nil {
			return err
		}
	}
	if report == nil {
		return nil
	}
	if report.DatasetVersion != fmt.Sprintf("set:%d:%d", run.Request.Set.SetID, run.Request.Set.Revision) || report.DatasetSHA256 != run.SetDigest || report.ConfigSHA256 != run.ConfigDigest || report.Mode != run.Request.Mode || report.ModelKind != c.ModelKind || report.ModelLabel != c.ModelLabel || len(report.Rows) != len(set.Definition.Cases) {
		return ErrReportInvalid
	}
	evidence, docs, chunks, err := reportBindings(*report)
	if err != nil {
		return err
	}
	for _, id := range docs {
		if !slices.Contains(c.AllowedDocumentIDs, id) {
			return ErrReportInvalid
		}
	}
	return checkReportAccess(ctx, q, actor, c.KnowledgeBaseID, docs, chunks, evidence, writing)
}

// Domain failures become bounded job failures, never success or an empty report.
// Cancellation and lost leases propagate so an obsolete worker cannot mutate state.
func executionFailure(err error) error {
	if err == nil || errors.Is(err, context.Canceled) || errors.Is(err, jobs.ErrLeaseLost) {
		return err
	}
	code := jobs.Internal
	var upstream *models.UpstreamError
	var network net.Error
	switch {
	case errors.Is(err, ErrSetNotFound), errors.Is(err, ErrSetForbidden), errors.Is(err, ErrReportNotFound), errors.Is(err, ErrReportForbidden), errors.Is(err, retrieval.ErrNotFound):
		code = jobs.PermissionRevoked
	case errors.Is(err, retrieval.ErrConfigurationChanged), errors.Is(err, retrieval.ErrBindingChanged), errors.Is(err, ErrRetrievalRevisionChanged), errors.Is(err, ErrSetArchived), errors.Is(err, ErrReportArchived):
		code = jobs.ConfigurationChanged
	case errors.Is(err, context.DeadlineExceeded):
		code = jobs.Timeout
	case errors.Is(err, models.ErrEgressDenied):
		code = jobs.EgressDenied
	case errors.Is(err, models.ErrInvalidResponse):
		code = jobs.ModelInvalid
	case errors.As(err, &upstream):
		code = jobs.ModelRejected
		if upstream.Retryable {
			code = jobs.ModelUnavailable
		}
	case errors.As(err, &network):
		code = jobs.ModelUnavailable
	}
	return &jobs.ProcessingFailure{Code: code, Cause: err}
}
