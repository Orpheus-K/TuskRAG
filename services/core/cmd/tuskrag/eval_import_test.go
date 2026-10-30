package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"tuskrag.local/core/internal/eval"
)

func TestEvalImport(t *testing.T) {
	dir := t.TempDir()
	manifest, questions, output := filepath.Join(dir, "manifest.json"), filepath.Join(dir, "questions.jsonl"), filepath.Join(dir, "dataset.json")
	for path, data := range map[string]string{
		manifest:  `{"version":"v1","documents":[{"key":"a","title":"制度","content":"申请制度"}]}`,
		questions: `{"id":"q1","question":"申请？","expected":["a"],"answerable":true}`,
	} {
		if err := os.WriteFile(path, []byte(data), 0600); err != nil {
			t.Fatal(err)
		}
	}
	args := []string{"import", manifest, questions, output}
	if err := runEval(context.Background(), args); err != nil {
		t.Fatal(err)
	}
	var data eval.Dataset
	if err := readEvalJSON(output, &data); err != nil || len(data.Questions) != 1 || data.Questions[0].ID != "q1" {
		t.Fatalf("imported dataset cannot be read: %+v %v", data, err)
	}
	if err := runEval(context.Background(), args); err == nil {
		t.Fatal("import overwrote an existing dataset")
	}
	if err := readEvalJSON(output, &data); err != nil || len(data.Questions) != 1 {
		t.Fatalf("existing dataset was changed: %+v %v", data, err)
	}
}
