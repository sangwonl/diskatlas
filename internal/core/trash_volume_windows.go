//go:build windows

package core

import (
	"os"
	"strings"

	"golang.org/x/sys/windows"
)

func trashVolumeID(path string) string {
	input, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return ""
	}
	buffer := make([]uint16, 32768)
	if err := windows.GetVolumePathName(input, &buffer[0], uint32(len(buffer))); err != nil {
		return ""
	}
	return strings.ToUpper(windows.UTF16ToString(buffer))
}
