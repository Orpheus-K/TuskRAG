package answer

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
	"tuskrag.local/core/internal/dbquery"
	"tuskrag.local/core/internal/models"
	"tuskrag.local/core/internal/retrieval"
)

var ErrConversationBusy = errors.New("conversation already has an active answer")
var ErrConversationFull = errors.New("conversation has reached the message limit")

type Conversation struct {
	ID              int64     `json:"id"`
	KnowledgeBaseID int64     `json:"kb_id"`
	ModelID         int64     `json:"model_id"`
	Title           string    `json:"title"`
	CreatedAt       time.Time `json:"created_at"`
}
type NewConversation struct {
	KnowledgeBaseID int64  `json:"kb_id" minimum:"1"`
	ModelID         int64  `json:"model_id" minimum:"1"`
	Title           string `json:"title" minLength:"1" maxLength:"120"`
}
type ConversationPage struct {
	Items      []Conversation `json:"items" nullable:"false"`
	NextCursor *int64         `json:"next_cursor" nullable:"true"`
}
type Feedback struct {
	Rating     int16  `json:"rating" enum:"-1,1"`
	Correction string `json:"correction" maxLength:"4000"`
}
type SavedMessage struct {
	ID        string           `json:"id"`
	Question  string           `json:"question"`
	Content   string           `json:"content"`
	Status    string           `json:"status" enum:"running,complete,failed,cancelled"`
	Evidence  []AnswerEvidence `json:"evidence" nullable:"false"`
	Citations Citations        `json:"citations"`
	Degraded  []string         `json:"degraded" nullable:"false"`
	Done      *Done            `json:"done,omitempty"`
	Feedback  *Feedback        `json:"feedback,omitempty"`
	CreatedAt time.Time        `json:"created_at"`
}
type ConversationDetail struct {
	Conversation Conversation   `json:"conversation"`
	Messages     []SavedMessage `json:"messages" nullable:"false"`
}
type storedEvidence struct {
	N          int     `json:"n"`
	ChunkID    int64   `json:"chunk_id"`
	ChunkIDs   []int64 `json:"chunk_ids,omitempty"`
	DocumentID int64   `json:"document_id"`
	VersionID  int64   `json:"version_id"`
	SectionID  int64   `json:"section_id"`
}

func (e storedEvidence) effectiveChunkIDs() []int64 {
	if len(e.ChunkIDs) > 0 {
		return e.ChunkIDs
	}
	if e.ChunkID > 0 {
		return []int64{e.ChunkID}
	}
	return nil
}
type Conversations struct {
	pool    *pgxpool.Pool
	answers *Service
}

func NewConversations(pool *pgxpool.Pool, answers *Service) (*Conversations, error) {
	if pool == nil || answers == nil {
		return nil, errors.New("conversation database and answer service required")
	}
	return &Conversations{pool: pool, answers: answers}, nil
}
func (s *Conversations) begin(ctx context.Context, a retrieval.Actor) (pgx.Tx, *dbquery.Queries, error) {
	if a.TenantID <= 0 || a.UserID <= 0 {
		return nil, nil, retrieval.ErrInvalidQuery
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, nil, err
	}
	q := dbquery.New(tx)
	if _, err = q.SetTenant(ctx, a.TenantID); err != nil {
		rollbackConversation(tx, &err)
		return nil, nil, err
	}
	return tx, q, nil
}
func rollbackConversation(tx pgx.Tx, result *error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := tx.Rollback(ctx); err != nil && !errors.Is(err, pgx.ErrTxClosed) {
		*result = errors.Join(*result, err)
	}
}
func visibleConversation(err error) error {
	if errors.Is(err, pgx.ErrNoRows) {
		return retrieval.ErrNotFound
	}
	return err
}
func conversationValue(r dbquery.Conversation) Conversation {
	return Conversation{ID: r.ID, KnowledgeBaseID: r.KbID, ModelID: r.ModelID, Title: r.Title, CreatedAt: r.CreatedAt.Time}
}

