//go:build windows

package trigger

import (
	"errors"
	"os"
	"syscall"
	"unsafe"
)

var lockFileEx = syscall.NewLazyDLL("kernel32.dll").NewProc("LockFileEx")

func tryFileLock(file *os.File) error {
	const exclusiveAndImmediate = 0x2 | 0x1
	var overlapped syscall.Overlapped
	result, _, err := lockFileEx.Call(file.Fd(), exclusiveAndImmediate, 0, 1, 0, uintptr(unsafe.Pointer(&overlapped)))
	if result == 0 {
		return err
	}
	return nil
}

func fileLockBusy(err error) bool {
	const errorLockViolation syscall.Errno = 33
	return errors.Is(err, errorLockViolation)
}
