package retrieval

import (
	"strings"
	"sync"
	"testing"
)

func TestCompatibilityTokenizerQuerySyntaxAndConcurrency(t *testing.T) {
	c, err := NewCompatibilityTokenizer()
	if err != nil {
		t.Fatal(err)
	}
	query := c.Query("差旅报销 TR-101 ! ' | : * ( )")
	if !strings.Contains(query, "差旅") || !strings.Contains(query, "报销") || strings.ContainsAny(query, "!*():") {
		t.Fatalf("unexpected token query: %s", query)
	}
	if c.Query("!|()'") != "" {
		t.Fatal("punctuation became a search expression")
	}
	var group sync.WaitGroup
	for range 8 {
		group.Go(func() {
			for range 10 {
				if c.Query("差旅报销 TR-101 ! ' | : * ( )") != query {
					t.Error("unstable concurrent tokenization")
				}
			}
		})
	}
	group.Wait()
}
