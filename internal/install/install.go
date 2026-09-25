// Package install locates nn's bundled resources and the nn-vision helper.
package install

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
)

var executable = os.Executable

func Resources() (string, bool) {
	exe, err := executable()
	if err != nil {
		return "", false
	}
	real, err := filepath.EvalSymlinks(exe)
	if err != nil {
		return "", false
	}
	var first string
	for _, dir := range []string{
		filepath.Join(filepath.Dir(real), "share", "nn"),
		filepath.Join(filepath.Dir(real), "..", "share", "nn"),
	} {
		info, err := os.Stat(dir)
		if err != nil || !info.IsDir() {
			continue
		}
		dir = filepath.Clean(dir)
		if isRegularFile(filepath.Join(dir, "nn-vision")) {
			return dir, true
		}
		if first == "" {
			first = dir
		}
	}
	return first, first != ""
}

func Helper() (string, error) {
	if p := os.Getenv("NN_VISION_HELPER"); p != "" {
		if !isRegularFile(p) {
			return "", fmt.Errorf("NN_VISION_HELPER is set to %s, which is not a file", p)
		}
		return p, nil
	}
	if share, ok := Resources(); ok {
		if p := filepath.Join(share, "nn-vision"); isRegularFile(p) {
			return p, nil
		}
	}
	if dataHome, ok := xdgDataHome(); ok {
		if p := filepath.Join(dataHome, "nn", "nn-vision"); isRegularFile(p) {
			return p, nil
		}
	}
	if p, ok := devHelper(); ok {
		return p, nil
	}
	return "", errors.New("nn-vision helper not found; set NN_VISION_HELPER or build one with scripts/build-macos-helper")
}

func isRegularFile(p string) bool {
	info, err := os.Stat(p)
	return err == nil && !info.IsDir()
}

func xdgDataHome() (string, bool) {
	if dir := os.Getenv("XDG_DATA_HOME"); filepath.IsAbs(dir) {
		return dir, true
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", false
	}
	return filepath.Join(home, ".local", "share"), true
}

func devHelper() (string, bool) {
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		return "", false
	}
	return devHelperFrom(file)
}

func devHelperFrom(file string) (string, bool) {
	if !filepath.IsAbs(file) {
		return "", false
	}

	root := filepath.Dir(filepath.Dir(filepath.Dir(file)))
	p := filepath.Join(root, "dist", "helper", "nn-vision")
	if isRegularFile(p) {
		return p, true
	}
	return "", false
}
