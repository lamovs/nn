//go:build unix

package vault

import (
	"io/fs"
	"syscall"
	"testing"
)

func TestReadUmask(t *testing.T) {
	old := syscall.Umask(0o027)
	defer syscall.Umask(old)

	if got := readUmask(); got != fs.FileMode(0o027) {
		t.Fatalf("readUmask = %03o, want 027", uint32(got))
	}
	if again := syscall.Umask(0o027); again != 0o027 {
		t.Fatalf("readUmask left the umask at %03o", uint32(again))
	}
}
