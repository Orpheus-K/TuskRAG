package mcpserver

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"tuskrag.local/core/internal/identity"
	"tuskrag.local/core/internal/kb"
	"tuskrag.local/core/internal/retrieval"
)

type stubAPIKeys struct {
	key identity.AuthenticatedAPIKey
	err error
}

func (s stubAPIKeys) AuthenticateAPIKey(_ context.Context, token, _ string) (identity.AuthenticatedAPIKey, error) {
	if s.err != nil {
		return identity.AuthenticatedAPIKey{}, s.err
	}
	if token != "tsk_valid" {
		return identity.AuthenticatedAPIKey{}, identity.ErrAPIKeyNotFound
	}
	return s.key, nil
}

type stubKnowledge struct{}

func (stubKnowledge) List(_ context.Context, _ kb.Actor) ([]kb.KnowledgeBase, error) {
	return []kb.KnowledgeBase{{ID: 1, Name: "KB1"}}, nil
}

func (stubKnowledge) ListDocumentsFiltered(_ context.Context, _ kb.Actor, kbID, _ int64, _ int32, filter kb.DocumentFilter) (kb.DocumentPage, error) {
	return kb.DocumentPage{
		Items: []kb.DocumentSummary{{
			ID: 10, Title: "差旅制度 " + filter.Query, MIME: "text/markdown",
			LatestVersionID: 100, LatestStatus: "ready", ByteSize: 120, LatestCreatedAt: time.Unix(1700000000, 0).UTC(),
		}},
	}, nil
}

type stubReader struct{}

func (stubReader) DocumentContents(_ context.Context, _ kb.Actor, docID int64, _ int32, _ int32) (kb.DocumentContents, error) {
	return kb.DocumentContents{
		KbID: 1, DocumentID: docID, VersionID: 100, Title: "差旅制度",
		Sections: []kb.SectionReference{{ID: 50, Ordinal: 1, Level: 1, Heading: "住宿", HeadingPath: []string{"住宿"}}},
	}, nil
}

func (stubReader) ReadSection(_ context.Context, _ kb.Actor, secID int64) (kb.SectionContent, error) {
	p1, p2 := int32(2), int32(3)
	return kb.SectionContent{
		KbID: 1, DocumentID: 10, VersionID: 100, Title: "差旅制度",
		Section:    kb.SectionReference{ID: secID, Ordinal: 1, Level: 1, Heading: "住宿", HeadingPath: []string{"住宿"}, PageStart: &p1, PageEnd: &p2},
		Markdown:   "一线城市住宿标准 500 元。",
		TokenCount: 12,
	}, nil
}

func (stubReader) GetChunkNeighbors(_ context.Context, _ kb.Actor, chunkID int64, _ int32, _ int32) (kb.ChunkNeighbors, error) {
	return kb.ChunkNeighbors{
		KbID: 1, DocumentID: 10, VersionID: 100, Title: "差旅制度",
		Section: kb.SectionReference{ID: 50, Ordinal: 1, Level: 1, Heading: "住宿", HeadingPath: []string{"住宿"}},
		Chunks: []kb.NeighborChunk{
			{ID: chunkID - 1, Ordinal: 1, Kind: "text", Content: "前一段", TokenCount: 5},
			{ID: chunkID, Ordinal: 2, Kind: "text", Content: "目标块", TokenCount: 5, IsTarget: true},
		},
	}, nil
}

type stubSearch struct{}

func (stubSearch) Search(_ context.Context, _ retrieval.Actor, req retrieval.Request) (retrieval.Result, error) {
	return retrieval.Result{
		RetrievalRevision: 1,
		Candidates: []retrieval.Candidate{{
			ChunkID: 77, DocumentID: 10, VersionID: 100, SectionID: 50,
			Title: "差旅制度", Content: "一线城市住宿标准 500 元。", HeadingPath: []string{"住宿"}, Score: 0.91,
		}},
	}, nil
}

func TestMCPToolsContractMatchesFile(t *testing.T) {
	generated, err := ToolsJSON()
	if err != nil {
		t.Fatal(err)
	}
	contractPath := filepath.Join("..", "..", "..", "..", "contracts", "mcp-tools.json")
	onDisk, err := os.ReadFile(contractPath)
	if err != nil {
		t.Fatalf("read %s: %v", contractPath, err)
	}
	if !bytes.Equal(generated, onDisk) {
		t.Fatalf("contracts/mcp-tools.json is out of date; run `tuskrag mcp-tools`")
	}
}

