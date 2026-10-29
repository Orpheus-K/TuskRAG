package ingest

import (
	"context"
	"errors"
	"fmt"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"tuskrag.local/core/internal/kb"
	"tuskrag.local/core/internal/parser"
	"tuskrag.local/core/internal/tokenizer"
)

func TestBuiltPlanStagesParsedDocumentAndVectors(t *testing.T) {
	f := setup(t)
	source := "# 差旅制度\n\n" + strings.Repeat("出差须先申请。主管需要核对目的。票据应真实完整。", 4) +
		"\n\n| 城市 | 限额 |\n| --- | --- |\n| 北京 | 600 |\n\n12. 第十二条：报销规定。\n\n" +
		"    - 事前提交申请。\n    - 票据需真实完整。\n    - 标准依据出差城市。\n    - 超标须提前审批。\n13. 第十三条：复核。\n\n```\nreturn 600\n```\n"
	knowledge, err := kb.NewService(f.pool)
	if err != nil {
		t.Fatal(err)
	}
	documents, err := kb.NewDocumentService(knowledge, f.files, f.queue)
	if err != nil {
		t.Fatal(err)
	}
	uploaded, err := documents.Upload(f.ctx, kb.Actor{TenantID: f.tenant, UserID: f.user}, kb.UploadRequest{
		KBID: f.kbID, Title: "policy.md", MIME: "text/markdown", Content: strings.NewReader(source),
	})
	if err != nil {
		t.Fatal(err)
	}
	job, err := f.queue.Claim(f.ctx, f.tenant, "built-plan-test", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := makeStage(t, f, nativePipelineParser(t)).Parse(f.ctx, job)
	if err != nil {
		t.Fatal(err)
	}
	normalized, err := AssembleMarkdown(f.ctx, parsed)
	if err != nil {
		t.Fatal(err)
	}
	var space int64
	var revision int32
	var profile EmbeddingProfile
	if err := f.admin.QueryRow(f.ctx, `SELECT k.embedding_space_id, k.config_revision, e.provider_id, e.model, e.dims, e.normalized, e.distance
		FROM knowledge_base k JOIN embedding_space e ON e.tenant_id=k.tenant_id AND e.id=k.embedding_space_id
		WHERE k.tenant_id=$1 AND k.id=$2`, f.tenant, f.kbID).Scan(&space, &revision, &profile.ProviderID, &profile.Model,
		&profile.Dimensions, &profile.Normalized, &profile.Distance); err != nil {
		t.Fatal(err)
	}
	counter, err := tokenizer.NewBGEM3(f.ctx, 1)
	if err != nil {
		t.Fatal(err)
	}
	request := planRequest(normalized)
	request.EmbeddingSpaceID, request.EmbeddingProfile, request.ConfigRevision = space, profile, revision
	request.CountTokens = counter.Count
	request.ParentMaxTokens = 40
	request.TableHeaders = map[BlockPosition]int{{Section: 1, Block: 2}: 1}
	plan, err := BuildPlan(f.ctx, request)
	if err != nil {
		t.Fatal(err)
	}
	var kinds []string
	for _, chunk := range plan.Chunks {
		kinds = append(kinds, chunk.Kind)
		actual, err := counter.Count(f.ctx, chunk.SearchText)
		if err != nil || actual != int(chunk.TokenCount) {
			t.Fatalf("search text token count changed: got=%d want=%d err=%v", chunk.TokenCount, actual, err)
		}
	}
	tableIndex := -1
	for index, kind := range kinds {
		if kind == "table" {
			tableIndex = index
			break
		}
	}
	if tableIndex < 2 || len(kinds)-tableIndex-2 < 2 || kinds[len(kinds)-1] != "code" {
		t.Fatalf("parsed blocks not represented in order: %v", kinds)
	}
	for _, kind := range kinds[:tableIndex] {
		if kind != "text" {
			t.Fatalf("long paragraph not preserved in order: %v", kinds)
		}
	}
	var listContent strings.Builder
	for index := tableIndex + 1; index < len(kinds)-1; index++ {
		if kinds[index] != "list" {
			t.Fatalf("long list not preserved in order: %v", kinds)
		}
		listContent.WriteString(plan.Chunks[index].Content)
	}
	previous := -1
	for _, item := range []string{"事前提交申请", "票据需真实完整", "标准依据出差城市", "超标须提前审批", "第十三条"} {
		position := strings.Index(listContent.String(), item)
		if position <= previous || strings.Count(listContent.String(), item) != 1 {
			t.Fatalf("long list items lost or reordered: %s", listContent.String())
		}
		previous = position
	}
	planStage, err := NewPlanStage(f.queue)
	if err != nil {
		t.Fatal(err)
	}
	staged, err := planStage.Stage(f.ctx, job, plan)
	if err != nil || len(staged.Chunks) != len(plan.Chunks) || staged.Reused {
		t.Fatalf("stage built plan: %+v %v", staged, err)
	}
	replayed, err := planStage.Stage(f.ctx, job, plan)
	if err != nil || !replayed.Reused || !reflect.DeepEqual(staged.Chunks, replayed.Chunks) {
		t.Fatalf("built plan replay changed rows: %+v %v", replayed, err)
	}
	vectorStage, err := NewVectorStage(f.queue)
	if err != nil {
		t.Fatal(err)
	}
	inputs := make([]VectorInput, len(staged.Chunks))
	for index := range inputs {
		inputs[index] = vectorInput(staged, index, testVector(1, float32(index)))
		var content, search, kind string
		if err := f.admin.QueryRow(f.ctx, `SELECT content, search_text, kind FROM chunk WHERE tenant_id=$1 AND id=$2`, f.tenant, staged.Chunks[index].ID).Scan(&content, &search, &kind); err != nil {
			t.Fatal(err)
		}
		if content != plan.Chunks[index].Content || search != plan.Chunks[index].SearchText || kind != plan.Chunks[index].Kind {
			t.Fatal("staged chunk differs from its plan")
		}
	}
	if err := vectorStage.Write(f.ctx, job, staged.Identity, inputs); err != nil {
		t.Fatal(err)
	}
	if err := vectorStage.Finish(f.ctx, job, staged.Identity); err != nil {
		t.Fatal(err)
	}
	var status string
	var noCurrent bool
	var vectors, currentChunks, currentVectors int
	if err := f.admin.QueryRow(f.ctx, `SELECT v.status, d.current_version_id IS NULL,
		(SELECT count(*) FROM chunk_vector WHERE tenant_id=$1 AND space_id=$3),
		(SELECT count(*) FROM chunk WHERE tenant_id=$1 AND version_id=$2 AND is_current),
		(SELECT count(*) FROM chunk_vector WHERE tenant_id=$1 AND space_id=$3 AND is_current)
		FROM document_version v JOIN document d ON d.tenant_id=v.tenant_id AND d.id=v.document_id
		WHERE v.tenant_id=$1 AND v.id=$2`, f.tenant, uploaded.VersionID, space).Scan(&status, &noCurrent, &vectors, &currentChunks, &currentVectors); err != nil {
		t.Fatal(err)
	}
	if status != "indexing" || !noCurrent || vectors != len(plan.Chunks) || currentChunks != 0 || currentVectors != 0 {
		t.Fatalf("staging published or lost data: status=%s vectors=%d currentChunks=%d currentVectors=%d", status, vectors, currentChunks, currentVectors)
	}
	tx, err := f.pool.Begin(f.ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	if _, err := tx.Exec(f.ctx, `SELECT set_config('tuskrag.tenant_id',$1,true)`, fmt.Sprint(f.foreign)); err != nil {
		t.Fatal(err)
	}
	var visible int
	if err := tx.QueryRow(f.ctx, `SELECT count(*) FROM chunk WHERE version_id=$1`, uploaded.VersionID).Scan(&visible); err != nil || visible != 0 {
		t.Fatalf("foreign tenant saw staged chunks: count=%d err=%v", visible, err)
	}
}

func TestBuiltLargeTableFromSandboxStagesRows(t *testing.T) {
	bwrap, cgroupRoot := os.Getenv("TUSKRAG_TEST_BWRAP"), os.Getenv("TUSKRAG_TEST_CGROUP")
	runtimeDir, executable := os.Getenv("TUSKRAG_TEST_PARSER_RUNTIME"), os.Getenv("TUSKRAG_TEST_PARSER")
	if bwrap == "" || cgroupRoot == "" || runtimeDir == "" || executable == "" {
		t.Skip("isolated Python sandbox is not configured")
	}
	f := setup(t)
	uploaded, job := f.upload(t, "城市,上限,用途\n北京,600,住宿\n上海,450,住宿\n成都,350,住宿\n深圳,700,住宿\n")
	sandbox, err := parser.NewSandbox(parser.SandboxConfig{Bubblewrap: bwrap, CgroupRoot: cgroupRoot, RuntimeDir: runtimeDir,
		Executable: executable, TempDir: t.TempDir(), MemoryBytes: 256 * 1024 * 1024, CPUSeconds: 5, WallTime: 10 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	validator, err := parser.NewValidator()
	if err != nil {
		t.Fatal(err)
	}
	implementation, err := parser.NewParser(sandbox, validator)
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := makeStage(t, f, implementation).Parse(f.ctx, job)
	if err != nil {
		t.Fatal(err)
	}
	normalized, err := AssembleMarkdown(f.ctx, parsed)
	if err != nil {
		t.Fatal(err)
	}
	var space int64
	var revision int32
	var profile EmbeddingProfile
	if err := f.admin.QueryRow(f.ctx, `SELECT k.embedding_space_id, k.config_revision, e.provider_id, e.model, e.dims, e.normalized, e.distance
		FROM knowledge_base k JOIN embedding_space e ON e.tenant_id=k.tenant_id AND e.id=k.embedding_space_id
		WHERE k.tenant_id=$1 AND k.id=$2`, f.tenant, f.kbID).Scan(&space, &revision, &profile.ProviderID, &profile.Model,
		&profile.Dimensions, &profile.Normalized, &profile.Distance); err != nil {
		t.Fatal(err)
	}
	request := planRequest(normalized)
	request.MIME, request.ParentMaxTokens = "text/csv", 90
	request.EmbeddingSpaceID, request.EmbeddingProfile, request.ConfigRevision = space, profile, revision
	if plan, err := BuildPlan(f.ctx, request); !errors.Is(err, ErrTableHeaderUnconfirmed) || len(plan.Chunks) != 0 {
		t.Fatalf("unconfirmed table returned a plan: %+v %v", plan, err)
	}
	request.TableHeaders = map[BlockPosition]int{{Section: 1, Block: 1}: 1}
	plan, err := BuildPlan(f.ctx, request)
	if err != nil || len(plan.Chunks) != 4 || len(plan.Sections) < 2 {
		t.Fatalf("build large table: %+v %v", plan, err)
	}
	stage, err := NewPlanStage(f.queue)
	if err != nil {
		t.Fatal(err)
	}
	staged, err := stage.Stage(f.ctx, job, plan)
	if err != nil || len(staged.Chunks) != 4 {
		t.Fatalf("stage large table: %+v %v", staged, err)
	}
	rows, err := f.admin.Query(f.ctx, `SELECT content FROM chunk WHERE tenant_id=$1 AND version_id=$2 ORDER BY ordinal`, f.tenant, uploaded.VersionID)
	if err != nil {
		t.Fatal(err)
	}
	var contents strings.Builder
	for rows.Next() {
		var content string
		if err := rows.Scan(&content); err != nil {
			rows.Close()
			t.Fatal(err)
		}
		if !strings.Contains(content, "城市") || !strings.Contains(content, "上限") {
			rows.Close()
			t.Fatal("table header missing from staged row group")
		}
		contents.WriteString(content)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	previous := -1
	for _, city := range []string{"北京", "上海", "成都", "深圳"} {
		position := strings.Index(contents.String(), city)
		if position <= previous || strings.Count(contents.String(), city) != 1 {
			t.Fatalf("table rows lost or reordered: %q", city)
		}
		previous = position
	}
}
