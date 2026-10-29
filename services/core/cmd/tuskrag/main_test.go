package main

import (
	"context"
	"errors"
	"net"
	"net/http"
	"testing"
)

func TestServeStopsWorkerWhenListenerFails(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := listener.Close(); err != nil {
			t.Error(err)
		}
	}()
	stopped := make(chan struct{})
	err = serve(context.Background(), &http.Server{Addr: listener.Addr().String()}, func(ctx context.Context) error {
		<-ctx.Done()
		close(stopped)
		return ctx.Err()
	})
	if err == nil {
		t.Fatal("listener failure was ignored")
	}
	select {
	case <-stopped:
	default:
		t.Fatal("worker was not stopped")
	}
}

func TestServeStopsWhenWorkerFails(t *testing.T) {
	want := errors.New("worker failed")
	err := serve(context.Background(), &http.Server{Addr: "127.0.0.1:0"}, func(context.Context) error { return want })
	if !errors.Is(err, want) {
		t.Fatalf("worker failure: %v", err)
	}
}

func TestServeStopsOnCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	err := serve(ctx, &http.Server{Addr: "127.0.0.1:0"}, func(ctx context.Context) error {
		cancel()
		<-ctx.Done()
		return ctx.Err()
	})
	if err != nil {
		t.Fatalf("shutdown: %v", err)
	}
}

func TestRunWorkerPreservesFailures(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := runWorker(ctx, func(context.Context) error { return context.Canceled }); err != nil {
		t.Fatalf("cancelled worker: %v", err)
	}
	want := errors.New("cleanup failed")
	err := runWorker(ctx, func(context.Context) error { return errors.Join(context.Canceled, want) })
	if !errors.Is(err, want) {
		t.Fatalf("cleanup failure was ignored: %v", err)
	}
}
