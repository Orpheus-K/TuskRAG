package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"mime/multipart"
	"net"
	"net/http"
	"os"
	"path"
	"strconv"
	"strings"
	"time"

	"github.com/danielgtaylor/huma/v2"
	"github.com/danielgtaylor/huma/v2/adapters/humago"

	"tuskrag.local/core/internal/kb"
	"tuskrag.local/core/internal/storage"
)

const maxUploadBodyBytes = kb.MaxDocumentBytes + 1024*1024

var errUploadDeadline = errors.New("upload read deadline unavailable")

type UploadDocumentInput struct {
	ID                     int64  `path:"kb" minimum:"1"`
	CSRF                   string `header:"X-CSRF-Token" required:"true"`
	IdempotencyKey         string `header:"Idempotency-Key" maxLength:"128"`
	AllowDuplicate         bool   `query:"allow_duplicate"`
	ConfirmFirstRowHeaders bool   `query:"confirm_first_row_headers"`
	RawBody                multipart.Form
}

type UploadVersionInput struct {
	ID                     int64  `path:"kb" minimum:"1"`
	DocumentID             int64  `path:"doc" minimum:"1"`
	CSRF                   string `header:"X-CSRF-Token" required:"true"`
	IdempotencyKey         string `header:"Idempotency-Key" maxLength:"128"`
	AllowDuplicate         bool   `query:"allow_duplicate"`
	ConfirmFirstRowHeaders bool   `query:"confirm_first_row_headers"`
	RawBody                multipart.Form
}

type UploadedDocumentBody struct {
	DocumentID int64     `json:"document_id"`
	VersionID  int64     `json:"version_id"`
	Title      string    `json:"title"`
	MIME       string    `json:"mime"`
	Status     string    `json:"status"`
	ByteSize   int64     `json:"byte_size"`
	CreatedAt  time.Time `json:"created_at"`
}

func registerDocumentUpload(api huma.API, deps Dependencies) {
	slots := make(chan struct{}, 2)
	huma.Register(api, huma.Operation{
		OperationID: "upload-document", Method: http.MethodPost, Path: "/kbs/{kb}/documents",
		MaxBodyBytes: maxUploadBodyBytes, BodyReadTimeout: 10 * time.Minute,
		Middlewares: huma.Middlewares{guardDocumentUpload(deps.Documents, slots, false)},
		RequestBody: documentUploadBody(),
	}, func(ctx context.Context, input *UploadDocumentInput) (*struct{ Body UploadedDocumentBody }, error) {
		return uploadDocumentForm(ctx, deps.Documents, &input.RawBody, kb.UploadRequest{KBID: input.ID,
			IdempotencyKey: input.IdempotencyKey, AllowDuplicate: input.AllowDuplicate, ConfirmFirstRowHeaders: input.ConfirmFirstRowHeaders})
	})
	huma.Register(api, huma.Operation{
		OperationID: "upload-document-version", Method: http.MethodPost, Path: "/kbs/{kb}/documents/{doc}/versions",
		MaxBodyBytes: maxUploadBodyBytes, BodyReadTimeout: 10 * time.Minute,
		Middlewares: huma.Middlewares{guardDocumentUpload(deps.Documents, slots, true)},
		RequestBody: documentUploadBody(),
	}, func(ctx context.Context, input *UploadVersionInput) (*struct{ Body UploadedDocumentBody }, error) {
		return uploadDocumentForm(ctx, deps.Documents, &input.RawBody, kb.UploadRequest{KBID: input.ID, DocumentID: input.DocumentID,
			IdempotencyKey: input.IdempotencyKey, AllowDuplicate: input.AllowDuplicate, ConfirmFirstRowHeaders: input.ConfirmFirstRowHeaders})
	})
}

func documentUploadBody() *huma.RequestBody {
	return &huma.RequestBody{Required: true, Content: map[string]*huma.MediaType{
		"multipart/form-data": {Schema: &huma.Schema{Type: "object", Required: []string{"file"}, Properties: map[string]*huma.Schema{
			"file": {Type: "string", Format: "binary"},
		}}},
	}}
}

