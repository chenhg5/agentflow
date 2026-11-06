//go:build windows

package store

import (
	"encoding/binary"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"
	"unsafe"
)

const (
	deleteAccess          = 0x00010000
	fileRenameInfoEx      = 22
	renameReplaceExisting = 0x1
	renamePOSIXSemantics  = 0x2
)

var setFileInformation = syscall.NewLazyDLL("kernel32.dll").NewProc("SetFileInformationByHandle")

func storeWindowsPath(path string) ([]uint16, error) {
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
	return syscall.UTF16FromString(name)
}

// Go's os.Open shares read/write access, but not deletion. Observers must allow
// replacement while they retain a handle to the previous immutable snapshot.
func openStoreFile(path string) (*os.File, error) {
	name, err := storeWindowsPath(path)
	if err != nil {
		return nil, err
	}
	handle, err := syscall.CreateFile(&name[0], syscall.GENERIC_READ, syscall.FILE_SHARE_READ|syscall.FILE_SHARE_WRITE|syscall.FILE_SHARE_DELETE, nil, syscall.OPEN_EXISTING, syscall.FILE_ATTRIBUTE_NORMAL, 0)
	if err != nil {
		return nil, &os.PathError{Op: "open", Path: path, Err: err}
	}
	return os.NewFile(uintptr(handle), path), nil
}

// FileRenameInfoEx (Windows 10 1607+) provides replacement with open observers,
// unlike MoveFileEx. The old handle keeps its snapshot and new opens see the new
// file. Layout follows FILE_RENAME_INFO, including native HANDLE alignment.
// https://learn.microsoft.com/en-us/windows/win32/api/winbase/ns-winbase-file_rename_info
func renameStoreFile(from, to string) error {
	source, err := storeWindowsPath(from)
	if err != nil {
		return err
	}
	target, err := storeWindowsPath(to)
	if err != nil {
		return err
	}
	handle, err := syscall.CreateFile(&source[0], deleteAccess, syscall.FILE_SHARE_READ|syscall.FILE_SHARE_WRITE|syscall.FILE_SHARE_DELETE, nil, syscall.OPEN_EXISTING, syscall.FILE_ATTRIBUTE_NORMAL, 0)
	if err != nil {
		return err
	}
	defer syscall.CloseHandle(handle)
	type renameLayout struct {
		Flags          uint32
		RootDirectory  syscall.Handle
		FileNameLength uint32
		FileName       uint16
	}
	var layout renameLayout
	offset := int(unsafe.Offsetof(layout.FileName))
	length := (len(target) - 1) * 2                                                   // UTF-16 bytes, excluding the trailing NUL.
	buffer := make([]byte, offset+length+2)                                           // Win32 path conversion also needs a trailing NUL.
	binary.LittleEndian.PutUint32(buffer, renameReplaceExisting|renamePOSIXSemantics) // REPLACE_IF_EXISTS | POSIX_SEMANTICS
	binary.LittleEndian.PutUint32(buffer[unsafe.Offsetof(layout.FileNameLength):], uint32(length))
	for i, char := range target {
		binary.LittleEndian.PutUint16(buffer[offset+i*2:], char)
	}
	result, _, err := setFileInformation.Call(uintptr(handle), fileRenameInfoEx, uintptr(unsafe.Pointer(&buffer[0])), uintptr(len(buffer)))
	if result != 0 {
		return nil
	}
	if err == syscall.Errno(0) {
		return syscall.EINVAL
	}
	return err
}

// External readers and scanners can still briefly deny replacement. Bound the
// retry window; permanent errors remain errors and never trigger delete-first.
func replaceStoreFile(from, to string) error {
	deadline := time.Now().Add(time.Second)
	delay := time.Millisecond
	for {
		err := renameStoreFile(from, to)
		if err == nil {
			return nil
		}
		transient := errors.Is(err, syscall.ERROR_ACCESS_DENIED) || errors.Is(err, syscall.Errno(32)) || errors.Is(err, syscall.Errno(33))
		if !transient || !time.Now().Before(deadline) {
			return &os.LinkError{Op: "rename", Old: from, New: to, Err: err}
		}
		time.Sleep(delay)
		if delay < 32*time.Millisecond {
			delay *= 2
		}
	}
}
