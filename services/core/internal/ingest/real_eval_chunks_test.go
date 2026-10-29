package ingest

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"

	"tuskrag.local/core/internal/eval"
	"tuskrag.local/core/internal/models"
	"tuskrag.local/core/internal/parser"
)

// Ablation fixtures use trusted, repository-authored synthetic Markdown only.
// Production parsing remains confined to the sandbox subprocess.
func replaceEvaluationChunks(t *testing.T, f *fixture, space, provider, document int64, doc eval.Document, scenario string, client *models.EmbeddingClient, scope models.EgressScope, count TokenCounter) {
	t.Helper()
	var plan PreparedPlan
	if strings.HasPrefix(scenario, "structured") {
		input, err := os.CreateTemp(t.TempDir(), "synthetic-*.md")
		if err != nil {
			t.Fatal(err)
		}
		if _, err = input.WriteString(doc.Content); err != nil {
			t.Fatal(err)
		}
		validator, err := parser.NewValidator()
		if err != nil {
			t.Fatal(err)
		}
		parsed, parseErr := parser.ParseNativeText(f.ctx, parser.ParseInput{File: input, MIME: "text/markdown", Limits: json.RawMessage(`{}`)}, validator)
		closeErr := input.Close()
		if parseErr != nil {
			t.Fatal(parseErr)
		}
		if closeErr != nil {
			t.Fatal(closeErr)
		}
		normalized, err := AssembleMarkdown(f.ctx, parsed)
		if err != nil {
			t.Fatal(err)
		}
		plan, err = BuildPlan(f.ctx, PlanBuildRequest{Document: normalized, MIME: "text/markdown", EmbeddingSpaceID: space, EmbeddingProfile: EmbeddingProfile{ProviderID: provider, Model: "BAAI/bge-m3", Dimensions: 1024, Normalized: true, Distance: "cosine"}, ConfigRevision: 1, ParentMaxTokens: 512, ChildTargetMin: 32, ChildTargetMax: 64, ChildHardMax: 128, OverlapMax: 16, CountTokens: count, TableHeaders: sourceTableHeaders("text/markdown", parser.NativeMarkdownProfile, false, normalized)})
		if err != nil {
			t.Fatalf("build %s: %v", doc.Key, err)
		}
	} else {
		// Fixed windows use the same BGE-M3 token counter, without sentence/table awareness.
		body := doc.Content
		if strings.HasPrefix(body, "# ") {
			if p := strings.IndexByte(body, '\n'); p >= 0 {
				body = body[p+1:]
			}
		}
		runes := []rune(strings.TrimSpace(body))
		plan.Sections = []PreparedSection{{ParentIndex: -1, Level: 1, Heading: doc.Title, HeadingPath: []string{doc.Title}, BodyMarkdown: body}}
		for start := 0; start < len(runes); {
			end := start
			for end < len(runes) {
				n, err := count(f.ctx, string(runes[start:end+1]))
				if err != nil {
					t.Fatal(err)
				}
				if n > 64 {
					break
				}
				end++
			}
			if end == start {
				t.Fatal("fixed chunk cannot advance")
			}
			text := string(runes[start:end])
			search := text
			if scenario == "fixed-heading" {
				search = doc.Title + "\n" + text
			}
			plan.Chunks = append(plan.Chunks, PreparedChunk{SectionIndex: 0, Kind: "text", Content: text, SearchText: search})
			if end == len(runes) {
				break
			}
			next := end
			for next > start+1 {
				n, err := count(f.ctx, string(runes[next-1:end]))
				if err != nil {
					t.Fatal(err)
				}
				if n > 16 {
					break
				}
				next--
			}
			start = next
		}
	}
	var version int64
	if err := f.admin.QueryRow(f.ctx, `SELECT current_version_id FROM document WHERE tenant_id=$1 AND id=$2`, f.tenant, document).Scan(&version); err != nil {
		t.Fatal(err)
	}
	if _, err := f.admin.Exec(f.ctx, `DELETE FROM chunk_vector WHERE tenant_id=$1 AND chunk_id IN (SELECT id FROM chunk WHERE tenant_id=$1 AND document_id=$2)`, f.tenant, document); err != nil {
		t.Fatal(err)
	}
	if _, err := f.admin.Exec(f.ctx, `DELETE FROM chunk WHERE tenant_id=$1 AND document_id=$2`, f.tenant, document); err != nil {
		t.Fatal(err)
	}
	if _, err := f.admin.Exec(f.ctx, `DELETE FROM section WHERE tenant_id=$1 AND document_id=$2`, f.tenant, document); err != nil {
		t.Fatal(err)
	}
	sectionIDs := make([]int64, len(plan.Sections))
	for i, s := range plan.Sections {
		if s.HeadingPath == nil {
			s.HeadingPath = []string{}
		}
		var parent any
		if s.ParentIndex >= 0 {
			parent = sectionIDs[s.ParentIndex]
		}
		if _, err := count(f.ctx, s.BodyMarkdown); err != nil && s.BodyMarkdown != "" {
			t.Fatal(err)
		}
		if err := f.admin.QueryRow(f.ctx, `INSERT INTO section(tenant_id,kb_id,document_id,version_id,ordinal,level,parent_section_id,heading,heading_path,body_md,token_count) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11) RETURNING id`, f.tenant, f.kbID, document, version, i+1, s.Level, parent, s.Heading, s.HeadingPath, s.BodyMarkdown, s.TokenCount).Scan(&sectionIDs[i]); err != nil {
			t.Fatal(err)
		}
	}
	tag := fmt.Sprintf("kb:%d:all", f.kbID)
	for i, c := range plan.Chunks {
		search := c.SearchText
		if scenario == "structured-no-heading" {
			path := strings.Join(plan.Sections[c.SectionIndex].HeadingPath, " › ")
			search = strings.TrimPrefix(search, path+"\n")
		}
		result, err := client.Embed(f.ctx, scope, []string{search})
		if err != nil {
			t.Fatal(err)
		}
		n, err := count(f.ctx, search)
		if err != nil {
			t.Fatal(err)
		}
		if result.Usage == nil || result.Usage.PromptTokens != n {
			t.Fatal("chunk token usage mismatch")
		}
		var chunk int64
		if err := f.admin.QueryRow(f.ctx, `INSERT INTO chunk(tenant_id,kb_id,document_id,version_id,section_id,ordinal,kind,content,search_text,token_count,is_current,access_tags,doc_mime,doc_updated_at) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,true,ARRAY[$11]::text[],'text/markdown',now()) RETURNING id`, f.tenant, f.kbID, document, version, sectionIDs[c.SectionIndex], i+1, c.Kind, c.Content, search, n, tag).Scan(&chunk); err != nil {
			t.Fatal(err)
		}
		vector, err := json.Marshal(result.Vectors[0])
		if err != nil {
			t.Fatal(err)
		}
		if _, err := f.admin.Exec(f.ctx, `INSERT INTO chunk_vector(tenant_id,kb_id,chunk_id,space_id,is_current,access_tags,embedding) VALUES($1,$2,$3,$4,true,ARRAY[$5]::text[],$6::halfvec)`, f.tenant, f.kbID, chunk, space, tag, string(vector)); err != nil {
			t.Fatal(err)
		}
	}
}
