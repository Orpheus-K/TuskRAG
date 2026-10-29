//go:build !linux

package parser

import (
	"errors"
	"testing"
)

func TestUnsupportedPlatformDoesNotRunUnsandboxed(t *testing.T) {
	if _, err := NewSandbox(SandboxConfig{}); !errors.Is(err, ErrSandboxUnavailable) {
		t.Fatalf("unsupported sandbox should fail closed: %v", err)
	}
}
