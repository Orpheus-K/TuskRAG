package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"tuskrag.local/core/internal/kb"
)

type testDocumentReader struct{}

func (testDocumentReader) DocumentContents(_ context.Context, actor kb.Actor, documentID int64, cursor, limit int32) (kb.DocumentContents, error) {
	if actor.TenantID != 1 || actor.UserID != 2 || documentID != 7 || cursor != 0 || limit != 1 {
		return kb.DocumentContents{}, kb.ErrNotFound
	}
	page := int32(2)
	return kb.DocumentContents{DocumentID: 7, VersionID: 8, Title: "制度.pdf", Sections: []kb.SectionReference{
		{ID: 9, Ordinal: 1, Level: 1, Heading: "标题", HeadingPath: []string{"标题"}, PageStart: &page, PageEnd: &page},
	}}, nil
}

func (testDocumentReader) ReadSection(_ context.Context, actor kb.Actor, sectionID int64) (kb.SectionContent, error) {
	if actor.TenantID != 1 || actor.UserID != 2 || sectionID != 9 {
		return kb.SectionContent{}, kb.ErrNotFound
	}
	return kb.SectionContent{DocumentID: 7, VersionID: 8, Title: "制度.pdf", Section: kb.SectionReference{
		ID: 9, Ordinal: 1, Level: 1, Heading: "标题", HeadingPath: []string{"标题"},
	}, Markdown: "正文", TokenCount: 2}, nil
}

func (testDocumentReader) GetChunkNeighbors(_ context.Context, actor kb.Actor, chunkID int64, before, after int32) (kb.ChunkNeighbors, error) {
	if actor.TenantID != 1 || actor.UserID != 2 || chunkID != 10 {
		return kb.ChunkNeighbors{}, kb.ErrNotFound
	}
	return kb.ChunkNeighbors{DocumentID: 7, VersionID: 8, Title: "制度.pdf", Section: kb.SectionReference{
		ID: 9, Ordinal: 1, Level: 1, Heading: "标题", HeadingPath: []string{"标题"},
	}, Chunks: []kb.NeighborChunk{{ID: 10, Ordinal: 1, Kind: "text", Content: "正文", TokenCount: 2, IsTarget: true}}}, nil
}

func TestDocumentReaderUsesSessionAndReturnsCurrentReferences(t *testing.T) {
	app := New(Dependencies{Identity: uploadTestIdentity{}, Reader: testDocumentReader{}, InsecureLocalCookies: true})
	request := authenticatedUploadRequest(nil)
	request.Method, request.URL.Path, request.URL.RawQuery = http.MethodGet, "/api/v1/documents/7/toc", "cursor=0&limit=1"
	response := httptest.NewRecorder()
	app.Handler.ServeHTTP(response, request)
	var toc struct {
		Document ContentDocumentBody    `json:"document"`
		Sections []SectionReferenceBody `json:"sections"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &toc); err != nil || response.Code != http.StatusOK || toc.Document.VersionID != 8 || len(toc.Sections) != 1 || len(toc.Sections[0].Pages) != 2 || toc.Sections[0].Pages[0] != 2 {
		t.Fatalf("contents response status=%d body=%s err=%v", response.Code, response.Body.String(), err)
	}
	request = authenticatedUploadRequest(nil)
	request.Method, request.URL.Path = http.MethodGet, "/api/v1/sections/9"
	response = httptest.NewRecorder()
	app.Handler.ServeHTTP(response, request)
	var section struct {
		Document ContentDocumentBody  `json:"document"`
		Section  SectionReferenceBody `json:"section"`
		Markdown string               `json:"markdown"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &section); err != nil || response.Code != http.StatusOK || section.Document.VersionID != 8 || section.Section.ID != 9 || section.Markdown != "正文" {
		t.Fatalf("section response status=%d body=%s err=%v", response.Code, response.Body.String(), err)
	}
	request = authenticatedUploadRequest(nil)
	request.Method, request.URL.Path = http.MethodGet, "/api/v1/sections/10"
	response = httptest.NewRecorder()
	app.Handler.ServeHTTP(response, request)
	if response.Code != http.StatusNotFound {
		t.Fatalf("invisible section status=%d", response.Code)
	}
}
