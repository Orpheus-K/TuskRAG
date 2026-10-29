package storage

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
)

var (
	ErrInvalidKey = errors.New("invalid file key")
	ErrExists     = errors.New("file already exists")
	ErrTooLarge   = errors.New("file exceeds size limit")
	ErrNotRegular = errors.New("stored object is not a regular file")
)

// Key identifies one immutable original document version.
type Key struct {
	TenantID   int64
	KBID       int64
	DocumentID int64
	VersionID  int64
}

// Path returns the canonical storage key after validating its identifiers.
func (k Key) Path() (string, error) { return k.path() }

func (k Key) path() (string, error) {
	if k.TenantID <= 0 || k.KBID <= 0 || k.DocumentID <= 0 || k.VersionID <= 0 {
		return "", ErrInvalidKey
	}
	return fmt.Sprintf("t/%d/kb/%d/d/%d/v/%d/original", k.TenantID, k.KBID, k.DocumentID, k.VersionID), nil
}

type ObjectInfo struct {
	Size   int64
	SHA256 [sha256.Size]byte
}

// Store publishes an original only after its entire content has been written.
// Delete must succeed when the object is already absent, so cleanup can resume.
type Store interface {
	Put(context.Context, Key, io.Reader, int64) (ObjectInfo, error)
	Open(context.Context, Key) (io.ReadCloser, error)
	Exists(context.Context, Key) (bool, error)
	Delete(context.Context, Key) error
	Close() error
}
