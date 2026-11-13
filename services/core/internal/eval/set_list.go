package eval

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"tuskrag.local/core/internal/dbquery"
	"tuskrag.local/core/internal/retrieval"
)

type SetListItem struct {
	ID        int64     `json:"id"`
	Revision  int32     `json:"revision"`
	Digest    string    `json:"digest"`
	Name      string    `json:"name"`
	CaseCount int32     `json:"case_count"`
	CreatedAt time.Time `json:"created_at"`
}
type SetPage struct {
	Items      []SetListItem `json:"items"`
	NextCursor *int64        `json:"next_cursor" nullable:"true"`
}

func (s *SetStore) List(ctx context.Context, actor retrieval.Actor, kbID, cursor int64, limit int32) (_ SetPage, resultErr error) {
	if cursor < 0 || limit < 1 || limit > 10 {
		return SetPage{}, ErrInvalidCase
	}
	tx, q, err := s.begin(ctx, actor, kbID)
	if err != nil {
		return SetPage{}, err
	}
	defer finishEvaluationTransaction(tx, &resultErr)
	if err := setAccessError(reportMembership(ctx, q, actor, kbID)); err != nil {
		return SetPage{}, err
	}
	rows, err := q.ListVisibleEvalSets(ctx, dbquery.ListVisibleEvalSetsParams{TenantID: actor.TenantID, KbID: kbID, Cursor: cursor, PageSize: limit + 1})
	if err != nil {
		return SetPage{}, fmt.Errorf("list evaluation sets: %w", err)
	}
	page := SetPage{Items: make([]SetListItem, 0, len(rows))}
	// Merge all bindings, including lookahead, before acquiring locks so the
	// document -> KB -> membership order is preserved across the entire page.
	bindings := SetDefinition{Cases: make([]CaseDefinition, 0, len(rows))}
	for _, row := range rows {
		var locations []ExpectedLocation
		if err := json.Unmarshal(row.Locations, &locations); err != nil {
			return SetPage{}, fmt.Errorf("decode evaluation set bindings: %w", err)
		}
		bindings.Cases = append(bindings.Cases, CaseDefinition{Expected: locations})
		page.Items = append(page.Items, SetListItem{ID: row.ID, Revision: row.Revision, Digest: row.Digest, Name: row.Name, CaseCount: row.CaseCount, CreatedAt: row.CreatedAt.Time})
	}
	// An empty page must still reject revoked membership, not report no matches.
	if err := checkSetAccess(ctx, q, actor, kbID, bindings, false); err != nil {
		return SetPage{}, err
	}
	if len(page.Items) > int(limit) {
		page.Items = page.Items[:limit]
		next := page.Items[len(page.Items)-1].ID
		page.NextCursor = &next
	}
	if err := tx.Commit(ctx); err != nil {
		return SetPage{}, fmt.Errorf("commit evaluation set list: %w", err)
	}
	return page, nil
}
