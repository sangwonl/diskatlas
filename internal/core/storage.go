package core

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

type StorageCategory struct {
	ID    string `json:"id"`
	Bytes int64  `json:"bytes"`
	Files int    `json:"files"`
}
type Storage struct {
	Total        uint64            `json:"total"`
	Available    uint64            `json:"available"`
	TrashPending int64             `json:"trashPending"`
	Categories   []StorageCategory `json:"categories"`
	Skipped      int               `json:"skipped"`
	Complete     bool              `json:"complete"`
}

func StorageInfo() (Storage, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return Storage{}, err
	}
	return StorageInfoAt(home)
}

func StorageInfoAt(path string) (Storage, error) {
	if strings.TrimSpace(path) == "" {
		return StorageInfo()
	}
	return storageInfoAt(path)
}

func storageInfoAt(path string) (Storage, error) {
	total, available, err := diskCapacity(path)
	return Storage{Total: total, Available: available, TrashPending: trashPendingBytes(path), Categories: []StorageCategory{}}, err
}

// File categories describe readable home-directory files. Unmeasured system and
// shared-volume usage remains explicitly unclassified in the interface.
func ScanStorage(emit func(Storage)) (Storage, error) {
	result, err := StorageInfo()
	if err != nil {
		return result, err
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return result, err
	}
	totals := map[string]*StorageCategory{}
	seen := &sync.Map{}
	last := time.Now()
	snapshot := func() Storage {
		copy := result
		copy.Categories = []StorageCategory{}
		for _, id := range []string{"video", "photos", "audio", "documents", "archives", "development", "other"} {
			if c := totals[id]; c != nil {
				copy.Categories = append(copy.Categories, *c)
			}
		}
		return copy
	}
	err = filepath.WalkDir(home, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			result.Skipped++
			return nil
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return nil
		}
		if entry.IsDir() {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			result.Skipped++
			return nil
		}
		if !info.Mode().IsRegular() {
			return nil
		}
		if key := fileIdentity(info); key != "" {
			if _, loaded := seen.LoadOrStore(key, true); loaded {
				return nil
			}
		}
		id := FileCategory(path)
		c := totals[id]
		if c == nil {
			c = &StorageCategory{ID: id}
			totals[id] = c
		}
		c.Bytes += allocatedSize(info)
		c.Files++
		if emit != nil && time.Since(last) > 500*time.Millisecond {
			emit(snapshot())
			last = time.Now()
		}
		return nil
	})
	result.Complete = err == nil
	return snapshot(), err
}

func FileCategory(path string) string {
	p := strings.ToLower(filepath.ToSlash(path))
	for _, part := range []string{"/node_modules/", "/.git/", "/deriveddata/", "/go-build/", "/.cargo/", "/.venv/"} {
		if strings.Contains(p, part) {
			return "development"
		}
	}
	ext := strings.ToLower(filepath.Ext(path))
	for id, extensions := range map[string]string{
		"video":       ".mp4 .mov .mkv .avi .webm .m4v",
		"photos":      ".jpg .jpeg .png .heic .gif .webp .raw .tiff .psd",
		"audio":       ".mp3 .m4a .wav .flac .aiff .ogg",
		"documents":   ".pdf .doc .docx .xls .xlsx .ppt .pptx .txt .epub .pages .numbers",
		"archives":    ".zip .gz .tar .7z .rar .dmg .iso .pkg",
		"development": ".go .js .ts .tsx .jsx .py .rs .swift .java .c .cpp .h",
	} {
		for _, candidate := range strings.Fields(extensions) {
			if ext == candidate {
				return id
			}
		}
	}
	return "other"
}
