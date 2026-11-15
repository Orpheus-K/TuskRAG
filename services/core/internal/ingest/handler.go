package ingest

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"

	"tuskrag.local/core/internal/db"
	"tuskrag.local/core/internal/dbquery"
	"tuskrag.local/core/internal/indexing"
	"tuskrag.local/core/internal/jobs"
	"tuskrag.local/core/internal/models"
	"tuskrag.local/core/internal/parser"
	"tuskrag.local/core/internal/tokenizer"
)

type IngestSettings struct {
	ParentMaxTokens int
	ChildTargetMin  int
	ChildTargetMax  int
	ChildHardMax    int
	OverlapMax      int
}

func DefaultIngestSettings() IngestSettings {
	return IngestSettings{ParentMaxTokens: 1500, ChildTargetMin: 300, ChildTargetMax: 500, ChildHardMax: 800, OverlapMax: 60}
}

type IngestHandler struct {
	queue       *jobs.Store
	parse       *ParseStage
	embedding   *EmbeddingStage
	plans       *PlanStage
	publication *PublicationStage
	settings    IngestSettings
}

var _ jobs.Handler = (*IngestHandler)(nil)

func NewIngestHandler(queue *jobs.Store, parse *ParseStage, embedding *EmbeddingStage, settings IngestSettings) (*IngestHandler, error) {
	if queue == nil || parse == nil || embedding == nil || parse.queue != queue || embedding.queue != queue || embedding.count == nil ||
		settings.ParentMaxTokens <= 0 || settings.ChildTargetMin <= 0 || settings.ChildTargetMax < settings.ChildTargetMin ||
		settings.ChildHardMax < settings.ChildTargetMax || settings.ChildHardMax > maxTextChunkTokens ||
		settings.OverlapMax < 0 || settings.OverlapMax > maxTextOverlapTokens || settings.OverlapMax > settings.ChildHardMax {
		return nil, errors.New("valid ingest stages and chunk settings are required")
	}
	plans, err := NewPlanStage(queue)
	if err != nil {
		return nil, err
	}
	publication, err := NewPublicationStage(queue)
	if err != nil {
		return nil, err
	}
	return &IngestHandler{queue: queue, parse: parse, embedding: embedding, plans: plans, publication: publication, settings: settings}, nil
}

// Prepare connects the resumable ingestion stages; the worker applies the
// returned publication effects in its fenced completion transaction.
func (h *IngestHandler) Prepare(ctx context.Context, job jobs.ClaimedJob) (func(context.Context, jobs.EffectsTx) error, error) {
	if h == nil || h.queue == nil || h.parse == nil || h.embedding == nil || h.plans == nil || h.publication == nil || ctx == nil {
		return nil, errors.New("initialized ingest handler and context are required")
	}
	ids, err := parseJob(job)
	if err != nil {
		return nil, &jobs.ProcessingFailure{Code: jobs.Internal, Cause: err}
	}
	job.Payload = bytes.Clone(job.Payload)
	document, err := h.parse.Parse(ctx, job)
	if err != nil {
		return nil, h.classify(ctx, job, ids, err)
	}
	target, err := h.target(ctx, job, ids)
	if err != nil {
		return nil, h.classify(ctx, job, ids, err)
	}
	quality, err := AssessQuality(ctx, document)
	if err != nil {
		return nil, h.classify(ctx, job, ids, err)
	}
	report, err := buildParseReport(ctx, document, quality, target.MIME)
	if err != nil {
		return nil, h.classify(ctx, job, ids, err)
	}
	if err := h.parse.SaveReport(ctx, job, report); err != nil {
		return nil, h.classify(ctx, job, ids, err)
	}
	if quality.NeedsReview {
		return nil, h.parse.failure(job, ids, jobs.QualityReview, errors.New("parsed pages need review"))
	}
	normalized, err := AssembleMarkdown(ctx, document)
	if err != nil {
		return nil, h.classify(ctx, job, ids, err)
	}
	if scan := ScanInjection(normalized.Markdown, "native"); scan.Detected {
		if saveErr := h.saveInjectionFindings(ctx, job, ids, scan.Findings); saveErr != nil {
			return nil, h.classify(ctx, job, ids, saveErr)
		}
		return nil, h.parse.failure(job, ids, jobs.QualityReview, errors.New("prompt injection pattern detected; requires review"))
	}
	headers := sourceTableHeaders(target.MIME, document.Meta.ParserProfile, target.ConfirmFirstRowHeaders, normalized)
	plan, err := BuildPlan(ctx, PlanBuildRequest{Document: normalized, MIME: target.MIME,
		EmbeddingSpaceID: target.SpaceID, EmbeddingProfile: target.Profile, ConfigRevision: target.Revision,
		ParentMaxTokens: h.settings.ParentMaxTokens, ChildTargetMin: h.settings.ChildTargetMin,
		ChildTargetMax: h.settings.ChildTargetMax, ChildHardMax: h.settings.ChildHardMax,
		OverlapMax: h.settings.OverlapMax, CountTokens: h.embedding.count, TableHeaders: headers})
	if err != nil {
		return nil, h.classify(ctx, job, ids, err)
	}
	staged, err := h.plans.Stage(ctx, job, plan)
	if err != nil {
		return nil, h.classify(ctx, job, ids, err)
	}
	if err := h.parse.VerifyOriginal(ctx, job); err != nil {
		return nil, h.classify(ctx, job, ids, err)
	}
	if err := h.embedding.Run(ctx, job, plan, staged); err != nil {
		return nil, h.classify(ctx, job, ids, err)
	}
	if err := h.parse.VerifyOriginal(ctx, job); err != nil {
		return nil, h.classify(ctx, job, ids, err)
	}
	effects, err := h.publication.Prepare(ctx, job, plan, staged)
	if err != nil {
		return nil, h.classify(ctx, job, ids, err)
	}
	return func(ctx context.Context, tx jobs.EffectsTx) error {
		if err := effects(ctx, tx); err != nil {
			return h.classify(ctx, job, ids, err)
		}
		return nil
	}, nil
}

