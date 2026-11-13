package httpapi

import (
	"context"
	"embed"
	"encoding/json"
	"fmt"
	"io/fs"
	"net/http"
	"path"
	"strings"

	"github.com/danielgtaylor/huma/v2"
	"github.com/danielgtaylor/huma/v2/adapters/humago"

	"tuskrag.local/core/internal/eval"
	"tuskrag.local/core/internal/identity"
	"tuskrag.local/core/internal/kb"
	"tuskrag.local/core/internal/models"
	"tuskrag.local/core/internal/retrieval"
)

//go:embed all:web
var web embed.FS

type Health struct {
	Body struct {
		Status string `json:"status"`
	}
}

type API struct {
	Handler http.Handler
	OpenAPI *huma.OpenAPI
}

type IdentityService interface {
	Login(context.Context, int64, string, []byte) (identity.Session, error)
	Authenticate(context.Context, string) (identity.Session, error)
	Logout(context.Context, string) error
}

type KnowledgeService interface {
	Create(context.Context, kb.Actor, kb.CreateRequest) (kb.KnowledgeBase, error)
	List(context.Context, kb.Actor) ([]kb.KnowledgeBase, error)
	Get(context.Context, kb.Actor, int64) (kb.KnowledgeBase, error)
	ListDocuments(context.Context, kb.Actor, int64, int64, int32) (kb.DocumentPage, error)
	Archive(context.Context, kb.Actor, int64) error
}

type DocumentService interface {
	AuthorizeUpload(context.Context, kb.Actor, int64) error
	AuthorizeVersionUpload(context.Context, kb.Actor, int64, int64) error
	Upload(context.Context, kb.Actor, kb.UploadRequest) (kb.UploadedDocument, error)
	Delete(context.Context, kb.Actor, int64, int64) error
}

type ConfigService interface {
	GetConfig(context.Context, kb.Actor, int64) (kb.KnowledgeConfig, error)
	PutConfig(context.Context, kb.Actor, int64, int32, kb.RetrievalConfig) (kb.KnowledgeConfig, error)
}

type JobControl interface {
	GetJob(context.Context, kb.Actor, int64) (kb.JobSummary, error)
	ListPurgeJobs(context.Context, kb.Actor, int64, int64, int32) (kb.PurgeJobPage, error)
	RetryJob(context.Context, kb.Actor, int64) (int64, error)
}

type DocumentReader interface {
	DocumentContents(context.Context, kb.Actor, int64, int32, int32) (kb.DocumentContents, error)
	ReadSection(context.Context, kb.Actor, int64) (kb.SectionContent, error)
}

type ModelCatalog interface {
	CreateSpace(context.Context, int64, int64, models.NewSpace) (models.Space, error)
	ListSpaces(context.Context, int64, int64) ([]models.Space, error)
}

type SearchService interface {
	Search(context.Context, retrieval.Actor, retrieval.Request) (retrieval.Result, error)
}

type Dependencies struct {
	Conversations        ConversationService
	Answers              AnswerService
	ChatModels           ChatCatalog
	Rebuilds             RebuildService
	Reports              ReportService
	EvalSets             EvalSetService
	EvalRuns             EvalRunService
	Check                func(context.Context) error
	Identity             IdentityService
	Knowledge            KnowledgeService
	Config               ConfigService
	Documents            DocumentService
	Jobs                 JobControl
	Reader               DocumentReader
	Models               ModelCatalog
	Search               SearchService
	InsecureLocalCookies bool
}

type ReportService interface {
	Compare(context.Context, retrieval.Actor, int64, string, string, int32, int32) (eval.ReportComparison, error)
	List(context.Context, retrieval.Actor, int64, string, int32) (eval.ReportPage, error)
	Import(context.Context, retrieval.Actor, int64, eval.RunReport) (eval.StoredReport, error)
	Get(context.Context, retrieval.Actor, int64, string) (eval.StoredReport, error)
}

func New(deps Dependencies) API {
	mux := http.NewServeMux()
	config := huma.DefaultConfig("TuskRAG API", "0.1.0")
	config.OpenAPIPath = ""
	config.DocsPath = ""
	config.SchemasPath = ""
	config.CreateHooks = nil
	api := humago.New(mux, config)
	huma.Get(api, "/api/health/live", func(context.Context, *struct{}) (*Health, error) {
		response := &Health{}
		response.Body.Status = "ok"
		return response, nil
	})
	huma.Get(api, "/api/health/ready", func(ctx context.Context, _ *struct{}) (*Health, error) {
		if deps.Check == nil || deps.Check(ctx) != nil {
			return nil, statusError(ctx, http.StatusServiceUnavailable, "dependency_unavailable", "dependency unavailable", true)
		}
		response := &Health{}
		response.Body.Status = "ok"
		return response, nil
	})
	registerWorkspace(api, deps)
	mux.HandleFunc("/api", apiNotFound)
	mux.HandleFunc("/api/", apiNotFound)
	mux.HandleFunc("/mcp", apiNotFound)
	mux.HandleFunc("/mcp/", apiNotFound)
	return API{Handler: withTrace(withSPA(mux)), OpenAPI: api.OpenAPI()}
}

func OpenAPIJSON() ([]byte, error) {
	data, err := json.MarshalIndent(New(Dependencies{}).OpenAPI, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("encode OpenAPI: %w", err)
	}
	return append(data, '\n'), nil
}

func withSPA(api http.Handler) http.Handler {
	assets, err := fs.Sub(web, "web")
	if err != nil {
		panic(err) // The embedded directory is checked at build time.
	}
	return serveSPA(api, assets)
}

func serveSPA(api http.Handler, assets fs.FS) http.Handler {
	files := http.FileServer(http.FS(assets))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cleanPath := path.Clean(r.URL.Path)
		if cleanPath == "/api" || strings.HasPrefix(cleanPath, "/api/") || cleanPath == "/mcp" || strings.HasPrefix(cleanPath, "/mcp/") {
			api.ServeHTTP(w, r)
			return
		}
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		name := strings.TrimPrefix(cleanPath, "/")
		if name != "." && name != "" {
			if info, statErr := fs.Stat(assets, name); statErr == nil && !info.IsDir() {
				files.ServeHTTP(w, r)
				return
			}
			if path.Ext(name) != "" {
				http.NotFound(w, r)
				return
			}
		}
		index, readErr := fs.ReadFile(assets, "index.html")
		if readErr != nil {
			http.Error(w, "console not built", http.StatusServiceUnavailable)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write(index)
	})
}
