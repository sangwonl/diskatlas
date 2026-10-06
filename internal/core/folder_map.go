package core

import (
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/gob"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// FolderMap describes one directory level and the measured sizes of its
// immediate children. Deeper levels are only listed when the user opens them.
type FolderMap struct {
	Root        string `json:"root"`
	Path        string `json:"path"`
	Name        string `json:"name"`
	Bytes       int64  `json:"bytes"`
	Files       int    `json:"files"`
	Directories int    `json:"directories"`
	ModifiedAt  string `json:"modifiedAt,omitempty"`
	GeneratedAt string `json:"generatedAt,omitempty"`
	Measured    bool   `json:"measured"`
	// SizeKnown means Bytes is a usable lower bound; SizeComplete means the
	// walk read every path and did not skip a separate-volume subtree.
	SizeKnown    bool             `json:"sizeKnown"`
	SizeComplete bool             `json:"sizeComplete"`
	Children     []FolderMapEntry `json:"children"`
}

type FolderMapEntry struct {
	Name       string `json:"name"`
	Path       string `json:"path"`
	Bytes      int64  `json:"bytes"`
	Files      int    `json:"files"`
	ModifiedAt string `json:"modifiedAt,omitempty"`
	Directory  bool   `json:"directory"`
	// Incomplete directories retain their readable byte total as a lower bound.
	SizeKnown    bool  `json:"sizeKnown"`
	SizeComplete bool  `json:"sizeComplete"`
	SizeStale    bool  `json:"sizeStale"`
	StatModTime  int64 `json:"statModTime"`
	StatSize     int64 `json:"statSize"`
}

const folderMapIndexVersion = 1

type folderMapAggregate struct {
	Bytes       int64
	Files       int
	ModifiedAt  string
	Known       bool
	Complete    bool
	StatModTime int64
	StatSize    int64
	ScannedAt   time.Time
}

type folderMapAggregateIndex struct {
	Version     int
	Root        string
	Directories map[string]folderMapAggregate
}

type folderMapIndexStore struct {
	mu     sync.RWMutex
	loaded bool
	index  *folderMapAggregateIndex
}

var folderMapIndexStores sync.Map

// ReadFolderMap reads only the immediate entries in target. It does not walk
// into child directories, so opening DiskAtlas is fast even on a large volume.
func ReadFolderMap(root, target string) (*FolderMap, error) {
	normalizedRoot, normalizedTarget, err := normalizeFolderMapPaths(root, target)
	if err != nil {
		return nil, err
	}
	if cached, ok := loadFolderMapSnapshot(normalizedRoot, normalizedTarget); ok {
		return cached, nil
	}
	result, err := readFolderMapLevel(normalizedRoot, normalizedTarget)
	if err != nil {
		return nil, err
	}
	applyFolderMapAggregates(result)
	return result, nil
}

// ReloadFolderMap rereads only the current directory level and reuses cached
// measurements for its child directories. It never descends into the tree.
func ReloadFolderMap(root, target string) (*FolderMap, error) {
	normalizedRoot, normalizedTarget, err := normalizeFolderMapPaths(root, target)
	if err != nil {
		return nil, err
	}
	result, err := readFolderMapLevel(normalizedRoot, normalizedTarget)
	if err != nil {
		return nil, err
	}
	applyFolderMapAggregates(result)
	if result.GeneratedAt != "" {
		_ = saveFolderMapSnapshot(result)
	}
	return result, nil
}

// MeasureFolderMap reads one level and measures each immediate subdirectory
// concurrently. The walk also persists a compact size aggregate for every
// visited directory so opening deeper levels reuses the same traversal.
func MeasureFolderMap(root, target string, force bool, onProgress func(Progress)) (*FolderMap, error) {
	return MeasureFolderMapContext(context.Background(), root, target, force, onProgress)
}

// MeasureFolderMapContext measures one folder and its immediate subdirectories,
// stopping promptly when the caller cancels the request.
func MeasureFolderMapContext(ctx context.Context, root, target string, force bool, onProgress func(Progress)) (*FolderMap, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	normalizedRoot, normalizedTarget, err := normalizeFolderMapPaths(root, target)
	if err != nil {
		return nil, err
	}
	var result *FolderMap
	if !force {
		if cached, ok := loadFolderMapSnapshot(normalizedRoot, normalizedTarget); ok {
			return cached, nil
		}
		result, err = readFolderMapLevel(normalizedRoot, normalizedTarget)
		if err != nil {
			return nil, err
		}
		applyFolderMapAggregates(result)
		targetAggregate, indexed := loadFolderMapAggregates(normalizedRoot, []string{normalizedTarget})[normalizedTarget]
		if result.GeneratedAt != "" && (!indexed || targetAggregate.Known) {
			if result.SizeComplete {
				_ = saveFolderMapSnapshot(result)
			}
			return result, nil
		}
	}
	if result == nil {
		result, err = readFolderMapLevel(normalizedRoot, normalizedTarget)
		if err != nil {
			return nil, err
		}
	}
	var scanned atomic.Int64
	var reportMu sync.Mutex
	lastReport := time.Time{}
	report := func(path string, force bool) {
		if onProgress == nil {
			return
		}
		reportMu.Lock()
		defer reportMu.Unlock()
		if !force && time.Since(lastReport) < 150*time.Millisecond {
			return
		}
		lastReport = time.Now()
		onProgress(Progress{Phase: "folder-map", FilesScanned: scanned.Load(), Path: path})
	}
	jobs := make(chan int)
	workers := min(runtime.GOMAXPROCS(0), 6)
	if workers < 1 {
		workers = 1
	}
	var workersDone sync.WaitGroup
	var resultsMu sync.Mutex
	allMeasured := true
	measuredSubtrees := make([]measuredFolder, len(result.Children))
	jobCount := 0
	for _, entry := range result.Children {
		if entry.Directory {
			jobCount++
		}
	}
	if jobCount > 0 {
		report(normalizedTarget, true)
		workersDone.Add(workers)
		for worker := 0; worker < workers; worker++ {
			go func() {
				defer workersDone.Done()
				for {
					select {
					case <-ctx.Done():
						return
					case index, ok := <-jobs:
						if !ok {
							return
						}
						entry := result.Children[index]
						measured, measureErr := measureFolderEntry(ctx, entry.Path, func(path string) {
							if scanned.Add(1)%128 == 0 {
								report(path, false)
							}
						})
						if measureErr != nil {
							return
						}
						measuredSubtrees[index] = measured
						entry.Bytes = measured.Bytes
						entry.Files = measured.Files
						if measured.ModifiedAt != "" {
							entry.ModifiedAt = measured.ModifiedAt
						}
						entry.SizeKnown = measured.Known
						entry.SizeComplete = measured.Complete
						result.Children[index] = entry
						if !measured.Complete {
							resultsMu.Lock()
							allMeasured = false
							resultsMu.Unlock()
						}
					}
				}
			}()
		}
	dispatch:
		for index, entry := range result.Children {
			if entry.Directory {
				select {
				case jobs <- index:
				case <-ctx.Done():
					break dispatch
				}
			}
		}
		close(jobs)
		workersDone.Wait()
	} else {
		close(jobs)
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	result.Bytes = 0
	result.Files = 0
	result.ModifiedAt = ""
	for _, entry := range result.Children {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if entry.Directory && !entry.SizeKnown {
			allMeasured = false
		}
		result.Bytes += entry.Bytes
		result.Files += entry.Files
		if entry.ModifiedAt != "" && (result.ModifiedAt == "" || entry.ModifiedAt > result.ModifiedAt) {
			result.ModifiedAt = entry.ModifiedAt
		}
	}
	result.Measured = allMeasured
	result.SizeKnown = true
	result.SizeComplete = allMeasured
	result.GeneratedAt = time.Now().UTC().Format(time.RFC3339)
	rootInfo, rootErr := os.Lstat(normalizedTarget)
	if rootErr == nil {
		rootAggregate := folderMapAggregate{
			Bytes: result.Bytes, Files: result.Files, ModifiedAt: result.ModifiedAt,
			Known: true, Complete: result.SizeComplete,
			StatModTime: rootInfo.ModTime().UnixNano(), StatSize: rootInfo.Size(), ScannedAt: time.Now(),
		}
		aggregates := map[string]folderMapAggregate{normalizedTarget: rootAggregate}
		for _, measured := range measuredSubtrees {
			for path, aggregate := range measured.Aggregates {
				aggregates[path] = aggregate
			}
		}
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		storeFolderMapAggregates(normalizedRoot, normalizedTarget, aggregates)
	}
	if jobCount > 0 {
		report(normalizedTarget, true)
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	_ = saveFolderMapSnapshot(result)
	return result, nil
}

func normalizeFolderMapPaths(root, target string) (string, string, error) {
	normalizedRoot, err := analysisRoot(root)
	if err != nil {
		return "", "", err
	}
	normalizedTarget := normalizedRoot
	if strings.TrimSpace(target) != "" {
		normalizedTarget, err = filepath.Abs(target)
		if err != nil {
			return "", "", err
		}
		normalizedTarget = filepath.Clean(normalizedTarget)
		if resolved, resolveErr := filepath.EvalSymlinks(normalizedTarget); resolveErr == nil {
			normalizedTarget = resolved
		}
	}
	if !isWithin(normalizedRoot, normalizedTarget) {
		return "", "", errors.New("분석 범위 밖의 경로입니다")
	}
	info, err := os.Stat(normalizedTarget)
	if err != nil {
		return "", "", err
	}
	if !info.IsDir() {
		return "", "", errors.New("폴더가 아닙니다")
	}
	return normalizedRoot, normalizedTarget, nil
}

func readFolderMapLevel(root, target string) (*FolderMap, error) {
	info, err := os.Stat(target)
	if err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(target)
	if err != nil {
		return nil, err
	}
	result := &FolderMap{
		Root: root, Path: target, Name: info.Name(),
		ModifiedAt:   formatFolderMapTime(info.ModTime().UnixNano()),
		SizeKnown:    true,
		SizeComplete: true,
		Children:     make([]FolderMapEntry, 0, len(entries)),
	}
	if target == string(filepath.Separator) {
		result.Name = target
	}
	for _, entry := range entries {
		childPath := filepath.Join(target, entry.Name())
		childInfo, statErr := os.Lstat(childPath)
		if statErr != nil {
			result.SizeComplete = false
			continue
		}
		child := FolderMapEntry{
			Name: entry.Name(), Path: childPath,
			Directory:   childInfo.IsDir(),
			ModifiedAt:  formatFolderMapTime(childInfo.ModTime().UnixNano()),
			StatModTime: childInfo.ModTime().UnixNano(),
			StatSize:    childInfo.Size(),
		}
		if child.Directory {
			result.Directories++
			result.SizeComplete = false
		} else {
			child.Bytes = allocatedSize(childInfo)
			child.Files = 1
			child.SizeKnown = true
			child.SizeComplete = true
			result.Bytes += child.Bytes
			result.Files++
		}
		result.Children = append(result.Children, child)
	}
	sortFolderMapEntries(result.Children)
	return result, nil
}

type measuredFolder struct {
	Bytes      int64
	Files      int
	ModifiedAt string
	Known      bool
	Complete   bool
	Aggregates map[string]folderMapAggregate
}

func measureFolderEntry(ctx context.Context, root string, onEntry func(string)) (measuredFolder, error) {
	if err := ctx.Err(); err != nil {
		return measuredFolder{}, err
	}
	rootInfo, rootErr := os.Stat(root)
	if rootErr != nil {
		return measuredFolder{Aggregates: map[string]folderMapAggregate{
			root: {Known: false, Complete: false, ScannedAt: time.Now()},
		}}, nil
	}
	rootDevice := filesystemIdentity(root, rootInfo)
	return measureFolderTree(ctx, root, rootDevice, onEntry)
}

func measureFolderTree(ctx context.Context, path string, rootDevice string, onEntry func(string)) (measuredFolder, error) {
	if err := ctx.Err(); err != nil {
		return measuredFolder{}, err
	}
	now := time.Now()
	result := measuredFolder{Known: true, Complete: true, Aggregates: make(map[string]folderMapAggregate)}
	info, err := os.Lstat(path)
	if err != nil {
		result.Known = false
		result.Complete = false
		result.Aggregates[path] = folderMapAggregate{Known: false, Complete: false, ScannedAt: now}
		return result, nil
	}
	entries, err := os.ReadDir(path)
	if err != nil {
		result.Known = false
		result.Complete = false
		result.Aggregates[path] = folderMapAggregate{
			Known: false, Complete: false, StatModTime: info.ModTime().UnixNano(), StatSize: info.Size(), ScannedAt: now,
		}
		return result, nil
	}
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return measuredFolder{}, err
		}
		childPath := filepath.Join(path, entry.Name())
		childInfo, statErr := os.Lstat(childPath)
		if statErr != nil {
			result.Complete = false
			continue
		}
		if childInfo.IsDir() {
			if isDataVolumeAlias(childPath) || isOtherFilesystem(rootDevice, childPath, childInfo) {
				result.Complete = false
				result.Aggregates[childPath] = folderMapAggregate{
					Known: false, Complete: false, StatModTime: childInfo.ModTime().UnixNano(), StatSize: childInfo.Size(), ScannedAt: now,
				}
				continue
			}
			child, childErr := measureFolderTree(ctx, childPath, rootDevice, onEntry)
			if childErr != nil {
				return measuredFolder{}, childErr
			}
			result.Bytes += child.Bytes
			result.Files += child.Files
			result.Complete = result.Complete && child.Complete
			if child.ModifiedAt > result.ModifiedAt {
				result.ModifiedAt = child.ModifiedAt
			}
			for aggregatePath, aggregate := range child.Aggregates {
				result.Aggregates[aggregatePath] = aggregate
			}
			continue
		}
		result.Bytes += allocatedSize(childInfo)
		result.Files++
		modified := childInfo.ModTime().UTC().Format(time.RFC3339)
		if modified > result.ModifiedAt {
			result.ModifiedAt = modified
		}
		if onEntry != nil {
			onEntry(childPath)
		}
	}
	result.Aggregates[path] = folderMapAggregate{
		Bytes: result.Bytes, Files: result.Files, ModifiedAt: result.ModifiedAt,
		Known: true, Complete: result.Complete,
		StatModTime: info.ModTime().UnixNano(), StatSize: info.Size(), ScannedAt: now,
	}
	return result, nil
}

func applyFolderMapAggregates(snapshot *FolderMap) bool {
	paths := []string{snapshot.Path}
	for _, entry := range snapshot.Children {
		if entry.Directory {
			paths = append(paths, entry.Path)
		}
	}
	aggregates := loadFolderMapAggregates(snapshot.Root, paths)
	allCovered := true
	var latestScan time.Time
	allComplete := true
	anyKnown := false
	snapshot.Bytes = 0
	snapshot.Files = 0
	snapshot.ModifiedAt = ""
	snapshot.Directories = 0
	for i := range snapshot.Children {
		entry := &snapshot.Children[i]
		if !entry.Directory {
			anyKnown = true
			snapshot.Bytes += entry.Bytes
			snapshot.Files += entry.Files
			if entry.ModifiedAt > snapshot.ModifiedAt {
				snapshot.ModifiedAt = entry.ModifiedAt
			}
			continue
		}
		snapshot.Directories++
		aggregate, ok := aggregates[entry.Path]
		if !ok {
			allCovered = false
			allComplete = false
			continue
		}
		entry.Bytes = aggregate.Bytes
		entry.Files = aggregate.Files
		entry.ModifiedAt = aggregate.ModifiedAt
		entry.SizeKnown = aggregate.Known
		entry.SizeComplete = aggregate.Complete
		entry.SizeStale = aggregate.StatModTime != entry.StatModTime || aggregate.StatSize != entry.StatSize
		anyKnown = anyKnown || aggregate.Known
		allComplete = allComplete && aggregate.Complete && !entry.SizeStale
		snapshot.Bytes += entry.Bytes
		snapshot.Files += entry.Files
		if entry.ModifiedAt > snapshot.ModifiedAt {
			snapshot.ModifiedAt = entry.ModifiedAt
		}
		if aggregate.ScannedAt.After(latestScan) {
			latestScan = aggregate.ScannedAt
		}
	}
	rootAggregate, rootOK := aggregates[snapshot.Path]
	if rootOK {
		snapshot.Measured = allCovered && allComplete
		snapshot.SizeKnown = rootAggregate.Known || anyKnown
		snapshot.SizeComplete = allCovered && allComplete
		snapshot.GeneratedAt = rootAggregate.ScannedAt.UTC().Format(time.RFC3339)
	} else if !latestScan.IsZero() {
		snapshot.Measured = false
		snapshot.SizeKnown = anyKnown
		snapshot.SizeComplete = allCovered && allComplete
		snapshot.GeneratedAt = latestScan.UTC().Format(time.RFC3339)
	} else if len(snapshot.Children) == 0 || snapshot.Directories == 0 {
		snapshot.Measured = true
		snapshot.SizeKnown = true
		snapshot.SizeComplete = true
		snapshot.GeneratedAt = time.Now().UTC().Format(time.RFC3339)
	}
	sortFolderMapEntries(snapshot.Children)
	return snapshot.GeneratedAt != ""
}

func loadFolderMapAggregates(root string, paths []string) map[string]folderMapAggregate {
	store := folderMapIndexStoreForRoot(root)
	store.mu.Lock()
	defer store.mu.Unlock()
	if !store.loaded {
		store.index = readFolderMapAggregateIndex(root)
		store.loaded = true
	}
	aggregates := make(map[string]folderMapAggregate, len(paths))
	if store.index == nil {
		return aggregates
	}
	for _, path := range paths {
		if aggregate, exists := store.index.Directories[path]; exists {
			aggregates[path] = aggregate
		}
	}
	return aggregates
}

func folderMapAggregateIndexPath(root string) (string, error) {
	cacheRoot, err := os.UserCacheDir()
	if err != nil {
		return "", err
	}
	key := sha256.Sum256([]byte("folder-map-index-v1\x00" + filepath.Clean(root)))
	return filepath.Join(cacheRoot, "shed", "folder-maps", "index-"+hex.EncodeToString(key[:12])+".gob.gz"), nil
}

func folderMapIndexStoreForRoot(root string) *folderMapIndexStore {
	value, _ := folderMapIndexStores.LoadOrStore(filepath.Clean(root), &folderMapIndexStore{})
	return value.(*folderMapIndexStore)
}

func readFolderMapAggregateIndex(root string) *folderMapAggregateIndex {
	path, err := folderMapAggregateIndexPath(root)
	if err != nil {
		return nil
	}
	file, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer file.Close()
	reader, err := gzip.NewReader(file)
	if err != nil {
		return nil
	}
	defer reader.Close()
	var index folderMapAggregateIndex
	if err := gob.NewDecoder(reader).Decode(&index); err != nil || index.Version != folderMapIndexVersion || index.Root != root {
		return nil
	}
	return &index
}

func storeFolderMapAggregates(root, target string, aggregates map[string]folderMapAggregate) {
	store := folderMapIndexStoreForRoot(root)
	store.mu.Lock()
	defer store.mu.Unlock()
	if !store.loaded {
		store.index = readFolderMapAggregateIndex(root)
		store.loaded = true
	}
	index := store.index
	if index == nil {
		index = &folderMapAggregateIndex{Version: folderMapIndexVersion, Root: root, Directories: make(map[string]folderMapAggregate)}
		store.index = index
	}
	previous, hadPrevious := index.Directories[target]
	current, hasCurrent := aggregates[target]
	canPropagate := hadPrevious && hasCurrent
	byteDelta, fileDelta := int64(0), 0
	if canPropagate {
		byteDelta = current.Bytes - previous.Bytes
		fileDelta = current.Files - previous.Files
	}
	for path := range index.Directories {
		if isWithin(target, path) {
			delete(index.Directories, path)
		}
	}
	for path, aggregate := range aggregates {
		index.Directories[path] = aggregate
	}
	for ancestor := filepath.Dir(target); isWithin(root, ancestor) && ancestor != target; ancestor = filepath.Dir(ancestor) {
		if aggregate, exists := index.Directories[ancestor]; exists {
			if canPropagate {
				aggregate.Bytes += byteDelta
				aggregate.Files += fileDelta
				if current.ModifiedAt > aggregate.ModifiedAt {
					aggregate.ModifiedAt = current.ModifiedAt
				}
				aggregate.Complete = aggregate.Complete && current.Complete
				aggregate.ScannedAt = time.Now()
				index.Directories[ancestor] = aggregate
			} else {
				delete(index.Directories, ancestor)
			}
		}
		if ancestor == root || filepath.Dir(ancestor) == ancestor {
			break
		}
	}
	_ = writeFolderMapAggregateIndex(root, index)
	for ancestor := filepath.Dir(target); isWithin(root, ancestor) && ancestor != target; ancestor = filepath.Dir(ancestor) {
		if snapshotPath, err := folderMapSnapshotPath(root, ancestor); err == nil {
			_ = os.Remove(snapshotPath)
		}
		if ancestor == root || filepath.Dir(ancestor) == ancestor {
			break
		}
	}
}

func writeFolderMapAggregateIndex(root string, index *folderMapAggregateIndex) error {
	path, err := folderMapAggregateIndexPath(root)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	temporary, err := os.CreateTemp(filepath.Dir(path), ".folder-map-index-*.tmp")
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	writer := gzip.NewWriter(temporary)
	encodeErr := gob.NewEncoder(writer).Encode(index)
	if closeErr := writer.Close(); encodeErr == nil {
		encodeErr = closeErr
	}
	if closeErr := temporary.Close(); encodeErr == nil {
		encodeErr = closeErr
	}
	if encodeErr != nil {
		return encodeErr
	}
	return os.Rename(temporaryPath, path)
}

func folderMapSnapshotPath(root, target string) (string, error) {
	cacheRoot, err := os.UserCacheDir()
	if err != nil {
		return "", err
	}
	key := sha256.Sum256([]byte("folder-map-v3\x00" + filepath.Clean(root) + "\x00" + filepath.Clean(target)))
	return filepath.Join(cacheRoot, "shed", "folder-maps", hex.EncodeToString(key[:12])+".gob.gz"), nil
}

func loadFolderMapSnapshot(root, target string) (*FolderMap, bool) {
	path, err := folderMapSnapshotPath(root, target)
	if err != nil {
		return nil, false
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, false
	}
	defer file.Close()
	reader, err := gzip.NewReader(file)
	if err != nil {
		return nil, false
	}
	defer reader.Close()
	var snapshot FolderMap
	if err := gob.NewDecoder(reader).Decode(&snapshot); err != nil || snapshot.Root != root || snapshot.Path != target || snapshot.GeneratedAt == "" {
		return nil, false
	}
	if !folderMapSnapshotFresh(&snapshot) {
		return nil, false
	}
	return &snapshot, true
}

// Check the cached layer's direct entries before using it. Deep changes are
// picked up by the explicit size refresh action, avoiding a cold volume walk.
func folderMapSnapshotFresh(snapshot *FolderMap) bool {
	if _, err := time.Parse(time.RFC3339, snapshot.GeneratedAt); err != nil {
		return false
	}
	entries, err := os.ReadDir(snapshot.Path)
	if err != nil || len(entries) != len(snapshot.Children) {
		return false
	}
	cached := make(map[string]FolderMapEntry, len(snapshot.Children))
	for _, entry := range snapshot.Children {
		cached[entry.Name] = entry
	}
	for _, entry := range entries {
		previous, exists := cached[entry.Name()]
		if !exists {
			return false
		}
		info, err := os.Lstat(filepath.Join(snapshot.Path, entry.Name()))
		if err != nil || info.IsDir() != previous.Directory || info.ModTime().UnixNano() != previous.StatModTime || info.Size() != previous.StatSize {
			return false
		}
	}
	return true
}

func saveFolderMapSnapshot(snapshot *FolderMap) error {
	path, err := folderMapSnapshotPath(snapshot.Root, snapshot.Path)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	temporary, err := os.CreateTemp(filepath.Dir(path), ".folder-map-*.tmp")
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	writer := gzip.NewWriter(temporary)
	encodeErr := gob.NewEncoder(writer).Encode(snapshot)
	if closeErr := writer.Close(); encodeErr == nil {
		encodeErr = closeErr
	}
	if closeErr := temporary.Close(); encodeErr == nil {
		encodeErr = closeErr
	}
	if encodeErr != nil {
		return encodeErr
	}
	return os.Rename(temporaryPath, path)
}

func sortFolderMapEntries(entries []FolderMapEntry) {
	sort.Slice(entries, func(i, j int) bool {
		if entries[i].SizeKnown != entries[j].SizeKnown {
			return entries[i].SizeKnown
		}
		if entries[i].Bytes != entries[j].Bytes {
			return entries[i].Bytes > entries[j].Bytes
		}
		return strings.ToLower(entries[i].Name) < strings.ToLower(entries[j].Name)
	})
}

func formatFolderMapTime(value int64) string {
	if value <= 0 {
		return ""
	}
	return time.Unix(0, value).UTC().Format(time.RFC3339)
}
