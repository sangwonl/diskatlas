//go:build !darwin && !windows

package core

import (
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"time"
)

func platformMoveToTrash(path string) (string, error) {
	filesDir, infoDir, err := trashDirectories(path)
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(filesDir, 0o700); err != nil {
		return "", err
	}
	if err := os.MkdirAll(infoDir, 0o700); err != nil {
		return "", err
	}
	name := filepath.Base(path)
	base, err := uniqueTrashName(filesDir, infoDir, name)
	if err != nil {
		return "", err
	}
	trashedPath := filepath.Join(filesDir, base)
	infoPath := filepath.Join(infoDir, base+".trashinfo")
	info := "[Trash Info]\nPath=" + (&url.URL{Path: path}).EscapedPath() + "\nDeletionDate=" + time.Now().Format("2006-01-02T15:04:05") + "\n"
	if err := os.WriteFile(infoPath, []byte(info), 0o600); err != nil {
		return "", err
	}
	if err := os.Rename(path, trashedPath); err != nil {
		_ = os.Remove(infoPath)
		return "", err
	}
	return trashedPath, nil
}

func uniqueTrashName(filesDir, infoDir, name string) (string, error) {
	for suffix := 0; ; suffix++ {
		candidate := name
		if suffix > 0 {
			candidate = fmt.Sprintf("%s.%d", name, suffix)
		}
		_, fileErr := os.Lstat(filepath.Join(filesDir, candidate))
		_, infoErr := os.Lstat(filepath.Join(infoDir, candidate+".trashinfo"))
		if errors.Is(fileErr, os.ErrNotExist) && errors.Is(infoErr, os.ErrNotExist) {
			return candidate, nil
		}
		if fileErr != nil && !errors.Is(fileErr, os.ErrNotExist) {
			return "", fileErr
		}
		if infoErr != nil && !errors.Is(infoErr, os.ErrNotExist) {
			return "", infoErr
		}
	}
}

func trashDirectories(path string) (string, string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", "", err
	}
	dataHome := os.Getenv("XDG_DATA_HOME")
	if dataHome == "" || !filepath.IsAbs(dataHome) {
		dataHome = filepath.Join(home, ".local", "share")
	}
	homeTrash := filepath.Join(dataHome, "Trash")
	if trashVolumeID(home) == trashVolumeID(path) {
		return filepath.Join(homeTrash, "files"), filepath.Join(homeTrash, "info"), nil
	}
	volumeRoot := path
	for parent := filepath.Dir(volumeRoot); parent != volumeRoot; parent = filepath.Dir(volumeRoot) {
		if trashVolumeID(parent) != trashVolumeID(path) {
			break
		}
		volumeRoot = parent
	}
	localTrash := filepath.Join(volumeRoot, fmt.Sprintf(".Trash-%d", os.Getuid()))
	return filepath.Join(localTrash, "files"), filepath.Join(localTrash, "info"), nil
}