func (s *Conversations) Create(ctx context.Context, a retrieval.Actor, r NewConversation) (_ Conversation, resultErr error) {
	r.Title = strings.TrimSpace(r.Title)
	if r.KnowledgeBaseID <= 0 || r.ModelID <= 0 || r.Title == "" || utf8.RuneCountInString(r.Title) > 120 || !utf8.ValidString(r.Title) {
		return Conversation{}, retrieval.ErrInvalidQuery
	}
	tx, q, err := s.begin(ctx, a)
	if err != nil {
		return Conversation{}, err
	}
	defer rollbackConversation(tx, &resultErr)
	if _, err = q.LockSearchBinding(ctx, dbquery.LockSearchBindingParams{TenantID: a.TenantID, KbID: r.KnowledgeBaseID, UserID: a.UserID}); err != nil {
		return Conversation{}, visibleConversation(err)
	}
	if _, err = q.LoadChatModel(ctx, dbquery.LoadChatModelParams{TenantID: a.TenantID, ID: r.ModelID}); err != nil {
		return Conversation{}, visibleConversation(err)
	}
	row, err := q.CreateConversation(ctx, dbquery.CreateConversationParams{TenantID: a.TenantID, UserID: a.UserID, KbID: r.KnowledgeBaseID, ModelID: r.ModelID, Title: r.Title})
	if err != nil {
		return Conversation{}, err
	}
	if err = q.AuditConversation(ctx, dbquery.AuditConversationParams{TenantID: a.TenantID, UserID: a.UserID, ID: row.ID, Action: "conversation.create"}); err != nil {
		return Conversation{}, err
	}
	if err = tx.Commit(ctx); err != nil {
		return Conversation{}, err
	}
	return conversationValue(row), nil
}
func (s *Conversations) List(ctx context.Context, a retrieval.Actor, kbID, cursor int64) (_ ConversationPage, resultErr error) {
	if kbID <= 0 || cursor < 0 {
		return ConversationPage{}, retrieval.ErrInvalidQuery
	}
	tx, q, err := s.begin(ctx, a)
	if err != nil {
		return ConversationPage{}, err
	}
	defer rollbackConversation(tx, &resultErr)
	if _, err = q.LockSearchBinding(ctx, dbquery.LockSearchBindingParams{TenantID: a.TenantID, UserID: a.UserID, KbID: kbID}); err != nil {
		return ConversationPage{}, visibleConversation(err)
	}
	rows, err := q.ListConversations(ctx, dbquery.ListConversationsParams{TenantID: a.TenantID, UserID: a.UserID, KbID: kbID, Cursor: cursor})
	if err != nil {
		return ConversationPage{}, err
	}
	page := ConversationPage{Items: []Conversation{}}
	if len(rows) > 20 {
		cursor = rows[19].ID
		page.NextCursor = &cursor
		rows = rows[:20]
	}
	for _, r := range rows {
		page.Items = append(page.Items, conversationValue(r))
	}
	if err = tx.Commit(ctx); err != nil {
		return ConversationPage{}, err
	}
	return page, nil
}
func lockConversation(ctx context.Context, q *dbquery.Queries, a retrieval.Actor, id int64) (dbquery.Conversation, error) {
	if id <= 0 {
		return dbquery.Conversation{}, retrieval.ErrInvalidQuery
	}
	row, err := q.LockConversation(ctx, dbquery.LockConversationParams{TenantID: a.TenantID, UserID: a.UserID, ID: id})
	return row, visibleConversation(err)
}
func (s *Conversations) Get(ctx context.Context, a retrieval.Actor, id int64) (_ ConversationDetail, resultErr error) {
	tx, q, err := s.begin(ctx, a)
	if err != nil {
		return ConversationDetail{}, err
	}
	defer rollbackConversation(tx, &resultErr)
	c, err := lockConversation(ctx, q, a, id)
	if err != nil {
		return ConversationDetail{}, err
	}
	messages, _, err := readConversation(ctx, q, a, c)
	if err != nil {
		return ConversationDetail{}, err
	}
	if err = tx.Commit(ctx); err != nil {
		return ConversationDetail{}, err
	}
	return ConversationDetail{Conversation: conversationValue(c), Messages: messages}, nil
}

