package httpapi

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"tuskrag.local/core/internal/identity"
	"tuskrag.local/core/internal/kb"
)

type uploadTestIdentity struct{}

func (uploadTestIdentity) Login(context.Context, int64, string, []byte) (identity.Session, error) {
	return identity.Session{}, errors.New("unexpected login")
}
func (uploadTestIdentity) Authenticate(context.Context, string) (identity.Session, error) {
	return identity.Session{Principal: identity.Principal{TenantID: 1, UserID: 2}}, nil
}
func (uploadTestIdentity) Logout(context.Context, string) error {
	return errors.New("unexpected logout")
}

type uploadTestDocuments struct {
	authorize        func(context.Context, kb.Actor, int64) error
	authorizeVersion func(context.Context, kb.Actor, int64, int64) error
	upload           func(context.Context, kb.Actor, kb.UploadRequest) (kb.UploadedDocument, error)
	delete           func(context.Context, kb.Actor, int64, int64) error
}

func (s uploadTestDocuments) AuthorizeVersionUpload(ctx context.Context, actor kb.Actor, kbID, documentID int64) error {
	return s.authorizeVersion(ctx, actor, kbID, documentID)
}

func (s uploadTestDocuments) AuthorizeUpload(ctx context.Context, actor kb.Actor, id int64) error {
	return s.authorize(ctx, actor, id)
}
func (s uploadTestDocuments) Upload(ctx context.Context, actor kb.Actor, req kb.UploadRequest) (kb.UploadedDocument, error) {
	return s.upload(ctx, actor, req)
}
func (s uploadTestDocuments) Delete(ctx context.Context, actor kb.Actor, kbID, documentID int64) error {
	return s.delete(ctx, actor, kbID, documentID)
}

type observedBody struct{ reads int }

func (r *observedBody) Read([]byte) (int, error) {
	r.reads++
	return 0, errors.New("request body must not be read")
}

func authenticatedUploadRequest(body io.Reader) *http.Request {
	secret := make([]byte, 32)
	mac := hmac.New(sha256.New, secret)
	_, _ = mac.Write([]byte("tuskrag.session.csrf.v1"))
	req := httptest.NewRequest(http.MethodPost, "/api/v1/kbs/7/documents", body)
	req.AddCookie(&http.Cookie{Name: "tuskrag_dev_session", Value: "1." + base64.RawURLEncoding.EncodeToString(secret)})
	req.Header.Set("X-CSRF-Token", base64.RawURLEncoding.EncodeToString(mac.Sum(nil)))
	return req
}

func TestUploadRejectsBeforeReadingBody(t *testing.T) {
	for _, tc := range []struct {
		name      string
		documents DocumentService
		length    int64
		status    int
		code      string
	}{
		{"storage unavailable", nil, 0, http.StatusServiceUnavailable, "dependency_unavailable"},
		{"forbidden", uploadTestDocuments{authorize: func(context.Context, kb.Actor, int64) error { return kb.ErrForbidden }}, 0, http.StatusForbidden, "forbidden"},
		{"archived", uploadTestDocuments{authorize: func(context.Context, kb.Actor, int64) error { return kb.ErrArchived }}, 0, http.StatusConflict, "knowledge_base_archived"},
		{"oversized request", uploadTestDocuments{authorize: func(context.Context, kb.Actor, int64) error { return nil }}, maxUploadBodyBytes + 1, http.StatusRequestEntityTooLarge, "limit_exceeded"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			app := New(Dependencies{Identity: uploadTestIdentity{}, Documents: tc.documents, InsecureLocalCookies: true})
			body := &observedBody{}
			req := authenticatedUploadRequest(body)
			req.Header.Set("Content-Type", "multipart/form-data; boundary=example")
			req.ContentLength = tc.length
			w := httptest.NewRecorder()
			app.Handler.ServeHTTP(w, req)
			var result struct {
				Error ErrorBody `json:"error"`
			}
			if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil || w.Code != tc.status || result.Error.Code != tc.code || result.Error.TraceID != w.Header().Get("X-Trace-ID") || body.reads != 0 {
				t.Fatalf("status=%d body=%s reads=%d err=%v", w.Code, w.Body.String(), body.reads, err)
			}
		})
	}
}

func TestVersionUploadChecksDocumentBeforeReadingBody(t *testing.T) {
	body := &observedBody{}
	req := authenticatedUploadRequest(body)
	req.URL.Path = "/api/v1/kbs/7/documents/9/versions"
	req.Header.Set("Content-Type", "multipart/form-data; boundary=example")
	documents := uploadTestDocuments{authorizeVersion: func(_ context.Context, actor kb.Actor, kbID, documentID int64) error {
		if actor.TenantID != 1 || actor.UserID != 2 || kbID != 7 || documentID != 9 {
			t.Fatal("wrong version upload target")
		}
		return kb.ErrForbidden
	}}
	w := httptest.NewRecorder()
	New(Dependencies{Identity: uploadTestIdentity{}, Documents: documents, InsecureLocalCookies: true}).Handler.ServeHTTP(w, req)
	if w.Code != http.StatusForbidden || body.reads != 0 {
		t.Fatalf("forbidden version upload: status=%d reads=%d body=%s", w.Code, body.reads, w.Body.String())
	}
}

