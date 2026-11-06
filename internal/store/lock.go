package store

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// ErrRunBusy means another live process is executing the run.
var ErrRunBusy = errors.New("run is being executed by another process")

const lockFile = "executor.lock"

// AcquireRunLock makes the caller the only executor of a run. A lock left by
// a process that no longer exists is taken over. The returned flag reports a
// takeover, which tells the caller that nodes marked running were orphaned.
func AcquireRunLock(runDir string) (release func(), recovered bool, err error) {
	path := filepath.Join(runDir, lockFile)
	for attempt := 0; attempt < 2; attempt++ {
		file, openErr := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
		if openErr == nil {
			_, writeErr := fmt.Fprintf(file, "%d\n", os.Getpid())
			closeErr := file.Close()
			if writeErr != nil || closeErr != nil {
				_ = os.Remove(path)
				return nil, false, errors.Join(writeErr, closeErr)
			}
			return func() { _ = os.Remove(path) }, recovered, nil
		}
		if !os.IsExist(openErr) {
			return nil, false, openErr
		}
		data, readErr := os.ReadFile(path)
		if readErr != nil && !os.IsNotExist(readErr) {
			return nil, false, readErr
		}
		pid, _ := strconv.Atoi(strings.TrimSpace(string(data)))
		if pid > 0 && processAlive(pid) {
			return nil, false, fmt.Errorf("%w (pid %d)", ErrRunBusy, pid)
		}
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			return nil, false, err
		}
		recovered = true
	}
	return nil, false, ErrRunBusy
}
