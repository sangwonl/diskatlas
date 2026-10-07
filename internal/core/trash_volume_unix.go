//go:build !windows

package core

import (
	"fmt"
	"os"
	"syscall"
)

func trashVolumeID(path string) string {
	info, err := os.Stat(path)
	if err != nil {
		return ""
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return ""
	}
	return fmt.Sprintf("%d", stat.Dev)
}
