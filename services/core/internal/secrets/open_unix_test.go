//go:build unix

package secrets

import (
	"path/filepath"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

func TestPrivateFileRejectsPipeWithoutBlocking(t *testing.T) {
	path := filepath.Join(t.TempDir(), "pipe")
	if err := unix.Mkfifo(path, 0600); err != nil {
		t.Fatal(err)
	}
	result := make(chan error, 1)
	go func() {
		_, err := ReadPrivateFile(path, 64)
		result <- err
	}()
	select {
	case err := <-result:
		if err == nil {
			t.Fatal("named pipe accepted as a private file")
		}
	case <-time.After(time.Second):
		t.Fatal("opening named pipe blocked")
	}
}
