//go:build windows

package core

import (
	"encoding/binary"
	"fmt"
	"path/filepath"
	"strings"
	"unicode/utf16"
	"unsafe"

	"golang.org/x/sys/windows"
)

const (
	fsctlQueryUSNJournal = 0x000900f4
	fsctlReadUSNJournal  = 0x000900eb
)

func readJournal(root string, cursor journalCursor) (journalCursor, []journalChange, bool, error) {
	volume := filepath.VolumeName(root)
	if volume == "" {
		return cursor, nil, false, fmt.Errorf("cannot determine volume for %s", root)
	}
	journal, err := openUSNVolume(volume)
	if err != nil {
		return cursor, nil, false, err
	}
	defer windows.CloseHandle(journal)

	journalID, firstUSN, nextUSN, err := queryUSNJournal(journal)
	if err != nil {
		return cursor, nil, false, err
	}
	current := journalCursor{Kind: "usn", Volume: volume, JournalID: journalID, ID: uint64(nextUSN)}
	if cursor.ID == 0 {
		return current, nil, true, nil
	}
	if cursor.Kind != "usn" || cursor.Volume != volume || cursor.JournalID != journalID || int64(cursor.ID) < firstUSN || int64(cursor.ID) > nextUSN {
		return current, nil, false, fmt.Errorf("USN journal cursor is no longer valid")
	}

	changed, err := readUSNRecords(journal, journalID, int64(cursor.ID), nextUSN)
	if err != nil {
		return current, nil, false, err
	}
	return current, changed, true, nil
}

func openUSNVolume(volume string) (windows.Handle, error) {
	path, err := windows.UTF16PtrFromString(`\\.\` + volume)
	if err != nil {
		return 0, err
	}
	return windows.CreateFile(
		path,
		windows.GENERIC_READ,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE,
		nil,
		windows.OPEN_EXISTING,
		0,
		0,
	)
}

func queryUSNJournal(volume windows.Handle) (uint64, int64, int64, error) {
	output := make([]byte, 128)
	var returned uint32
	err := windows.DeviceIoControl(volume, fsctlQueryUSNJournal, nil, 0, &output[0], uint32(len(output)), &returned, nil)
	if err != nil {
		return 0, 0, 0, err
	}
	if returned < 40 {
		return 0, 0, 0, fmt.Errorf("USN journal response is truncated")
	}
	return binary.LittleEndian.Uint64(output[0:8]), int64(binary.LittleEndian.Uint64(output[8:16])), int64(binary.LittleEndian.Uint64(output[16:24])), nil
}

var openFileByID = windows.NewLazySystemDLL("kernel32.dll").NewProc("OpenFileById")

// Resolve the parent, rather than the changed file: deleted files are already
// gone, but their surviving parent still identifies the directory to reconcile.
func usnDirectoryPath(volume windows.Handle, id uint64) (string, error) {
	descriptor := struct {
		Size    uint32
		Type    uint32
		ID      uint64
		Padding uint64
	}{Size: 24, ID: id}
	handle, _, callErr := openFileByID.Call(
		uintptr(volume), uintptr(unsafe.Pointer(&descriptor)), 0,
		uintptr(windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE),
		0, uintptr(windows.FILE_FLAG_BACKUP_SEMANTICS),
	)
	if windows.Handle(handle) == windows.InvalidHandle {
		return "", callErr
	}
	defer windows.CloseHandle(windows.Handle(handle))
	buffer := make([]uint16, 32768)
	n, err := windows.GetFinalPathNameByHandle(windows.Handle(handle), &buffer[0], uint32(len(buffer)), 0)
	if err != nil {
		return "", err
	}
	if n == 0 || n >= uint32(len(buffer)) {
		return "", fmt.Errorf("USN parent path is unavailable")
	}
	path := windows.UTF16ToString(buffer[:n])
	if strings.HasPrefix(path, `\\?\UNC\`) {
		return `\\` + strings.TrimPrefix(path, `\\?\UNC\`), nil
	}
	return strings.TrimPrefix(path, `\\?\`), nil
}

func readUSNRecords(volume windows.Handle, journalID uint64, startUSN, endUSN int64) ([]journalChange, error) {
	input := make([]byte, 48)
	binary.LittleEndian.PutUint32(input[8:12], 0xFFFFFFFF)
	binary.LittleEndian.PutUint64(input[32:40], journalID)
	binary.LittleEndian.PutUint16(input[40:42], 2)
	binary.LittleEndian.PutUint16(input[42:44], 2)
	buffer := make([]byte, 1024*1024)
	changes := []journalChange{}
	parents := map[uint64]string{}
	for startUSN < endUSN {
		binary.LittleEndian.PutUint64(input[0:8], uint64(startUSN))
		var returned uint32
		err := windows.DeviceIoControl(volume, fsctlReadUSNJournal, &input[0], uint32(len(input)), &buffer[0], uint32(len(buffer)), &returned, nil)
		if err != nil {
			return nil, err
		}
		if returned < 8 {
			return nil, fmt.Errorf("USN response is truncated")
		}
		next := int64(binary.LittleEndian.Uint64(buffer[:8]))
		if next <= startUSN {
			return nil, fmt.Errorf("USN journal did not advance")
		}
		for offset := 8; offset < int(returned); {
			if offset+60 > int(returned) {
				return nil, fmt.Errorf("USN record is truncated")
			}
			recordLength := int(binary.LittleEndian.Uint32(buffer[offset : offset+4]))
			if recordLength < 60 || offset+recordLength > int(returned) {
				return nil, fmt.Errorf("invalid USN record length")
			}
			record := buffer[offset : offset+recordLength]
			if binary.LittleEndian.Uint16(record[4:6]) != 2 {
				return nil, fmt.Errorf("unsupported USN record version")
			}
			usn := int64(binary.LittleEndian.Uint64(record[24:32]))
			if usn >= endUSN {
				break
			}
			parentID := binary.LittleEndian.Uint64(record[16:24])
			parent, exists := parents[parentID]
			if !exists {
				parent, err = usnDirectoryPath(volume, parentID)
				if err != nil {
					return nil, err
				}
				parents[parentID] = parent
			}
			nameLength := int(binary.LittleEndian.Uint16(record[56:58]))
			nameOffset := int(binary.LittleEndian.Uint16(record[58:60]))
			if nameLength%2 != 0 || nameOffset < 60 || nameOffset+nameLength > len(record) {
				return nil, fmt.Errorf("invalid USN filename")
			}
			units := make([]uint16, nameLength/2)
			for i := range units {
				units[i] = binary.LittleEndian.Uint16(record[nameOffset+i*2 : nameOffset+i*2+2])
			}
			name := string(utf16.Decode(units))
			attributes := binary.LittleEndian.Uint32(record[52:56])
			reason := binary.LittleEndian.Uint32(record[40:44])
			changes = append(changes, journalChange{Path: filepath.Join(parent, name), Recursive: attributes&windows.FILE_ATTRIBUTE_DIRECTORY != 0 && reason&0x3300 != 0})
			offset += recordLength
		}
		startUSN = next
	}
	return changes, nil
}
