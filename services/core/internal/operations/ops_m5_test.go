package operations

import (
	"context"
	"testing"

	"tuskrag.local/core/internal/secrets"
)

func TestDiagnoseAndRotateMasterKeyValidation(t *testing.T) {
	if _, err := Diagnose(context.Background(), nil, 1); err == nil {
		t.Fatal("expected error when pool is nil")
	}
	if _, err := Diagnose(context.Background(), nil, 0); err == nil {
		t.Fatal("expected error when tenantID <= 0")
	}
	vault, err := secrets.NewVault(make([]byte, 32))
	if err != nil {
		t.Fatalf("NewVault: %v", err)
	}
	if _, err := RotateMasterKey(context.Background(), nil, 1, vault, vault); err == nil {
		t.Fatal("expected error when pool is nil")
	}
	if _, err := RotateMasterKey(context.Background(), nil, 0, vault, vault); err == nil {
		t.Fatal("expected error when tenantID <= 0")
	}
}
