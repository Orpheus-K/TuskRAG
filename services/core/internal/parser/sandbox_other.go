//go:build !linux

package parser

import "context"

type Sandbox struct{}

func NewSandbox(_ SandboxConfig) (*Sandbox, error) { return nil, ErrSandboxUnavailable }

func (s *Sandbox) run(_ context.Context, _ ParseInput) ([]byte, error) {
	return nil, ErrSandboxUnavailable
}