func uploadDocumentForm(ctx context.Context, documents DocumentService, body *multipart.Form, request kb.UploadRequest) (output *struct{ Body UploadedDocumentBody }, resultErr error) {
	defer func() {
		if err := body.RemoveAll(); err != nil {
			output = nil
			resultErr = operationError(ctx, "remove temporary upload", err)
		}
	}()
	files := body.File["file"]
	if len(body.File) != 1 || len(files) != 1 || len(body.Value) != 0 {
		return nil, statusError(ctx, http.StatusBadRequest, "invalid_request", "one file is required", false)
	}
	fileHeader := files[0]
	if fileHeader.Size <= 0 {
		return nil, statusError(ctx, http.StatusBadRequest, "invalid_request", "empty file", false)
	}
	if fileHeader.Size > kb.MaxDocumentBytes {
		return nil, statusError(ctx, http.StatusRequestEntityTooLarge, "limit_exceeded", "file is too large", false)
	}
	title := path.Base(strings.ReplaceAll(fileHeader.Filename, `\`, "/"))
	mime := documentMIME(title)
	if mime == "" {
		return nil, statusError(ctx, http.StatusBadRequest, "unsupported_format", "file format is not supported", false)
	}
	file, err := fileHeader.Open()
	if err != nil {
		return nil, operationError(ctx, "open uploaded file", err)
	}
	defer func() {
		if err := file.Close(); err != nil {
			output = nil
			resultErr = operationError(ctx, "close uploaded file", err)
		}
	}()
	request.Title, request.MIME, request.Content = title, mime, file
	row, err := documents.Upload(ctx, currentActor(ctx), request)
	if err != nil {
		return nil, documentUploadError(ctx, err)
	}
	return &struct{ Body UploadedDocumentBody }{Body: UploadedDocumentBody{
		DocumentID: row.DocumentID, VersionID: row.VersionID, Title: row.Title,
		MIME: row.MIME, Status: row.Status, ByteSize: row.ByteSize, CreatedAt: row.CreatedAt,
	}}, nil
}

// Check authorization before multipart parsing can create temporary files.
func guardDocumentUpload(documents DocumentService, slots chan struct{}, version bool) func(huma.Context, func(huma.Context)) {
	return func(ctx huma.Context, next func(huma.Context)) {
		if documents == nil {
			writeUploadGuardError(ctx, statusError(ctx.Context(), http.StatusServiceUnavailable, "dependency_unavailable", "document storage unavailable", true))
			return
		}
		id, err := strconv.ParseInt(ctx.Param("kb"), 10, 64)
		if err != nil || id <= 0 {
			writeUploadGuardError(ctx, statusError(ctx.Context(), http.StatusBadRequest, "invalid_request", "invalid knowledge base ID", false))
			return
		}
		if version {
			docID, parseErr := strconv.ParseInt(ctx.Param("doc"), 10, 64)
			if parseErr != nil || docID <= 0 {
				writeUploadGuardError(ctx, statusError(ctx.Context(), http.StatusBadRequest, "invalid_request", "invalid document ID", false))
				return
			}
			err = documents.AuthorizeVersionUpload(ctx.Context(), currentActor(ctx.Context()), id, docID)
		} else {
			err = documents.AuthorizeUpload(ctx.Context(), currentActor(ctx.Context()), id)
		}
		if err != nil {
			writeUploadGuardError(ctx, documentUploadError(ctx.Context(), err))
			return
		}
		req, writer := humago.Unwrap(ctx)
		if req.ContentLength > maxUploadBodyBytes {
			writeUploadGuardError(ctx, statusError(ctx.Context(), http.StatusRequestEntityTooLarge, "limit_exceeded", "request is too large", false))
			return
		}
		select {
		case slots <- struct{}{}:
			defer func() { <-slots }()
		default:
			ctx.SetHeader("Retry-After", "1")
			writeUploadGuardError(ctx, statusError(ctx.Context(), http.StatusTooManyRequests, "rate_limited", "too many uploads", true))
			return
		}
		defer func() {
			if req.MultipartForm != nil {
				if err := req.MultipartForm.RemoveAll(); err != nil {
					slog.Error("remove temporary upload", "trace_id", ctx.Context().Value(traceKey{}), "error", err)
				}
			}
		}()
		if err := readUploadForm(ctx, req, writer); err != nil {
			writeUploadGuardError(ctx, uploadReadError(ctx.Context(), err))
			return
		}
		next(ctx)
	}
}

func readUploadForm(ctx huma.Context, req *http.Request, writer http.ResponseWriter) (resultErr error) {
	if err := ctx.SetReadDeadline(time.Now().Add(10 * time.Minute)); err != nil && !errors.Is(err, http.ErrNotSupported) {
		return errors.Join(errUploadDeadline, err)
	}
	defer func() {
		if err := ctx.SetReadDeadline(time.Time{}); err != nil && !errors.Is(err, http.ErrNotSupported) {
			resultErr = errors.Join(resultErr, errUploadDeadline, err)
		}
	}()
	req.Body = http.MaxBytesReader(writer, req.Body, maxUploadBodyBytes)
	return req.ParseMultipartForm(humago.MultipartMaxMemory)
}

func uploadReadError(ctx context.Context, err error) error {
	var maxError *http.MaxBytesError
	var pathError *os.PathError
	var networkError net.Error
	switch {
	case errors.As(err, &maxError), errors.Is(err, multipart.ErrMessageTooLarge):
		return statusError(ctx, http.StatusRequestEntityTooLarge, "limit_exceeded", "request is too large", false)
	case errors.As(err, &pathError):
		slog.Error("prepare temporary upload", "trace_id", ctx.Value(traceKey{}), "error", err)
		return statusError(ctx, http.StatusServiceUnavailable, "dependency_unavailable", "temporary upload storage unavailable", true)
	case errors.Is(err, errUploadDeadline):
		slog.Error("set upload read deadline", "trace_id", ctx.Value(traceKey{}), "error", err)
		return statusError(ctx, http.StatusServiceUnavailable, "dependency_unavailable", "upload read deadline unavailable", true)
	case errors.As(err, &networkError) && networkError.Timeout():
		return statusError(ctx, http.StatusRequestTimeout, "timeout", "request body read timed out", true)
	default:
		return statusError(ctx, http.StatusBadRequest, "invalid_request", "invalid multipart request", false)
	}
}

func writeUploadGuardError(ctx huma.Context, err error) {
	var status huma.StatusError
	if !errors.As(err, &status) {
		status = statusError(ctx.Context(), http.StatusInternalServerError, "internal", "internal error", false)
	}
	ctx.SetHeader("Content-Type", "application/json")
	ctx.SetStatus(status.GetStatus())
	if writeErr := json.NewEncoder(ctx.BodyWriter()).Encode(status); writeErr != nil {
		slog.Error("write upload error", "trace_id", ctx.Context().Value(traceKey{}), "error", writeErr)
	}
}

func documentMIME(title string) string {
	switch strings.ToLower(path.Ext(title)) {
	case ".csv":
		return "text/csv"
	case ".md", ".markdown":
		return "text/markdown"
	case ".txt":
		return "text/plain"
	case ".pdf":
		return "application/pdf"
	case ".docx":
		return "application/vnd.openxmlformats-officedocument.wordprocessingml.document"
	case ".xlsx":
		return "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet"
	default:
		return ""
	}
}

func documentUploadError(ctx context.Context, err error) error {
	switch {
	case errors.Is(err, kb.ErrDuplicateContent):
		return statusError(ctx, http.StatusConflict, "duplicate_content", "document content already exists", false)
	case errors.Is(err, kb.ErrArchived):
		return statusError(ctx, http.StatusConflict, "knowledge_base_archived", "knowledge base is archived", false)
	case errors.Is(err, kb.ErrUnsupportedMIME):
		return statusError(ctx, http.StatusBadRequest, "unsupported_format", "file format is not supported", false)
	case errors.Is(err, kb.ErrVersionFormat):
		return statusError(ctx, http.StatusBadRequest, "version_format_mismatch", "new version format must match the document", false)
	case errors.Is(err, kb.ErrInvalidRequest):
		return statusError(ctx, http.StatusBadRequest, "invalid_request", "invalid document request", false)
	case errors.Is(err, storage.ErrTooLarge):
		return statusError(ctx, http.StatusRequestEntityTooLarge, "limit_exceeded", "file is too large", false)
	default:
		return knowledgeError(ctx, "upload document", err)
	}
}
