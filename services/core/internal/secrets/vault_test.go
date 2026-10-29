package secrets

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestVaultAuthenticatesProviderAndTenant(t *testing.T) {
	var key [32]byte
	if _, err := rand.Read(key[:]); err != nil {
		t.Fatal(err)
	}
	vault, err := NewVault(key[:])
	if err != nil {
		t.Fatal(err)
	}
	plaintext := []byte("test-provider-credential")
	first, err := vault.Seal(context.Background(), 1, 2, plaintext)
	if err != nil {
		t.Fatal(err)
	}
	second, err := vault.Seal(context.Background(), 1, 2, plaintext)
	if err != nil || bytes.Equal(first.Nonce, second.Nonce) || bytes.Contains(first.Ciphertext, plaintext) {
		t.Fatalf("invalid encryption output: %v", err)
	}
	opened, err := vault.Open(context.Background(), 1, 2, first)
	if err != nil || !bytes.Equal(opened, plaintext) {
		t.Fatalf("decrypt failed: %v", err)
	}
	clear(opened)
	for _, ids := range [][2]int64{{1, 3}, {2, 2}} {
		if _, err := vault.Open(context.Background(), ids[0], ids[1], first); !errors.Is(err, ErrInvalidSecret) {
			t.Fatalf("ciphertext accepted for another provider or tenant: %v", err)
		}
	}
	tampered := first
	tampered.Ciphertext = bytes.Clone(first.Ciphertext)
	tampered.Ciphertext[0] ^= 1
	if _, err := vault.Open(context.Background(), 1, 2, tampered); !errors.Is(err, ErrInvalidSecret) {
		t.Fatalf("tampered ciphertext accepted: %v", err)
	}
	otherKey := bytes.Repeat([]byte{1}, 32)
	other, err := NewVault(otherKey)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := other.Open(context.Background(), 1, 2, first); !errors.Is(err, ErrKeyUnavailable) {
		t.Fatalf("wrong master key accepted: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := vault.Seal(ctx, 1, 2, plaintext); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled encryption accepted: %v", err)
	}
}

func TestLoadVaultRequiresPrivateValidKeyFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "master-key")
	key := bytes.Repeat([]byte{0x42}, 32)
	if err := os.WriteFile(path, []byte(hex.EncodeToString(key)+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	vault, err := LoadVault(path)
	if err != nil {
		t.Fatal(err)
	}
	direct, err := NewVault(key)
	if err != nil || vault.keyID != direct.keyID {
		t.Fatalf("loaded key differs: %v", err)
	}
	if err := os.WriteFile(path, []byte("not-a-key"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadVault(path); err == nil {
		t.Fatal("invalid master key accepted")
	}
	if runtime.GOOS != "windows" {
		if err := os.WriteFile(path, []byte(hex.EncodeToString(key)), 0600); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(path, 0644); err != nil {
			t.Fatal(err)
		}
		if _, err := LoadVault(path); err == nil {
			t.Fatal("public master key file accepted")
		}
	}
}
