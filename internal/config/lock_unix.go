//go:build unix

package config

import (
	"errors"
	"os"
	"syscall"
)

var errLocked = errors.New("config file is locked")

var errNoLocking = errors.New("file locking is not implemented on this platform")

func lockFile(f *os.File) error {
	err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
	if errors.Is(err, syscall.EWOULDBLOCK) {
		return errLocked
	}
	return err
}

func unlockFile(f *os.File) error {
	return syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
}
