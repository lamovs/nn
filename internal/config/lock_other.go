//go:build !unix

package config

import (
	"errors"
	"os"
)

var errLocked = errors.New("config file is locked")

var errNoLocking = errors.New("file locking is not implemented on this platform")

func lockFile(*os.File) error { return errNoLocking }

func unlockFile(*os.File) error { return nil }
