package retrieval

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/url"
	"os"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"tuskrag.local/core/internal/db"
)

// TestRestoredProjectRecall compares the production BM25/HNSW/RRF driver with
// identical real query vectors. It does not conflate provider variation with
// backup corruption and never exports document text in its report.
func TestRestoredProjectRecall(t *testing.T) {
	if os.Getenv("TUSKRAG_RESTORE_EVAL") != "project-documents" {
		t.Skip("requires explicit prepared project-document databases")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	sourceURL, targetURL := os.Getenv("TUSKRAG_RESTORE_SOURCE_URL"), os.Getenv("TUSKRAG_RESTORE_TARGET_URL")
	for _, value := range []string{sourceURL, targetURL} {
		parsed, err := url.Parse(value)
		if err != nil || (parsed.Hostname() != "127.0.0.1" && parsed.Hostname() != "localhost") || !strings.HasPrefix(parsed.Path, "/tuskrag_test_") {
			t.Fatal("two loopback tuskrag_test_ databases are required")
		}
	}
	if sourceURL == targetURL {
		t.Fatal("source and restored database must differ")
	}
	fixtureBytes, err := os.ReadFile(os.Getenv("TUSKRAG_RESTORE_FIXTURE"))
	if err != nil {
		t.Fatal(err)
	}
	var fixture struct {
		Dataset            string  `json:"dataset"`
		TenantID           int64   `json:"tenant_id"`
		UserID             int64   `json:"user_id"`
		KBID               int64   `json:"kb_id"`
		AllowedDocumentIDs []int64 `json:"allowed_document_ids"`
		Cases              []struct {
			ID     string    `json:"id"`
			Query  string    `json:"query"`
			Vector []float32 `json:"vector"`
		} `json:"cases"`
	}
	if err := json.Unmarshal(fixtureBytes, &fixture); err != nil {
		t.Fatal(err)
	}
	if fixture.Dataset != "project-documents-qa-v1" || len(fixture.Cases) != 12 || len(fixture.AllowedDocumentIDs) != 4 || fixture.TenantID <= 0 || fixture.UserID <= 0 || fixture.KBID <= 0 {
		t.Fatal("invalid project fixture")
	}
	source, err := db.OpenWithLimit(ctx, sourceURL, 1)
	if err != nil {
		t.Fatal(err)
	}
	defer source.Close()
	target, err := db.OpenWithLimit(ctx, targetURL, 1)
	if err != nil {
		t.Fatal(err)
	}
	defer target.Close()
	before, err := NewDriver(source)
	if err != nil {
		t.Fatal(err)
	}
	after, err := NewDriver(target)
	if err != nil {
		t.Fatal(err)
	}
	actor := Actor{TenantID: fixture.TenantID, UserID: fixture.UserID}
	binding, err := before.Binding(ctx, actor, fixture.KBID)
	if err != nil {
		t.Fatal(err)
	}
	restoredBinding, err := after.Binding(ctx, actor, fixture.KBID)
	if err != nil || binding != restoredBinding {
		t.Fatalf("restored embedding binding changed: %v", err)
	}
	type row struct {
		ID         string `json:"id"`
		Candidates int    `json:"candidates"`
		SHA256     string `json:"candidates_sha256"`
		Identical  bool   `json:"identical"`
	}
	rows := make([]row, 0, len(fixture.Cases))
	for _, sample := range fixture.Cases {
		expected, err := before.Recall(ctx, actor, fixture.KBID, binding, sample.Query, sample.Vector, Filters{}, 30)
		if err != nil {
			t.Fatalf("source recall %s: %v", sample.ID, err)
		}
		actual, err := after.Recall(ctx, actor, fixture.KBID, binding, sample.Query, sample.Vector, Filters{}, 30)
		if err != nil {
			t.Fatalf("restored recall %s: %v", sample.ID, err)
		}
		for _, candidate := range actual {
			if !slices.Contains(fixture.AllowedDocumentIDs, candidate.DocumentID) {
				t.Fatalf("out-of-scope evidence: %s", sample.ID)
			}
		}
		if len(expected) == 0 || !reflect.DeepEqual(actual, expected) {
			t.Fatalf("restored candidates changed: %s (source=%d restored=%d)", sample.ID, len(expected), len(actual))
		}
		encoded, err := json.Marshal(actual)
		if err != nil {
			t.Fatal(err)
		}
		digest := sha256.Sum256(encoded)
		rows = append(rows, row{ID: sample.ID, Candidates: len(actual), SHA256: hex.EncodeToString(digest[:]), Identical: true})
		t.Logf("%s: %d identical candidates", sample.ID, len(actual))
	}
	fixtureDigest := sha256.Sum256(fixtureBytes)
	report, err := json.MarshalIndent(struct {
		Dataset       string `json:"dataset"`
		FixtureSHA256 string `json:"fixture_sha256"`
		Cases         []row  `json:"cases"`
	}{fixture.Dataset, hex.EncodeToString(fixtureDigest[:]), rows}, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	file, err := os.OpenFile(os.Getenv("TUSKRAG_RESTORE_REPORT"), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := file.Write(append(report, '\n')); err != nil {
		_ = file.Close()
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
}
