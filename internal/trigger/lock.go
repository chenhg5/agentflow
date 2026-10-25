package trigger

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

var ErrServiceBusy = errors.New("trigger service already running for this workspace")
var errWorkspaceBusy = errors.New("workspace lock is held")

// The file stays in place so every process locks the same inode. Closing the
// handle (including process termination) releases the operating system lock.
func acquireWorkspaceLock(ctx context.Context, root, name string, wait bool) (func(), error) {
	file, err := os.OpenFile(filepath.Join(root, name), os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return nil, err
	}
	for {
		if err := tryFileLock(file); err == nil {
			return func() { _ = file.Close() }, nil
		} else if !fileLockBusy(err) {
			_ = file.Close()
			return nil, err
		}
		if !wait {
			_ = file.Close()
			return nil, fmt.Errorf("%w: %s", errWorkspaceBusy, root)
		}
		select {
		case <-ctx.Done():
			_ = file.Close()
			return nil, ctx.Err()
		case <-time.After(10 * time.Millisecond):
		}
	}
}