// Read saved answers only after rechecking every source against its current version.
// Stored evidence contains identifiers, never a second copy of document text.
func readConversation(ctx context.Context, q *dbquery.Queries, a retrieval.Actor, c dbquery.Conversation) ([]SavedMessage, []retrieval.Candidate, error) {
	if err := q.ExpireConversationMessages(ctx, dbquery.ExpireConversationMessagesParams{TenantID: a.TenantID, ConversationID: c.ID}); err != nil {
		return nil, nil, err
	}
	rows, err := q.ConversationMessages(ctx, dbquery.ConversationMessagesParams{TenantID: a.TenantID, ConversationID: c.ID})
	if err != nil {
		return nil, nil, err
	}
	references := make([][]storedEvidence, len(rows))
	ids := []int64{}
	seen := map[int64]bool{}
	for i, r := range rows {
		if err = json.Unmarshal(r.Evidence, &references[i]); err != nil {
			return nil, nil, fmt.Errorf("decode saved evidence: %w", err)
		}
		for _, e := range references[i] {
			cids := e.effectiveChunkIDs()
			if len(cids) == 0 {
				return nil, nil, retrieval.ErrEvidenceChanged
			}
			for _, cid := range cids {
				if !seen[cid] {
					seen[cid] = true
					ids = append(ids, cid)
				}
			}
		}
	}
	principal, err := retrieval.PrincipalTags(ctx, q, a.TenantID, c.KbID, a.UserID)
	if err != nil {
		return nil, nil, err
	}
	evidence, err := q.RecheckSearchEvidence(ctx, dbquery.RecheckSearchEvidenceParams{TenantID: a.TenantID, KbID: c.KbID, UserID: a.UserID, ChunkIds: ids, PrincipalTags: principal, DocTags: []string{}, Mimes: []string{}})
	if err != nil {
		return nil, nil, err
	}
	if len(evidence) != len(ids) {
		return nil, nil, retrieval.ErrEvidenceChanged
	}
	byID := map[int64]dbquery.RecheckSearchEvidenceRow{}
	candidates := []retrieval.Candidate{}
	for _, e := range evidence {
		if e.PageStart.Valid != e.PageEnd.Valid {
			return nil, nil, retrieval.ErrEvidenceChanged
		}
		byID[e.ID] = e
		candidates = append(candidates, retrieval.Candidate{ChunkID: e.ID, DocumentID: e.DocumentID, VersionID: e.VersionID, SectionID: e.SectionID, Ordinal: e.Ordinal, Kind: e.Kind, TokenCount: e.TokenCount, MIME: e.Mime, Content: e.Content, Title: e.Title, HeadingPath: e.HeadingPath})
	}
	messages := make([]SavedMessage, 0, len(rows))
	for i, r := range rows {
		m := SavedMessage{ID: r.ID, Question: r.Question, Status: r.Status, Evidence: []AnswerEvidence{}, Citations: Citations{Items: []Citation{}}, Degraded: []string{}, CreatedAt: r.CreatedAt.Time}
		for j, ref := range references[i] {
			cids := ref.effectiveChunkIDs()
			if ref.N != j+1 || len(cids) == 0 {
				return nil, nil, retrieval.ErrEvidenceChanged
			}
			chunks := make([]sectionChunk, 0, len(cids))
			for _, cid := range cids {
				e, ok := byID[cid]
				if !ok || e.DocumentID != ref.DocumentID || e.VersionID != ref.VersionID || e.SectionID != ref.SectionID {
					return nil, nil, retrieval.ErrEvidenceChanged
				}
				ch := sectionChunk{
					ChunkID: e.ID, DocumentID: e.DocumentID, VersionID: e.VersionID, SectionID: e.SectionID,
					Ordinal: e.Ordinal, Kind: e.Kind, TokenCount: e.TokenCount, Content: e.Content,
					Title: e.Title, MIME: e.Mime, HeadingPath: e.HeadingPath,
				}
				if e.PageStart.Valid && e.PageEnd.Valid && e.Mime == "application/pdf" {
					ps, pe := e.PageStart.Int32, e.PageEnd.Int32
					ch.PageStart, ch.PageEnd = &ps, &pe
				}
				chunks = append(chunks, ch)
			}
			sort.Slice(chunks, func(x, y int) bool {
				if chunks[x].Ordinal != chunks[y].Ordinal {
					return chunks[x].Ordinal < chunks[y].Ordinal
				}
				return chunks[x].ChunkID < chunks[y].ChunkID
			})
			first := chunks[0]
			orderedIDs := make([]int64, len(chunks))
			for idx, ch := range chunks {
				orderedIDs[idx] = ch.ChunkID
			}
			item := AnswerEvidence{
				N: ref.N, ID: fmt.Sprintf("c_%d", first.ChunkID), ChunkIDs: orderedIDs,
				DocumentID: first.DocumentID, VersionID: first.VersionID, SectionID: first.SectionID,
				Content: stitchChunks(chunks), Title: first.Title, HeadingPath: append([]string{}, first.HeadingPath...),
				Pages: aggregatePages(chunks),
			}
			m.Evidence = append(m.Evidence, item)
		}
		if r.Status == "complete" {
			m.Content = r.Content
			m.Degraded = append([]string{}, r.Degraded...)
			if err = json.Unmarshal(r.Citations, &m.Citations); err != nil {
				return nil, nil, err
			}
			for _, cite := range m.Citations.Items {
				if cite.N < 1 || cite.N > len(m.Evidence) || cite.EvidenceID != m.Evidence[cite.N-1].ID {
					return nil, nil, errors.New("saved citation does not match evidence")
				}
			}
			m.Done = &Done{MessageID: m.ID, Outcome: r.Outcome.String, PromptVersion: r.PromptVersion.String}
			if len(r.Usage) > 0 {
				var usage models.ChatUsage
				if err = json.Unmarshal(r.Usage, &usage); err != nil {
					return nil, nil, err
				}
				m.Done.Usage = &usage
			}
			if r.Rating.Valid {
				m.Feedback = &Feedback{Rating: r.Rating.Int16, Correction: r.Correction.String}
			}
		} else {
			m.Evidence = []AnswerEvidence{}
		}
		messages = append(messages, m)
	}
	return messages, candidates, nil
}

