//go:build !unix

package vault

import (
	"errors"
	"io/fs"
	"os"
)

var errLocked = errors.New("locked by another process")

var errNoLocking = errors.New("file locking is not implemented on this platform")

func lockFile(*os.File) error { return errNoLocking }

func unlockFile(*os.File) error { return nil }

var processUmask = readUmask()

func readUmask() fs.FileMode { return 0 }
