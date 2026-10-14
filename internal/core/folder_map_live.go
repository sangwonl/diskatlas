package core

// Completed directory measurements are available to readers during a scan,
// without changing the persistent index or its journal checkpoint prematurely.
func folderMapScanActive(root string) bool {
	store := folderMapIndexStoreForRoot(root)
	store.mu.RLock()
	defer store.mu.RUnlock()
	return store.live != nil
}

func loadVisibleFolderMapAggregates(root string, paths []string) (map[string]folderMapAggregate, bool) {
	aggregates := loadFolderMapAggregates(root, paths)
	store := folderMapIndexStoreForRoot(root)
	store.mu.RLock()
	defer store.mu.RUnlock()
	for _, path := range paths {
		if aggregate, exists := store.live[path]; exists {
			aggregates[path] = aggregate
		}
	}
	return aggregates, store.live != nil
}

func publishFolderMapAggregate(root, path string, aggregate folderMapAggregate) {
	store := folderMapIndexStoreForRoot(root)
	store.mu.Lock()
	defer store.mu.Unlock()
	if store.live == nil {
		return
	}
	var previous, rootBaseline folderMapAggregate
	var exists bool
	if store.index != nil {
		previous, exists = store.index.Directories[path]
		rootBaseline = store.index.Directories[root]
	}
	baselineAt := store.scanBaselineAt
	if baselineAt.IsZero() {
		baselineAt = rootBaseline.BaselineAt
	}
	if !baselineAt.IsZero() {
		rootScan := store.scanRoot || rootBaseline.DeltaSince.IsZero()
		if bytes, ok := folderMapRootCheckpointBytes(root, path, previous, exists, baselineAt, rootScan); ok {
			aggregate.BaselineBytes = bytes
			aggregate.BaselineAt = baselineAt
			aggregate.BaselineKnown = true
			aggregate.BaselineComplete = true
		}
	}

	if aggregate.Known && aggregate.Complete && aggregate.BaselineKnown && aggregate.BaselineComplete {
		aggregate.DeltaKnown = true
		aggregate.DeltaBytes = aggregate.Bytes - aggregate.BaselineBytes
		aggregate.DeltaSince = aggregate.BaselineAt
	}
	store.live[path] = aggregate
}