func TestVersionUploadPassesTargetAndConfirmation(t *testing.T) {
	documents := uploadTestDocuments{
		authorizeVersion: func(_ context.Context, _ kb.Actor, kbID, documentID int64) error {
			if kbID != 7 || documentID != 9 {
				t.Fatal("wrong authorization target")
			}
			return nil
		},
		upload: func(_ context.Context, _ kb.Actor, request kb.UploadRequest) (kb.UploadedDocument, error) {
			data, err := io.ReadAll(request.Content)
			if err != nil || string(data) != "列\n一\n" || request.KBID != 7 || request.DocumentID != 9 ||
				request.MIME != "text/csv" || request.IdempotencyKey != "version-key" || !request.ConfirmFirstRowHeaders {
				t.Fatalf("wrong version request: %+v data=%q err=%v", request, data, err)
			}
			return kb.UploadedDocument{DocumentID: 9, VersionID: 12, Title: "原始.csv", MIME: "text/csv", Status: "queued", ByteSize: int64(len(data)), CreatedAt: time.Now()}, nil
		},
	}
	var body bytes.Buffer
	form := multipart.NewWriter(&body)
	part, err := form.CreateFormFile("file", "修订.csv")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := io.WriteString(part, "列\n一\n"); err != nil {
		t.Fatal(err)
	}
	if err := form.Close(); err != nil {
		t.Fatal(err)
	}
	req := authenticatedUploadRequest(&body)
	req.URL.Path = "/api/v1/kbs/7/documents/9/versions"
	req.URL.RawQuery = "confirm_first_row_headers=true"
	req.Header.Set("Content-Type", form.FormDataContentType())
	req.Header.Set("Idempotency-Key", "version-key")
	w := httptest.NewRecorder()
	New(Dependencies{Identity: uploadTestIdentity{}, Documents: documents, InsecureLocalCookies: true}).Handler.ServeHTTP(w, req)
	var got UploadedDocumentBody
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil || w.Code != http.StatusOK || got.DocumentID != 9 || got.VersionID != 12 || got.Title != "原始.csv" {
		t.Fatalf("version response: status=%d result=%+v body=%s err=%v", w.Code, got, w.Body.String(), err)
	}
}

func TestUploadRemovesMultipartTemporaryFile(t *testing.T) {
	var temporary string
	documents := uploadTestDocuments{
		authorize: func(_ context.Context, actor kb.Actor, id int64) error {
			if actor.TenantID != 1 || actor.UserID != 2 || id != 7 {
				t.Fatal("wrong upload actor or knowledge base")
			}
			return nil
		},
		upload: func(_ context.Context, _ kb.Actor, req kb.UploadRequest) (kb.UploadedDocument, error) {
			if !req.ConfirmFirstRowHeaders {
				t.Fatal("table header confirmation was not passed to the upload service")
			}
			file, ok := req.Content.(interface{ Name() string })
			if !ok {
				t.Fatalf("expected a temporary file, got %T", req.Content)
			}
			temporary = file.Name()
			if n, err := io.Copy(io.Discard, req.Content); err != nil || n != 16384 {
				t.Fatalf("uploaded size=%d err=%v", n, err)
			}
			return kb.UploadedDocument{DocumentID: 1, VersionID: 2, Title: req.Title, MIME: req.MIME, Status: "queued", ByteSize: 16384, CreatedAt: time.Now()}, nil
		},
	}
	var body bytes.Buffer
	form := multipart.NewWriter(&body)
	part, err := form.CreateFormFile("file", "example.csv")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := io.WriteString(part, strings.Repeat("a", 16384)); err != nil {
		t.Fatal(err)
	}
	if err := form.Close(); err != nil {
		t.Fatal(err)
	}
	req := authenticatedUploadRequest(&body)
	req.URL.RawQuery = "confirm_first_row_headers=true"
	req.Header.Set("Content-Type", form.FormDataContentType())
	req.Header.Set("Idempotency-Key", "test-upload")
	w := httptest.NewRecorder()
	New(Dependencies{Identity: uploadTestIdentity{}, Documents: documents, InsecureLocalCookies: true}).Handler.ServeHTTP(w, req)
	if w.Code != http.StatusOK || temporary == "" {
		t.Fatalf("status=%d body=%s temporary=%q", w.Code, w.Body.String(), temporary)
	}
	if _, err := os.Stat(temporary); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("multipart temporary file was not removed: %v", err)
	}
}

