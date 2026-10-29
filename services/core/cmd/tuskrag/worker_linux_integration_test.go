//go:build linux

package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"tuskrag.local/core/internal/config"
	"tuskrag.local/core/internal/db"
	"tuskrag.local/core/internal/identity"
	"tuskrag.local/core/internal/indexing"
	"tuskrag.local/core/internal/jobs"
	"tuskrag.local/core/internal/kb"
	markdowntext "tuskrag.local/core/internal/markdown"
	"tuskrag.local/core/internal/models"
	"tuskrag.local/core/internal/storage"
	"tuskrag.local/core/internal/tokenizer"
)

func TestMain(m *testing.M) {
	if len(os.Args) > 1 && os.Args[1] == "native-parse" {
		os.Exit(nativeParseExitCode(runNativeParse(os.Args[2:])))
	}
	os.Exit(m.Run())
}

// TestWorkerProcessHelper is launched by TestWorkerProcess with production startup.
func TestWorkerProcessHelper(t *testing.T) {
	if os.Getenv("TUSKRAG_TEST_WORKER_PROCESS") != "1" {
		return
	}
	os.Args = []string{"tuskrag"}
	if err := run(); err != nil {
		t.Fatal(err)
	}
}

func TestWorkerProcess(t *testing.T) {
	for _, name := range []string{"TUSKRAG_TEST_BWRAP", "TUSKRAG_TEST_PARSER_RUNTIME", "TUSKRAG_TEST_PARSER", "TUSKRAG_TEST_CGROUP"} {
		if os.Getenv(name) == "" {
			t.Skip("worker process test requires the isolated parser environment")
		}
	}
	adminURL, appURL, migratorURL := workerTestDatabase(t)
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()
	admin, err := pgx.Connect(ctx, adminURL)
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close(context.Background())
	pool, err := db.OpenWithLimit(ctx, appURL, 4)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	counter, err := tokenizer.NewBGEM3(ctx, 1)
	if err != nil {
		t.Fatal(err)
	}
	badRuntime := t.TempDir()
	badParser := filepath.Join(badRuntime, "parser")
	if err := os.WriteFile(badParser, []byte("#!/bin/sh\nprintf '%s' '{\"tool\":\"tuskrag-parse\",\"version\":\"wrong\",\"schema_version\":\"1.0.0\",\"drivers\":{\"csv\":\"x\",\"docx\":\"x\",\"xlsx\":\"x\"}}'\n"), 0700); err != nil {
		t.Fatal(err)
	}
	_, err = newWorkerRuntime(ctx, config.Config{Parser: badParser, ParserRuntimeDir: badRuntime,
		Bubblewrap: os.Getenv("TUSKRAG_TEST_BWRAP"), CgroupRoot: os.Getenv("TUSKRAG_TEST_CGROUP"), ParserTempDir: t.TempDir()}, pool, counter.Count)
	if err == nil || !strings.Contains(err.Error(), "check parser version") {
		t.Fatalf("incompatible parser version was accepted: %v", err)
	}
	var modelCalls atomic.Int64
	var blockFirstDocument atomic.Bool
	blockFirstDocument.Store(true)
	modelBlocked := make(chan struct{}, 1)
	revisionEntered, revisionRelease := make(chan struct{}, 1), make(chan struct{})
	var releaseRevision sync.Once
	defer releaseRevision.Do(func() { close(revisionRelease) })
	model := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			Model string   `json:"model"`
			Input []string `json:"input"`
		}
		if r.Method != http.MethodPost || r.URL.Path != "/v1/embeddings" ||
			json.NewDecoder(r.Body).Decode(&request) != nil || request.Model != "test" || len(request.Input) == 0 {
			t.Error("invalid model request")
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		modelCalls.Add(1)
		data := make([]any, 0, len(request.Input))
		tokens := 0
		for index, input := range request.Input {
			if strings.Contains(input, "修订版内容R72") {
				select {
				case revisionEntered <- struct{}{}:
				default:
				}
				select {
				case <-revisionRelease:
				case <-r.Context().Done():
					return
				}
			}
			if strings.Contains(input, "独特内容Q9") && blockFirstDocument.CompareAndSwap(true, false) {
				modelBlocked <- struct{}{}
				<-r.Context().Done()
				return
			}
			count, err := counter.Count(r.Context(), input)
			if err != nil {
				t.Error(err)
				w.WriteHeader(http.StatusInternalServerError)
				return
			}
			tokens += count
			vector := make([]float32, 64)
			vector[0] = 1
			data = append(data, map[string]any{"index": index, "embedding": vector})
		}
		if err := json.NewEncoder(w).Encode(map[string]any{"data": data, "usage": map[string]int{"prompt_tokens": tokens, "total_tokens": tokens}}); err != nil {
			t.Error(err)
		}
	}))
	defer model.Close()
	hasher, err := identity.NewPasswordHasher(1)
	if err != nil {
		t.Fatal(err)
	}
	identities, err := identity.NewService(pool, hasher, identity.SessionConfig{IdleTimeout: 8 * time.Hour, AbsoluteTimeout: 24 * time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	principal, err := identities.Bootstrap(ctx, identity.BootstrapRequest{TenantName: "worker process test", DisplayName: "owner",
		Login: "worker-test", Password: []byte("integration-only-password")})
	if err != nil {
		t.Fatal(err)
	}
	tenant, user := principal.TenantID, principal.UserID
	var provider, space int64
	statements := []struct {
		query string
		args  []any
		id    *int64
	}{
		{`INSERT INTO model_provider(tenant_id,name,base_url,locality) VALUES ($1,'test',$2,'internal') RETURNING id`, []any{tenant, model.URL}, &provider},
		{`INSERT INTO embedding_space(tenant_id,provider_id,model,dims,normalized,distance) VALUES ($1,$2,'test',64,true,'cosine') RETURNING id`, nil, &space},
	}
	for i := range statements {
		if i == 1 {
			statements[i].args = []any{tenant, provider}
		}
		if err := admin.QueryRow(ctx, statements[i].query, statements[i].args...).Scan(statements[i].id); err != nil {
			t.Fatal(err)
		}
	}
	knowledge, err := kb.NewService(pool)
	if err != nil {
		t.Fatal(err)
	}
	actor := kb.Actor{TenantID: tenant, UserID: user}
	base, err := knowledge.Create(ctx, actor, kb.CreateRequest{Name: "worker test", EmbeddingSpaceID: space, EgressPolicy: models.Deny})
	if err != nil {
		t.Fatal(err)
	}
	if err := indexing.Provision(ctx, migratorURL, tenant, space); err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	files, err := storage.NewLocal(ctx, root)
	if err != nil {
		t.Fatal(err)
	}
	defer files.Close()
	queue, err := jobs.NewStore(pool, jobs.RetryPolicy{Initial: time.Second, Maximum: time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	documents, err := kb.NewDocumentService(knowledge, files, queue)
	if err != nil {
		t.Fatal(err)
	}
	markdown, err := documents.Upload(ctx, actor, kb.UploadRequest{KBID: base.ID, Title: "policy.md", MIME: "text/markdown", Content: strings.NewReader("# 制度\n\n独特内容Q9需要先申请。\n")})
	if err != nil {
		t.Fatal(err)
	}
	csv, err := os.ReadFile(filepath.Join("..", "..", "..", "..", "services", "parser", "tests", "fixtures", "csv", "policy.csv"))
	if err != nil {
		t.Fatal(err)
	}
	table, err := documents.Upload(ctx, actor, kb.UploadRequest{KBID: base.ID, Title: "policy.csv", MIME: "text/csv", Content: bytes.NewReader(csv)})
	if err != nil {
		t.Fatal(err)
	}
	pdf, err := os.ReadFile(filepath.Join("..", "..", "..", "..", "services", "parser", "tests", "fixtures", "pdf", "policy.pdf"))
	if err != nil {
		t.Fatal(err)
	}
	unconfirmedPDF, err := documents.Upload(ctx, actor, kb.UploadRequest{KBID: base.ID, Title: "policy.pdf", MIME: "application/pdf", Content: bytes.NewReader(pdf)})
	if err != nil {
		t.Fatal(err)
	}
	scannedPDF, err := os.ReadFile(filepath.Join("..", "..", "..", "..", "services", "parser", "tests", "fixtures", "pdf", "scanned.pdf"))
	if err != nil {
		t.Fatal(err)
	}
	scanned, err := documents.Upload(ctx, actor, kb.UploadRequest{KBID: base.ID, Title: "scanned.pdf", MIME: "application/pdf", Content: bytes.NewReader(scannedPDF)})
	if err != nil {
		t.Fatal(err)
	}
	type confirmedUpload struct {
		document     kb.UploadedDocument
		tables       int
		markers      []string
		searchMarker string
	}
	var confirmed []confirmedUpload
	for _, fixture := range []struct {
		folder, file, mime string
		tables             int
		markers            []string
		searchMarker       string
	}{
		{"csv", "policy.csv", "text/csv", 1, []string{"城市", "北京", "上海", "成都", "450"}, "住宿上限: 450"},
		{"docx", "policy.docx", "application/vnd.openxmlformats-officedocument.wordprocessingml.document", 1, []string{"城市", "北京", "上海", "600元"}, "差旅管理办法"},
		{"xlsx", "travel.xlsx", "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet", 2, []string{"城市", "北京", "上海", "华东", "60.0"}, "地区: 华东"},
		{"pdf", "policy.pdf", "application/pdf", 1, []string{"城市", "北京", "上海", "600", "450"}, "差旅管理办法"},
	} {
		data, err := os.ReadFile(filepath.Join("..", "..", "..", "..", "services", "parser", "tests", "fixtures", fixture.folder, fixture.file))
		if err != nil {
			t.Fatal(err)
		}
		document, err := documents.Upload(ctx, actor, kb.UploadRequest{KBID: base.ID, Title: fixture.file, MIME: fixture.mime,
			Content: bytes.NewReader(data), AllowDuplicate: fixture.folder == "csv" || fixture.folder == "pdf", ConfirmFirstRowHeaders: true})
		if err != nil {
			t.Fatal(err)
		}
		confirmed = append(confirmed, confirmedUpload{document: document, tables: fixture.tables, markers: fixture.markers, searchMarker: fixture.searchMarker})
	}
	cmd := exec.Command(os.Args[0], "-test.run=^TestWorkerProcessHelper$")
	cmd.Env = append(os.Environ(),
		"TUSKRAG_TEST_WORKER_PROCESS=1", "TUSKRAG_ROLE=worker", "TUSKRAG_DATABASE_URL="+appURL,
		"TUSKRAG_STORAGE_DIR="+root, "TUSKRAG_PARSER_TEMP_DIR="+t.TempDir(),
		"TUSKRAG_BUBBLEWRAP="+os.Getenv("TUSKRAG_TEST_BWRAP"), "TUSKRAG_PARSER_RUNTIME_DIR="+os.Getenv("TUSKRAG_TEST_PARSER_RUNTIME"),
		"TUSKRAG_PARSER="+os.Getenv("TUSKRAG_TEST_PARSER"), "TUSKRAG_CGROUP_ROOT="+os.Getenv("TUSKRAG_TEST_CGROUP"),
		fmt.Sprintf("TUSKRAG_WORKER_TENANTS=%d", tenant), "TUSKRAG_MASTER_KEY_FILE=")
	var output bytes.Buffer
	cmd.Stdout, cmd.Stderr = &output, &output
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	stopped := false
	defer func() {
		if !stopped {
			_ = cmd.Process.Kill()
			<-done
		}
	}()
	select {
	case <-modelBlocked:
	case err := <-done:
		stopped = true
		t.Fatalf("worker exited before model call: %v; output: %s", err, output.String())
	case <-time.After(30 * time.Second):
		t.Fatal("worker did not reach the document model call")
	}
	if err := cmd.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	<-done
	stopped = true
	// Advance only this test job's lease after the worker has actually died.
	tag, err := admin.Exec(ctx, `UPDATE job SET lease_until=clock_timestamp()-interval '1 second'
		WHERE tenant_id=$1 AND kind='ingest' AND status='running' AND (payload->>'version_id')::bigint=$2`, tenant, markdown.VersionID)
	if err != nil || tag.RowsAffected() != 1 {
		t.Fatalf("expire killed worker lease: rows=%d err=%v", tag.RowsAffected(), err)
	}
	environment := cmd.Env
	cmd = exec.Command(os.Args[0], "-test.run=^TestWorkerProcessHelper$")
	cmd.Env = environment
	output.Reset()
	cmd.Stdout, cmd.Stderr = &output, &output
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	done = make(chan error, 1)
	stopped = false
	go func() { done <- cmd.Wait() }()
	deadline := time.NewTimer(90 * time.Second)
	defer deadline.Stop()
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for {
		var markdownStatus, tableStatus, tableFailure, pdfStatus, pdfFailure, scannedStatus, scannedFailure string
		var current, chunks, vectors, parsedTable, attempts int
		err := admin.QueryRow(ctx, `SELECT
			(SELECT status FROM document_version WHERE tenant_id=$1 AND id=$2),
			(SELECT status FROM document_version WHERE tenant_id=$1 AND id=$3),
			COALESCE((SELECT failure_code FROM document_version WHERE tenant_id=$1 AND id=$3),''),
			(SELECT count(*) FROM document WHERE tenant_id=$1 AND id=$4 AND current_version_id=$2),
			(SELECT count(*) FROM chunk WHERE tenant_id=$1 AND version_id=$2 AND is_current),
			(SELECT count(*) FROM chunk_vector cv JOIN chunk c ON c.tenant_id=cv.tenant_id AND c.id=cv.chunk_id WHERE c.tenant_id=$1 AND c.version_id=$2 AND cv.is_current),
			(SELECT count(*) FROM parse_artifact WHERE tenant_id=$1 AND version_id=$3),
			(SELECT attempts FROM job WHERE tenant_id=$1 AND kind='ingest' AND (payload->>'version_id')::bigint=$2),
			(SELECT status FROM document_version WHERE tenant_id=$1 AND id=$5),
			(SELECT COALESCE(failure_code,'') FROM document_version WHERE tenant_id=$1 AND id=$5),
			(SELECT status FROM document_version WHERE tenant_id=$1 AND id=$6),
			(SELECT COALESCE(failure_code,'') FROM document_version WHERE tenant_id=$1 AND id=$6)`, tenant, markdown.VersionID, table.VersionID, markdown.DocumentID, unconfirmedPDF.VersionID, scanned.VersionID).
			Scan(&markdownStatus, &tableStatus, &tableFailure, &current, &chunks, &vectors, &parsedTable, &attempts, &pdfStatus, &pdfFailure, &scannedStatus, &scannedFailure)
		if err != nil {
			t.Fatal(err)
		}
		allConfirmedReady := true
		for _, item := range confirmed {
			var status, failure string
			var tableChunks, chunks, vectors int
			var decision bool
			err := admin.QueryRow(ctx, `SELECT v.status,COALESCE(v.failure_code,''),v.confirm_first_row_headers,
				(SELECT count(*) FROM chunk WHERE tenant_id=$1 AND version_id=$2 AND kind='table' AND is_current),
				(SELECT count(*) FROM chunk WHERE tenant_id=$1 AND version_id=$2 AND is_current),
				(SELECT count(*) FROM chunk_vector cv JOIN chunk c ON c.tenant_id=cv.tenant_id AND c.id=cv.chunk_id
				 WHERE c.tenant_id=$1 AND c.version_id=$2 AND cv.is_current)
				FROM document_version v WHERE v.tenant_id=$1 AND v.id=$2`, tenant, item.document.VersionID).
				Scan(&status, &failure, &decision, &tableChunks, &chunks, &vectors)
			if err != nil {
				t.Fatal(err)
			}
			if status == "failed" {
				t.Fatalf("confirmed %s failed: %s", item.document.Title, failure)
			}
			if status != "ready" {
				allConfirmedReady = false
			} else if !decision || tableChunks != item.tables || chunks < tableChunks || vectors != chunks {
				t.Fatalf("confirmed %s: decision=%t tables=%d chunks=%d vectors=%d", item.document.Title, decision, tableChunks, chunks, vectors)
			} else {
				var content, searchText string
				if err := admin.QueryRow(ctx, `SELECT string_agg(content,E'\n' ORDER BY ordinal), string_agg(search_text,E'\n' ORDER BY ordinal)
					FROM chunk WHERE tenant_id=$1 AND version_id=$2 AND is_current`, tenant, item.document.VersionID).Scan(&content, &searchText); err != nil {
					t.Fatal(err)
				}
				for _, marker := range item.markers {
					if !strings.Contains(content, markdowntext.EscapeInline(marker)) {
						t.Fatalf("confirmed %s omitted synthetic table value %q", item.document.Title, marker)
					}
				}
				if item.searchMarker != "" && !strings.Contains(searchText, item.searchMarker) {
					t.Fatalf("confirmed %s omitted a searchable column value", item.document.Title)
				}
			}
		}
		if pdfStatus == "ready" || scannedStatus == "ready" {
			t.Fatalf("unconfirmed or scanned PDF became ready: pdf=%s scanned=%s", pdfStatus, scannedStatus)
		}
		if markdownStatus == "ready" && tableStatus == "failed" && pdfStatus == "failed" && scannedStatus == "failed" && allConfirmedReady {
			if tableFailure != string(jobs.TableHeaderRequired) || pdfFailure != string(jobs.TableHeaderRequired) || scannedFailure != string(jobs.QualityReview) || current != 1 || chunks != 1 || vectors != 1 || parsedTable != 1 || attempts != 2 || modelCalls.Load() < 28 {
				t.Fatalf("worker results: table=%s pdf=%s scanned=%s current=%d chunks=%d vectors=%d parsedTable=%d attempts=%d modelCalls=%d", tableFailure, pdfFailure, scannedFailure, current, chunks, vectors, parsedTable, attempts, modelCalls.Load())
			}
			break
		}
		select {
		case err := <-done:
			stopped = true
			t.Fatalf("worker exited early: %v; output: %s", err, output.String())
		case <-deadline.C:
			t.Fatalf("worker timed out: markdown=%s table=%s failure=%s", markdownStatus, tableStatus, tableFailure)
		case <-ticker.C:
		}
	}
	if err := cmd.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		stopped = true
		if err != nil {
			t.Fatalf("worker shutdown: %v; output: %s", err, output.String())
		}
	case <-time.After(10 * time.Second):
		t.Fatal("worker did not stop after SIGTERM")
	}
	testAllProcess(t, ctx, admin, cmd.Env, actor, base.ID, root, documents, revisionEntered, func() { releaseRevision.Do(func() { close(revisionRelease) }) })
}

func testAllProcess(t *testing.T, ctx context.Context, admin *pgx.Conn, environment []string, actor kb.Actor, kbID int64, root string, documents *kb.DocumentService, revisionEntered <-chan struct{}, releaseRevision func()) {
	t.Helper()
	uploaded, err := documents.Upload(ctx, actor, kb.UploadRequest{KBID: kbID, Title: "combined.md", MIME: "text/markdown", Content: strings.NewReader("# 综合模式\n\n另一份需要申请的制度。\n")})
	if err != nil {
		t.Fatal(err)
	}
	retryDocument, err := documents.Upload(ctx, actor, kb.UploadRequest{KBID: kbID, Title: "recover.md", MIME: "text/markdown", Content: strings.NewReader("# 恢复处理\n\n服务恢复后重新处理的制度。\n")})
	if err != nil {
		t.Fatal(err)
	}
	var failedJobID int64
	if err := admin.QueryRow(ctx, `UPDATE job SET status='dead',attempts=max_attempts,last_error='model_unavailable',finished_at=clock_timestamp()
		WHERE tenant_id=$1 AND kind='ingest' AND dedupe_key=$2 RETURNING id`, actor.TenantID, fmt.Sprintf("document-version:%d", retryDocument.VersionID)).Scan(&failedJobID); err != nil {
		t.Fatal(err)
	}
	if _, err := admin.Exec(ctx, `UPDATE document_version SET status='failed',failure_code='model_unavailable' WHERE tenant_id=$1 AND id=$2`, actor.TenantID, retryDocument.VersionID); err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	address := listener.Addr().String()
	if err := listener.Close(); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(os.Args[0], "-test.run=^TestWorkerProcessHelper$")
	cmd.Env = append(environment, "TUSKRAG_ROLE=all", "TUSKRAG_LISTEN="+address, "TUSKRAG_INSECURE_LOCAL_COOKIES=true")
	var output bytes.Buffer
	cmd.Stdout, cmd.Stderr = &output, &output
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	stopped := false
	defer func() {
		if !stopped {
			_ = cmd.Process.Kill()
			<-done
		}
	}()
	client := &http.Client{Timeout: 20 * time.Second}
	deadline := time.NewTimer(45 * time.Second)
	defer deadline.Stop()
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for {
		var status string
		if err := admin.QueryRow(ctx, `SELECT status FROM document_version WHERE tenant_id=$1 AND id=$2`, actor.TenantID, uploaded.VersionID).Scan(&status); err != nil {
			t.Fatal(err)
		}
		response, requestErr := client.Get("http://" + address + "/api/health/ready")
		ready := requestErr == nil && response.StatusCode == http.StatusOK
		if response != nil {
			_ = response.Body.Close()
		}
		if ready && status == "ready" {
			break
		}
		select {
		case err := <-done:
			stopped = true
			t.Fatalf("all process exited early: %v; output: %s", err, output.String())
		case <-deadline.C:
			t.Fatalf("all process timed out: HTTP ready=%t document=%s", ready, status)
		case <-ticker.C:
		}
	}
	loginBody := fmt.Sprintf(`{"tenant_id":%d,"login":"worker-test","password":"integration-only-password"}`, actor.TenantID)
	login, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://"+address+"/api/v1/auth/login", strings.NewReader(loginBody))
	if err != nil {
		t.Fatal(err)
	}
	login.Header.Set("Content-Type", "application/json")
	login.Header.Set("X-TuskRAG-Login", "1")
	response, err := client.Do(login)
	if err != nil {
		t.Fatal(err)
	}
	var session struct {
		CSRF string `json:"csrf_token"`
	}
	decodeErr := json.NewDecoder(response.Body).Decode(&session)
	closeErr := response.Body.Close()
	if response.StatusCode != http.StatusOK || decodeErr != nil || closeErr != nil || session.CSRF == "" || len(response.Cookies()) != 1 {
		t.Fatalf("login status=%d decode=%v close=%v", response.StatusCode, decodeErr, closeErr)
	}
	cookie := response.Cookies()[0]
	jobURL := fmt.Sprintf("http://%s/api/v1/jobs/%d", address, failedJobID)
	jobRequest, err := http.NewRequestWithContext(ctx, http.MethodGet, jobURL, nil)
	if err != nil {
		t.Fatal(err)
	}
	jobRequest.AddCookie(cookie)
	response, err = client.Do(jobRequest)
	if err != nil {
		t.Fatal(err)
	}
	var failedJob struct {
		ID        int64  `json:"id"`
		Status    string `json:"status"`
		Retryable bool   `json:"retryable"`
	}
	decodeErr = json.NewDecoder(response.Body).Decode(&failedJob)
	closeErr = response.Body.Close()
	if response.StatusCode != http.StatusOK || decodeErr != nil || closeErr != nil || failedJob.ID != failedJobID || failedJob.Status != "dead" || !failedJob.Retryable {
		t.Fatalf("read failed job status=%d result=%+v decode=%v close=%v", response.StatusCode, failedJob, decodeErr, closeErr)
	}
	retryRequest, err := http.NewRequestWithContext(ctx, http.MethodPost, jobURL+":retry", nil)
	if err != nil {
		t.Fatal(err)
	}
	retryRequest.AddCookie(cookie)
	retryRequest.Header.Set("X-CSRF-Token", session.CSRF)
	response, err = client.Do(retryRequest)
	if err != nil {
		t.Fatal(err)
	}
	var retryResponse struct {
		JobID  int64  `json:"job_id"`
		Status string `json:"status"`
	}
	decodeErr = json.NewDecoder(response.Body).Decode(&retryResponse)
	closeErr = response.Body.Close()
	if response.StatusCode != http.StatusAccepted || decodeErr != nil || closeErr != nil || retryResponse.JobID <= failedJobID || retryResponse.Status != "queued" {
		t.Fatalf("retry job status=%d result=%+v decode=%v close=%v", response.StatusCode, retryResponse, decodeErr, closeErr)
	}
	retryDeadline := time.NewTimer(45 * time.Second)
	defer retryDeadline.Stop()
	for {
		var versionStatus, newJobStatus, oldJobStatus string
		var current bool
		if err := admin.QueryRow(ctx, `SELECT
			(SELECT status FROM document_version WHERE tenant_id=$1 AND id=$2),
			(SELECT status FROM job WHERE tenant_id=$1 AND id=$3),
			(SELECT status FROM job WHERE tenant_id=$1 AND id=$4),
			(SELECT COALESCE(current_version_id=$2,false) FROM document WHERE tenant_id=$1 AND id=$5)`,
			actor.TenantID, retryDocument.VersionID, retryResponse.JobID, failedJobID, retryDocument.DocumentID).
			Scan(&versionStatus, &newJobStatus, &oldJobStatus, &current); err != nil {
			t.Fatal(err)
		}
		if versionStatus == "ready" && newJobStatus == "succeeded" && oldJobStatus == "dead" && current {
			break
		}
		if versionStatus == "failed" || newJobStatus == "failed" || newJobStatus == "dead" {
			t.Fatalf("retried document failed: version=%s new job=%s old job=%s", versionStatus, newJobStatus, oldJobStatus)
		}
		select {
		case err := <-done:
			stopped = true
			t.Fatalf("all process exited during retry: %v; output: %s", err, output.String())
		case <-retryDeadline.C:
			t.Fatalf("retried document timed out: version=%s new job=%s", versionStatus, newJobStatus)
		case <-ticker.C:
		}
	}
	var uploadBody bytes.Buffer
	form := multipart.NewWriter(&uploadBody)
	part, err := form.CreateFormFile("file", "live.csv")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := part.Write([]byte("城市,限额\n杭州,650\n南京,500\n")); err != nil {
		t.Fatal(err)
	}
	if err := form.Close(); err != nil {
		t.Fatal(err)
	}
	uploadURL := fmt.Sprintf("http://%s/api/v1/kbs/%d/documents?confirm_first_row_headers=true", address, kbID)
	upload, err := http.NewRequestWithContext(ctx, http.MethodPost, uploadURL, &uploadBody)
	if err != nil {
		t.Fatal(err)
	}
	upload.Header.Set("Content-Type", form.FormDataContentType())
	upload.Header.Set("X-CSRF-Token", session.CSRF)
	upload.AddCookie(cookie)
	response, err = client.Do(upload)
	if err != nil {
		t.Fatal(err)
	}
	var received struct {
		VersionID int64  `json:"version_id"`
		Status    string `json:"status"`
	}
	decodeErr = json.NewDecoder(response.Body).Decode(&received)
	closeErr = response.Body.Close()
	if response.StatusCode != http.StatusOK || decodeErr != nil || closeErr != nil || received.VersionID <= 0 || received.Status != "queued" {
		t.Fatalf("upload status=%d result=%+v decode=%v close=%v", response.StatusCode, received, decodeErr, closeErr)
	}
	uploadDeadline := time.NewTimer(45 * time.Second)
	defer uploadDeadline.Stop()
	for {
		var status, failure string
		if err := admin.QueryRow(ctx, `SELECT status,COALESCE(failure_code,'') FROM document_version WHERE tenant_id=$1 AND id=$2`, actor.TenantID, received.VersionID).Scan(&status, &failure); err != nil {
			t.Fatal(err)
		}
		if status == "ready" {
			break
		}
		if status == "failed" {
			t.Fatalf("HTTP uploaded table failed: %s", failure)
		}
		select {
		case err := <-done:
			stopped = true
			t.Fatalf("all process exited during upload: %v; output: %s", err, output.String())
		case <-uploadDeadline.C:
			t.Fatalf("HTTP uploaded table timed out: %s", status)
		case <-ticker.C:
		}
	}
	searchBody := fmt.Sprintf(`{"query":"杭州","kb_ids":[%d],"options":{"rerank":false}}`, kbID)
	search, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://"+address+"/api/v1/search", strings.NewReader(searchBody))
	if err != nil {
		t.Fatal(err)
	}
	search.Header.Set("Content-Type", "application/json")
	search.Header.Set("X-CSRF-Token", session.CSRF)
	search.AddCookie(cookie)
	response, err = client.Do(search)
	if err != nil {
		t.Fatal(err)
	}
	var searchResult struct {
		Status   string   `json:"status"`
		Degraded []string `json:"degraded"`
		Evidence []struct {
			ScoreKind string `json:"score_kind"`
			Content   string `json:"content"`
			Document  struct {
				Title string `json:"title"`
			} `json:"document"`
		} `json:"evidence"`
	}
	decodeErr = json.NewDecoder(response.Body).Decode(&searchResult)
	closeErr = response.Body.Close()
	if response.StatusCode != http.StatusOK || decodeErr != nil || closeErr != nil || searchResult.Status != "ok" ||
		len(searchResult.Evidence) == 0 || searchResult.Evidence[0].ScoreKind != "rrf" || len(searchResult.Degraded) != 1 || searchResult.Degraded[0] != "rerank" {
		t.Fatalf("search status=%d result=%+v decode=%v close=%v", response.StatusCode, searchResult, decodeErr, closeErr)
	}
	found := false
	for _, evidence := range searchResult.Evidence {
		if evidence.Document.Title == "live.csv" && strings.Contains(evidence.Content, "杭州") {
			found = true
		}
	}
	if !found {
		t.Fatal("search did not return the HTTP-uploaded table")
	}
	pdfData, err := os.ReadFile(filepath.Join("..", "..", "..", "..", "services", "parser", "tests", "fixtures", "pdf", "policy.pdf"))
	if err != nil {
		t.Fatal(err)
	}
	var pdfBody bytes.Buffer
	pdfForm := multipart.NewWriter(&pdfBody)
	pdfPart, err := pdfForm.CreateFormFile("file", "live-policy.pdf")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pdfPart.Write(pdfData); err != nil {
		t.Fatal(err)
	}
	if err := pdfForm.Close(); err != nil {
		t.Fatal(err)
	}
	pdfUpload, err := http.NewRequestWithContext(ctx, http.MethodPost, uploadURL+"&allow_duplicate=true", &pdfBody)
	if err != nil {
		t.Fatal(err)
	}
	pdfUpload.Header.Set("Content-Type", pdfForm.FormDataContentType())
	pdfUpload.Header.Set("X-CSRF-Token", session.CSRF)
	pdfUpload.AddCookie(cookie)
	response, err = client.Do(pdfUpload)
	if err != nil {
		t.Fatal(err)
	}
	var pdfReceived struct {
		DocumentID int64  `json:"document_id"`
		VersionID  int64  `json:"version_id"`
		MIME       string `json:"mime"`
		Status     string `json:"status"`
	}
	decodeErr = json.NewDecoder(response.Body).Decode(&pdfReceived)
	closeErr = response.Body.Close()
	if response.StatusCode != http.StatusOK || decodeErr != nil || closeErr != nil || pdfReceived.DocumentID <= 0 || pdfReceived.VersionID <= 0 || pdfReceived.MIME != "application/pdf" || pdfReceived.Status != "queued" {
		t.Fatalf("PDF upload status=%d result=%+v decode=%v close=%v", response.StatusCode, pdfReceived, decodeErr, closeErr)
	}
	pdfDeadline := time.NewTimer(45 * time.Second)
	defer pdfDeadline.Stop()
	for {
		var status, failure string
		if err := admin.QueryRow(ctx, `SELECT status,COALESCE(failure_code,'') FROM document_version WHERE tenant_id=$1 AND id=$2`, actor.TenantID, pdfReceived.VersionID).Scan(&status, &failure); err != nil {
			t.Fatal(err)
		}
		if status == "ready" {
			break
		}
		if status == "failed" {
			t.Fatalf("HTTP uploaded PDF failed: %s", failure)
		}
		select {
		case err := <-done:
			stopped = true
			t.Fatalf("all process exited during PDF upload: %v; output: %s", err, output.String())
		case <-pdfDeadline.C:
			t.Fatalf("HTTP uploaded PDF timed out: %s", status)
		case <-ticker.C:
		}
	}
	pdfQuery := fmt.Sprintf(`{"query":"住宿上限","kb_ids":[%d],"top_k":30,"filters":{"mimes":["application/pdf"]},"options":{"rerank":false}}`, kbID)
	pdfSearch, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://"+address+"/api/v1/search", strings.NewReader(pdfQuery))
	if err != nil {
		t.Fatal(err)
	}
	pdfSearch.Header.Set("Content-Type", "application/json")
	pdfSearch.Header.Set("X-CSRF-Token", session.CSRF)
	pdfSearch.AddCookie(cookie)
	response, err = client.Do(pdfSearch)
	if err != nil {
		t.Fatal(err)
	}
	var pdfResult struct {
		Evidence []struct {
			Content  string  `json:"content"`
			Pages    []int32 `json:"pages"`
			Document struct {
				VersionID int64 `json:"version_id"`
			} `json:"document"`
			Section struct {
				ID int64 `json:"id"`
			} `json:"section"`
		} `json:"evidence"`
	}
	decodeErr = json.NewDecoder(response.Body).Decode(&pdfResult)
	closeErr = response.Body.Close()
	if response.StatusCode != http.StatusOK || decodeErr != nil || closeErr != nil {
		t.Fatalf("PDF search status=%d decode=%v close=%v", response.StatusCode, decodeErr, closeErr)
	}
	var pdfSectionID int64
	for _, evidence := range pdfResult.Evidence {
		if evidence.Document.VersionID == pdfReceived.VersionID && strings.Contains(evidence.Content, "北京") &&
			len(evidence.Pages) == 2 && evidence.Pages[0] == 1 && evidence.Pages[1] == 1 {
			pdfSectionID = evidence.Section.ID
		}
	}
	if pdfSectionID <= 0 {
		t.Fatal("search did not return the PDF table with its physical page")
	}
	tocURL := fmt.Sprintf("http://%s/api/v1/documents/%d/toc?cursor=0&limit=30", address, pdfReceived.DocumentID)
	tocRequest, err := http.NewRequestWithContext(ctx, http.MethodGet, tocURL, nil)
	if err != nil {
		t.Fatal(err)
	}
	tocRequest.AddCookie(cookie)
	response, err = client.Do(tocRequest)
	if err != nil {
		t.Fatal(err)
	}
	var toc struct {
		Document struct {
			ID        int64 `json:"id"`
			VersionID int64 `json:"version_id"`
		} `json:"document"`
		Sections []struct {
			ID    int64   `json:"id"`
			Pages []int32 `json:"pages"`
		} `json:"sections"`
	}
	decodeErr = json.NewDecoder(response.Body).Decode(&toc)
	closeErr = response.Body.Close()
	if response.StatusCode != http.StatusOK || decodeErr != nil || closeErr != nil || toc.Document.ID != pdfReceived.DocumentID || toc.Document.VersionID != pdfReceived.VersionID {
		t.Fatalf("PDF contents status=%d result=%+v decode=%v close=%v", response.StatusCode, toc, decodeErr, closeErr)
	}
	foundSection := false
	for _, section := range toc.Sections {
		if section.ID == pdfSectionID && len(section.Pages) == 2 && section.Pages[0] == 1 && section.Pages[1] >= 1 {
			foundSection = true
		}
	}
	if !foundSection {
		t.Fatalf("PDF contents omitted the cited section or its page: %+v", toc.Sections)
	}
	sectionRequest, err := http.NewRequestWithContext(ctx, http.MethodGet, fmt.Sprintf("http://%s/api/v1/sections/%d", address, pdfSectionID), nil)
	if err != nil {
		t.Fatal(err)
	}
	sectionRequest.AddCookie(cookie)
	response, err = client.Do(sectionRequest)
	if err != nil {
		t.Fatal(err)
	}
	var sectionBody struct {
		Markdown string `json:"markdown"`
		Document struct {
			ID        int64 `json:"id"`
			VersionID int64 `json:"version_id"`
		} `json:"document"`
	}
	decodeErr = json.NewDecoder(response.Body).Decode(&sectionBody)
	closeErr = response.Body.Close()
	if response.StatusCode != http.StatusOK || decodeErr != nil || closeErr != nil || sectionBody.Document.ID != pdfReceived.DocumentID || sectionBody.Document.VersionID != pdfReceived.VersionID ||
		!strings.Contains(sectionBody.Markdown, "北京") || !strings.Contains(sectionBody.Markdown, "600") {
		t.Fatalf("PDF section status=%d result=%+v decode=%v close=%v", response.StatusCode, sectionBody, decodeErr, closeErr)
	}
	assertSearchVersion := func(versionID int64, expectedText string) {
		t.Helper()
		body := fmt.Sprintf(`{"query":"制度","kb_ids":[%d],"top_k":30,"options":{"rerank":false}}`, kbID)
		request, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://"+address+"/api/v1/search", strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		request.Header.Set("Content-Type", "application/json")
		request.Header.Set("X-CSRF-Token", session.CSRF)
		request.AddCookie(cookie)
		response, err := client.Do(request)
		if err != nil {
			t.Fatal(err)
		}
		var result struct {
			Evidence []struct {
				Content  string `json:"content"`
				Document struct {
					ID        int64 `json:"id"`
					VersionID int64 `json:"version_id"`
				} `json:"document"`
			} `json:"evidence"`
		}
		decodeErr := json.NewDecoder(response.Body).Decode(&result)
		closeErr := response.Body.Close()
		if response.StatusCode != http.StatusOK || decodeErr != nil || closeErr != nil {
			t.Fatalf("version search status=%d decode=%v close=%v", response.StatusCode, decodeErr, closeErr)
		}
		found := false
		for _, evidence := range result.Evidence {
			if evidence.Document.ID != uploaded.DocumentID {
				continue
			}
			if evidence.Document.VersionID != versionID {
				t.Fatalf("mixed version evidence: %+v", evidence.Document)
			}
			if strings.Contains(evidence.Content, expectedText) {
				found = true
			}
		}
		if !found {
			t.Fatalf("expected version %d with %q missing from search: %+v", versionID, expectedText, result.Evidence)
		}
	}
	assertSearchVersion(uploaded.VersionID, "另一份需要申请的制度")
	var revisionBody bytes.Buffer
	revisionForm := multipart.NewWriter(&revisionBody)
	revisionPart, err := revisionForm.CreateFormFile("file", "combined.md")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := io.WriteString(revisionPart, "# 综合模式\n\n修订版内容R72需要复核。\n"); err != nil {
		t.Fatal(err)
	}
	if err := revisionForm.Close(); err != nil {
		t.Fatal(err)
	}
	revisionURL := fmt.Sprintf("http://%s/api/v1/kbs/%d/documents/%d/versions", address, kbID, uploaded.DocumentID)
	revision, err := http.NewRequestWithContext(ctx, http.MethodPost, revisionURL, &revisionBody)
	if err != nil {
		t.Fatal(err)
	}
	revision.Header.Set("Content-Type", revisionForm.FormDataContentType())
	revision.Header.Set("X-CSRF-Token", session.CSRF)
	revision.Header.Set("Idempotency-Key", "worker-version-test")
	revision.AddCookie(cookie)
	response, err = client.Do(revision)
	if err != nil {
		t.Fatal(err)
	}
	var newVersion struct {
		DocumentID int64  `json:"document_id"`
		VersionID  int64  `json:"version_id"`
		Status     string `json:"status"`
	}
	decodeErr, closeErr = json.NewDecoder(response.Body).Decode(&newVersion), response.Body.Close()
	if response.StatusCode != http.StatusOK || decodeErr != nil || closeErr != nil || newVersion.DocumentID != uploaded.DocumentID || newVersion.VersionID == uploaded.VersionID || newVersion.Status != "queued" {
		t.Fatalf("version upload: status=%d result=%+v decode=%v close=%v", response.StatusCode, newVersion, decodeErr, closeErr)
	}
	select {
	case <-revisionEntered:
	case err := <-done:
		stopped = true
		t.Fatalf("all process exited during version processing: %v; output: %s", err, output.String())
	case <-time.After(45 * time.Second):
		t.Fatal("new version did not reach embedding")
	}
	assertSearchVersion(uploaded.VersionID, "另一份需要申请的制度")
	releaseRevision()
	revisionDeadline := time.NewTimer(45 * time.Second)
	defer revisionDeadline.Stop()
	for {
		var status, failure string
		var current int64
		if err := admin.QueryRow(ctx, `SELECT v.status,COALESCE(v.failure_code,''),d.current_version_id FROM document_version v JOIN document d
			ON d.tenant_id=v.tenant_id AND d.id=v.document_id WHERE v.tenant_id=$1 AND v.id=$2`, actor.TenantID, newVersion.VersionID).Scan(&status, &failure, &current); err != nil {
			t.Fatal(err)
		}
		if status == "ready" && current == newVersion.VersionID {
			break
		}
		if status == "failed" {
			t.Fatalf("revision failed: %s", failure)
		}
		select {
		case err := <-done:
			stopped = true
			t.Fatalf("all process exited during revision: %v; output: %s", err, output.String())
		case <-revisionDeadline.C:
			t.Fatalf("revision timed out: status=%s current=%d", status, current)
		case <-ticker.C:
		}
	}
	assertSearchVersion(newVersion.VersionID, "修订版内容R72")
	var badBody bytes.Buffer
	badForm := multipart.NewWriter(&badBody)
	badPart, err := badForm.CreateFormFile("file", "combined.md")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := io.WriteString(badPart, "# 综合模式\n\n![不支持的图片](image.png)\n"); err != nil {
		t.Fatal(err)
	}
	if err := badForm.Close(); err != nil {
		t.Fatal(err)
	}
	badRequest, err := http.NewRequestWithContext(ctx, http.MethodPost, revisionURL, &badBody)
	if err != nil {
		t.Fatal(err)
	}
	badRequest.Header.Set("Content-Type", badForm.FormDataContentType())
	badRequest.Header.Set("X-CSRF-Token", session.CSRF)
	badRequest.AddCookie(cookie)
	response, err = client.Do(badRequest)
	if err != nil {
		t.Fatal(err)
	}
	var badVersion struct {
		VersionID int64  `json:"version_id"`
		Status    string `json:"status"`
	}
	decodeErr, closeErr = json.NewDecoder(response.Body).Decode(&badVersion), response.Body.Close()
	if response.StatusCode != http.StatusOK || decodeErr != nil || closeErr != nil || badVersion.VersionID <= newVersion.VersionID || badVersion.Status != "queued" {
		t.Fatalf("failing revision upload: status=%d result=%+v decode=%v close=%v", response.StatusCode, badVersion, decodeErr, closeErr)
	}
	badDeadline := time.NewTimer(45 * time.Second)
	defer badDeadline.Stop()
	for {
		var status, failure string
		var current int64
		if err := admin.QueryRow(ctx, `SELECT v.status,COALESCE(v.failure_code,''),d.current_version_id FROM document_version v JOIN document d
			ON d.tenant_id=v.tenant_id AND d.id=v.document_id WHERE v.tenant_id=$1 AND v.id=$2`, actor.TenantID, badVersion.VersionID).Scan(&status, &failure, &current); err != nil {
			t.Fatal(err)
		}
		if status == "failed" {
			if failure != string(jobs.Unsupported) || current != newVersion.VersionID {
				t.Fatalf("failed revision changed current version: failure=%s current=%d", failure, current)
			}
			break
		}
		select {
		case err := <-done:
			stopped = true
			t.Fatalf("all process exited during failing revision: %v; output: %s", err, output.String())
		case <-badDeadline.C:
			t.Fatalf("failing revision timed out: status=%s", status)
		case <-ticker.C:
		}
	}
	assertSearchVersion(newVersion.VersionID, "修订版内容R72")
	deleteURL := fmt.Sprintf("http://%s/api/v1/kbs/%d/documents/%d", address, kbID, uploaded.DocumentID)
	deleteRequest, err := http.NewRequestWithContext(ctx, http.MethodDelete, deleteURL, nil)
	if err != nil {
		t.Fatal(err)
	}
	deleteRequest.Header.Set("X-CSRF-Token", session.CSRF)
	deleteRequest.AddCookie(cookie)
	response, err = client.Do(deleteRequest)
	if err != nil {
		t.Fatal(err)
	}
	var deletion struct {
		Status string `json:"status"`
	}
	decodeErr, closeErr = json.NewDecoder(response.Body).Decode(&deletion), response.Body.Close()
	if response.StatusCode != http.StatusAccepted || decodeErr != nil || closeErr != nil || deletion.Status != "deleting" {
		t.Fatalf("delete response: status=%d result=%+v decode=%v close=%v", response.StatusCode, deletion, decodeErr, closeErr)
	}
	searchBody = fmt.Sprintf(`{"query":"制度","kb_ids":[%d],"top_k":30,"options":{"rerank":false}}`, kbID)
	search, err = http.NewRequestWithContext(ctx, http.MethodPost, "http://"+address+"/api/v1/search", strings.NewReader(searchBody))
	if err != nil {
		t.Fatal(err)
	}
	search.Header.Set("Content-Type", "application/json")
	search.Header.Set("X-CSRF-Token", session.CSRF)
	search.AddCookie(cookie)
	response, err = client.Do(search)
	if err != nil {
		t.Fatal(err)
	}
	var afterDelete struct {
		Evidence []struct {
			Document struct {
				ID int64 `json:"id"`
			} `json:"document"`
		} `json:"evidence"`
	}
	decodeErr, closeErr = json.NewDecoder(response.Body).Decode(&afterDelete), response.Body.Close()
	if response.StatusCode != http.StatusOK || decodeErr != nil || closeErr != nil {
		t.Fatalf("search after delete: status=%d decode=%v close=%v", response.StatusCode, decodeErr, closeErr)
	}
	for _, evidence := range afterDelete.Evidence {
		if evidence.Document.ID == uploaded.DocumentID {
			t.Fatal("deleted document was returned by search")
		}
	}
	purgeDeadline := time.NewTimer(45 * time.Second)
	defer purgeDeadline.Stop()
	for {
		var versions, chunks, activePurges, failedPurges int
		var deleted, noCurrent bool
		if err := admin.QueryRow(ctx, `SELECT d.deleted_at IS NOT NULL,d.current_version_id IS NULL,
			(SELECT count(*) FROM document_version v WHERE v.tenant_id=d.tenant_id AND v.document_id=d.id),
			(SELECT count(*) FROM chunk c WHERE c.tenant_id=d.tenant_id AND c.document_id=d.id),
			(SELECT count(*) FROM job j WHERE j.tenant_id=d.tenant_id AND j.kind='purge' AND j.payload->>'document_id'=d.id::text AND j.status IN ('queued','running')),
			(SELECT count(*) FROM job j WHERE j.tenant_id=d.tenant_id AND j.kind='purge' AND j.payload->>'document_id'=d.id::text AND j.status IN ('failed','dead'))
			FROM document d WHERE d.tenant_id=$1 AND d.id=$2`, actor.TenantID, uploaded.DocumentID).
			Scan(&deleted, &noCurrent, &versions, &chunks, &activePurges, &failedPurges); err != nil {
			t.Fatal(err)
		}
		if failedPurges > 0 {
			t.Fatalf("document cleanup failed: %d jobs", failedPurges)
		}
		if deleted && noCurrent && versions == 0 && chunks == 0 && activePurges == 0 {
			break
		}
		select {
		case err := <-done:
			stopped = true
			t.Fatalf("all process exited during cleanup: %v; output: %s", err, output.String())
		case <-purgeDeadline.C:
			t.Fatalf("document cleanup timed out: deleted=%t versions=%d chunks=%d active=%d", deleted, versions, chunks, activePurges)
		case <-ticker.C:
		}
	}
	for _, versionID := range []int64{uploaded.VersionID, newVersion.VersionID, badVersion.VersionID} {
		key, err := (storage.Key{TenantID: actor.TenantID, KBID: kbID, DocumentID: uploaded.DocumentID, VersionID: versionID}).Path()
		if err != nil {
			t.Fatal(err)
		}
		if _, err := os.Stat(filepath.Join(root, filepath.FromSlash(key))); !os.IsNotExist(err) {
			t.Fatalf("deleted version original still exists: version=%d err=%v", versionID, err)
		}
	}
	if err := cmd.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		stopped = true
		if err != nil {
			t.Fatalf("all process shutdown: %v; output: %s", err, output.String())
		}
	case <-time.After(10 * time.Second):
		t.Fatal("all process did not stop after SIGTERM")
	}
}

func workerTestDatabase(t *testing.T) (string, string, string) {
	t.Helper()
	names := []string{"TUSKRAG_TEST_ADMIN_URL", "TUSKRAG_TEST_APP_URL", "TUSKRAG_TEST_MIGRATOR_URL"}
	urls := make([]*url.URL, len(names))
	for i, name := range names {
		if os.Getenv(name) == "" {
			t.Skip("worker process test requires isolated PostgreSQL URLs")
		}
		u, err := url.Parse(os.Getenv(name))
		if err != nil || u == nil || u.User == nil || (u.Scheme != "postgres" && u.Scheme != "postgresql") ||
			(u.Hostname() != "127.0.0.1" && u.Hostname() != "localhost" && u.Hostname() != "::1") ||
			!strings.HasPrefix(strings.TrimPrefix(u.Path, "/"), "tuskrag_test_") {
			t.Fatal("worker test requires loopback tuskrag_test_ database URLs")
		}
		if i > 0 && (u.Host != urls[0].Host || u.Path != urls[0].Path) {
			t.Fatal("worker test URLs must address the same isolated database")
		}
		urls[i] = u
	}
	if urls[1].User.Username() != "tuskrag_app" || urls[2].User.Username() == "" {
		t.Fatal("worker test requires application and migration roles")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	root, err := pgx.Connect(ctx, urls[0].String())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = root.Close(context.Background()) })
	var nonce [8]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		t.Fatal(err)
	}
	name := "tuskrag_test_worker_" + hex.EncodeToString(nonce[:])
	owner := urls[2].User.Username()
	if _, err := root.Exec(ctx, "CREATE DATABASE "+pgx.Identifier{name}.Sanitize()+" OWNER "+pgx.Identifier{owner}.Sanitize()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cleanup, done := context.WithTimeout(context.Background(), 20*time.Second)
		defer done()
		if _, err := root.Exec(cleanup, "DROP DATABASE "+pgx.Identifier{name}.Sanitize()); err != nil {
			t.Errorf("drop owned worker test database: %v", err)
		}
	})
	for _, u := range urls {
		u.Path = "/" + name
	}
	admin, err := pgx.Connect(ctx, urls[0].String())
	if err != nil {
		t.Fatal(err)
	}
	for _, statement := range []string{"CREATE EXTENSION vector", "CREATE EXTENSION pg_search", "ALTER SCHEMA public OWNER TO " + pgx.Identifier{owner}.Sanitize(), "REVOKE CREATE ON SCHEMA public FROM PUBLIC"} {
		if _, err := admin.Exec(ctx, statement); err != nil {
			_ = admin.Close(context.Background())
			t.Fatal(err)
		}
	}
	if err := admin.Close(ctx); err != nil {
		t.Fatal(err)
	}
	if err := db.Migrate(ctx, urls[2].String()); err != nil {
		t.Fatal(err)
	}
	return urls[0].String(), urls[1].String(), urls[2].String()
}
