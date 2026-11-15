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
	"tuskrag.local/core/internal/mcpserver"
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

type APIKeyService interface {
	CreateAPIKey(context.Context, identity.Principal, identity.CreateAPIKeyRequest) (identity.CreatedAPIKey, error)
	ListAPIKeys(context.Context, identity.Principal) ([]identity.APIKey, error)
	RevokeAPIKey(context.Context, identity.Principal, int64) error
	AuthenticateAPIKey(context.Context, string, string) (identity.AuthenticatedAPIKey, error)
}

type KnowledgeService interface {
	Create(context.Context, kb.Actor, kb.CreateRequest) (kb.KnowledgeBase, error)
	List(context.Context, kb.Actor) ([]kb.KnowledgeBase, error)
	Get(context.Context, kb.Actor, int64) (kb.KnowledgeBase, error)
	ListDocuments(context.Context, kb.Actor, int64, int64, int32) (kb.DocumentPage, error)
	ListDocumentsFiltered(context.Context, kb.Actor, int64, int64, int32, kb.DocumentFilter) (kb.DocumentPage, error)
	Archive(context.Context, kb.Actor, int64) error
	ListMembers(context.Context, kb.Actor, int64) ([]kb.Member, error)
	SetMember(context.Context, kb.Actor, int64, int64, kb.Role) error
	RemoveMember(context.Context, kb.Actor, int64, int64) error
	ListTenantUsers(context.Context, kb.Actor) ([]kb.TenantUser, error)
	SetEgressPolicy(context.Context, kb.Actor, int64, models.EgressPolicy) (kb.KnowledgeBase, error)
	ListGroups(context.Context, kb.Actor) ([]kb.UserGroup, error)
	CreateGroup(context.Context, kb.Actor, string) (kb.UserGroup, error)
	DeleteGroup(context.Context, kb.Actor, int64) error
	ListGroupMembers(context.Context, kb.Actor, int64) ([]kb.GroupMember, error)
	AddGroupMember(context.Context, kb.Actor, int64, int64) error
	RemoveGroupMember(context.Context, kb.Actor, int64, int64) error
	GetDocumentACL(context.Context, kb.Actor, int64, int64) (kb.DocumentACL, error)
	SetDocumentACL(context.Context, kb.Actor, int64, int64, kb.ACLMode, []kb.ACLSubject) (kb.DocumentACL, error)
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
	GetChunkNeighbors(context.Context, kb.Actor, int64, int32, int32) (kb.ChunkNeighbors, error)
}

type ModelCatalog interface {
	CreateSpace(context.Context, int64, int64, models.NewSpace) (models.Space, error)
	ListSpaces(context.Context, int64, int64) ([]models.Space, error)
}

type ModelProviderCatalog interface {
	ListProviders(context.Context, int64, int64) ([]models.ProviderRecord, error)
	CreateProvider(context.Context, int64, int64, models.ProviderInput) (models.ProviderRecord, error)
	UpdateProvider(context.Context, int64, int64, int64, models.ProviderInput) (models.ProviderRecord, error)
	DeleteProvider(context.Context, int64, int64, int64) error
	TestProviderConnectivity(context.Context, int64, int64, int64) (models.ProviderConnectivityResult, error)
}

type AuditService interface {
	ListAuditLogs(context.Context, identity.Principal, identity.AuditLogFilter) ([]identity.AuditLogEntry, error)
	ExportAuditLogs(context.Context, identity.Principal, identity.AuditLogFilter, string) ([]byte, string, error)
	GetTenantEgressPolicy(context.Context, identity.Principal) (string, error)
	UpdateTenantEgressPolicy(context.Context, identity.Principal, string) (string, error)
}

type OIDCService interface {
	GetTenantAuthPolicy(context.Context, int64) (identity.TenantAuthPolicy, error)
	SetDisableLocalLogin(context.Context, identity.Principal, bool) (bool, error)
	ListOIDCProviders(context.Context, identity.Principal) ([]identity.OIDCProvider, error)
	CreateOIDCProvider(context.Context, identity.Principal, identity.CreateOIDCProviderRequest) (identity.OIDCProvider, error)
	DeleteOIDCProvider(context.Context, identity.Principal, int64) error
	StartOIDCLogin(context.Context, int64, int64, string) (string, error)
	CompleteOIDCLogin(context.Context, string, string) (identity.Session, error)
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
	AnswerReports        AnswerReportService
	EvalSets             EvalSetService
	EvalRuns             EvalRunService
	Check                func(context.Context) error
	Identity             IdentityService
	APIKeys              APIKeyService
	Audit                AuditService
	OIDC                 OIDCService
	Knowledge            KnowledgeService
	Config               ConfigService
	Documents            DocumentService
	Jobs                 JobControl
	Reader               DocumentReader
	Models               ModelCatalog
	ModelProviders       ModelProviderCatalog
	Search               SearchService
	InsecureLocalCookies bool
}

type ReportService interface {
	Compare(context.Context, retrieval.Actor, int64, string, string, int32, int32) (eval.ReportComparison, error)
	List(context.Context, retrieval.Actor, int64, string, int32) (eval.ReportPage, error)
	Import(context.Context, retrieval.Actor, int64, eval.RunReport) (eval.StoredReport, error)
	Get(context.Context, retrieval.Actor, int64, string) (eval.StoredReport, error)
}

type AnswerReportService interface {
	Compare(context.Context, retrieval.Actor, int64, string, string, int32, int32) (eval.AnswerReportComparisonView, error)
	List(context.Context, retrieval.Actor, int64, string, int32) (eval.AnswerReportPage, error)
	Import(context.Context, retrieval.Actor, int64, eval.AnswerReport) (eval.StoredAnswerReport, error)
	Get(context.Context, retrieval.Actor, int64, string) (eval.StoredAnswerReport, error)
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
	if deps.APIKeys != nil || deps.OIDC != nil {
		oauthAuth, _ := deps.OIDC.(mcpserver.MCPBearerAuthenticator)
		mcpHandler := mcpserver.NewServer(mcpserver.Dependencies{
			APIKeys:   deps.APIKeys,
			OAuth:     oauthAuth,
			Knowledge: deps.Knowledge,
			Reader:    deps.Reader,
			Search:    deps.Search,
		})
		mux.Handle("/mcp", mcpHandler)
		mux.HandleFunc("/.well-known/oauth-protected-resource", mcpHandler.ServeProtectedResourceMetadata)
		mux.HandleFunc("/.well-known/oauth-protected-resource/mcp", mcpHandler.ServeProtectedResourceMetadata)
	} else {
		mux.HandleFunc("/mcp", apiNotFound)
	}
	mux.HandleFunc("/mcp/", apiNotFound)
	metrics := &metricsRecorder{}
	mux.HandleFunc("/metrics", metrics.handler(deps))
	return API{Handler: withTrace(metrics.middleware(withSPA(mux))), OpenAPI: api.OpenAPI()}
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
		if cleanPath == "/api" || strings.HasPrefix(cleanPath, "/api/") || cleanPath == "/mcp" || strings.HasPrefix(cleanPath, "/mcp/") || cleanPath == "/metrics" || strings.HasPrefix(cleanPath, "/.well-known/") {
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