func TestUploadLimitsConcurrentBodies(t *testing.T) {
	entered := make(chan struct{}, 2)
	release := make(chan struct{})
	released := false
	defer func() {
		if !released {
			close(release)
		}
	}()
	documents := uploadTestDocuments{
		authorize: func(context.Context, kb.Actor, int64) error { return nil },
		upload: func(_ context.Context, _ kb.Actor, req kb.UploadRequest) (kb.UploadedDocument, error) {
			entered <- struct{}{}
			select {
			case <-release:
				return kb.UploadedDocument{DocumentID: 1, VersionID: 2, Title: req.Title, MIME: req.MIME, Status: "queued", ByteSize: 1, CreatedAt: time.Now()}, nil
			case <-time.After(10 * time.Second):
				return kb.UploadedDocument{}, errors.New("upload test timed out")
			}
		},
	}
	app := New(Dependencies{Identity: uploadTestIdentity{}, Documents: documents, InsecureLocalCookies: true})
	makeRequest := func() *http.Request {
		t.Helper()
		var body bytes.Buffer
		form := multipart.NewWriter(&body)
		part, err := form.CreateFormFile("file", "example.csv")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := io.WriteString(part, "a"); err != nil {
			t.Fatal(err)
		}
		if err := form.Close(); err != nil {
			t.Fatal(err)
		}
		req := authenticatedUploadRequest(&body)
		req.Header.Set("Content-Type", form.FormDataContentType())
		return req
	}
	results := make(chan int, 2)
	for range 2 {
		req := makeRequest()
		go func() {
			w := httptest.NewRecorder()
			app.Handler.ServeHTTP(w, req)
			results <- w.Code
		}()
	}
	for range 2 {
		select {
		case <-entered:
		case <-time.After(10 * time.Second):
			t.Fatal("uploads did not enter the service")
		}
	}
	body := &observedBody{}
	req := authenticatedUploadRequest(body)
	req.Header.Set("Content-Type", "multipart/form-data; boundary=example")
	w := httptest.NewRecorder()
	app.Handler.ServeHTTP(w, req)
	var result struct {
		Error ErrorBody `json:"error"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil || w.Code != http.StatusTooManyRequests || !result.Error.Retryable || w.Header().Get("Retry-After") != "1" || body.reads != 0 {
		t.Fatalf("status=%d body=%s reads=%d err=%v", w.Code, w.Body.String(), body.reads, err)
	}
	close(release)
	released = true
	for range 2 {
		select {
		case status := <-results:
			if status != http.StatusOK {
				t.Fatalf("accepted upload status=%d", status)
			}
		case <-time.After(10 * time.Second):
			t.Fatal("accepted upload did not finish")
		}
	}
}

func TestUploadRemovesTemporaryFileOnInvalidForm(t *testing.T) {
	directory := t.TempDir()
	t.Setenv("TMPDIR", directory)
	t.Setenv("TMP", directory)
	t.Setenv("TEMP", directory)
	documents := uploadTestDocuments{authorize: func(context.Context, kb.Actor, int64) error { return nil }}
	var body bytes.Buffer
	form := multipart.NewWriter(&body)
	part, err := form.CreateFormFile("file", "example.csv")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := io.WriteString(part, strings.Repeat("a", 16384)); err != nil {
		t.Fatal(err)
	}
	if err := form.WriteField("unexpected", "value"); err != nil {
		t.Fatal(err)
	}
	if err := form.Close(); err != nil {
		t.Fatal(err)
	}
	req := authenticatedUploadRequest(&body)
	req.Header.Set("Content-Type", form.FormDataContentType())
	w := httptest.NewRecorder()
	New(Dependencies{Identity: uploadTestIdentity{}, Documents: documents, InsecureLocalCookies: true}).Handler.ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	entries, err := os.ReadDir(directory)
	if err != nil || len(entries) != 0 {
		t.Fatalf("temporary upload files remain: entries=%v err=%v", entries, err)
	}
}

func TestUploadReportsUnavailableTemporaryStorage(t *testing.T) {
	directory := filepath.Join(t.TempDir(), "missing")
	t.Setenv("TMPDIR", directory)
	t.Setenv("TMP", directory)
	t.Setenv("TEMP", directory)
	documents := uploadTestDocuments{authorize: func(context.Context, kb.Actor, int64) error { return nil }}
	var body bytes.Buffer
	form := multipart.NewWriter(&body)
	part, err := form.CreateFormFile("file", "example.csv")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := io.WriteString(part, strings.Repeat("a", 16384)); err != nil {
		t.Fatal(err)
	}
	if err := form.Close(); err != nil {
		t.Fatal(err)
	}
	req := authenticatedUploadRequest(&body)
	req.Header.Set("Content-Type", form.FormDataContentType())
	w := httptest.NewRecorder()
	New(Dependencies{Identity: uploadTestIdentity{}, Documents: documents, InsecureLocalCookies: true}).Handler.ServeHTTP(w, req)
	var result struct {
		Error ErrorBody `json:"error"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil || w.Code != http.StatusServiceUnavailable || result.Error.Code != "dependency_unavailable" || !result.Error.Retryable {
		t.Fatalf("status=%d body=%s err=%v", w.Code, w.Body.String(), err)
	}
}
