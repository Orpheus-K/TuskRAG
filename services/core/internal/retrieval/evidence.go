package retrieval

import (
	"context"
	"errors"
	"slices"

	"github.com/jackc/pgx/v5"
	"tuskrag.local/core/internal/dbquery"
	"tuskrag.local/core/internal/models"
)

var ErrEvidenceChanged = errors.New("answer evidence is no longer available")

// AuthorizeEvidence fences the entire generation snapshot, including empty results.
// Locks last only for this read; no model or socket I/O happens in the transaction.
func (d *Driver) AuthorizeEvidence(ctx context.Context, actor Actor, kbID int64, evidence []Candidate) (_ models.EgressScope, resultErr error) {
	tx, q, err := d.begin(ctx, actor, kbID)
	if err != nil {
		return models.EgressScope{}, err
	}
	defer rollback(tx, &resultErr)
	binding, err := q.LockSearchBinding(ctx, dbquery.LockSearchBindingParams{TenantID: actor.TenantID, KbID: kbID, UserID: actor.UserID})
	if errors.Is(err, pgx.ErrNoRows) {
		return models.EgressScope{}, ErrNotFound
	}
	if err != nil {
		return models.EgressScope{}, err
	}
	ids := make([]int64, len(evidence))
	for i, e := range evidence {
		ids[i] = e.ChunkID
	}
	principal, err := PrincipalTags(ctx, q, actor.TenantID, kbID, actor.UserID)
	if err != nil {
		return models.EgressScope{}, err
	}
	rows, err := q.RecheckSearchEvidence(ctx, dbquery.RecheckSearchEvidenceParams{TenantID: actor.TenantID, KbID: kbID, UserID: actor.UserID, ChunkIds: ids, PrincipalTags: principal, DocTags: []string{}, Mimes: []string{}})
	if err != nil {
		return models.EgressScope{}, err
	}
	if len(rows) != len(evidence) {
		return models.EgressScope{}, ErrEvidenceChanged
	}
	byID := make(map[int64]dbquery.RecheckSearchEvidenceRow, len(rows))
	for _, r := range rows {
		byID[r.ID] = r
	}
	for _, e := range evidence {
		r, ok := byID[e.ChunkID]
		if !ok || r.DocumentID != e.DocumentID || r.VersionID != e.VersionID || r.SectionID != e.SectionID || r.Content != e.Content || r.Title != e.Title || !slices.Equal(r.HeadingPath, e.HeadingPath) {
			return models.EgressScope{}, ErrEvidenceChanged
		}
	}
	scope, err := q.GetAuthorizedSearchScope(ctx, dbquery.GetAuthorizedSearchScopeParams{TenantID: actor.TenantID, KbID: kbID, UserID: actor.UserID, SpaceID: binding.EmbeddingSpaceID, ConfigRevision: binding.ConfigRevision})
	if errors.Is(err, pgx.ErrNoRows) {
		return models.EgressScope{}, ErrNotFound
	}
	if err != nil {
		return models.EgressScope{}, err
	}
	if err = tx.Commit(ctx); err != nil {
		return models.EgressScope{}, err
	}
	return models.EgressScope{TenantID: actor.TenantID, KnowledgeBaseID: kbID, TenantPolicy: models.EgressPolicy(scope.EgressPolicyMax), KnowledgeBasePolicy: models.EgressPolicy(scope.EgressPolicy)}, nil
}
