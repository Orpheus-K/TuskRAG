package parser

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
)

const ExpectedSchemaVersion = "1.0.0"
const ExpectedToolVersion = "0.5.0"

type Version struct {
	Tool          string            `json:"tool"`
	Version       string            `json:"version"`
	SchemaVersion string            `json:"schema_version"`
	Drivers       map[string]string `json:"drivers"`
}

type boundedBuffer struct {
	buffer bytes.Buffer
}

func (b *boundedBuffer) Write(p []byte) (int, error) {
	if b.buffer.Len()+len(p) > 4096 {
		return 0, errors.New("parser version output exceeds limit")
	}
	return b.buffer.Write(p)
}

func ReadVersion(ctx context.Context, executable string) (Version, error) {
	if !filepath.IsAbs(executable) {
		return Version{}, errors.New("parser executable must be an absolute path")
	}
	cmd := exec.CommandContext(ctx, executable, "version")
	cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "SYSTEMROOT=" + os.Getenv("SYSTEMROOT")}
	var output boundedBuffer
	cmd.Stdout = &output
	cmd.Stderr = io.Discard
	if err := cmd.Run(); err != nil {
		return Version{}, fmt.Errorf("run parser version: %w", err)
	}
	var version Version
	decoder := json.NewDecoder(&output.buffer)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&version); err != nil {
		return Version{}, fmt.Errorf("decode parser version: %w", err)
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return Version{}, errors.New("parser version contains trailing data")
	}
	if version.Tool != "tuskrag-parse" || version.Version != ExpectedToolVersion || version.SchemaVersion != ExpectedSchemaVersion || version.Drivers["csv"] == "" || version.Drivers["docx"] == "" || version.Drivers["xlsx"] == "" || version.Drivers["pdf"] == "" {
		return Version{}, errors.New("parser version or schema is incompatible")
	}
	return version, nil
}
