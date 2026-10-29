package tokenizer

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"reflect"
	"strings"
	"sync"
	"testing"
)

type referenceCase struct {
	ID        string `json:"id"`
	Text      string `json:"text"`
	Reference []int  `json:"reference_ids"`
}

var (
	fixtureOnce      sync.Once
	fixtureTokenizer *BGEM3
	fixtureError     error
)

func tokenizerFixture(t testing.TB) *BGEM3 {
	t.Helper()
	fixtureOnce.Do(func() { fixtureTokenizer, fixtureError = NewBGEM3(context.Background(), 4) })
	if fixtureError != nil {
		t.Fatal(fixtureError)
	}
	return fixtureTokenizer
}

func checkReferences(t *testing.T, path string) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(data) > 64*1024*1024 {
		t.Fatal("reference set exceeds limit")
	}
	var cases []referenceCase
	if err := json.Unmarshal(data, &cases); err != nil || len(cases) == 0 {
		t.Fatalf("invalid reference set: %v", err)
	}
	counter := tokenizerFixture(t)
	checked, skipped := 0, 0
	for _, tc := range cases {
		if strings.ContainsRune(tc.Text, '\x00') || len(tc.Text) > maxInputBytes {
			skipped++
			continue
		}
		ids, err := counter.tokenIDs(context.Background(), tc.Text)
		if err != nil || !reflect.DeepEqual(ids, tc.Reference) {
			first := 0
			for first < len(ids) && first < len(tc.Reference) && ids[first] == tc.Reference[first] {
				first++
			}
			t.Fatalf("reference %s: got %d IDs, want %d, first difference at %d, err=%v", tc.ID, len(ids), len(tc.Reference), first, err)
		}
		checked++
	}
	if checked == 0 {
		t.Fatal("no valid reference cases")
	}
	t.Logf("checked=%d skipped=%d", checked, skipped)
}

func TestBGEM3Golden(t *testing.T) { checkReferences(t, "testdata/reference.json") }

func TestBGEM3ExternalReferences(t *testing.T) {
	path := os.Getenv("TUSKRAG_TOKENIZER_REFERENCE")
	if path == "" {
		t.Skip("additional tokenizer reference corpus is not set")
	}
	checkReferences(t, path)
}

func TestBGEM3RejectsInvalidInput(t *testing.T) {
	counter := tokenizerFixture(t)
	if _, err := new(BGEM3).Count(context.Background(), "content"); err == nil {
		t.Fatal("uninitialized tokenizer accepted")
	}
	for _, tc := range []struct {
		text     string
		expected error
	}{
		{"invalid\xff", ErrInvalidInput},
		{"nul\x00value", ErrInvalidInput},
		{strings.Repeat("a", maxInputBytes+1), ErrInputTooLarge},
	} {
		if _, err := counter.Count(context.Background(), tc.text); !errors.Is(err, tc.expected) {
			t.Fatalf("got %v, want %v", err, tc.expected)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := counter.Count(ctx, "content"); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled count: %v", err)
	}
	busy := &BGEM3{model: counter.model, normalizer: counter.normalizer, slots: make(chan struct{}, 1)}
	busy.slots <- struct{}{}
	if _, err := busy.Count(ctx, "waiting"); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled while waiting: %v", err)
	}
	if _, err := NewBGEM3(ctx, 1); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled load: %v", err)
	}
	if _, err := NewBGEM3(context.Background(), 0); err == nil {
		t.Fatal("invalid concurrency accepted")
	}
	if _, err := readAsset([]byte("invalid gzip")); err == nil {
		t.Fatal("invalid asset accepted")
	}
}

func TestBGEM3ConcurrentCounting(t *testing.T) {
	counter := tokenizerFixture(t)
	text := "差旅报销制度：北京住宿标准。<mask>Ａ́"
	want, err := counter.Count(context.Background(), text)
	if err != nil {
		t.Fatal(err)
	}
	errorsCh := make(chan error, 16)
	var group sync.WaitGroup
	for range 16 {
		group.Add(1)
		go func() {
			defer group.Done()
			for range 20 {
				count, err := counter.Count(context.Background(), text)
				if err != nil {
					errorsCh <- err
					return
				}
				if count != want {
					errorsCh <- errors.New("concurrent token count changed")
					return
				}
			}
		}()
	}
	group.Wait()
	close(errorsCh)
	for err := range errorsCh {
		t.Error(err)
	}
}

func BenchmarkBGEM3Count(b *testing.B) {
	counter := tokenizerFixture(b)
	text := strings.Repeat("差旅报销标准与住宿审批。", 50)
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		if _, err := counter.Count(context.Background(), text); err != nil {
			b.Fatal(err)
		}
	}
}
