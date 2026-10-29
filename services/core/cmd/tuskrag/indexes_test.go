package main

import (
	"context"
	"testing"
)

func TestProvisionIndexesRejectsInvalidArguments(t *testing.T) {
	for _, args := range [][]string{
		nil, {"1"}, {"1", "2", "3"}, {"0", "2"}, {"1", "-2"}, {"1; DROP TABLE chunk", "2"},
	} {
		if err := provisionIndexes(context.Background(), args); err == nil {
			t.Fatalf("invalid arguments accepted: %q", args)
		}
	}
}
