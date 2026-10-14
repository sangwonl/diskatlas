package core

import (
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"time"
)

// A refresh reads the persistent journal before scanning. Its cursor is saved
// only with the successful measurement, so changes during a scan are replayed
// on the next refresh. Comparison baselines are independent of this cursor.
type folderMapRefreshPlan struct {
	rootScan   bool
	baselineAt time.Time
	aggregates map[string]folderMapAggregate
	children   map[string][]string
	dirty      map[string]bool
	recursive  map[string]bool
	rescanned  sync.Map
}

func readFolderMapJournal(root string, cursor journalCursor) (journalCursor, []journalChange, bool, error) {
	if runtime.GOOS != "darwin" && runtime.GOOS != "windows" {
		return cursor, nil, false, nil
	}
	return readJournal(root, cursor)
}

type folderMapJournalReader func(string, journalCursor) (journalCursor, []journalChange, bool, error)

func prepareFolderMapRefresh(root, target string, read folderMapJournalReader) (*folderMapRefreshPlan, journalCursor, string) {
	info, err := os.Lstat(target)
	if err != nil {
		return nil, journalCursor{}, ""
	}
	device := filesystemIdentity(target, info)
	start, _, available, err := read(target, journalCursor{})
	if err != nil || !available || (start.Kind != "fsevents" && start.Kind != "usn") {
		return nil, journalCursor{}, device
	}
	store := folderMapIndexStoreForRoot(root)
	store.mu.Lock()
	if !store.loaded {
		store.index = readFolderMapAggregateIndex(root)
		store.loaded = true
	}
	plan := &folderMapRefreshPlan{
		aggregates: map[string]folderMapAggregate{}, children: map[string][]string{},
		dirty: map[string]bool{}, recursive: map[string]bool{},
	}
	if store.index != nil {
		for path, aggregate := range store.index.Directories {
			if isWithin(target, path) {
				plan.aggregates[path] = aggregate
				if path != target {
					parent := filepath.Dir(path)
					plan.children[parent] = append(plan.children[parent], path)
				}
			}
		}
	}
	store.mu.Unlock()
	previous, exists := plan.aggregates[target]
	if !exists || !previous.Known || previous.Journal.ID == 0 || device == "" || previous.JournalDevice != device {
		return nil, start, device
	}
	_, changes, complete, err := read(target, previous.Journal)
	if err != nil || !complete {
		return nil, start, device
	}
	for _, change := range changes {
		if change.Path == "" {
			// A journal record without a resolvable path cannot justify reuse.
			return nil, start, device
		}
		path := filepath.Clean(change.Path)
		if !isWithin(target, path) {
			if isWithin(path, target) {
				return nil, start, device
			}
			continue
		}
		if change.Recursive {
			plan.recursive[path] = true
		}
		for current := path; isWithin(target, current); current = filepath.Dir(current) {
			plan.dirty[current] = true
			if current == target {
				break
			}
		}
	}
	return plan, start, device
}

func (plan *folderMapRefreshPlan) reuse(path string, info os.FileInfo) (measuredFolder, bool) {
	if plan == nil || info == nil || !info.IsDir() || plan.dirty[path] {
		return measuredFolder{}, false
	}
	for ancestor := path; ; ancestor = filepath.Dir(ancestor) {
		if plan.recursive[ancestor] {
			return measuredFolder{}, false
		}
		if filepath.Dir(ancestor) == ancestor {
			break
		}
	}
	aggregate, exists := plan.aggregates[path]
	if !exists || !aggregate.Known || !aggregate.Complete || aggregate.SizeStale || aggregate.StatModTime != info.ModTime().UnixNano() || aggregate.StatSize != info.Size() {
		return measuredFolder{}, false
	}
	result := measuredFolder{
		Bytes: aggregate.Bytes, Files: aggregate.Files, ModifiedAt: aggregate.ModifiedAt,
		Known: true, Complete: true, DeltaBytes: aggregate.DeltaBytes,
		DeltaKnown: aggregate.DeltaKnown, DeltaGrowthBytes: aggregate.DeltaGrowthBytes,
		DeltaShrinkBytes: aggregate.DeltaShrinkBytes, DeltaActivityKnown: aggregate.DeltaActivityKnown,
		Aggregates: map[string]folderMapAggregate{},
	}
	if !aggregate.DeltaSince.IsZero() {
		result.DeltaSince = aggregate.DeltaSince.UTC().Format(time.RFC3339)
	}
	pending := []string{path}
	for len(pending) > 0 {
		last := len(pending) - 1
		current := pending[last]
		pending = pending[:last]
		value := plan.aggregates[current]
		if plan.rootScan {
			value.DeltaBytes = 0
			value.DeltaKnown = true
			value.DeltaSince = plan.baselineAt
			value.DeltaGrowthBytes = 0
			value.DeltaShrinkBytes = 0
			value.DeltaActivityKnown = true
		}
		result.Aggregates[current] = value
		pending = append(pending, plan.children[current]...)
	}
	if plan.rootScan {
		result.DeltaBytes = 0
		result.DeltaKnown = true
		result.DeltaSince = plan.baselineAt.UTC().Format(time.RFC3339)
		result.DeltaGrowthBytes = 0
		result.DeltaShrinkBytes = 0
		result.DeltaActivityKnown = true
	}
	return result, true
}
