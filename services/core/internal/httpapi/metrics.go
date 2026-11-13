package httpapi

import (
	"context"
	"crypto/subtle"
	"fmt"
	"net"
	"net/http"
	"os"
	"runtime"
	"strings"
	"sync/atomic"
	"time"
)

type metricsRecorder struct {
	requests2xx atomic.Uint64
	requests4xx atomic.Uint64
	requests5xx atomic.Uint64
	durationUs  atomic.Uint64
	inFlight    atomic.Int64
}

type statusCaptureWriter struct {
	http.ResponseWriter
	status int
}

func (w *statusCaptureWriter) WriteHeader(code int) {
	w.status = code
	w.ResponseWriter.WriteHeader(code)
}

func (w *statusCaptureWriter) Flush() {
	if flusher, ok := w.ResponseWriter.(http.Flusher); ok {
		flusher.Flush()
	}
}

func (m *metricsRecorder) middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/metrics" {
			next.ServeHTTP(w, r)
			return
		}
		m.inFlight.Add(1)
		defer m.inFlight.Add(-1)
		start := time.Now()
		cw := &statusCaptureWriter{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(cw, r)
		elapsed := time.Since(start).Microseconds()
		if elapsed > 0 {
			m.durationUs.Add(uint64(elapsed))
		}
		switch {
		case cw.status >= 500:
			m.requests5xx.Add(1)
		case cw.status >= 400:
			m.requests4xx.Add(1)
		default:
			m.requests2xx.Add(1)
		}
	})
}

func (m *metricsRecorder) handler(deps Dependencies) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		if !authorizeMetricsRequest(r) {
			w.WriteHeader(http.StatusForbidden)
			return
		}
		var mem runtime.MemStats
		runtime.ReadMemStats(&mem)
		readyVal := 0
		if deps.Check != nil {
			ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
			if deps.Check(ctx) == nil {
				readyVal = 1
			}
			cancel()
		}
		var b strings.Builder
		b.WriteString("# HELP tuskrag_up Whether the TuskRAG HTTP server is up.\n")
		b.WriteString("# TYPE tuskrag_up gauge\n")
		b.WriteString("tuskrag_up 1\n")
		b.WriteString("# HELP tuskrag_ready Whether dependency readiness check succeeds.\n")
		b.WriteString("# TYPE tuskrag_ready gauge\n")
		fmt.Fprintf(&b, "tuskrag_ready %d\n", readyVal)
		b.WriteString("# HELP tuskrag_http_requests_in_flight Current in-flight HTTP requests.\n")
		b.WriteString("# TYPE tuskrag_http_requests_in_flight gauge\n")
		fmt.Fprintf(&b, "tuskrag_http_requests_in_flight %d\n", m.inFlight.Load())
		b.WriteString("# HELP tuskrag_http_requests_total Total HTTP requests by status class.\n")
		b.WriteString("# TYPE tuskrag_http_requests_total counter\n")
		fmt.Fprintf(&b, "tuskrag_http_requests_total{status_class=\"2xx\"} %d\n", m.requests2xx.Load())
		fmt.Fprintf(&b, "tuskrag_http_requests_total{status_class=\"4xx\"} %d\n", m.requests4xx.Load())
		fmt.Fprintf(&b, "tuskrag_http_requests_total{status_class=\"5xx\"} %d\n", m.requests5xx.Load())
		b.WriteString("# HELP tuskrag_http_request_duration_seconds_sum Total HTTP request duration in seconds.\n")
		b.WriteString("# TYPE tuskrag_http_request_duration_seconds_sum counter\n")
		fmt.Fprintf(&b, "tuskrag_http_request_duration_seconds_sum %.6f\n", float64(m.durationUs.Load())/1e6)
		b.WriteString("# HELP tuskrag_go_goroutines Number of goroutines.\n")
		b.WriteString("# TYPE tuskrag_go_goroutines gauge\n")
		fmt.Fprintf(&b, "tuskrag_go_goroutines %d\n", runtime.NumGoroutine())
		b.WriteString("# HELP tuskrag_go_mem_alloc_bytes Allocated heap bytes.\n")
		b.WriteString("# TYPE tuskrag_go_mem_alloc_bytes gauge\n")
		fmt.Fprintf(&b, "tuskrag_go_mem_alloc_bytes %d\n", mem.Alloc)
		b.WriteString("# HELP tuskrag_go_mem_sys_bytes System memory obtained from OS.\n")
		b.WriteString("# TYPE tuskrag_go_mem_sys_bytes gauge\n")
		fmt.Fprintf(&b, "tuskrag_go_mem_sys_bytes %d\n", mem.Sys)
		w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
		w.WriteHeader(http.StatusOK)
		if r.Method == http.MethodGet {
			_, _ = w.Write([]byte(b.String()))
		}
	}
}

func authorizeMetricsRequest(r *http.Request) bool {
	token := strings.TrimSpace(os.Getenv("TUSKRAG_METRICS_TOKEN"))
	if token != "" {
		auth := strings.TrimSpace(r.Header.Get("Authorization"))
		const prefix = "Bearer "
		if !strings.HasPrefix(auth, prefix) {
			return false
		}
		got := strings.TrimSpace(strings.TrimPrefix(auth, prefix))
		return subtle.ConstantTimeCompare([]byte(got), []byte(token)) == 1
	}
	host := r.RemoteAddr
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	ip := net.ParseIP(strings.TrimSpace(host))
	return ip != nil && ip.IsLoopback()
}
