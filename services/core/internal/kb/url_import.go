package kb

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"mime"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"path"
	"strings"
	"syscall"
	"time"
)

const MaxURLImportBytes = 20 << 20

var (
	ErrURLBlocked      = errors.New("import url blocked by ssrf policy")
	ErrURLFetchFailed  = errors.New("failed to fetch import url")
	ErrURLTooLarge     = errors.New("import url exceeds 20 MiB limit")
)

type ImportedURLContent struct {
	Title string
	MIME  string
	Data  []byte
}

// ValidateImportURL checks basic URL syntax and scheme safety before dialing.
func ValidateImportURL(raw string) (*url.URL, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" || len(trimmed) > 2048 || trimmed != raw {
		return nil, ErrInvalidRequest
	}
	parsed, err := url.Parse(trimmed)
	if err != nil || parsed == nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Hostname() == "" || parsed.User != nil {
		return nil, ErrInvalidRequest
	}
	return parsed, nil
}

// IsDisallowedImportIP rejects loopback, private, link-local (including cloud metadata 169.254.169.254), multicast, and unspecified IPs.
func IsDisallowedImportIP(addr netip.Addr) bool {
	ip := addr.Unmap()
	return !ip.IsValid() ||
		ip.IsLoopback() ||
		ip.IsPrivate() ||
		ip.IsLinkLocalUnicast() ||
		ip.IsLinkLocalMulticast() ||
		ip.IsMulticast() ||
		ip.IsUnspecified()
}

func safeDialControl(_ string, address string, _ syscall.RawConn) error {
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		return ErrURLBlocked
	}
	addr, err := netip.ParseAddr(host)
	if err != nil || IsDisallowedImportIP(addr) {
		return ErrURLBlocked
	}
	return nil
}

// FetchURLForImport fetches a remote document with strict SSRF IP/redirect/size guards.
func FetchURLForImport(ctx context.Context, rawURL string) (ImportedURLContent, error) {
	parsed, err := ValidateImportURL(rawURL)
	if err != nil {
		return ImportedURLContent{}, err
	}
	dialer := &net.Dialer{
		Timeout: 10 * time.Second,
		Control: safeDialControl,
	}
	client := &http.Client{
		Timeout: 30 * time.Second,
		Transport: &http.Transport{
			DialContext:         dialer.DialContext,
			DisableKeepAlives:   true,
			ForceAttemptHTTP2:   false,
			TLSHandshakeTimeout: 10 * time.Second,
		},
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 3 {
				return ErrURLBlocked
			}
			if _, err := ValidateImportURL(req.URL.String()); err != nil {
				return ErrURLBlocked
			}
			return nil
		},
	}
	return fetchWithClient(ctx, client, parsed)
}

func fetchWithClient(ctx context.Context, client *http.Client, parsed *url.URL) (ImportedURLContent, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, parsed.String(), nil)
	if err != nil {
		return ImportedURLContent{}, ErrInvalidRequest
	}
	req.Header.Set("User-Agent", "TuskRAG-Importer/1.0")
	resp, err := client.Do(req)
	if err != nil {
		if errors.Is(err, ErrURLBlocked) {
			return ImportedURLContent{}, ErrURLBlocked
		}
		return ImportedURLContent{}, fmt.Errorf("%w: %v", ErrURLFetchFailed, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return ImportedURLContent{}, fmt.Errorf("%w: status %d", ErrURLFetchFailed, resp.StatusCode)
	}

	data, err := io.ReadAll(io.LimitReader(resp.Body, MaxURLImportBytes+1))
	if err != nil {
		return ImportedURLContent{}, fmt.Errorf("%w: %v", ErrURLFetchFailed, err)
	}
	if len(data) == 0 {
		return ImportedURLContent{}, ErrInvalidRequest
	}
	if len(data) > MaxURLImportBytes {
		return ImportedURLContent{}, ErrURLTooLarge
	}

	resolvedMIME := resolveImportedMIME(resp.Header.Get("Content-Type"), resp.Request.URL.Path)
	if resolvedMIME == "" {
		return ImportedURLContent{}, ErrUnsupportedMIME
	}
	title := deriveImportTitle(resp.Request.URL, resolvedMIME)
	return ImportedURLContent{
		Title: title,
		MIME:  resolvedMIME,
		Data:  bytes.Clone(data),
	}, nil
}

func resolveImportedMIME(contentType, urlPath string) string {
	mediaType, _, _ := mime.ParseMediaType(contentType)
	mediaType = strings.ToLower(strings.TrimSpace(mediaType))
	switch mediaType {
	case "text/html", "application/xhtml+xml", "text/markdown", "text/plain", "text/csv", "application/pdf",
		"application/vnd.openxmlformats-officedocument.wordprocessingml.document",
		"application/vnd.openxmlformats-officedocument.spreadsheetml.sheet",
		"application/vnd.openxmlformats-officedocument.presentationml.presentation":
		return mediaType
	}
	switch strings.ToLower(path.Ext(urlPath)) {
	case ".html", ".htm":
		return "text/html"
	case ".xhtml":
		return "application/xhtml+xml"
	case ".md", ".markdown":
		return "text/markdown"
	case ".txt":
		return "text/plain"
	case ".csv":
		return "text/csv"
	case ".pdf":
		return "application/pdf"
	case ".docx":
		return "application/vnd.openxmlformats-officedocument.wordprocessingml.document"
	case ".xlsx":
		return "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet"
	case ".pptx":
		return "application/vnd.openxmlformats-officedocument.presentationml.presentation"
	default:
		return ""
	}
}

func deriveImportTitle(u *url.URL, mimeType string) string {
	base := path.Base(strings.TrimSuffix(u.Path, "/"))
	if base == "" || base == "/" || base == "." {
		base = u.Hostname()
	}
	if mimeType == "text/html" && !strings.HasSuffix(strings.ToLower(base), ".html") && !strings.HasSuffix(strings.ToLower(base), ".htm") {
		base += ".html"
	}
	runes := []rune(base)
	if len(runes) > 180 {
		base = string(runes[:180])
	}
	return base
}
