package kb

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"tuskrag.local/core/internal/dbquery"
	"tuskrag.local/core/internal/jobs"
	"tuskrag.local/core/internal/storage"
)

var (
	ErrDuplicateContent = errors.New("document content already exists in the knowledge base")
	ErrArchived         = errors.New("knowledge base is archived")
	ErrUnsupportedMIME  = errors.New("document format is not supported")
	ErrVersionFormat    = errors.New("new version format must match the document")
	ErrCommitUncertain  = errors.New("document commit outcome is uncertain")
)

// MaxDocumentBytes bounds both stored originals and upload requests.
const MaxDocumentBytes int64 = 200 * 1024 * 1024

type DocumentQueue interface {
	EnqueueTx(context.Context, pgx.Tx, int64, jobs.Request) (int64, error)
	LockActiveTx(context.Context, pgx.Tx, int64, jobs.Kind, string, json.RawMessage) (int64, error)
	CancelTx(context.Context, pgx.Tx, int64, int64) (jobs.Kind, error)
}

type DocumentService struct {
	knowledge *Service
	files     storage.Store
	queue     DocumentQueue
}

type UploadRequest struct {
	KBID                   int64
	DocumentID             int64
	Title                  string
	MIME                   string
	IdempotencyKey         string
	AllowDuplicate         bool
	ConfirmFirstRowHeaders bool
	Content                io.Reader
}

type UploadedDocument struct {
	DocumentID int64
	VersionID  int64
	Title      string
	MIME       string
	Status     string
	ByteSize   int64
	CreatedAt  time.Time
}

func NewDocumentService(knowledge *Service, files storage.Store, queue DocumentQueue) (*DocumentService, error) {
	if knowledge == nil || files == nil || queue == nil {
		return nil, errors.New("knowledge base, file store and job queue are required")
	}
	return &DocumentService{knowledge: knowledge, files: files, queue: queue}, nil
}

