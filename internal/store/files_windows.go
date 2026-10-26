//go:build windows

package store

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

// Go's os.Open shares read/write access, but not deletion. Every store reader
// must also allow replacement so an atomic state checkpoint can coexist with
// observers and concurrent idempotent callers.
func openStoreFile(path string) (*os.File, error) {
	name, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	if len(name) >= 248 && !strings.HasPrefix(name, `\\?\`) {
		if strings.HasPrefix(name, `\\`) {
			name = `\\?\UNC\` + name[2:]
		} else {
			name = `\\?\` + name
		}
	}
	encoded, err := syscall.UTF16PtrFromString(name)
	if err != nil {
		return nil, err
	}
	handle, err := syscall.CreateFile(encoded, syscall.GENERIC_READ, syscall.FILE_SHARE_READ|syscall.FILE_SHARE_WRITE|syscall.FILE_SHARE_DELETE, nil, syscall.OPEN_EXISTING, syscall.FILE_ATTRIBUTE_NORMAL, 0)
	if err != nil {
		return nil, &os.PathError{Op: "open", Path: path, Err: err}
	}
	return os.NewFile(uintptr(handle), path), nil
}

// External readers and filesystem scanners may still briefly deny replacement.
// Retry only Windows sharing/locking/access conflicts, with a finite deadline;
// permanent permission errors remain errors and the committed file stays intact.
func replaceStoreFile(from, to string) error {
	deadline := time.Now().Add(time.Second)
	delay := time.Millisecond
	for {
		err := os.Rename(from, to)
		if err == nil {
			return nil
		}
		transient := errors.Is(err, syscall.ERROR_ACCESS_DENIED) || errors.Is(err, syscall.Errno(32)) || errors.Is(err, syscall.Errno(33))
		if !transient || !time.Now().Before(deadline) {
			return err
		}
		time.Sleep(delay)
		if delay < 32*time.Millisecond {
			delay *= 2
		}
	}
}
