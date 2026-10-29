package httpapi

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"log/slog"
	"net/http"
	"strings"

	"github.com/danielgtaylor/huma/v2"
)

type traceKey struct{}

type ErrorBody struct {
	Code      string `json:"code"`
	Message   string `json:"message"`
	Retryable bool   `json:"retryable"`
	TraceID   string `json:"trace_id"`
}

type ErrorModel struct {
	Body   ErrorBody `json:"error"`
	status int
}

func (e *ErrorModel) Error() string  { return e.Body.Message }
func (e *ErrorModel) GetStatus() int { return e.status }

func init() {
	huma.NewError = func(status int, message string, _ ...error) huma.StatusError {
		return statusError(context.Background(), status, "", message, status == http.StatusServiceUnavailable)
	}
	huma.NewErrorWithContext = func(ctx huma.Context, status int, message string, _ ...error) huma.StatusError {
		return statusError(ctx.Context(), status, "", message, status == http.StatusServiceUnavailable)
	}
}

func statusError(ctx context.Context, status int, code, message string, retryable bool) *ErrorModel {
	if code == "" {
		switch status {
		case http.StatusBadRequest, http.StatusUnprocessableEntity:
			code = "invalid_request"
		case http.StatusUnauthorized:
			code = "unauthenticated"
		case http.StatusForbidden:
			code = "forbidden"
		case http.StatusNotFound:
			code = "not_found"
		case http.StatusConflict:
			code = "revision_conflict"
		case http.StatusTooManyRequests:
			code = "rate_limited"
		case http.StatusRequestEntityTooLarge:
			code = "limit_exceeded"
		case http.StatusServiceUnavailable:
			code = "dependency_unavailable"
		default:
			code = "internal"
		}
	}
	if status >= 500 && status != http.StatusServiceUnavailable {
		message = "internal error"
	}
	trace, _ := ctx.Value(traceKey{}).(string)
	return &ErrorModel{Body: ErrorBody{Code: code, Message: message, Retryable: retryable, TraceID: trace}, status: status}
}

func withTrace(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var raw [16]byte
		if _, err := rand.Read(raw[:]); err != nil {
			slog.Error("generate request ID", "error", err)
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		trace := hex.EncodeToString(raw[:])
		w.Header().Set("X-Trace-ID", trace)
		if r.URL.Path == "/api" || strings.HasPrefix(r.URL.Path, "/api/") {
			w.Header().Set("Cache-Control", "no-store")
		}
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), traceKey{}, trace)))
	})
}

func apiNotFound(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusNotFound)
	if err := json.NewEncoder(w).Encode(statusError(r.Context(), http.StatusNotFound, "", "not found", false)); err != nil {
		slog.Error("write not-found response", "error", err)
	}
}