func (s *Conversations) start(ctx context.Context, a retrieval.Actor, id int64, question string) (_ ConversationDetail, _ []retrieval.Candidate, _ string, resultErr error) {
	if strings.TrimSpace(question) == "" || len(question) > 4096 || !utf8.ValidString(question) {
		return ConversationDetail{}, nil, "", retrieval.ErrInvalidQuery
	}
	tx, q, err := s.begin(ctx, a)
	if err != nil {
		return ConversationDetail{}, nil, "", err
	}
	defer rollbackConversation(tx, &resultErr)
	c, err := lockConversation(ctx, q, a, id)
	if err != nil {
		return ConversationDetail{}, nil, "", err
	}
	messages, candidates, err := readConversation(ctx, q, a, c)
	if err != nil {
		return ConversationDetail{}, nil, "", err
	}
	for _, m := range messages {
		if m.Status == "running" {
			return ConversationDetail{}, nil, "", ErrConversationBusy
		}
	}
	if len(messages) >= 50 {
		return ConversationDetail{}, nil, "", ErrConversationFull
	}
	messageID := rand.Text()
	if err = q.StartConversationMessage(ctx, dbquery.StartConversationMessageParams{TenantID: a.TenantID, ConversationID: id, ID: messageID, Ordinal: int32(len(messages) + 1), Question: question}); err != nil {
		return ConversationDetail{}, nil, "", err
	}
	if err = tx.Commit(ctx); err != nil {
		return ConversationDetail{}, nil, "", err
	}
	return ConversationDetail{Conversation: conversationValue(c), Messages: messages}, candidates, messageID, nil
}

