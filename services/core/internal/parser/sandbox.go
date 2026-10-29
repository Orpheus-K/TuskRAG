package parser

import (
	"encoding/json"
	"errors"
	"os"
	"time"
)

var (
	ErrSandboxUnavailable = errors.New("parser sandbox unavailable")
	ErrUnsupportedFormat  = errors.New("unsupported document format")
	ErrEncrypted          = errors.New("encrypted document")
	ErrLimitExceeded      = errors.New("parser limit exceeded")
	ErrParse              = errors.New("document parse failed")
	ErrTimeout            = errors.New("parser timed out")
	ErrInvalidOutput      = errors.New("invalid parser output")
)

const (
	MaxInputBytes  int64 = 200 * 1024 * 1024
	MaxOutputBytes int64 = 200 * 1024 * 1024
	MaxMemoryBytes int64 = 1024 * 1024 * 1024
	MaxCPUSeconds  int64 = 300
	MaxWallTime          = 600 * time.Second
)

// SandboxConfig points to a dedicated, trusted parser installation, without secrets.
type SandboxConfig struct {
	Bubblewrap       string
	RuntimeDir       string
	Executable       string
	NativeExecutable string
	TempDir          string
	CgroupRoot       string
	MemoryBytes      int64
	CPUSeconds       int64
	WallTime         time.Duration
}

type ParseInput struct {
	File   *os.File
	MIME   string
	Limits json.RawMessage
}
