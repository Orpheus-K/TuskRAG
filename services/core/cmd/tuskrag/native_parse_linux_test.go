//go:build linux

package main

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"tuskrag.local/core/internal/parser"
)

func TestNativeTextSandboxMatchesBaseline(t *testing.T) {
	for _, name := range []string{"TUSKRAG_TEST_BWRAP", "TUSKRAG_TEST_PARSER_RUNTIME", "TUSKRAG_TEST_PARSER", "TUSKRAG_TEST_CGROUP"} {
		if os.Getenv(name) == "" {
			t.Skip("native parser test requires the isolated parser environment")
		}
	}
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	if productionBinary := os.Getenv("TUSKRAG_TEST_NATIVE_BINARY"); productionBinary != "" {
		executable = productionBinary
	}
	sandbox, err := parser.NewSandbox(parser.SandboxConfig{
		Bubblewrap: os.Getenv("TUSKRAG_TEST_BWRAP"), RuntimeDir: os.Getenv("TUSKRAG_TEST_PARSER_RUNTIME"),
		Executable: os.Getenv("TUSKRAG_TEST_PARSER"), NativeExecutable: executable,
		TempDir: t.TempDir(), CgroupRoot: os.Getenv("TUSKRAG_TEST_CGROUP"),
		MemoryBytes: parser.MaxMemoryBytes, CPUSeconds: 30, WallTime: 60 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	validator, err := parser.NewValidator()
	if err != nil {
		t.Fatal(err)
	}
	isolated, err := parser.NewParser(sandbox, validator)
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range []struct{ file, mime string }{
		{"policy.md", "text/markdown"}, {"encoding.md", "text/markdown"},
		{"script.md", "text/markdown"}, {"policy.txt", "text/plain"},
	} {
		t.Run(item.file, func(t *testing.T) {
			file, err := os.Open(filepath.Join("..", "..", "internal", "parser", "testdata", "native-text", item.file))
			if err != nil {
				t.Fatal(err)
			}
			defer func() {
				if err := file.Close(); err != nil {
					t.Error(err)
				}
			}()
			input := parser.ParseInput{File: file, MIME: item.mime, Limits: json.RawMessage(`{}`)}
			baseline, err := parser.ParseNativeText(context.Background(), input, validator)
			if err != nil {
				t.Fatal(err)
			}
			got, err := isolated.Parse(context.Background(), input)
			if err != nil || !reflect.DeepEqual(got, baseline) {
				t.Fatalf("isolated parser changed output: got=%+v baseline=%+v err=%v", got, baseline, err)
			}
		})
	}
	for _, item := range []struct {
		name, source, limits string
		want                 error
	}{
		{"unsupported", "# 标题\n\n![图片](image.png)\n", `{}`, parser.ErrUnsupportedFormat},
		{"limit", "# 标题\n\n正文。\n", `{"max_input_bytes":5}`, parser.ErrLimitExceeded},
		{"invalid_utf8", "# 标题\n\n\xff\xfe", `{}`, parser.ErrParse},
	} {
		t.Run(item.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "input")
			if err := os.WriteFile(path, []byte(item.source), 0600); err != nil {
				t.Fatal(err)
			}
			file, err := os.Open(path)
			if err != nil {
				t.Fatal(err)
			}
			defer func() {
				if err := file.Close(); err != nil {
					t.Error(err)
				}
			}()
			input := parser.ParseInput{File: file, MIME: "text/markdown", Limits: json.RawMessage(item.limits)}
			_, baselineErr := parser.ParseNativeText(context.Background(), input, validator)
			_, gotErr := isolated.Parse(context.Background(), input)
			if !errors.Is(baselineErr, item.want) || !errors.Is(gotErr, item.want) {
				t.Fatalf("failure changed: baseline=%v isolated=%v want=%v", baselineErr, gotErr, item.want)
			}
		})
	}
}
