package core

import (
	"context"
	"os"
	"path/filepath"
	"time"
)

// A trash move changes current measurements, not the root comparison checkpoint.
// Re-read only the immediate parent and propagate its size change to ancestors.
func recordFolderMapRemoval(root, target string) error {
	store := folderMapIndexStoreForRoot(root)
	store.refreshMu.Lock()
	defer store.refreshMu.Unlock()
	loadFolderMapAggregates(root, []string{root})
	store.mu.RLock()
	if store.index == nil {
		store.mu.RUnlock()
		return nil
	}
	original := *store.index
	original.Directories = make(map[string]folderMapAggregate, len(store.index.Directories))
	for path, aggregate := range store.index.Directories {
		original.Directories[path] = aggregate
	}
	store.mu.RUnlock()
	parent := filepath.Dir(target)
	previous, exists := original.Directories[parent]
	if !exists || !previous.Known {
		return nil
	}
	snapshot, err := readFolderMapLevel(root, parent)
	if err != nil {
		return err
	}
	applyFolderMapAggregates(snapshot)
	baseline, _ := loadStoredFolderMapSnapshot(root, parent)
	snapshot.DeltaKnown = false
	snapshot.DeltaBytes = 0
	snapshot.DeltaSince = ""
	applyFolderMapDeltas(snapshot, baseline, false)
	snapshot.DeltaGrowthBytes, snapshot.DeltaShrinkBytes, snapshot.DeltaActivityKnown = folderMapDeltaActivity(snapshot.Children, baseline)
	info, err := os.Lstat(parent)
	if err != nil {
		return err
	}
	now := time.Now().UTC()
	checkpoint := original.Directories[root].BaselineAt
	comparison := original.Directories[root].DeltaSince
	current := previous
	current.Bytes = snapshot.Bytes
	current.Files = snapshot.Files
	current.Known = snapshot.SizeKnown
	current.Complete = snapshot.SizeComplete
	current.SizeStale = false
	current.DeltaBytes = snapshot.DeltaBytes
	current.DeltaKnown = snapshot.DeltaKnown
	current.DeltaSince = comparison
	current.DeltaGrowthBytes = snapshot.DeltaGrowthBytes
	current.DeltaShrinkBytes = snapshot.DeltaShrinkBytes
	current.DeltaActivityKnown = snapshot.DeltaActivityKnown
	current.ModifiedAt = snapshot.ModifiedAt
	current.StatModTime = info.ModTime().UnixNano()
	current.StatSize = info.Size()
	current.ScannedAt = now
	snapshot.GeneratedAt = now.Format(time.RFC3339)
	bytesChanged := current.Bytes - previous.Bytes
	filesChanged := current.Files - previous.Files
	store.mu.Lock()
	index := store.index
	for path := range index.Directories {
		if isWithin(target, path) {
			delete(index.Directories, path)
		}
	}
	index.Directories[parent] = current
	for path := filepath.Dir(parent); parent != root && isWithin(root, path); path = filepath.Dir(path) {
		if aggregate, ok := index.Directories[path]; ok {
			aggregate.Bytes += bytesChanged
			aggregate.Files += filesChanged
			aggregate.ScannedAt = now
			if aggregate.DeltaKnown && current.Complete && previous.Complete {
				aggregate.DeltaBytes += bytesChanged
			} else {
				aggregate.DeltaKnown = false
				aggregate.DeltaBytes = 0
			}
			aggregate.DeltaSince = comparison
			if aggregate.DeltaActivityKnown && previous.DeltaActivityKnown && current.DeltaActivityKnown {
				aggregate.DeltaGrowthBytes = max(0, aggregate.DeltaGrowthBytes+current.DeltaGrowthBytes-previous.DeltaGrowthBytes)
				aggregate.DeltaShrinkBytes = max(0, aggregate.DeltaShrinkBytes+current.DeltaShrinkBytes-previous.DeltaShrinkBytes)
			} else {
				aggregate.DeltaActivityKnown = false
				aggregate.DeltaGrowthBytes = 0
				aggregate.DeltaShrinkBytes = 0
			}
			index.Directories[path] = aggregate
		}
		if path == root || filepath.Dir(path) == path {
			break
		}
	}
	store.mu.Unlock()
	batch, err := newFolderMapSnapshotBatch(false)
	if err == nil {
		defer batch.cleanup()
		batch.previous = &original
		err = batch.stage(snapshot)
		if err == nil {
			err = batch.commitScan(context.Background(), root, index, checkpoint, comparison)
		}
	}
	if err != nil {
		store.mu.Lock()
		store.index = &original
		store.mu.Unlock()
	}
	return err
}