func (s *DocumentService) Upload(ctx context.Context, actor Actor, request UploadRequest) (_ UploadedDocument, resultErr error) {
	request.Title = strings.TrimSpace(request.Title)
	if request.KBID <= 0 || request.DocumentID < 0 || request.Title == "" || !utf8.ValidString(request.Title) || utf8.RuneCountInString(request.Title) > 200 ||
		strings.ContainsRune(request.Title, '\x00') || !validIdempotencyKey(request.IdempotencyKey) || request.Content == nil {
		return UploadedDocument{}, ErrInvalidRequest
	}
	switch request.MIME {
	case "text/csv", "text/markdown", "text/plain", "application/pdf", "application/vnd.openxmlformats-officedocument.wordprocessingml.document", "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet":
	default:
		return UploadedDocument{}, ErrUnsupportedMIME
	}
	ids, err := s.allocate(ctx, actor, request.KBID, request.DocumentID, request.MIME)
	if err != nil {
		return UploadedDocument{}, err
	}
	key := storage.Key{TenantID: actor.TenantID, KBID: request.KBID, DocumentID: ids.DocumentID, VersionID: ids.VersionID}
	fileKey, err := key.Path()
	if err != nil {
		return UploadedDocument{}, err
	}
	info, err := s.files.Put(ctx, key, request.Content, MaxDocumentBytes)
	if err != nil {
		return UploadedDocument{}, fmt.Errorf("publish document original: %w", err)
	}
	keepFile := false
	defer func() {
		if !keepFile {
			cleanupCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			if err := s.files.Delete(cleanupCtx, key); err != nil {
				resultErr = errors.Join(resultErr, fmt.Errorf("remove uncommitted document original: %w", err))
			}
		}
	}()
	if info.Size <= 0 {
		return UploadedDocument{}, ErrInvalidRequest
	}
	encoded, err := json.Marshal(struct {
		KBID                   int64
		Title                  string
		MIME                   string
		ContentSHA256          [sha256.Size]byte
		AllowDuplicate         bool
		ConfirmFirstRowHeaders bool  `json:",omitempty"`
		DocumentID             int64 `json:",omitempty"`
	}{request.KBID, request.Title, request.MIME, info.SHA256, request.AllowDuplicate, request.ConfirmFirstRowHeaders, request.DocumentID})
	if err != nil {
		return UploadedDocument{}, fmt.Errorf("encode document request: %w", err)
	}
	hash := sha256.Sum256(encoded)
	tx, queries, err := s.knowledge.begin(ctx, actor)
	if err != nil {
		return UploadedDocument{}, err
	}
	defer rollback(tx, &resultErr)
	operation := "document.upload"
	title := request.Title
	if request.DocumentID > 0 {
		operation = "document.version.upload"
		// Publication locks the document before the knowledge base. Use the
		// same order to avoid deadlocking with a concurrent version switch.
		target, err := queries.LockEditableDocument(ctx, dbquery.LockEditableDocumentParams{
			TenantID: actor.TenantID, KbID: request.KBID, DocumentID: request.DocumentID, UserID: actor.UserID,
		})
		if errors.Is(err, pgx.ErrNoRows) {
			return UploadedDocument{}, deny(ctx, tx, queries, actor, request.KBID, 0, "", operation, ErrNotFound)
		}
		if err != nil {
			return UploadedDocument{}, fmt.Errorf("lock version upload document: %w", err)
		}
		if target.Mime != request.MIME {
			return UploadedDocument{}, ErrVersionFormat
		}
		title = target.Title
	}
	if err := lockEditor(ctx, queries, actor, request.KBID); err != nil {
		if errors.Is(err, ErrNotFound) || errors.Is(err, ErrForbidden) || errors.Is(err, ErrArchived) {
			return UploadedDocument{}, deny(ctx, tx, queries, actor, request.KBID, 0, "", operation, err)
		}
		return UploadedDocument{}, err
	}
	if request.IdempotencyKey != "" {
		_, err := queries.ReserveIdempotency(ctx, dbquery.ReserveIdempotencyParams{
			TenantID: actor.TenantID, UserID: actor.UserID, Operation: operation, RequestKey: request.IdempotencyKey, RequestHash: hash[:],
		})
		if errors.Is(err, pgx.ErrNoRows) {
			previous, findErr := queries.FindIdempotency(ctx, dbquery.FindIdempotencyParams{
				TenantID: actor.TenantID, UserID: actor.UserID, Operation: operation, RequestKey: request.IdempotencyKey,
			})
			if findErr != nil {
				return UploadedDocument{}, fmt.Errorf("find document idempotency record: %w", findErr)
			}
			if !bytes.Equal(previous.RequestHash, hash[:]) {
				return UploadedDocument{}, ErrIdempotencyConflict
			}
			if !previous.ObjectID.Valid {
				return UploadedDocument{}, errors.New("incomplete document idempotency record")
			}
			row, findErr := queries.GetVisibleDocumentVersion(ctx, dbquery.GetVisibleDocumentVersionParams{
				TenantID: actor.TenantID, KbID: request.KBID, VersionID: previous.ObjectID.Int64, UserID: actor.UserID,
			})
			if errors.Is(findErr, pgx.ErrNoRows) {
				return UploadedDocument{}, deny(ctx, tx, queries, actor, request.KBID, 0, "", operation+".replay", ErrNotFound)
			}
			if findErr != nil {
				return UploadedDocument{}, fmt.Errorf("read idempotent document: %w", findErr)
			}
			if request.DocumentID > 0 && row.DocumentID != request.DocumentID {
				return UploadedDocument{}, errors.New("idempotent version belongs to another document")
			}
			if err := auditDocument(ctx, queries, actor, row.DocumentID, operation+".replay"); err != nil {
				return UploadedDocument{}, err
			}
			if err := tx.Commit(ctx); err != nil {
				return UploadedDocument{}, fmt.Errorf("commit document replay: %w", err)
			}
			return fromDocumentVersion(row), nil
		}
		if err != nil {
			return UploadedDocument{}, fmt.Errorf("reserve document idempotency key: %w", err)
		}
	}
	if request.DocumentID > 0 {
		duplicate, err := queries.DuplicateDocumentVersion(ctx, dbquery.DuplicateDocumentVersionParams{
			TenantID: actor.TenantID, DocumentID: request.DocumentID, ContentSha256: info.SHA256[:],
			ConfirmFirstRowHeaders: request.ConfirmFirstRowHeaders,
		})
		if err != nil {
			return UploadedDocument{}, fmt.Errorf("check document version content: %w", err)
		}
		if duplicate {
			return UploadedDocument{}, ErrDuplicateContent
		}
	}
	if !request.AllowDuplicate {
		var duplicate bool
		if request.DocumentID > 0 {
			duplicate, err = queries.DuplicateOtherDocumentContent(ctx, dbquery.DuplicateOtherDocumentContentParams{
				TenantID: actor.TenantID, KbID: request.KBID, DocumentID: request.DocumentID, ContentSha256: info.SHA256[:],
			})
		} else {
			duplicate, err = queries.DuplicateKnowledgeContent(ctx, dbquery.DuplicateKnowledgeContentParams{
				TenantID: actor.TenantID, KbID: request.KBID, ContentSha256: info.SHA256[:],
			})
		}
		if err != nil {
			return UploadedDocument{}, fmt.Errorf("check knowledge base content: %w", err)
		}
		if duplicate {
			return UploadedDocument{}, ErrDuplicateContent
		}
	}
	if request.DocumentID == 0 {
		if err := queries.CreateDocument(ctx, dbquery.CreateDocumentParams{
			DocumentID: ids.DocumentID, TenantID: actor.TenantID, KbID: request.KBID, Title: title, Mime: request.MIME,
		}); err != nil {
			return UploadedDocument{}, fmt.Errorf("create document: %w", err)
		}
	}
	createdAt, err := queries.CreateDocumentVersion(ctx, dbquery.CreateDocumentVersionParams{
		VersionID: ids.VersionID, TenantID: actor.TenantID, KbID: request.KBID, DocumentID: ids.DocumentID,
		ContentSha256: info.SHA256[:], ByteSize: info.Size, FileKey: fileKey, UserID: actor.UserID,
		ConfirmFirstRowHeaders: request.ConfirmFirstRowHeaders,
	})
	if err != nil {
		return UploadedDocument{}, fmt.Errorf("create document version: %w", err)
	}
	if !createdAt.Valid {
		return UploadedDocument{}, errors.New("document version has no creation time")
	}
	payload, err := ingestPayload(request.KBID, ids.DocumentID, ids.VersionID)
	if err != nil {
		return UploadedDocument{}, fmt.Errorf("encode ingest job: %w", err)
	}
	dedupe := fmt.Sprintf("document-version:%d", ids.VersionID)
	if _, err := s.queue.EnqueueTx(ctx, tx, actor.TenantID, jobs.Request{Kind: jobs.Ingest, DedupeKey: &dedupe, Payload: payload, MaxAttempts: 3}); err != nil {
		return UploadedDocument{}, fmt.Errorf("enqueue document ingest: %w", err)
	}
	if request.IdempotencyKey != "" {
		updated, err := queries.CompleteIdempotency(ctx, dbquery.CompleteIdempotencyParams{
			TenantID: actor.TenantID, UserID: actor.UserID, Operation: operation, RequestKey: request.IdempotencyKey,
			ObjectID: pgtype.Int8{Int64: ids.VersionID, Valid: true},
		})
		if err != nil {
			return UploadedDocument{}, fmt.Errorf("complete document idempotency record: %w", err)
		}
		if updated != 1 {
			return UploadedDocument{}, errors.New("document idempotency record not completed")
		}
	}
	if err := auditDocument(ctx, queries, actor, ids.DocumentID, operation); err != nil {
		return UploadedDocument{}, err
	}
	keepFile = true
	if err := tx.Commit(ctx); err != nil {
		return UploadedDocument{}, fmt.Errorf("%w: %w", ErrCommitUncertain, err)
	}
	return UploadedDocument{DocumentID: ids.DocumentID, VersionID: ids.VersionID, Title: title,
		MIME: request.MIME, Status: "queued", ByteSize: info.Size, CreatedAt: createdAt.Time}, nil
}

