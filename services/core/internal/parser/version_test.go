package parser

import (
	"context"
	"os"
	"testing"
	"time"
)

func TestReadVersionRejectsRelativePath(t *testing.T) {
	if _, err := ReadVersion(context.Background(), "tuskrag-parse"); err == nil {
		t.Fatal("relative executable path accepted")
	}
}

func TestPythonParserVersion(t *testing.T) {
	executable := os.Getenv("TUSKRAG_TEST_PARSER")
	if executable == "" {
		t.Skip("TUSKRAG_TEST_PARSER is not set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	version, err := ReadVersion(ctx, executable)
	if err != nil {
		t.Fatal(err)
	}
	if version.SchemaVersion != ExpectedSchemaVersion {
		t.Fatalf("unexpected schema version %q", version.SchemaVersion)
	}
}
