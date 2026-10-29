//go:build !windows

package app

import (
	"errors"
	"os"
	"syscall"
)

// tryLock takes an exclusive lock on f without waiting. It returns false when
// another open file holds the lock. The lock (flock) belongs to the open file,
// so two opens of the same path in one program also exclude each other.
func tryLock(f *os.File) (bool, error) {
	for {
		err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB) //nolint:gosec // G115: a file descriptor fits in an int
		switch {
		case err == nil:
			return true, nil
		case errors.Is(err, syscall.EINTR):
			continue
		case errors.Is(err, syscall.EWOULDBLOCK):
			return false, nil
		default:
			return false, err
		}
	}
}

// unlockFile releases the lock of tryLock.
func unlockFile(f *os.File) error {
	return syscall.Flock(int(f.Fd()), syscall.LOCK_UN) //nolint:gosec // G115: a file descriptor fits in an int
}
