//go:build windows

package core

import (
	"encoding/binary"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"
	"unicode/utf16"
	"unsafe"

	"golang.org/x/sys/windows"
)

const (
	foDelete          = 3
	fofSilent         = 0x0004
	fofNoConfirmation = 0x0010
	fofAllowUndo      = 0x0040
	fofNoErrorUI      = 0x0400
)

type shellFileOperation struct {
	window            uintptr
	operation         uint32
	from              *uint16
	to                *uint16
	flags             uint16
	operationsAborted int32
	nameMappings      uintptr
	progressTitle     *uint16
}

var shell32 = windows.NewLazySystemDLL("shell32.dll")
var procSHFileOperationW = shell32.NewProc("SHFileOperationW")

func platformMoveToTrash(path string) (string, error) {
	recycleDir, err := recycleDirectory(path)
	if err != nil {
		return "", err
	}
	before := map[string]bool{}
	if entries, err := filepath.Glob(filepath.Join(recycleDir, "$I*")); err == nil {
		for _, entry := range entries {
			before[strings.ToLower(filepath.Base(entry))] = true
		}
	}
	from, err := windows.UTF16FromString(path)
	if err != nil {
		return "", err
	}
	from = append(from, 0)
	operation := shellFileOperation{
		operation: foDelete,
		from:      &from[0],
		flags:     fofSilent | fofNoConfirmation | fofAllowUndo | fofNoErrorUI,
	}
	result, _, _ := procSHFileOperationW.Call(uintptr(unsafe.Pointer(&operation)))
	if result != 0 {
		return "", syscall.Errno(result)
	}
	if operation.operationsAborted != 0 {
		return "", errors.New("Windows에서 휴지통 이동이 취소되었습니다")
	}
	trashedPath, err := findRecycledPath(recycleDir, path, before)
	if err != nil {
		return "", nil
	}
	return trashedPath, nil
}

func recycleDirectory(path string) (string, error) {
	volume, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return "", err
	}
	buffer := make([]uint16, 32768)
	if err := windows.GetVolumePathName(volume, &buffer[0], uint32(len(buffer))); err != nil {
		return "", err
	}
	volumePath := windows.UTF16ToString(buffer)
	if strings.HasPrefix(volumePath, `\\`) {
		return "", errors.New("네트워크 경로는 Windows 휴지통을 지원하지 않습니다")
	}
	recycleRoot := filepath.Join(volumePath, "$Recycle.Bin")
	if info, err := os.Stat(recycleRoot); err != nil || !info.IsDir() {
		return "", errors.New("이 볼륨에서 Windows 휴지통을 사용할 수 없습니다")
	}
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		return "", err
	}
	sid := user.User.Sid.String()
	return filepath.Join(recycleRoot, sid), nil
}

func findRecycledPath(recycleDir, original string, before map[string]bool) (string, error) {
	entries, err := filepath.Glob(filepath.Join(recycleDir, "$I*"))
	if err != nil {
		return "", err
	}
	var selected string
	var selectedTime time.Time
	for _, entry := range entries {
		if before[strings.ToLower(filepath.Base(entry))] {
			continue
		}
		data, err := os.ReadFile(entry)
		if err != nil || len(data) < 24 {
			continue
		}
		version := binary.LittleEndian.Uint64(data[:8])
		if version != 1 && version != 2 {
			continue
		}
		pathUnits := make([]uint16, 0, (len(data)-24)/2)
		for offset := 24; offset+1 < len(data); offset += 2 {
			value := binary.LittleEndian.Uint16(data[offset : offset+2])
			if value == 0 {
				break
			}
			pathUnits = append(pathUnits, value)
		}
		recycledOriginal := string(utf16.Decode(pathUnits))
		if !strings.EqualFold(filepath.Clean(recycledOriginal), filepath.Clean(original)) {
			continue
		}
		info, err := os.Stat(entry)
		if err != nil || !info.ModTime().After(selectedTime) {
			continue
		}
		selected = filepath.Join(recycleDir, "$R"+strings.TrimPrefix(filepath.Base(entry), "$I"))
		selectedTime = info.ModTime()
	}
	if selected == "" {
		return "", fmt.Errorf("휴지통에 이동된 항목을 찾을 수 없습니다")
	}
	return selected, nil
}