// Cancel stops the active ingest and records the document failure in the same transaction.
func (s *DocumentService) Cancel(ctx context.Context, actor Actor, kbID, documentID, versionID int64) (resultErr error) {
	if kbID <= 0 || documentID <= 0 || versionID <= 0 {
		return ErrInvalidRequest
	}
	payload, err := ingestPayload(kbID, documentID, versionID)
	if err != nil {
		return fmt.Errorf("encode ingest job: %w", err)
	}
	tx, queries, err := s.knowledge.begin(ctx, actor)
	if err != nil {
		return err
	}
	defer rollback(tx, &resultErr)
	jobID, err := s.queue.LockActiveTx(ctx, tx, actor.TenantID, jobs.Ingest, fmt.Sprintf("document-version:%d", versionID), payload)
	if errors.Is(err, jobs.ErrNotFound) {
		return deny(ctx, tx, queries, actor, kbID, 0, "", "document.cancel", ErrNotFound)
	}
	if err != nil {
		return fmt.Errorf("lock document ingest: %w", err)
	}
	if _, err := queries.LockDocumentVersionForCancel(ctx, dbquery.LockDocumentVersionForCancelParams{
		TenantID: actor.TenantID, KbID: kbID, DocumentID: documentID, VersionID: versionID,
	}); errors.Is(err, pgx.ErrNoRows) {
		return deny(ctx, tx, queries, actor, kbID, 0, "", "document.cancel", ErrNotFound)
	} else if err != nil {
		return fmt.Errorf("lock document version: %w", err)
	}
	role, _, err := lockDocumentAccess(ctx, queries, actor, kbID)
	if err == nil && role != Owner && role != Admin && role != Editor {
		err = ErrForbidden
	}
	if errors.Is(err, ErrNotFound) || errors.Is(err, ErrForbidden) {
		return deny(ctx, tx, queries, actor, kbID, 0, "", "document.cancel", err)
	}
	if err != nil {
		return err
	}
	updated, err := queries.CancelDocumentVersion(ctx, dbquery.CancelDocumentVersionParams{
		TenantID: actor.TenantID, KbID: kbID, DocumentID: documentID, VersionID: versionID,
	})
	if err != nil {
		return fmt.Errorf("cancel document version: %w", err)
	}
	if updated != 1 {
		return errors.New("document version was not cancelled")
	}
	kind, err := s.queue.CancelTx(ctx, tx, actor.TenantID, jobID)
	if err != nil {
		return fmt.Errorf("cancel document ingest: %w", err)
	}
	if kind != jobs.Ingest {
		return errors.New("cancelled job was not an ingest")
	}
	if err := auditDocument(ctx, queries, actor, documentID, "document.cancel"); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit document cancellation: %w", err)
	}
	return nil
}

