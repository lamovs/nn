package main

import (
	"fmt"
	"syscall"
)

func filterFileReady(fd int) (bool, error) {
	var set syscall.FdSet
	if fd < 0 || fd >= len(set.Bits)*64 {
		return false, fmt.Errorf("stdin descriptor %d exceeds supported select range", fd)
	}
	set.Bits[fd/64] |= 1 << uint(fd%64)
	timeout := syscall.Timeval{Usec: 50000}
	_, err := syscall.Select(fd+1, &set, nil, nil, &timeout)
	if err != nil {
		return false, err
	}
	return set.Bits[fd/64]&(1<<uint(fd%64)) != 0, nil
}
