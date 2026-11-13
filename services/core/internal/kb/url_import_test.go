package kb

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"testing"
)

func TestSSRFDisallowedIPsAndSchemes(t *testing.T) {
	disallowed := []string{
		"127.0.0.1",
		"10.0.0.1",
		"172.16.5.4",
		"192.168.1.10",
		"169.254.169.254",
		"0.0.0.0",
		"::1",
		"fe80::1",
		"fc00::1",
		"::ffff:127.0.0.1",
		"::ffff:169.254.169.254",
	}
	for _, raw := range disallowed {
		addr := netip.MustParseAddr(raw)
		if !IsDisallowedImportIP(addr) {
			t.Fatalf("expected %s to be disallowed by SSRF policy", raw)
		}
	}
	if IsDisallowedImportIP(netip.MustParseAddr("93.184.216.34")) {
		t.Fatalf("public IPv4 should be allowed")
	}

	for _, badURL := range []string{
		"",
		"file:///etc/passwd",
		"ftp://example.com/a.pdf",
		"https://user:pass@example.com/a.pdf",
		"javascript:alert(1)",
	} {
		if _, err := ValidateImportURL(badURL); !errors.Is(err, ErrInvalidRequest) {
			t.Fatalf("expected ErrInvalidRequest for %q, got %v", badURL, err)
		}
	}

	// Real FetchURLForImport against localhost must be blocked by safeDialControl!
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("<h1>hi</h1>"))
	}))
	defer srv.Close()

	if _, err := FetchURLForImport(context.Background(), srv.URL+"/doc.html"); !errors.Is(err, ErrURLBlocked) {
		t.Fatalf("expected ErrURLBlocked for loopback httptest server, got %v", err)
	}
}

func TestFetchWithClientHTMLAndTitle(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte("<h1>Policy</h1><p>Hello</p>"))
	}))
	defer srv.Close()

	parsed, err := url.Parse(srv.URL + "/guide")
	if err != nil {
		t.Fatal(err)
	}
	res, err := fetchWithClient(context.Background(), srv.Client(), parsed)
	if err != nil {
		t.Fatalf("fetchWithClient failed: %v", err)
	}
	if res.MIME != "text/html" || res.Title != "guide.html" || len(res.Data) == 0 {
		t.Fatalf("unexpected imported url content: %+v", res)
	}
}
