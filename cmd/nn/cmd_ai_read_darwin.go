package main

import (
	"fmt"
	"syscall"
)

func filterFileReady(fd int) (bool, error) {
	var set syscall.FdSet
	if fd < 0 || fd >= len(set.Bits)*32 {
		return false, fmt.Errorf("stdin descriptor %d exceeds supported select range", fd)
	}
	set.Bits[fd/32] |= 1 << uint(fd%32)
	timeout := syscall.Timeval{Usec: 50000}
	if err := syscall.Select(fd+1, &set, nil, nil, &timeout); err != nil {
		return false, err
	}
	return set.Bits[fd/32]&(1<<uint(fd%32)) != 0, nil
}