func (s *Conversations) PutFeedback(ctx context.Context, a retrieval.Actor, messageID string, feedback Feedback) (resultErr error) {
	if len(messageID) < 16 || len(messageID) > 64 || (feedback.Rating != 1 && feedback.Rating != -1) || len(feedback.Correction) > 4000 || !utf8.ValidString(feedback.Correction) {
		return retrieval.ErrInvalidQuery
	}
	tx, q, err := s.begin(ctx, a)
	if err != nil {
		return err
	}
	defer rollbackConversation(tx, &resultErr)
	id, err := q.FeedbackConversationID(ctx, dbquery.FeedbackConversationIDParams{TenantID: a.TenantID, UserID: a.UserID, MessageID: messageID})
	if err != nil {
		return visibleConversation(err)
	}
	c, err := lockConversation(ctx, q, a, id)
	if err != nil {
		return err
	}
	if _, _, err = readConversation(ctx, q, a, c); err != nil {
		return err
	}
	if err = q.PutAnswerFeedback(ctx, dbquery.PutAnswerFeedbackParams{TenantID: a.TenantID, MessageID: messageID, Rating: feedback.Rating, Correction: feedback.Correction}); err != nil {
		return err
	}
	if err = q.AuditConversation(ctx, dbquery.AuditConversationParams{TenantID: a.TenantID, UserID: a.UserID, ID: id, Action: "answer.feedback", MessageID: messageID}); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (s *Conversations) saveEvent(ctx context.Context, a retrieval.Actor, id int64, messageID string, event Event, citations Citations, duration time.Duration) (resultErr error) {
	tx, q, err := s.begin(ctx, a)
	if err != nil {
		return err
	}
	defer rollbackConversation(tx, &resultErr)
	c, err := lockConversation(ctx, q, a, id)
	if err != nil {
		return err
	}
	var changed int64
	switch {
	case event.Retrieval != nil:
		refs := make([]storedEvidence, 0, len(event.Retrieval.Evidence))
		for _, e := range event.Retrieval.Evidence {
			cids := e.ChunkIDs
			if len(cids) == 0 {
				chunk, err := strconv.ParseInt(strings.TrimPrefix(e.ID, "c_"), 10, 64)
				if err != nil {
					return err
				}
				cids = []int64{chunk}
			}
			refs = append(refs, storedEvidence{
				N:          e.N,
				ChunkID:    cids[0],
				ChunkIDs:   append([]int64{}, cids...),
				DocumentID: e.DocumentID,
				VersionID:  e.VersionID,
				SectionID:  e.SectionID,
			})
		}
		raw, err := json.Marshal(refs)
		if err != nil {
			return err
		}
		changed, err = q.SetMessageEvidence(ctx, dbquery.SetMessageEvidenceParams{TenantID: a.TenantID, ConversationID: id, ID: messageID, Evidence: raw, Degraded: event.Retrieval.Degraded, RetrievalRevision: pgtype.Int4{Int32: event.Retrieval.RetrievalRevision, Valid: true}})
		if err != nil {
			return err
		}
	case event.Delta != nil:
		changed, err = q.AppendConversationMessage(ctx, dbquery.AppendConversationMessageParams{TenantID: a.TenantID, ConversationID: id, ID: messageID, Delta: event.Delta.Text})
		if err != nil {
			return err
		}
	case event.Done != nil:
		if err = checkConversationSources(ctx, q, a, c); err != nil {
			return err
		}
		raw, err := json.Marshal(citations)
		if err != nil {
			return err
		}
		var usage []byte
		if event.Done.Usage != nil {
			usage, err = json.Marshal(event.Done.Usage)
			if err != nil {
				return err
			}
		}
		changed, err = q.FinishConversationMessage(ctx, dbquery.FinishConversationMessageParams{TenantID: a.TenantID, ConversationID: id, ID: messageID, Citations: raw, Outcome: pgtype.Text{String: event.Done.Outcome, Valid: true}, PromptVersion: pgtype.Text{String: event.Done.PromptVersion, Valid: true}, Usage: usage, DurationMs: pgtype.Int8{Int64: duration.Milliseconds(), Valid: true}})
		if err != nil {
			return err
		}
		if err = q.AuditConversation(ctx, dbquery.AuditConversationParams{TenantID: a.TenantID, UserID: a.UserID, ID: id, Action: "answer.save", MessageID: messageID}); err != nil {
			return err
		}
	default:
		return errors.New("unsupported saved answer event")
	}
	if changed != 1 {
		return ErrConversationBusy
	}
	return tx.Commit(ctx)
}

// Finalization checks only source identifiers; it never reloads the streamed
// answer text into memory merely to commit a terminal state.
func checkConversationSources(ctx context.Context, q *dbquery.Queries, a retrieval.Actor, c dbquery.Conversation) error {
	rows, err := q.ConversationEvidence(ctx, dbquery.ConversationEvidenceParams{TenantID: a.TenantID, ConversationID: c.ID})
	if err != nil {
		return err
	}
	refs := map[int64]storedEvidence{}
	ids := []int64{}
	for _, raw := range rows {
		var current []storedEvidence
		if err = json.Unmarshal(raw, &current); err != nil {
			return err
		}
		for _, r := range current {
			cids := r.effectiveChunkIDs()
			if len(cids) == 0 {
				return retrieval.ErrEvidenceChanged
			}
			for _, cid := range cids {
				if old, ok := refs[cid]; ok {
					if old.DocumentID != r.DocumentID || old.VersionID != r.VersionID || old.SectionID != r.SectionID {
						return retrieval.ErrEvidenceChanged
					}
					continue
				}
				refs[cid] = r
				ids = append(ids, cid)
			}
		}
	}
	principal, err := retrieval.PrincipalTags(ctx, q, a.TenantID, c.KbID, a.UserID)
	if err != nil {
		return err
	}
	found, err := q.RecheckSearchEvidence(ctx, dbquery.RecheckSearchEvidenceParams{TenantID: a.TenantID, KbID: c.KbID, UserID: a.UserID, ChunkIds: ids, PrincipalTags: principal, DocTags: []string{}, Mimes: []string{}})
	if err != nil {
		return err
	}
	if len(found) != len(ids) {
		return retrieval.ErrEvidenceChanged
	}
	for _, e := range found {
		r := refs[e.ID]
		if r.DocumentID != e.DocumentID || r.VersionID != e.VersionID || r.SectionID != e.SectionID {
			return retrieval.ErrEvidenceChanged
		}
	}
	return nil
}
func (s *Conversations) fail(a retrieval.Actor, messageID string, cancelled bool) (resultErr error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	tx, q, err := s.begin(ctx, a)
	if err != nil {
		return err
	}
	defer rollbackConversation(tx, &resultErr)
	status := "failed"
	if cancelled {
		status = "cancelled"
	}
	if err = q.FailConversationMessage(ctx, dbquery.FailConversationMessageParams{TenantID: a.TenantID, UserID: a.UserID, ID: messageID, Status: status}); err != nil {
		return err
	}
	return tx.Commit(ctx)
}
