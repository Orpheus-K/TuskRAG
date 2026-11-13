package kb

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"tuskrag.local/core/internal/operations"
	"tuskrag.local/core/internal/storage"
)

func TestVerifyStoragePostgres(t *testing.T) {
	f := setup(t)
	base := f.create(t)
	service, files, root := documentFixture(t, f)
	var last UploadedDocument
	var size int64
	for i := range 101 {
		body := fmt.Sprintf("Storage verification document %03d.\n", i)
		var err error
		last, err = service.Upload(f.ctx, f.actor(f.owner), UploadRequest{KBID: base.ID, Title: fmt.Sprintf("%d.txt", i), MIME: "text/plain", Content: strings.NewReader(body)})
		if err != nil {
			t.Fatal(err)
		}
		size += int64(len(body))
	}
	report, err := operations.VerifyStorage(f.ctx, f.pool, files, f.tenantA)
	if err != nil || report.Versions != 101 || report.Bytes != size {
		t.Fatalf("paginated originals: %+v %v", report, err)
	}
	foreign, err := operations.VerifyStorage(f.ctx, f.pool, files, f.tenantB)
	if err != nil || foreign.Versions != 0 {
		t.Fatalf("tenant isolation: %+v %v", foreign, err)
	}
	if _, err := operations.VerifyStorage(f.ctx, f.pool, files, 9223372036854775807); err == nil {
		t.Fatal("unknown tenant reported success")
	}
	cancelled, cancel := context.WithCancel(f.ctx)
	cancel()
	if _, err := operations.VerifyStorage(cancelled, f.pool, files, f.tenantA); err == nil {
		t.Fatal("cancelled verification reported success")
	}
	key := storage.Key{TenantID: f.tenantA, KBID: base.ID, DocumentID: last.DocumentID, VersionID: last.VersionID}
	name, err := key.Path()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.admin.Exec(f.ctx, `UPDATE document_version SET file_key='wrong/original' WHERE tenant_id=$1 AND id=$2`, f.tenantA, last.VersionID); err != nil {
		t.Fatal(err)
	}
	if _, err := operations.VerifyStorage(f.ctx, f.pool, files, f.tenantA); err == nil || !strings.Contains(err.Error(), "noncanonical") {
		t.Fatalf("wrong key: %v", err)
	}
	if _, err := f.admin.Exec(f.ctx, `UPDATE document_version SET file_key=$1 WHERE tenant_id=$2 AND id=$3`, name, f.tenantA, last.VersionID); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, filepath.FromSlash(name))
	original, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	original[0] ^= 1
	if err := os.WriteFile(path, original, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := operations.VerifyStorage(f.ctx, f.pool, files, f.tenantA); err == nil || !strings.Contains(err.Error(), "SHA-256 mismatch") {
		t.Fatalf("same-size corruption: %v", err)
	}
	if err := files.Delete(f.ctx, key); err != nil {
		t.Fatal(err)
	}
	if _, err := operations.VerifyStorage(f.ctx, f.pool, files, f.tenantA); err == nil {
		t.Fatal("missing original reported success")
	}
	var status string
	if err := f.admin.QueryRow(f.ctx, `SELECT status FROM document_version WHERE tenant_id=$1 AND id=$2`, f.tenantA, last.VersionID).Scan(&status); err != nil || status != "queued" {
		t.Fatalf("verification modified version: %s %v", status, err)
	}
}
