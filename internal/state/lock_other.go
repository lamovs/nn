//go:build !unix

package state

import (
	"errors"
	"os"
)

var errLocked = errors.New("state file is locked")

var errNoLocking = errors.New("process locking is not implemented on this platform")

func lockFile(*os.File) error { return errNoLocking }

func unlockFile(*os.File) error { return nil }
