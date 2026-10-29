package main

import (
	"bytes"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestReadBootstrapPasswordFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "password")
	if err := os.WriteFile(path, []byte("correct horse battery staple\r\n"), 0600); err != nil {
		t.Fatal(err)
	}
	password, err := readPasswordFile(path)
	if err != nil || !bytes.Equal(password, []byte("correct horse battery staple")) {
		t.Fatalf("password file read failed: %v", err)
	}
	clear(password)
	if err := os.WriteFile(path, bytes.Repeat([]byte("x"), 1027), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := readPasswordFile(path); err == nil {
		t.Fatal("oversized password file accepted")
	}
	if _, err := readPasswordFile(filepath.Dir(path)); err == nil {
		t.Fatal("password directory accepted")
	}
	if runtime.GOOS != "windows" {
		if err := os.Chmod(path, 0644); err != nil {
			t.Fatal(err)
		}
		if _, err := readPasswordFile(path); err == nil {
			t.Fatal("public password file accepted")
		}
	}
}
