//go:build darwin || linux || freebsd || netbsd || openbsd || dragonfly

package bitbucket

import (
	"errors"
	"os"
	"syscall"
)

// lockFile takes an exclusive advisory lock on f, blocking until it is
// available. The lock is released by unlockFile or when f is closed.
func lockFile(f *os.File) error {
	for {
		err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX) // #nosec G115 -- a file descriptor always fits in int
		if !errors.Is(err, syscall.EINTR) {
			return err
		}
	}
}

func unlockFile(f *os.File) error {
	return syscall.Flock(int(f.Fd()), syscall.LOCK_UN) // #nosec G115 -- a file descriptor always fits in int
}