type ingestTarget struct {
	MIME                   string
	SpaceID                int64
	Revision               int32
	Profile                EmbeddingProfile
	ConfirmFirstRowHeaders bool
}

func (h *IngestHandler) target(ctx context.Context, job jobs.ClaimedJob, ids documentIDs) (ingestTarget, error) {
	var target ingestTarget
	err := h.queue.WithLease(ctx, job.Lease, func(ctx context.Context, tx jobs.EffectsTx) error {
		queries := dbquery.New(tx)
		if err := verifyClaimedIngestJob(ctx, queries, job); err != nil {
			return err
		}
		row, err := queries.LockPlanTarget(ctx, dbquery.LockPlanTargetParams{
			TenantID: job.Lease.TenantID, KbID: ids.KBID, DocumentID: ids.DocumentID, VersionID: ids.VersionID,
		})
		if errors.Is(err, pgx.ErrNoRows) {
			return errTarget
		}
		if err != nil {
			return fmt.Errorf("load ingest target: %w", err)
		}
		if row.Status != "embedding" && row.Status != "indexing" || row.CreatedBy <= 0 || !bytes.Equal(row.ContentSha256, row.ParsedSha256) {
			return ErrPlanCorrupt
		}
		target = ingestTarget{MIME: row.Mime, SpaceID: row.EmbeddingSpaceID, Revision: row.ConfigRevision, ConfirmFirstRowHeaders: row.ConfirmFirstRowHeaders,
			Profile: EmbeddingProfile{ProviderID: row.ProviderID, Model: row.Model, Dimensions: row.Dims, Normalized: row.Normalized, Distance: row.Distance}}
		if err := db.CheckVersionGuards(ctx, tx); err != nil {
			return err
		}
		return indexing.Check(ctx, tx, target.SpaceID, target.Profile.Dimensions, target.Profile.Distance)
	})
	return target, err
}

func (h *IngestHandler) saveInjectionFindings(ctx context.Context, job jobs.ClaimedJob, ids documentIDs, findings []InjectionFinding) error {
	encoded, err := json.Marshal(findings)
	if err != nil {
		return fmt.Errorf("encode injection findings: %w", err)
	}
	return h.queue.WithLease(ctx, job.Lease, func(ctx context.Context, tx jobs.EffectsTx) error {
		queries := dbquery.New(tx)
		return queries.SaveDocumentVersionInjectionFindings(ctx, dbquery.SaveDocumentVersionInjectionFindingsParams{
			TenantID:   job.Lease.TenantID,
			KbID:       ids.KBID,
			DocumentID: ids.DocumentID,
			VersionID:  ids.VersionID,
			Findings:   encoded,
		})
	})
}

