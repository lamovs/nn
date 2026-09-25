//go:build unix

package vault

import (
	"errors"
	"io/fs"
	"os"
	"syscall"
)

var errLocked = errors.New("locked by another process")

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

// processUmask is read once at start-up: reading it later would race other
// goroutines creating files while the mask is momentarily zero.
var processUmask = readUmask()

func readUmask() fs.FileMode {
	mask := syscall.Umask(0)
	syscall.Umask(mask)
	return fs.FileMode(mask) & fs.ModePerm
}
