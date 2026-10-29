package storage

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func localStore(t *testing.T) (*Local, string) {
	t.Helper()
	directory := t.TempDir()
	store, err := NewLocal(context.Background(), directory)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := store.Close(); err != nil {
			t.Errorf("close storage: %v", err)
		}
	})
	return store, directory
}

func TestLocalRoundTripAndImmutableVersions(t *testing.T) {
	store, _ := localStore(t)
	ctx := context.Background()
	key := Key{TenantID: 1, KBID: 2, DocumentID: 3, VersionID: 4}
	content := []byte("内容 and table\n")
	info, err := store.Put(ctx, key, bytes.NewReader(content), int64(len(content)))
	if err != nil || info.Size != int64(len(content)) || info.SHA256 != sha256.Sum256(content) {
		t.Fatalf("put result=%+v err=%v", info, err)
	}
	reader, err := store.Open(ctx, key)
	if err != nil {
		t.Fatal(err)
	}
	got, readErr := io.ReadAll(reader)
	closeErr := reader.Close()
	if readErr != nil || closeErr != nil || !bytes.Equal(got, content) {
		t.Fatalf("content=%q read=%v close=%v", got, readErr, closeErr)
	}
	if _, err := store.Put(ctx, key, strings.NewReader("replacement"), 32); !errors.Is(err, ErrExists) {
		t.Fatalf("overwrite must fail: %v", err)
	}
	other := key
	other.TenantID++
	if exists, err := store.Exists(ctx, other); err != nil || exists {
		t.Fatalf("other tenant's object: exists=%t err=%v", exists, err)
	}
	if exists, err := store.Exists(ctx, key); err != nil || !exists {
		t.Fatalf("object missing: exists=%t err=%v", exists, err)
	}
	if err := store.Delete(ctx, key); err != nil {
		t.Fatal(err)
	}
	if err := store.Delete(ctx, key); err != nil {
		t.Fatalf("repeated deletion should be harmless: %v", err)
	}
	if _, err := store.Open(ctx, key); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("deleted object was opened: %v", err)
	}
	if _, err := store.Put(ctx, Key{}, bytes.NewReader(content), 100); !errors.Is(err, ErrInvalidKey) {
		t.Fatalf("invalid key accepted: %v", err)
	}
}

type failingReader struct{ err error }

func (r failingReader) Read([]byte) (int, error) { return 0, r.err }

type cancellingReader struct{ cancel context.CancelFunc }

func (r cancellingReader) Read(p []byte) (int, error) {
	r.cancel()
	return copy(p, "partial"), nil
}

func TestLocalFailedWritesAreNotPublished(t *testing.T) {
	store, directory := localStore(t)
	key := Key{TenantID: 1, KBID: 1, DocumentID: 1, VersionID: 1}
	ctx := context.Background()
	if _, err := store.Put(ctx, key, strings.NewReader("12345"), 4); !errors.Is(err, ErrTooLarge) {
		t.Fatalf("size limit not enforced: %v", err)
	}
	readErr := errors.New("input failure")
	if _, err := store.Put(ctx, key, failingReader{err: readErr}, 10); !errors.Is(err, readErr) {
		t.Fatalf("input failure not returned: %v", err)
	}
	cancelCtx, cancel := context.WithCancel(ctx)
	if _, err := store.Put(cancelCtx, key, cancellingReader{cancel: cancel}, 10); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled write not rejected: %v", err)
	}
	if exists, err := store.Exists(ctx, key); err != nil || exists {
		t.Fatalf("failed write was published: exists=%t err=%v", exists, err)
	}
	if err := filepath.WalkDir(directory, func(_ string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if strings.HasPrefix(entry.Name(), ".upload-") {
			return fmt.Errorf("temporary file was left behind: %s", entry.Name())
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestLocalConcurrentPublish(t *testing.T) {
	store, _ := localStore(t)
	key := Key{TenantID: 1, KBID: 2, DocumentID: 3, VersionID: 4}
	type result struct {
		body string
		err  error
	}
	start := make(chan struct{})
	results := make(chan result, 8)
	for i := range 8 {
		go func() {
			body := fmt.Sprintf("writer-%d", i)
			<-start
			_, err := store.Put(context.Background(), key, strings.NewReader(body), 100)
			results <- result{body: body, err: err}
		}()
	}
	close(start)
	winner := ""
	for range 8 {
		result := <-results
		if result.err == nil {
			if winner != "" {
				t.Fatal("more than one writer published the same version")
			}
			winner = result.body
		} else if !errors.Is(result.err, ErrExists) {
			t.Fatalf("unexpected competing write error: %v", result.err)
		}
	}
	if winner == "" {
		t.Fatal("no writer published the object")
	}
	reader, err := store.Open(context.Background(), key)
	if err != nil {
		t.Fatal(err)
	}
	got, readErr := io.ReadAll(reader)
	closeErr := reader.Close()
	if readErr != nil || closeErr != nil || string(got) != winner {
		t.Fatalf("published=%q winner=%q read=%v close=%v", got, winner, readErr, closeErr)
	}
}

func TestLocalRejectsEscapingSymlink(t *testing.T) {
	store, directory := localStore(t)
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(directory, "t")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	key := Key{TenantID: 1, KBID: 2, DocumentID: 3, VersionID: 4}
	if _, err := store.Put(context.Background(), key, strings.NewReader("secret"), 100); err == nil {
		t.Fatal("storage path escaped its root")
	}
	entries, err := os.ReadDir(outside)
	if err != nil || len(entries) != 0 {
		t.Fatalf("outside directory modified: entries=%v err=%v", entries, err)
	}
}

func TestLocalReadCancellationAndInvalidObject(t *testing.T) {
	store, directory := localStore(t)
	ctx := context.Background()
	key := Key{TenantID: 1, KBID: 2, DocumentID: 3, VersionID: 4}
	if _, err := store.Put(ctx, key, strings.NewReader("content"), 10); err != nil {
		t.Fatal(err)
	}
	readCtx, cancel := context.WithCancel(ctx)
	reader, err := store.Open(readCtx, key)
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	cancel()
	_, readErr := io.ReadAll(reader)
	closeErr := reader.Close()
	if !errors.Is(readErr, context.Canceled) || closeErr != nil {
		t.Fatalf("cancelled read: read=%v close=%v", readErr, closeErr)
	}
	if err := store.Delete(ctx, key); err != nil {
		t.Fatal(err)
	}
	name, err := key.path()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(directory, filepath.FromSlash(name)), 0700); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Exists(ctx, key); !errors.Is(err, ErrNotRegular) {
		t.Fatalf("non-file object accepted: %v", err)
	}
	if _, err := store.Open(ctx, key); !errors.Is(err, ErrNotRegular) {
		t.Fatalf("non-file object opened: %v", err)
	}
}