func ingestPayload(kbID, documentID, versionID int64) (json.RawMessage, error) {
	return json.Marshal(struct {
		KBID       int64 `json:"kb_id"`
		DocumentID int64 `json:"document_id"`
		VersionID  int64 `json:"version_id"`
	}{kbID, documentID, versionID})
}

// AuthorizeUpload checks access before an HTTP server reads a request body.
// Upload repeats the check before allocating IDs and again before committing.
func (s *DocumentService) AuthorizeUpload(ctx context.Context, actor Actor, kbID int64) (resultErr error) {
	if kbID <= 0 {
		return ErrInvalidRequest
	}
	tx, queries, err := s.knowledge.begin(ctx, actor)
	if err != nil {
		return err
	}
	defer rollback(tx, &resultErr)
	if err := checkUploadAccess(ctx, tx, queries, actor, kbID); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit document upload authorization: %w", err)
	}
	return nil
}

// AuthorizeVersionUpload checks document access before reading a request body.
func (s *DocumentService) AuthorizeVersionUpload(ctx context.Context, actor Actor, kbID, documentID int64) (resultErr error) {
	if kbID <= 0 || documentID <= 0 {
		return ErrInvalidRequest
	}
	tx, queries, err := s.knowledge.begin(ctx, actor)
	if err != nil {
		return err
	}
	defer rollback(tx, &resultErr)
	if err := checkVersionUploadAccess(ctx, tx, queries, actor, kbID, documentID, ""); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit version upload authorization: %w", err)
	}
	return nil
}

