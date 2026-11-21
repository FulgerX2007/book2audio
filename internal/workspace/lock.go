//go:build unix

package workspace

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
)

// ErrLocked is returned when another book2audio process works on the book.
var ErrLocked = errors.New("the book is being processed by another book2audio process")

// lockName is the lock file inside a working folder.
const lockName = ".lock"

// Lock is an exclusive advisory lock on a working folder. The kernel drops
// it when the process exits, so a crash never leaves a stale lock.
type Lock struct {
	f *os.File
}

// AcquireLock takes the exclusive lock of the working folder dir without
// waiting. It returns ErrLocked when the lock is held by someone else.
func AcquireLock(dir string) (*Lock, error) {
	f, err := os.OpenFile(filepath.Join(dir, lockName), os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return nil, fmt.Errorf("locking the working folder: %w", err)
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		f.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) {
			return nil, fmt.Errorf("%w (%s)", ErrLocked, dir)
		}
		return nil, fmt.Errorf("locking the working folder: %w", err)
	}
	return &Lock{f: f}, nil
}

// Release drops the lock. It is safe to call more than once.
func (l *Lock) Release() error {
	if l == nil || l.f == nil {
		return nil
	}
	err := l.f.Close() // closing the descriptor releases the flock
	l.f = nil
	return err
}
