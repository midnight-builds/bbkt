//go:build windows

package bitbucket

import (
	"errors"
	"os"
	"time"

	"golang.org/x/sys/windows"
)

// lockFile takes an exclusive lock on the first byte of f, blocking until it
// is available. The lock is released by unlockFile or when f is closed.
func lockFile(f *os.File) error {
	return windows.LockFileEx(windows.Handle(f.Fd()), windows.LOCKFILE_EXCLUSIVE_LOCK, 0, 1, 0, new(windows.Overlapped))
}

func unlockFile(f *os.File) error {
	return windows.UnlockFileEx(windows.Handle(f.Fd()), 0, 1, 0, new(windows.Overlapped))
}

// replaceFile renames src over dst. On Windows the rename fails while another
// process (a concurrent bbkt reader, antivirus, an indexer) has dst open
// without FILE_SHARE_DELETE, so transient access/sharing errors are retried
// with a short backoff.
func replaceFile(src, dst string) error {
	var err error
	delay := 10 * time.Millisecond
	for range 8 {
		err = os.Rename(src, dst)
		if err == nil || !isTransientRenameError(err) {
			return err
		}
		time.Sleep(delay)
		delay *= 2
	}
	return err
}

func isTransientRenameError(err error) bool {
	return errors.Is(err, windows.ERROR_ACCESS_DENIED) || errors.Is(err, windows.ERROR_SHARING_VIOLATION)
}
