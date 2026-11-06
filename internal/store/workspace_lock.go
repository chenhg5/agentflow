package store

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

var ErrWorkspaceBusy = errors.New("workspace service lock is held")

// The inode stays in place. Process exit releases the OS lock, including after
// an unclean shutdown; PID files and time based lease stealing are unnecessary.
func AcquireWorkspaceLock(ctx context.Context, root, name string, wait bool) (func(), error) {
	if err := EnsureDir(root); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(filepath.Join(root, name), os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, err
	}
	for {
		if err := ctx.Err(); err != nil {
			f.Close()
			return nil, err
		}
		err := tryCreationLock(f)
		if err == nil {
			return func() { _ = f.Close() }, nil
		}
		if !creationLockBusy(err) {
			f.Close()
			return nil, err
		}
		if !wait {
			f.Close()
			return nil, fmt.Errorf("%w: %s", ErrWorkspaceBusy, name)
		}
		select {
		case <-ctx.Done():
			f.Close()
			return nil, ctx.Err()
		case <-time.After(5 * time.Millisecond):
		}
	}
}