func sourceTableHeaders(mime, profile string, confirmed bool, document NormalizedDocument) map[BlockPosition]int {
	if !confirmed && (mime != "text/markdown" || profile != parser.NativeMarkdownProfile) {
		return nil
	}
	// GFM declares the first row as a header; other formats require user confirmation.
	headers := make(map[BlockPosition]int)
	for _, section := range document.Sections {
		for index, block := range section.Blocks {
			if block.Kind == "table" {
				headers[BlockPosition{Section: section.Ordinal, Block: index + 1}] = 1
			}
		}
	}
	return headers
}

func (h *IngestHandler) classify(ctx context.Context, job jobs.ClaimedJob, ids documentIDs, err error) error {
	if err == nil {
		return nil
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	var failure *jobs.ProcessingFailure
	if errors.As(err, &failure) || errors.Is(err, jobs.ErrLeaseLost) {
		return err
	}
	if errors.Is(err, ErrVersionSuperseded) {
		return h.supersededFailure(job, ids, err)
	}
	var upstream *models.UpstreamError
	if errors.As(err, &upstream) {
		code := jobs.ModelRejected
		if upstream.Retryable {
			code = jobs.ModelUnavailable
		}
		return h.parse.failure(job, ids, code, err)
	}
	var code jobs.FailureCode
	switch {
	case errors.Is(err, context.DeadlineExceeded):
		code = jobs.Timeout
	case errors.Is(err, indexing.ErrIndexMissing), errors.Is(err, indexing.ErrIndexBusy), errors.Is(err, indexing.ErrIndexInvalid), errors.Is(err, indexing.ErrIndexConflict):
		code = jobs.IndexUnavailable
	case errors.Is(err, indexing.ErrUnsupportedDistance):
		code = jobs.UnsupportedEmbedding
	case errors.Is(err, models.ErrEgressDenied):
		code = jobs.EgressDenied
	case errors.Is(err, models.ErrTokenizerMismatch), errors.Is(err, models.ErrTokenizerUsageMissing):
		code = jobs.TokenizerMismatch
	case errors.Is(err, models.ErrInvalidResponse), errors.Is(err, ErrInvalidVector), errors.Is(err, ErrInvalidTokenCount):
		code = jobs.ModelInvalid
	case errors.Is(err, models.ErrRuntimeChanged), errors.Is(err, models.ErrRuntimeUnavailable), errors.Is(err, ErrPlanConflict):
		code = jobs.ConfigurationChanged
	case errors.Is(err, ErrTableHeaderUnconfirmed), errors.Is(err, ErrTableHeaderMissing):
		code = jobs.TableHeaderRequired
	case errors.Is(err, ErrPlanLimit), errors.Is(err, ErrNormalizedLimit), errors.Is(err, ErrParentUnitTooLarge),
		errors.Is(err, ErrTextUnitTooLarge), errors.Is(err, ErrStructuredUnitTooLarge), errors.Is(err, ErrTableUnitTooLarge),
		errors.Is(err, errCountInputTooLarge), errors.Is(err, tokenizer.ErrInputTooLarge):
		code = jobs.LimitExceeded
	case errors.Is(err, ErrUnsupportedParsedDocument), errors.Is(err, ErrUnsupportedChunkMIME), errors.Is(err, parser.ErrUnsupportedFormat):
		code = jobs.Unsupported
	case errors.Is(err, ErrInvalidParsedDocument), errors.Is(err, ErrInvalidPlan):
		code = jobs.ParseError
	case errors.Is(err, ErrPlanCorrupt), errors.Is(err, ErrVectorsIncomplete), errors.Is(err, ErrJobMismatch):
		code = jobs.Internal
	case errors.Is(err, errTarget):
		return &jobs.ProcessingFailure{Code: jobs.Internal, Cause: err}
	default:
		return err
	}
	return h.parse.failure(job, ids, code, err)
}
