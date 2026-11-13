package eval

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"

	"tuskrag.local/core/internal/dbquery"
	"tuskrag.local/core/internal/retrieval"
)

var ErrRunScopeTooLarge = errors.New("evaluation document scope exceeds 10000")

// StartRequest is the transport boundary: callers cannot choose identities,
// visibility scopes, model provenance labels or execution configuration.
type StartRequest struct {
	SetID    int64  `json:"set_id" minimum:"1"`
	Revision int32  `json:"revision" minimum:"1"`
	Mode     string `json:"mode" enum:"lexical,vector,rrf,rerank"`
}
type StartedRun struct {
	ID    int64 `json:"id"`
	JobID int64 `json:"job_id"`
}

func (s *RunStore) Start(ctx context.Context, actor retrieval.Actor, kbID int64, key string, input StartRequest) (_ StartedRun, resultErr error) {
	if input.SetID <= 0 || input.Revision <= 0 || len(key) == 0 || len(key) > 128 || (input.Mode != "lexical" && input.Mode != "vector" && input.Mode != "rrf" && input.Mode != "rerank") {
		return StartedRun{}, ErrInvalidCase
	}
	for _, c := range key {
		if c < 0x21 || c > 0x7e {
			return StartedRun{}, ErrInvalidCase
		}
	}
	data, err := json.Marshal(input)
	if err != nil {
		return StartedRun{}, err
	}
	hash := sha256.Sum256(data)
	tx, q, err := s.sets.begin(ctx, actor, kbID)
	if err != nil {
		return StartedRun{}, err
	}
	defer finishEvaluationTransaction(tx, &resultErr)
	if err := setAccessError(reportMembership(ctx, q, actor, kbID)); err != nil {
		return StartedRun{}, err
	}
	operation := fmt.Sprintf("eval.run.start/%d", kbID)
	_, err = q.ReserveIdempotency(ctx, dbquery.ReserveIdempotencyParams{TenantID: actor.TenantID, UserID: actor.UserID, Operation: operation, RequestKey: key, RequestHash: hash[:]})
	if errors.Is(err, pgx.ErrNoRows) {
		prior, err := q.FindIdempotency(ctx, dbquery.FindIdempotencyParams{TenantID: actor.TenantID, UserID: actor.UserID, Operation: operation, RequestKey: key})
		if err != nil {
			return StartedRun{}, err
		}
		if !prior.ObjectID.Valid {
			return StartedRun{}, errors.New("incomplete evaluation start reservation")
		}
		row, err := q.GetEvalRun(ctx, dbquery.GetEvalRunParams{TenantID: actor.TenantID, KbID: kbID, ID: prior.ObjectID.Int64})
		if err != nil {
			return StartedRun{}, err
		}
		run, err := decodeStoredRun(row)
		if err != nil {
			return StartedRun{}, err
		}
		set, err := loadRunSet(ctx, q, actor, kbID, run.Request.Set)
		if err != nil {
			return StartedRun{}, err
		}
		if set.Digest != run.SetDigest {
			return StartedRun{}, ErrReportInvalid
		}
		if err := runAccess(ctx, q, actor, kbID, run.Request, set.Definition, true); err != nil {
			return StartedRun{}, err
		}
		if !bytes.Equal(prior.RequestHash, hash[:]) {
			return StartedRun{}, ErrRunIdempotencyConflict
		}
		if err := tx.Commit(ctx); err != nil {
			return StartedRun{}, err
		}
		return StartedRun{ID: run.ID, JobID: run.JobID}, nil
	}
	if err != nil {
		return StartedRun{}, err
	}
	set, err := loadRunSet(ctx, q, actor, kbID, SetBinding{SetID: input.SetID, Revision: input.Revision})
	if err != nil {
		return StartedRun{}, err
	}
	documents, err := q.LockEvalRunScope(ctx, dbquery.LockEvalRunScopeParams{TenantID: actor.TenantID, KbID: kbID})
	if err != nil {
		return StartedRun{}, err
	}
	if len(documents) > 10000 {
		if err := checkReportAccess(ctx, q, actor, kbID, documents, nil, nil, true); err != nil {
			return StartedRun{}, setAccessError(err)
		}
		return StartedRun{}, ErrRunScopeTooLarge
	}
	request := RunRequest{Set: SetBinding{SetID: input.SetID, Revision: input.Revision}, Mode: input.Mode, Config: RunConfig{Actor: actor, KnowledgeBaseID: kbID, AllowedDocumentIDs: documents, ModelKind: "real"}}
	if err := runAccess(ctx, q, actor, kbID, request, set.Definition, true); err != nil {
		if errors.Is(err, ErrInvalidCase) {
			return StartedRun{}, ErrSetNotFound
		}
		return StartedRun{}, err
	}
	bound, err := q.LockSearchBinding(ctx, dbquery.LockSearchBindingParams{TenantID: actor.TenantID, KbID: kbID, UserID: actor.UserID})
	if err != nil {
		return StartedRun{}, err
	}
	snapshot := retrieval.ConfigurationSnapshot{Embedding: retrieval.Binding{SpaceID: bound.EmbeddingSpaceID, Revision: bound.ConfigRevision, Dimensions: bound.Dims, Distance: bound.Distance, Normalized: bound.Normalized}}
	saved, err := q.LatestRetrievalConfig(ctx, dbquery.LatestRetrievalConfigParams{TenantID: actor.TenantID, KbID: kbID})
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return StartedRun{}, err
	}
	if err == nil {
		snapshot.RetrievalRevision = saved.Revision
		if input.Mode == "rerank" && saved.RerankProviderID.Valid {
			request.Config.Reranker = &retrieval.RerankOptions{ProviderID: saved.RerankProviderID.Int64, Model: saved.RerankModel.String, TopN: int(saved.RerankTopN.Int32), Fallback: false}
			if saved.RerankMinScore.Valid {
				score := saved.RerankMinScore.Float64
				request.Config.Reranker.MinScore = &score
			}
		}
	}
	if input.Mode == "rerank" && request.Config.Reranker == nil {
		return StartedRun{}, retrieval.ErrRerankUnavailable
	}
	request.Config.Configuration = &snapshot
	request.Config.ModelLabel = fmt.Sprintf("space:%d/retrieval:%d", bound.EmbeddingSpaceID, snapshot.RetrievalRevision)
	if err := validateRunRequest(actor, kbID, key, request); err != nil {
		return StartedRun{}, err
	}
	configuration, err := json.Marshal(request.Config)
	if err != nil {
		return StartedRun{}, err
	}
	if len(configuration) > 1024*1024 {
		return StartedRun{}, ErrInvalidCase
	}
	configHash := sha256.Sum256(configuration)
	run, err := s.insertRun(ctx, tx, q, actor, kbID, key, operation, request, set, configuration, hex.EncodeToString(configHash[:]))
	if err != nil {
		return StartedRun{}, err
	}
	return StartedRun{ID: run.ID, JobID: run.JobID}, nil
}
