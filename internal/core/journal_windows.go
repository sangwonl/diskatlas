//go:build windows

package core

import (
	"encoding/binary"
	"fmt"
	"path/filepath"

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
	if cursor.Kind != "usn" || cursor.Volume != volume || cursor.JournalID != journalID || int64(cursor.ID) < firstUSN {
		return current, nil, false, fmt.Errorf("USN journal cursor is no longer valid")
	}

	changed, lastUSN, err := readUSNRecords(journal, journalID, int64(cursor.ID))
	if err != nil {
		return current, nil, false, err
	}
	if lastUSN > 0 {
		current.ID = uint64(lastUSN + 1)
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

func readUSNRecords(volume windows.Handle, journalID uint64, startUSN int64) ([]journalChange, int64, error) {
	input := make([]byte, 48)
	binary.LittleEndian.PutUint64(input[0:8], uint64(startUSN))
	binary.LittleEndian.PutUint32(input[8:12], 0xFFFFFFFF) // all reasons
	binary.LittleEndian.PutUint32(input[12:16], 0)
	binary.LittleEndian.PutUint64(input[16:24], 0)
	binary.LittleEndian.PutUint64(input[24:32], 0)
	binary.LittleEndian.PutUint64(input[32:40], journalID)
	binary.LittleEndian.PutUint16(input[40:42], 2)
	binary.LittleEndian.PutUint16(input[42:44], 2)
	buffer := make([]byte, 1024*1024)
	var returned uint32
	err := windows.DeviceIoControl(volume, fsctlReadUSNJournal, &input[0], uint32(len(input)), &buffer[0], uint32(len(buffer)), &returned, nil)
	if err != nil {
		if err == windows.ERROR_HANDLE_EOF {
			return nil, 0, nil
		}
		return nil, 0, err
	}
	if returned < 8 {
		return nil, 0, nil
	}

	changes := []journalChange{}
	var lastUSN int64
	offset := 8 // first four bytes are the next record offset marker
	for offset+60 <= int(returned) {
		recordLength := int(binary.LittleEndian.Uint32(buffer[offset : offset+4]))
		if recordLength < 60 || offset+recordLength > int(returned) {
			break
		}
		usn := int64(binary.LittleEndian.Uint64(buffer[offset+24 : offset+32]))
		if usn > lastUSN {
			lastUSN = usn
		}
		changes = append(changes, journalChange{})
		offset += recordLength
	}
	return changes, lastUSN, nil
}
