//go:build windows

package store

import (
	"errors"
	"os"
	"syscall"
	"unsafe"
)

var creationLockFileEx = syscall.NewLazyDLL("kernel32.dll").NewProc("LockFileEx")

func tryCreationLock(file *os.File) error {
	var overlapped syscall.Overlapped
	result, _, err := creationLockFileEx.Call(file.Fd(), 0x2|0x1, 0, 1, 0, uintptr(unsafe.Pointer(&overlapped)))
	if result == 0 {
		return err
	}
	return nil
}

func creationLockBusy(err error) bool { return errors.Is(err, syscall.Errno(33)) }

// Files are flushed before rename; Go's directory Sync is not supported here.
func syncCreationDirectory(string) error { return nil }
