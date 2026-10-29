package storage

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"math"
	"os"
	"path"
	"path/filepath"
)

type Local struct {
	root *os.Root
}

var _ Store = (*Local)(nil)

func NewLocal(ctx context.Context, directory string) (*Local, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if !filepath.IsAbs(directory) {
		return nil, errors.New("storage directory must be absolute")
	}
	if err := os.MkdirAll(directory, 0700); err != nil {
		return nil, fmt.Errorf("create storage directory: %w", err)
	}
	root, err := os.OpenRoot(directory)
	if err != nil {
		return nil, fmt.Errorf("open storage directory: %w", err)
	}
	return &Local{root: root}, nil
}

func (s *Local) Close() error {
	if err := s.root.Close(); err != nil {
		return fmt.Errorf("close storage directory: %w", err)
	}
	return nil
}

// Put never replaces an existing version; callers must reconcile errors with
// database state because cleanup can fail after the object has been published.
func (s *Local) Put(ctx context.Context, key Key, input io.Reader, limit int64) (result ObjectInfo, resultErr error) {
	name, err := key.path()
	if err != nil {
		return result, err
	}
	if input == nil || limit <= 0 || limit == math.MaxInt64 {
		return result, errors.New("input and a positive bounded size limit are required")
	}
	if err := ctx.Err(); err != nil {
		return result, err
	}
	directory := path.Dir(name)
	if err := s.root.MkdirAll(directory, 0700); err != nil {
		return result, fmt.Errorf("create object directory: %w", err)
	}
	var nonce [16]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return result, fmt.Errorf("generate temporary file name: %w", err)
	}
	temporary := path.Join(directory, ".upload-"+hex.EncodeToString(nonce[:]))
	file, err := s.root.OpenFile(temporary, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return result, fmt.Errorf("create temporary object: %w", err)
	}
	defer func() {
		if file != nil {
			if err := file.Close(); err != nil {
				resultErr = errors.Join(resultErr, fmt.Errorf("close temporary object: %w", err))
			}
		}
		if err := s.root.Remove(temporary); err != nil && !errors.Is(err, fs.ErrNotExist) {
			resultErr = errors.Join(resultErr, fmt.Errorf("remove temporary object: %w", err))
		}
	}()
	hash := sha256.New()
	size, err := io.Copy(io.MultiWriter(file, hash), io.LimitReader(contextReader{ctx: ctx, reader: input}, limit+1))
	if err != nil {
		return result, fmt.Errorf("write object: %w", err)
	}
	if size > limit {
		return result, ErrTooLarge
	}
	if err := ctx.Err(); err != nil {
		return result, err
	}
	if err := file.Sync(); err != nil {
		return result, fmt.Errorf("sync object: %w", err)
	}
	err = file.Close()
	file = nil
	if err != nil {
		return result, fmt.Errorf("close object: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return result, err
	}
	if err := s.root.Link(temporary, name); errors.Is(err, fs.ErrExist) {
		return result, ErrExists
	} else if err != nil {
		return result, fmt.Errorf("publish object: %w", err)
	}
	result.Size = size
	copy(result.SHA256[:], hash.Sum(nil))
	return result, nil
}

func (s *Local) Open(ctx context.Context, key Key) (io.ReadCloser, error) {
	name, err := key.path()
	if err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	file, err := s.root.Open(name)
	if err != nil {
		return nil, fmt.Errorf("open object: %w", err)
	}
	info, err := file.Stat()
	if err == nil && !info.Mode().IsRegular() {
		err = ErrNotRegular
	}
	if err != nil {
		if closeErr := file.Close(); closeErr != nil {
			err = errors.Join(err, fmt.Errorf("close object: %w", closeErr))
		}
		return nil, fmt.Errorf("inspect object: %w", err)
	}
	return contextReadCloser{contextReader: contextReader{ctx: ctx, reader: file}, file: file}, nil
}

func (s *Local) Exists(ctx context.Context, key Key) (bool, error) {
	name, err := key.path()
	if err != nil {
		return false, err
	}
	if err := ctx.Err(); err != nil {
		return false, err
	}
	info, err := s.root.Stat(name)
	if errors.Is(err, fs.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("inspect object: %w", err)
	}
	if !info.Mode().IsRegular() {
		return false, ErrNotRegular
	}
	return true, nil
}

func (s *Local) Delete(ctx context.Context, key Key) error {
	name, err := key.path()
	if err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := s.root.Remove(name); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("delete object: %w", err)
	}
	return nil
}

type contextReader struct {
	ctx    context.Context
	reader io.Reader
}

func (r contextReader) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	n, err := r.reader.Read(p)
	if contextErr := r.ctx.Err(); contextErr != nil {
		return n, contextErr
	}
	return n, err
}

type contextReadCloser struct {
	contextReader
	file *os.File
}

func (r contextReadCloser) Close() error { return r.file.Close() }
