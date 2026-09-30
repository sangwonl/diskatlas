//go:build windows

package core

import "golang.org/x/sys/windows"

func diskCapacity(path string) (uint64, uint64, error) {
	p, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return 0, 0, err
	}
	var available, total, free uint64
	err = windows.GetDiskFreeSpaceEx(p, &available, &total, &free)
	return total, available, err
}

func diskFree(path string) (uint64, error) {
	_, available, err := diskCapacity(path)
	return available, err
}