func TestMCPStreamableHTTPAuthToolsAndScope(t *testing.T) {
	srv := NewServer(Dependencies{
		APIKeys: stubAPIKeys{
			key: identity.AuthenticatedAPIKey{
				ID:        1,
				Principal: identity.Principal{TenantID: 1, UserID: 2},
				KBIDs:     []int64{1},
				Actions:   []string{"mcp"},
			},
		},
		Knowledge: stubKnowledge{},
		Reader:    stubReader{},
		Search:    stubSearch{},
	})

	call := func(token, body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, "/mcp", bytes.NewBufferString(body))
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		srv.ServeHTTP(rec, req)
		return rec
	}

	// 1. Missing/invalid key -> 401
	if rec := call("", `{"jsonrpc":"2.0","id":1,"method":"tools/list"}`); rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", rec.Code)
	}

	// 2. initialize -> 200 with Mcp-Session-Id
	initRec := call("tsk_valid", `{"jsonrpc":"2.0","id":1,"method":"initialize"}`)
	if initRec.Code != http.StatusOK || initRec.Header().Get("Mcp-Session-Id") == "" {
		t.Fatalf("unexpected initialize response: code=%d header=%q", initRec.Code, initRec.Header().Get("Mcp-Session-Id"))
	}

	// 3. tools/list -> 5 tools
	listRec := call("tsk_valid", `{"jsonrpc":"2.0","id":2,"method":"tools/list"}`)
	var listResp struct {
		Result struct {
			Tools []ToolDefinition `json:"tools"`
		} `json:"result"`
	}
	if err := json.Unmarshal(listRec.Body.Bytes(), &listResp); err != nil {
		t.Fatal(err)
	}
	if len(listResp.Result.Tools) != 5 {
		t.Fatalf("expected 5 tools, got %d", len(listResp.Result.Tools))
	}

	// 4. All 5 tools execute and return citation / expected structure
	for _, tc := range []struct {
		name string
		args string
	}{
		{"search", `{"query":"住宿","kb_ids":[1]}`},
		{"list_documents", `{"kb_id":1,"query":"差旅"}`},
		{"get_toc", `{"document_id":10}`},
		{"read_section", `{"section_id":50}`},
		{"get_neighbors", `{"chunk_id":77,"before":1,"after":0}`},
	} {
		rec := call("tsk_valid", `{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"`+tc.name+`","arguments":`+tc.args+`}}`)
		if rec.Code != http.StatusOK {
			t.Fatalf("%s returned HTTP %d", tc.name, rec.Code)
		}
		var resp struct {
			Result struct {
				IsError bool `json:"isError"`
			} `json:"result"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
			t.Fatal(err)
		}
		if resp.Result.IsError {
			t.Fatalf("%s returned tool error: %s", tc.name, rec.Body.String())
		}
	}

	// 5. Out-of-scope KB in list_documents -> isError=true (not_found)
	deniedRec := call("tsk_valid", `{"jsonrpc":"2.0","id":4,"method":"tools/call","params":{"name":"list_documents","arguments":{"kb_id":999}}}`)
	var deniedResp struct {
		Result struct {
			IsError bool `json:"isError"`
		} `json:"result"`
	}
	_ = json.Unmarshal(deniedRec.Body.Bytes(), &deniedResp)
	if !deniedResp.Result.IsError {
		t.Fatalf("expected out-of-scope kb_id 999 to return tool error, got %s", deniedRec.Body.String())
	}

	// 6. Missing mcp scope -> 403
	noScopeSrv := NewServer(Dependencies{
		APIKeys: stubAPIKeys{
			key: identity.AuthenticatedAPIKey{ID: 1, Principal: identity.Principal{TenantID: 1, UserID: 2}, Actions: []string{"search"}},
		},
	})
	req := httptest.NewRequest(http.MethodPost, "/mcp", bytes.NewBufferString(`{"jsonrpc":"2.0","id":1,"method":"tools/list"}`))
	req.Header.Set("Authorization", "Bearer tsk_valid")
	rec := httptest.NewRecorder()
	noScopeSrv.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected 403 for missing mcp scope, got %d", rec.Code)
	}
}