func (s *DocumentService) allocate(ctx context.Context, actor Actor, kbID, documentID int64, mime string) (_ dbquery.AllocateDocumentIDsRow, resultErr error) {
	tx, queries, err := s.knowledge.begin(ctx, actor)
	if err != nil {
		return dbquery.AllocateDocumentIDsRow{}, err
	}
	defer rollback(tx, &resultErr)
	var ids dbquery.AllocateDocumentIDsRow
	if documentID > 0 {
		if err := checkVersionUploadAccess(ctx, tx, queries, actor, kbID, documentID, mime); err != nil {
			return ids, err
		}
		ids.DocumentID = documentID
		ids.VersionID, err = queries.AllocateDocumentVersionID(ctx)
	} else {
		if err := checkUploadAccess(ctx, tx, queries, actor, kbID); err != nil {
			return ids, err
		}
		ids, err = queries.AllocateDocumentIDs(ctx)
	}
	if err != nil {
		return ids, fmt.Errorf("allocate document IDs: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return dbquery.AllocateDocumentIDsRow{}, fmt.Errorf("commit document ID allocation: %w", err)
	}
	return ids, nil
}

func checkVersionUploadAccess(ctx context.Context, tx pgx.Tx, queries *dbquery.Queries, actor Actor, kbID, documentID int64, mime string) error {
	target, err := queries.GetVisibleVersionUploadTarget(ctx, dbquery.GetVisibleVersionUploadTargetParams{
		TenantID: actor.TenantID, KbID: kbID, DocumentID: documentID, UserID: actor.UserID,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return deny(ctx, tx, queries, actor, kbID, 0, "", "document.version.upload", ErrNotFound)
	}
	if err != nil {
		return fmt.Errorf("check version upload document: %w", err)
	}
	if err := mayUpload(Role(target.Role), target.Status); err != nil {
		return deny(ctx, tx, queries, actor, kbID, 0, "", "document.version.upload", err)
	}
	if mime != "" && target.Mime != mime {
		return ErrVersionFormat
	}
	return nil
}

func checkUploadAccess(ctx context.Context, tx pgx.Tx, queries *dbquery.Queries, actor Actor, kbID int64) error {
	row, err := queries.GetVisibleKnowledgeBase(ctx, dbquery.GetVisibleKnowledgeBaseParams{TenantID: actor.TenantID, KbID: kbID, UserID: actor.UserID})
	if errors.Is(err, pgx.ErrNoRows) {
		return deny(ctx, tx, queries, actor, kbID, 0, "", "document.upload", ErrNotFound)
	}
	if err != nil {
		return fmt.Errorf("check document knowledge base: %w", err)
	}
	if err := mayUpload(Role(row.Role), row.Status); err != nil {
		return deny(ctx, tx, queries, actor, kbID, 0, "", "document.upload", err)
	}
	return nil
}

func lockEditor(ctx context.Context, queries *dbquery.Queries, actor Actor, kbID int64) error {
	role, status, err := lockDocumentAccess(ctx, queries, actor, kbID)
	if err != nil {
		return err
	}
	return mayUpload(role, status)
}

func lockDocumentAccess(ctx context.Context, queries *dbquery.Queries, actor Actor, kbID int64) (Role, string, error) {
	if _, err := queries.LockVisibleKnowledgeBase(ctx, dbquery.LockVisibleKnowledgeBaseParams{TenantID: actor.TenantID, KbID: kbID, UserID: actor.UserID}); errors.Is(err, pgx.ErrNoRows) {
		return "", "", ErrNotFound
	} else if err != nil {
		return "", "", fmt.Errorf("lock document knowledge base: %w", err)
	}
	role, err := queries.LockMemberRole(ctx, dbquery.LockMemberRoleParams{TenantID: actor.TenantID, KbID: kbID, UserID: actor.UserID})
	if errors.Is(err, pgx.ErrNoRows) {
		return "", "", ErrNotFound
	}
	if err != nil {
		return "", "", fmt.Errorf("lock document membership: %w", err)
	}
	row, err := queries.GetVisibleKnowledgeBase(ctx, dbquery.GetVisibleKnowledgeBaseParams{TenantID: actor.TenantID, KbID: kbID, UserID: actor.UserID})
	if err != nil {
		return "", "", fmt.Errorf("read document knowledge base: %w", err)
	}
	return Role(role), row.Status, nil
}

func mayUpload(role Role, status string) error {
	if role != Owner && role != Admin && role != Editor {
		return ErrForbidden
	}
	if status != "active" {
		return ErrArchived
	}
	return nil
}

func auditDocument(ctx context.Context, queries *dbquery.Queries, actor Actor, documentID int64, action string) error {
	if err := queries.AuditDocumentAction(ctx, dbquery.AuditDocumentActionParams{
		TenantID: actor.TenantID, UserID: actor.UserID, DocumentID: documentID, Action: action, Result: "allow",
	}); err != nil {
		return fmt.Errorf("audit document action: %w", err)
	}
	return nil
}

func fromDocumentVersion(row dbquery.GetVisibleDocumentVersionRow) UploadedDocument {
	return UploadedDocument{DocumentID: row.DocumentID, VersionID: row.VersionID, Title: row.Title,
		MIME: row.Mime, Status: row.Status, ByteSize: row.ByteSize, CreatedAt: row.CreatedAt.Time}
}
