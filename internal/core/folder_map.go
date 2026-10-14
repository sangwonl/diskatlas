package core

import (
	"context"
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
	Root               string `json:"root"`
	Path               string `json:"path"`
	Name               string `json:"name"`
	Bytes              int64  `json:"bytes"`
	DeltaBytes         int64  `json:"deltaBytes"`
	DeltaKnown         bool   `json:"deltaKnown"`
	DeltaSince         string `json:"deltaSince,omitempty"`
	DeltaGrowthBytes   int64  `json:"deltaGrowthBytes"`
	DeltaShrinkBytes   int64  `json:"deltaShrinkBytes"`
	DeltaActivityKnown bool   `json:"deltaActivityKnown"`
	Files              int    `json:"files"`
	Directories        int    `json:"directories"`
	ModifiedAt         string `json:"modifiedAt,omitempty"`
	GeneratedAt        string `json:"generatedAt,omitempty"`
	Measured           bool   `json:"measured"`
	// SizeKnown means Bytes is a usable lower bound; SizeComplete means the
	// walk read every path and did not skip a separate-volume subtree.
	SizeKnown    bool             `json:"sizeKnown"`
	SizeComplete bool             `json:"sizeComplete"`
	Children     []FolderMapEntry `json:"children"`
}

type FolderMapEntry struct {
	Name               string `json:"name"`
	Path               string `json:"path"`
	Bytes              int64  `json:"bytes"`
	DeltaBytes         int64  `json:"deltaBytes"`
	DeltaKnown         bool   `json:"deltaKnown"`
	DeltaSince         string `json:"deltaSince,omitempty"`
	DeltaGrowthBytes   int64  `json:"deltaGrowthBytes"`
	DeltaShrinkBytes   int64  `json:"deltaShrinkBytes"`
	DeltaActivityKnown bool   `json:"deltaActivityKnown"`
	Files              int    `json:"files"`
	ModifiedAt         string `json:"modifiedAt,omitempty"`
	Directory          bool   `json:"directory"`
	Symlink            bool   `json:"symlink"`
	// Incomplete directories retain their readable byte total as a lower bound.
	SizeKnown    bool  `json:"sizeKnown"`
	SizeComplete bool  `json:"sizeComplete"`
	SizeStale    bool  `json:"sizeStale"`
	StatModTime  int64 `json:"statModTime"`
	StatSize     int64 `json:"statSize"`
}

const folderMapIndexVersion = 1

type folderMapAggregate struct {
	Bytes              int64
	BaselineBytes      int64
	BaselineKnown      bool
	BaselineComplete   bool
	BaselineAt         time.Time
	DeltaBytes         int64
	DeltaKnown         bool
	DeltaSince         time.Time
	DeltaGrowthBytes   int64
	DeltaShrinkBytes   int64
	DeltaActivityKnown bool
	Files              int
	ModifiedAt         string
	Known              bool
	Complete           bool
	SizeStale          bool
	StatModTime        int64
	StatSize           int64
	ScannedAt          time.Time
	Journal            journalCursor
	JournalDevice      string
}

type folderMapAggregateIndex struct {
	Version     int
	Root        string
	Directories map[string]folderMapAggregate
}

type folderMapIndexStore struct {
	refreshMu      sync.Mutex
	mu             sync.RWMutex
	loaded         bool
	index          *folderMapAggregateIndex
	live           map[string]folderMapAggregate
	scanRoot       bool
	scanBaselineAt time.Time
}

var folderMapIndexStores sync.Map

// ReadFolderMap reads only the immediate entries in target. It does not walk
// into child directories, so opening DiskAtlas is fast even on a large volume.
func ReadFolderMap(root, target string) (*FolderMap, error) {
	normalizedRoot, normalizedTarget, err := normalizeFolderMapPaths(root, target)
	if err != nil {
		return nil, err
	}
	previous, _ := loadStoredFolderMapSnapshot(normalizedRoot, normalizedTarget)
	if folderMapScanActive(normalizedRoot) {
		result, err := readFolderMapLevel(normalizedRoot, normalizedTarget)
		if err != nil {
			return nil, err
		}
		applyFolderMapAggregates(result)
		applyFolderMapDeltas(result, previous, false)
		return result, nil
	}
	if cached, ok := loadFolderMapSnapshot(normalizedRoot, normalizedTarget); ok {
		applyFolderMapAggregates(cached)
		applyFolderMapDeltas(cached, previous, false)
		return cached, nil
	}
	result, err := readFolderMapLevel(normalizedRoot, normalizedTarget)
	if err != nil {
		return nil, err
	}
	applyFolderMapAggregates(result)
	applyFolderMapDeltas(result, previous, false)
	return result, nil
}

// ReloadFolderMap rereads only the current directory level and reuses cached
// measurements for its child directories. It never descends into the tree.
func ReloadFolderMap(root, target string) (*FolderMap, error) {
	normalizedRoot, normalizedTarget, err := normalizeFolderMapPaths(root, target)
	if err != nil {
		return nil, err
	}
	previous, _ := loadStoredFolderMapSnapshot(normalizedRoot, normalizedTarget)
	result, err := readFolderMapLevel(normalizedRoot, normalizedTarget)
	if err != nil {
		return nil, err
	}
	applyFolderMapAggregates(result)
	applyFolderMapDeltas(result, previous, false)
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
	store := folderMapIndexStoreForRoot(normalizedRoot)
	store.refreshMu.Lock()
	defer store.refreshMu.Unlock()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if _, err := openFolderMapDB(); err != nil {
		return nil, err
	}
	rootAggregate := loadFolderMapAggregates(normalizedRoot, []string{normalizedRoot})[normalizedRoot]
	isRootScan := filepath.Clean(normalizedTarget) == filepath.Clean(normalizedRoot)
	scanBaselineAt := rootAggregate.DeltaSince
	if isRootScan || scanBaselineAt.IsZero() {
		scanBaselineAt = rootAggregate.BaselineAt
	}
	rootAggregate.BaselineAt = scanBaselineAt
	previousSnapshot, _ := loadScanFolderMapSnapshot(normalizedRoot, normalizedTarget, isRootScan, scanBaselineAt)
	hasPreviousBaseline := !scanBaselineAt.IsZero()
	store.mu.RLock()
	var originalIndex *folderMapAggregateIndex
	if store.index != nil {
		copyIndex := *store.index
		copyIndex.Directories = make(map[string]folderMapAggregate, len(store.index.Directories))
		for path, aggregate := range store.index.Directories {
			copyIndex.Directories[path] = aggregate
		}
		originalIndex = &copyIndex
	}
	store.mu.RUnlock()
	committed := false

	var result *FolderMap
	if !force {
		if cached, ok := loadFolderMapSnapshot(normalizedRoot, normalizedTarget); ok {
			applyFolderMapAggregates(cached)
			applyFolderMapDeltas(cached, previousSnapshot, false)
			return cached, nil
		}
		result, err = readFolderMapLevel(normalizedRoot, normalizedTarget)
		if err != nil {
			return nil, err
		}
		applyFolderMapAggregates(result)
		targetAggregate, indexed := loadFolderMapAggregates(normalizedRoot, []string{normalizedTarget})[normalizedTarget]
		if result.GeneratedAt != "" && (!indexed || targetAggregate.Known) {
			applyFolderMapDeltas(result, previousSnapshot, false)
			return result, nil
		}
	}
	if result == nil {
		result, err = readFolderMapLevel(normalizedRoot, normalizedTarget)
		if err != nil {
			return nil, err
		}
	}
	refreshPlan, journalStart, journalDevice := prepareFolderMapRefresh(normalizedRoot, normalizedTarget, readFolderMapJournal)
	store.mu.Lock()
	store.live = make(map[string]folderMapAggregate)
	store.scanRoot = isRootScan
	store.scanBaselineAt = scanBaselineAt
	store.mu.Unlock()
	defer func() {
		store.mu.Lock()
		store.live = nil
		if !committed {
			store.index = originalIndex
		}
		store.mu.Unlock()
	}()
	initializeBaseline := !hasPreviousBaseline && previousSnapshot == nil && isRootScan
	snapshotBatch, batchErr := newFolderMapSnapshotBatch(initializeBaseline)
	if batchErr != nil {
		return nil, batchErr
	}
	snapshotBatch.rootScan = isRootScan
	snapshotBatch.previous = originalIndex
	if refreshPlan != nil {
		refreshPlan.rootScan = isRootScan
		refreshPlan.baselineAt = scanBaselineAt
	}
	if snapshotBatch != nil {
		defer snapshotBatch.cleanup()
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
	ctx, cancelScan := context.WithCancel(ctx)
	defer cancelScan()
	var measurementErr error
	var measurementFailure sync.Once
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
		parallelSlots := make(chan struct{}, workers)
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
						select {
						case parallelSlots <- struct{}{}:
						case <-ctx.Done():
							return
						}
						entry := result.Children[index]
						measured, measureErr := measureFolderEntry(ctx, normalizedRoot, entry.Path, rootAggregate, snapshotBatch, refreshPlan, parallelSlots, func(path string) {
							if scanned.Add(1)%128 == 0 {
								report(path, false)
							}
						})
						<-parallelSlots
						if measureErr != nil {
							measurementFailure.Do(func() { measurementErr = measureErr; cancelScan() })
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
						entry.DeltaGrowthBytes = measured.DeltaGrowthBytes
						entry.DeltaShrinkBytes = measured.DeltaShrinkBytes
						entry.DeltaActivityKnown = measured.DeltaActivityKnown
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
		if measurementErr != nil {
			return nil, measurementErr
		}
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
	applyFolderMapDeltas(result, previousSnapshot, initializeBaseline)
	result.DeltaGrowthBytes, result.DeltaShrinkBytes, result.DeltaActivityKnown = folderMapDeltaActivity(result.Children, previousSnapshot)
	aggregatesStored := false
	var aggregates map[string]folderMapAggregate
	rootInfo, rootErr := os.Lstat(normalizedTarget)
	if rootErr == nil {
		measurementTime := time.Now()
		rootAggregate := folderMapAggregate{
			Bytes: result.Bytes, Files: result.Files, ModifiedAt: result.ModifiedAt,
			DeltaBytes: result.DeltaBytes, DeltaKnown: result.DeltaKnown,
			DeltaGrowthBytes: result.DeltaGrowthBytes, DeltaShrinkBytes: result.DeltaShrinkBytes, DeltaActivityKnown: result.DeltaActivityKnown,
			Known: true, Complete: result.SizeComplete,
			StatModTime: rootInfo.ModTime().UnixNano(), StatSize: rootInfo.Size(), ScannedAt: measurementTime,
		}
		if result.DeltaSince != "" {
			rootAggregate.DeltaSince, _ = time.Parse(time.RFC3339, result.DeltaSince)
		}
		if previousSnapshot != nil && previousSnapshot.SizeKnown && previousSnapshot.SizeComplete {
			if baselineAt, parseErr := time.Parse(time.RFC3339, previousSnapshot.GeneratedAt); parseErr == nil {
				rootAggregate.BaselineBytes = previousSnapshot.Bytes
				rootAggregate.BaselineKnown = true
				rootAggregate.BaselineComplete = true
				rootAggregate.BaselineAt = baselineAt
			}
		}
		aggregates = map[string]folderMapAggregate{normalizedTarget: rootAggregate}
		for _, measured := range measuredSubtrees {
			for path, aggregate := range measured.Aggregates {
				if refreshPlan == nil {
					aggregate.ScannedAt = measurementTime
				} else if _, rescanned := refreshPlan.rescanned.Load(path); rescanned {
					aggregate.ScannedAt = measurementTime
				}
				aggregates[path] = aggregate
			}
		}
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		for path, aggregate := range aggregates {
			if refreshPlan == nil || path == normalizedTarget {
				aggregate.Journal = journalStart
				aggregate.JournalDevice = journalDevice
			} else if _, rescanned := refreshPlan.rescanned.Load(path); rescanned {
				aggregate.Journal = journalStart
				aggregate.JournalDevice = journalDevice
			}
			aggregates[path] = aggregate
		}
		storeFolderMapAggregates(normalizedRoot, normalizedTarget, aggregates, refreshPlan)
		aggregatesStored = true
	}
	applyFolderMapAggregates(result)
	applyFolderMapDeltas(result, previousSnapshot, initializeBaseline)
	if jobCount > 0 {
		report(normalizedTarget, true)
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if !aggregatesStored {
		return nil, errors.New("폴더 측정 결과를 저장할 수 없습니다")
	}
	if initializeBaseline {
		if err := advanceFolderMapAggregateBaseline(normalizedRoot, true); err != nil {
			return nil, err
		}
		applyFolderMapAggregates(result)
	}
	if err := snapshotBatch.stage(result); err != nil {
		return nil, err
	}
	if isRootScan && !initializeBaseline {
		if err := advanceFolderMapAggregateBaseline(normalizedRoot, false); err != nil {
			return nil, err
		}
	}
	store.mu.Lock()
	index := store.index
	if !isRootScan && originalIndex != nil {
		checkpointAt := originalIndex.Directories[normalizedRoot].BaselineAt
		for path, aggregate := range index.Directories {
			old, exists := originalIndex.Directories[path]
			if exists {
				aggregate.BaselineBytes = old.BaselineBytes
				aggregate.BaselineKnown = old.BaselineKnown
				aggregate.BaselineComplete = old.BaselineComplete
			} else if !checkpointAt.IsZero() {
				aggregate.BaselineBytes = 0
				aggregate.BaselineKnown = true
				aggregate.BaselineComplete = true
			}
			aggregate.BaselineAt = checkpointAt
			index.Directories[path] = aggregate
		}
	}
	checkpoint := index.Directories[normalizedRoot].BaselineAt
	comparison := scanBaselineAt
	if initializeBaseline {
		comparison = checkpoint
	}
	// Root comparison metadata remains available even when permissions make the
	// root total incomplete; readable children still share this comparison.
	for path, aggregate := range index.Directories {
		if aggregate.DeltaKnown || path == normalizedRoot {
			aggregate.DeltaSince = comparison
			index.Directories[path] = aggregate
		}
		store.live[path] = aggregate
	}
	store.mu.Unlock()
	if err := snapshotBatch.commitScan(ctx, normalizedRoot, index, checkpoint, comparison); err != nil {
		return nil, err
	}
	committed = true

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
			Symlink:     childInfo.Mode()&os.ModeSymlink != 0,
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
	Bytes              int64
	DeltaBytes         int64
	DeltaKnown         bool
	DeltaSince         string
	DeltaGrowthBytes   int64
	DeltaShrinkBytes   int64
	DeltaActivityKnown bool
	Files              int
	ModifiedAt         string
	Known              bool
	Complete           bool
	Aggregates         map[string]folderMapAggregate
}

func measureFolderEntry(ctx context.Context, scanRoot, path string, rootBaseline folderMapAggregate, snapshots *folderMapSnapshotBatch, refreshPlan *folderMapRefreshPlan, parallelSlots chan struct{}, onEntry func(string)) (measuredFolder, error) {
	if err := ctx.Err(); err != nil {
		return measuredFolder{}, err
	}
	rootInfo, rootErr := os.Lstat(path)
	if rootErr != nil {
		return measuredFolder{Aggregates: map[string]folderMapAggregate{
			path: {Known: false, Complete: false, ScannedAt: time.Now()},
		}}, nil
	}
	rootDevice := filesystemIdentity(path, rootInfo)
	return measureFolderTree(ctx, scanRoot, path, rootDevice, rootBaseline, snapshots, refreshPlan, rootInfo, parallelSlots, onEntry)
}

func measureFolderTree(ctx context.Context, scanRoot, path, rootDevice string, rootBaseline folderMapAggregate, snapshots *folderMapSnapshotBatch, refreshPlan *folderMapRefreshPlan, info os.FileInfo, parallelSlots chan struct{}, onEntry func(string)) (measuredFolder, error) {
	if err := ctx.Err(); err != nil {
		return measuredFolder{}, err
	}
	if cached, ok := refreshPlan.reuse(path, info); ok {
		return cached, nil
	}
	if refreshPlan != nil {
		refreshPlan.rescanned.Store(path, true)
	}
	now := time.Now()
	previousSnapshot, _ := loadScanFolderMapSnapshot(scanRoot, path, snapshots != nil && snapshots.rootScan, rootBaseline.BaselineAt)
	previousAggregate, hasPreviousAggregate := loadFolderMapAggregates(scanRoot, []string{path})[path]
	if previousSnapshot == nil && !hasPreviousAggregate && folderMapHasComparisonBaseline(rootBaseline) {
		baselineAt := rootBaseline.BaselineAt
		if baselineAt.IsZero() {
			baselineAt = rootBaseline.ScannedAt
		}
		previousSnapshot = &FolderMap{
			Root: scanRoot, Path: path, Name: filepath.Base(path),
			GeneratedAt: baselineAt.UTC().Format(time.RFC3339),
			SizeKnown:   true, SizeComplete: true, Children: []FolderMapEntry{},
		}
	}
	result := measuredFolder{Known: true, Complete: true, Aggregates: make(map[string]folderMapAggregate)}
	snapshot := &FolderMap{
		Root: scanRoot, Path: path, Name: filepath.Base(path),
		SizeKnown: true, SizeComplete: true,
		Children: make([]FolderMapEntry, 0),
	}
	if path == string(filepath.Separator) {
		snapshot.Name = path
	}
	if info == nil || !info.IsDir() {
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
	children := make([]FolderMapEntry, len(entries))
	includeChild := make([]bool, len(entries))
	measurements := make([]measuredFolder, len(entries))
	measurementErrors := make([]error, len(entries))
	measuredDirectory := make([]bool, len(entries))
	var childrenDone sync.WaitGroup
	for index, entry := range entries {
		if ctx.Err() != nil {
			break
		}
		childPath := filepath.Join(path, entry.Name())
		childInfo, statErr := os.Lstat(childPath)
		if statErr != nil {
			result.Complete = false
			continue
		}
		childEntry := FolderMapEntry{
			Name: entry.Name(), Path: childPath,
			Directory: childInfo.IsDir(), Symlink: childInfo.Mode()&os.ModeSymlink != 0,
			ModifiedAt:  formatFolderMapTime(childInfo.ModTime().UnixNano()),
			StatModTime: childInfo.ModTime().UnixNano(), StatSize: childInfo.Size(),
		}
		if childInfo.IsDir() {
			snapshot.Directories++
			if isDataVolumeAlias(childPath) || isOtherFilesystem(rootDevice, childPath, childInfo) {
				result.Complete = false
				result.Aggregates[childPath] = folderMapAggregate{
					Known: false, Complete: false, StatModTime: childInfo.ModTime().UnixNano(), StatSize: childInfo.Size(), ScannedAt: now,
				}
				childEntry.SizeKnown = false
				childEntry.SizeComplete = false
				children[index] = childEntry
				includeChild[index] = true
				continue
			}
			children[index] = childEntry
			includeChild[index] = true
			measuredDirectory[index] = true
			select {
			case parallelSlots <- struct{}{}:
				childrenDone.Add(1)
				go func(index int, childPath string, childInfo os.FileInfo) {
					defer childrenDone.Done()
					defer func() { <-parallelSlots }()
					measurements[index], measurementErrors[index] = measureFolderTree(ctx, scanRoot, childPath, rootDevice, rootBaseline, snapshots, refreshPlan, childInfo, parallelSlots, onEntry)
				}(index, childPath, childInfo)
			default:
				measurements[index], measurementErrors[index] = measureFolderTree(ctx, scanRoot, childPath, rootDevice, rootBaseline, snapshots, refreshPlan, childInfo, parallelSlots, onEntry)
			}
			continue
		}
		childEntry.Bytes = allocatedSize(childInfo)
		childEntry.Files = 1
		childEntry.SizeKnown = true
		childEntry.SizeComplete = true
		children[index] = childEntry
		includeChild[index] = true
		if onEntry != nil {
			onEntry(childPath)
		}
	}
	childrenDone.Wait()
	if err := ctx.Err(); err != nil {
		return measuredFolder{}, err
	}
	for index, childEntry := range children {
		if !includeChild[index] {
			continue
		}
		if measuredDirectory[index] {
			child := measurements[index]
			if measurementErrors[index] != nil {
				return measuredFolder{}, measurementErrors[index]
			}
			childEntry.Bytes = child.Bytes
			childEntry.Files = child.Files
			childEntry.SizeKnown = child.Known
			childEntry.SizeComplete = child.Complete
			childEntry.DeltaBytes = child.DeltaBytes
			childEntry.DeltaKnown = child.DeltaKnown
			childEntry.DeltaSince = child.DeltaSince
			childEntry.DeltaGrowthBytes = child.DeltaGrowthBytes
			childEntry.DeltaShrinkBytes = child.DeltaShrinkBytes
			childEntry.DeltaActivityKnown = child.DeltaActivityKnown
			if child.ModifiedAt != "" {
				childEntry.ModifiedAt = child.ModifiedAt
			}
			result.Complete = result.Complete && child.Complete
			for aggregatePath, aggregate := range child.Aggregates {
				result.Aggregates[aggregatePath] = aggregate
			}
		}
		result.Bytes += childEntry.Bytes
		result.Files += childEntry.Files
		if childEntry.ModifiedAt > result.ModifiedAt {
			result.ModifiedAt = childEntry.ModifiedAt
		}
		snapshot.Children = append(snapshot.Children, childEntry)
	}
	snapshot.Bytes = result.Bytes
	snapshot.Files = result.Files
	snapshot.ModifiedAt = result.ModifiedAt
	snapshot.GeneratedAt = now.UTC().Format(time.RFC3339)
	snapshot.Measured = result.Known && result.Complete
	snapshot.SizeKnown = result.Known
	snapshot.SizeComplete = result.Complete
	sortFolderMapEntries(snapshot.Children)
	resetUnchanged := snapshots != nil && snapshots.initializeBaseline
	applyFolderMapDeltas(snapshot, previousSnapshot, resetUnchanged)
	snapshot.DeltaGrowthBytes, snapshot.DeltaShrinkBytes, snapshot.DeltaActivityKnown = folderMapDeltaActivity(snapshot.Children, previousSnapshot)
	result.DeltaGrowthBytes = snapshot.DeltaGrowthBytes
	result.DeltaShrinkBytes = snapshot.DeltaShrinkBytes
	result.DeltaActivityKnown = snapshot.DeltaActivityKnown
	result.Aggregates[path] = folderMapAggregate{
		Bytes: result.Bytes, Files: result.Files, ModifiedAt: result.ModifiedAt,
		DeltaGrowthBytes: result.DeltaGrowthBytes, DeltaShrinkBytes: result.DeltaShrinkBytes, DeltaActivityKnown: result.DeltaActivityKnown,
		Known: true, Complete: result.Complete,
		StatModTime: info.ModTime().UnixNano(), StatSize: info.Size(), ScannedAt: now,
	}
	if previousSnapshot != nil && previousSnapshot.SizeKnown && previousSnapshot.SizeComplete {
		if baselineAt, parseErr := time.Parse(time.RFC3339, previousSnapshot.GeneratedAt); parseErr == nil {
			aggregate := result.Aggregates[path]
			aggregate.BaselineBytes = previousSnapshot.Bytes
			aggregate.BaselineKnown = true
			aggregate.BaselineComplete = true
			aggregate.BaselineAt = baselineAt
			result.Aggregates[path] = aggregate
		}
	}
	if !snapshot.DeltaKnown && hasPreviousAggregate && previousAggregate.Known && previousAggregate.Complete && result.Known && result.Complete && folderMapHasComparisonBaseline(previousAggregate) {
		baselineBytes := previousAggregate.Bytes
		baselineAt := previousAggregate.ScannedAt
		if previousAggregate.BaselineKnown {
			baselineBytes = previousAggregate.BaselineBytes
			baselineAt = previousAggregate.BaselineAt
		}
		snapshot.DeltaBytes = result.Bytes - baselineBytes
		snapshot.DeltaKnown = true
		snapshot.DeltaSince = baselineAt.UTC().Format(time.RFC3339)
	}
	result.DeltaBytes = snapshot.DeltaBytes
	result.DeltaKnown = snapshot.DeltaKnown
	result.DeltaSince = snapshot.DeltaSince
	publishFolderMapAggregate(scanRoot, path, result.Aggregates[path])
	if snapshots != nil {
		// Stage one database row; the scan publishes all rows together.
		if err := snapshots.stage(snapshot); err != nil {
			return measuredFolder{}, err
		}
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
	aggregates, live := loadVisibleFolderMapAggregates(snapshot.Root, paths)
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
		entry.SizeStale = aggregate.SizeStale || aggregate.StatModTime != entry.StatModTime || aggregate.StatSize != entry.StatSize
		entry.DeltaBytes = aggregate.DeltaBytes
		entry.DeltaKnown = aggregate.DeltaKnown && !entry.SizeStale
		entry.DeltaGrowthBytes = aggregate.DeltaGrowthBytes
		entry.DeltaShrinkBytes = aggregate.DeltaShrinkBytes
		entry.DeltaActivityKnown = aggregate.DeltaActivityKnown
		if !aggregate.DeltaSince.IsZero() {
			entry.DeltaSince = aggregate.DeltaSince.UTC().Format(time.RFC3339)
		}
		if !entry.DeltaKnown && !entry.SizeStale && aggregate.Known && aggregate.Complete && aggregate.BaselineKnown && aggregate.BaselineComplete {
			entry.DeltaBytes = aggregate.Bytes - aggregate.BaselineBytes
			entry.DeltaKnown = true
			entry.DeltaSince = aggregate.BaselineAt.UTC().Format(time.RFC3339)
		}
		if entry.SizeStale {
			entry.DeltaBytes = 0
			entry.DeltaSince = ""
		}
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
		if !rootAggregate.DeltaKnown && allCovered && allComplete && rootAggregate.BaselineKnown && rootAggregate.BaselineComplete {
			rootAggregate.DeltaBytes = snapshot.Bytes - rootAggregate.BaselineBytes
			rootAggregate.DeltaKnown = true
			rootAggregate.DeltaSince = rootAggregate.BaselineAt
		} else if live && allCovered && allComplete && rootAggregate.Known && rootAggregate.Complete && rootAggregate.DeltaKnown {
			rootAggregate.DeltaBytes += snapshot.Bytes - rootAggregate.Bytes
		}
		snapshot.Measured = allCovered && allComplete
		snapshot.SizeKnown = rootAggregate.Known || anyKnown
		snapshot.SizeComplete = allCovered && allComplete
		snapshot.GeneratedAt = rootAggregate.ScannedAt.UTC().Format(time.RFC3339)
		snapshot.DeltaBytes = rootAggregate.DeltaBytes
		snapshot.DeltaKnown = rootAggregate.DeltaKnown && snapshot.SizeComplete
		snapshot.DeltaGrowthBytes = rootAggregate.DeltaGrowthBytes
		snapshot.DeltaShrinkBytes = rootAggregate.DeltaShrinkBytes
		snapshot.DeltaActivityKnown = rootAggregate.DeltaActivityKnown
		if !rootAggregate.DeltaSince.IsZero() {
			snapshot.DeltaSince = rootAggregate.DeltaSince.UTC().Format(time.RFC3339)
		}
		if !snapshot.DeltaKnown {
			snapshot.DeltaBytes = 0
			snapshot.DeltaSince = ""
		}
		if !snapshot.DeltaActivityKnown {
			snapshot.DeltaGrowthBytes = 0
			snapshot.DeltaShrinkBytes = 0
		}
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

func folderMapIndexStoreForRoot(root string) *folderMapIndexStore {
	value, _ := folderMapIndexStores.LoadOrStore(filepath.Clean(root), &folderMapIndexStore{})
	return value.(*folderMapIndexStore)
}

func storeFolderMapAggregates(root, target string, aggregates map[string]folderMapAggregate, refreshPlan *folderMapRefreshPlan) {
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
	previousRoot := index.Directories[root]
	rootBaselineKnown := !previousRoot.BaselineAt.IsZero()
	rootScan := filepath.Clean(root) == filepath.Clean(target)
	rootBaselineAt := previousRoot.BaselineAt
	if !rootScan && !previousRoot.DeltaSince.IsZero() {
		rootBaselineAt = previousRoot.DeltaSince
	}
	if rootBaselineAt.IsZero() {
		rootBaselineAt = previousRoot.ScannedAt
	}
	for path, aggregate := range aggregates {
		previous, exists := index.Directories[path]
		switch {
		case rootBaselineKnown:
			if baselineBytes, ok := folderMapRootCheckpointBytes(root, path, previous, exists, rootBaselineAt, rootScan); ok {
				aggregate.BaselineBytes = baselineBytes
				aggregate.BaselineKnown = true
				aggregate.BaselineComplete = true
				aggregate.BaselineAt = rootBaselineAt
			} else {
				aggregate.BaselineKnown = false
				aggregate.BaselineComplete = false
				aggregate.BaselineAt = time.Time{}
			}
		case previous.BaselineKnown && previous.BaselineComplete:
			aggregate.BaselineBytes = previous.BaselineBytes
			aggregate.BaselineKnown = true
			aggregate.BaselineComplete = previous.BaselineComplete
			aggregate.BaselineAt = previous.BaselineAt
		case exists && previous.Known && previous.Complete:
			// Seed an older aggregate index from its last measured value.
			aggregate.BaselineBytes = previous.Bytes
			aggregate.BaselineKnown = true
			aggregate.BaselineComplete = true
			aggregate.BaselineAt = previous.ScannedAt
		}
		if aggregate.BaselineKnown && aggregate.BaselineComplete && aggregate.Known && aggregate.Complete {
			aggregate.DeltaBytes = aggregate.Bytes - aggregate.BaselineBytes
			aggregate.DeltaKnown = true
			aggregate.DeltaSince = aggregate.BaselineAt
		} else {
			aggregate.DeltaBytes = 0
			aggregate.DeltaKnown = false
			aggregate.DeltaSince = time.Time{}
		}
		aggregates[path] = aggregate
	}
	previous, hadPrevious := index.Directories[target]
	current, hasCurrent := aggregates[target]
	canPropagate := hasCurrent && current.Known && current.Complete
	canPropagateActivity := hasCurrent && current.DeltaActivityKnown
	byteDelta, fileDelta := int64(0), 0
	if canPropagate {
		if hadPrevious && previous.Known && previous.Complete {
			byteDelta = current.Bytes - previous.Bytes
			fileDelta = current.Files - previous.Files
		} else {
			byteDelta = current.Bytes
			fileDelta = current.Files
		}
	}
	for path := range index.Directories {
		if isWithin(target, path) {
			delete(index.Directories, path)
		}
	}
	for path, aggregate := range aggregates {
		index.Directories[path] = aggregate
		if store.live != nil {
			store.live[path] = aggregate
		}
	}
	for ancestor := filepath.Dir(target); isWithin(root, ancestor) && ancestor != target; ancestor = filepath.Dir(ancestor) {
		if aggregate, exists := index.Directories[ancestor]; exists {
			if canPropagate || canPropagateActivity {
				canPropagateBytes := canPropagate && aggregate.Complete
				if canPropagateBytes {
					if rootBaselineKnown {
						if baselineBytes, ok := folderMapRootCheckpointBytes(root, ancestor, aggregate, true, rootBaselineAt, rootScan); ok {
							aggregate.BaselineBytes = baselineBytes
							aggregate.BaselineKnown = true
							aggregate.BaselineComplete = true
							aggregate.BaselineAt = rootBaselineAt
						} else {
							aggregate.BaselineKnown = false
							aggregate.BaselineComplete = false
							aggregate.BaselineAt = time.Time{}
						}
					}
					aggregate.Bytes += byteDelta
					aggregate.Files += fileDelta
					if aggregate.BaselineKnown && aggregate.BaselineComplete && aggregate.Known && aggregate.Complete {
						aggregate.DeltaBytes = aggregate.Bytes - aggregate.BaselineBytes
						aggregate.DeltaKnown = true
						aggregate.DeltaSince = aggregate.BaselineAt
					} else {
						aggregate.DeltaBytes = 0
						aggregate.DeltaKnown = false
						aggregate.DeltaSince = time.Time{}
					}
				} else {
					aggregate.Complete = false
					aggregate.SizeStale = true
					aggregate.DeltaBytes = 0
					aggregate.DeltaKnown = false
					aggregate.DeltaSince = time.Time{}
				}
				if canPropagateActivity && (current.DeltaGrowthBytes > 0 || current.DeltaShrinkBytes > 0) && !aggregate.DeltaActivityKnown {
					aggregate.DeltaGrowthBytes = current.DeltaGrowthBytes
					aggregate.DeltaShrinkBytes = current.DeltaShrinkBytes
					aggregate.DeltaActivityKnown = true
				} else if canPropagateActivity && (!hadPrevious || previous.DeltaActivityKnown) && aggregate.DeltaActivityKnown {
					previousGrowth, previousShrink := int64(0), int64(0)
					if hadPrevious {
						previousGrowth = previous.DeltaGrowthBytes
						previousShrink = previous.DeltaShrinkBytes
					}
					aggregate.DeltaGrowthBytes += current.DeltaGrowthBytes - previousGrowth
					aggregate.DeltaShrinkBytes += current.DeltaShrinkBytes - previousShrink
				} else {
					aggregate.DeltaGrowthBytes = 0
					aggregate.DeltaShrinkBytes = 0
					aggregate.DeltaActivityKnown = false
				}
				if current.ModifiedAt > aggregate.ModifiedAt {
					aggregate.ModifiedAt = current.ModifiedAt
				}
				if canPropagateBytes {
					aggregate.Complete = aggregate.Complete && current.Complete
				}
				aggregate.ScannedAt = current.ScannedAt
				index.Directories[ancestor] = aggregate
				if store.live != nil {
					store.live[ancestor] = aggregate
				}
			} else {
				delete(index.Directories, ancestor)
				delete(store.live, ancestor)
			}
		}
		if ancestor == root || filepath.Dir(ancestor) == ancestor {
			break
		}
	}
}

// Root scans compare against the latest checkpoint; local refreshes keep the
// displayed root comparison. Aggregate records avoid decoding a snapshot when
// they already contain the matching baseline.
func folderMapRootCheckpointBytes(root, path string, previous folderMapAggregate, exists bool, checkpoint time.Time, rootScan bool) (int64, bool) {
	if exists && previous.Known && previous.Complete {
		if rootScan && previous.BaselineKnown && previous.BaselineComplete && previous.BaselineAt.Equal(checkpoint) {
			return previous.BaselineBytes, true
		}
		if !rootScan && previous.DeltaKnown && previous.DeltaSince.Equal(checkpoint) {
			return previous.Bytes - previous.DeltaBytes, true
		}
	}
	kind := folderMapComparison
	if rootScan {
		kind = folderMapCheckpoint
	}
	if filepath.Clean(path) != filepath.Clean(root) {
		if parent, ok := readFolderMapDBSnapshot(root, filepath.Dir(path), kind); ok && parent.SizeKnown && parent.SizeComplete {
			for _, entry := range parent.Children {
				if entry.Path == path {
					return entry.Bytes, entry.SizeKnown && entry.SizeComplete && !entry.SizeStale
				}
			}
			return 0, true
		}
	}
	if snapshot, ok := readFolderMapDBSnapshot(root, path, kind); ok && snapshot.SizeKnown && snapshot.SizeComplete {
		return snapshot.Bytes, true
	}
	return 0, false
}

func advanceFolderMapAggregateBaseline(root string, initialize bool) error {
	store := folderMapIndexStoreForRoot(root)
	store.mu.Lock()
	defer store.mu.Unlock()
	if !store.loaded {
		store.index = readFolderMapAggregateIndex(root)
		store.loaded = true
	}
	index := store.index
	if index == nil {
		return errors.New("폴더 측정 인덱스가 없습니다")
	}
	rootAggregate, exists := index.Directories[root]
	if !exists || !rootAggregate.Known || rootAggregate.ScannedAt.IsZero() {
		return errors.New("폴더 측정 결과가 없습니다")
	}
	for path, aggregate := range index.Directories {
		if !isWithin(root, path) {
			continue
		}
		if aggregate.Known && aggregate.Complete {
			firstBaseline := !aggregate.BaselineKnown || !aggregate.BaselineComplete
			aggregate.BaselineBytes = aggregate.Bytes
			aggregate.BaselineKnown = true
			aggregate.BaselineComplete = true
			aggregate.BaselineAt = rootAggregate.ScannedAt
			if initialize && firstBaseline {
				aggregate.DeltaBytes = 0
				aggregate.DeltaKnown = true
				aggregate.DeltaSince = rootAggregate.ScannedAt
				aggregate.DeltaGrowthBytes = 0
				aggregate.DeltaShrinkBytes = 0
				aggregate.DeltaActivityKnown = true
			}
		} else {
			aggregate.BaselineComplete = false
			aggregate.BaselineAt = rootAggregate.ScannedAt
		}
		index.Directories[path] = aggregate
		if store.live != nil {
			store.live[path] = aggregate
		}
	}
	return nil
}

func folderMapHasComparisonBaseline(aggregate folderMapAggregate) bool {
	return aggregate.BaselineKnown && aggregate.BaselineComplete || aggregate.Known && aggregate.Complete
}

func loadFolderMapSnapshot(root, target string) (*FolderMap, bool) {
	if snapshot, ok := readFolderMapDBSnapshot(root, target, folderMapDisplay); ok && folderMapSnapshotFresh(snapshot) {
		return snapshot, true
	}
	return nil, false
}

func loadStoredFolderMapSnapshot(root, target string) (*FolderMap, bool) {
	aggregate := loadFolderMapAggregates(root, []string{root})[root]
	baseline := aggregate.DeltaSince
	if baseline.IsZero() {
		baseline = aggregate.BaselineAt
	}
	return loadScanFolderMapSnapshot(root, target, false, baseline)
}

func loadScanFolderMapSnapshot(root, target string, rootScan bool, baseline time.Time) (*FolderMap, bool) {
	kind := folderMapComparison
	if rootScan {
		kind = folderMapCheckpoint
	}
	snapshot, ok := readFolderMapDBSnapshot(root, target, kind)
	if !ok {
		return nil, false
	}
	snapshot.GeneratedAt = baseline.UTC().Format(time.RFC3339)
	snapshot.DeltaBytes = 0
	snapshot.DeltaKnown = snapshot.SizeKnown && snapshot.SizeComplete
	snapshot.DeltaSince = snapshot.GeneratedAt
	snapshot.DeltaGrowthBytes = 0
	snapshot.DeltaShrinkBytes = 0
	snapshot.DeltaActivityKnown = snapshot.DeltaKnown
	for i := range snapshot.Children {
		entry := &snapshot.Children[i]
		entry.DeltaBytes = 0
		entry.DeltaKnown = entry.SizeKnown && entry.SizeComplete && !entry.SizeStale
		entry.DeltaSince = snapshot.GeneratedAt
		entry.DeltaGrowthBytes = 0
		entry.DeltaShrinkBytes = 0
		entry.DeltaActivityKnown = entry.DeltaKnown
	}
	return snapshot, true
}

func applyFolderMapDeltas(snapshot, previous *FolderMap, resetUnchanged bool) {
	if snapshot == nil || previous == nil || snapshot.Path != previous.Path {
		return
	}
	if !snapshot.DeltaKnown && snapshot.SizeKnown && snapshot.SizeComplete && previous.SizeKnown && previous.SizeComplete {
		if !resetUnchanged && snapshot.Bytes == previous.Bytes {
			if previous.DeltaKnown {
				snapshot.DeltaBytes = previous.DeltaBytes
				snapshot.DeltaSince = previous.DeltaSince
			} else {
				snapshot.DeltaBytes = 0
				snapshot.DeltaSince = previous.GeneratedAt
			}
			snapshot.DeltaKnown = true
		} else {
			snapshot.DeltaBytes = snapshot.Bytes - previous.Bytes
			snapshot.DeltaKnown = true
			snapshot.DeltaSince = previous.GeneratedAt
		}
	}
	previousEntries := make(map[string]FolderMapEntry, len(previous.Children))
	for _, entry := range previous.Children {
		previousEntries[entry.Path] = entry
	}
	for index := range snapshot.Children {
		entry := &snapshot.Children[index]
		if !entry.Directory {
			entry.DeltaKnown = false
			entry.DeltaBytes = 0
			entry.DeltaSince = ""
		}
		if entry.DeltaKnown {
			continue
		}
		old, existed := previousEntries[entry.Path]
		if existed && !entry.SizeStale && entry.SizeKnown && entry.SizeComplete && old.SizeKnown && old.SizeComplete {
			if !resetUnchanged && entry.Bytes == old.Bytes {
				if old.DeltaKnown {
					entry.DeltaBytes = old.DeltaBytes
					entry.DeltaSince = old.DeltaSince
				} else {
					entry.DeltaBytes = 0
					entry.DeltaSince = previous.GeneratedAt
				}
				entry.DeltaKnown = true
			} else {
				entry.DeltaBytes = entry.Bytes - old.Bytes
				entry.DeltaKnown = true
				entry.DeltaSince = previous.GeneratedAt
			}
		} else if !existed && previous.SizeKnown && previous.SizeComplete && entry.SizeKnown && entry.SizeComplete {
			entry.DeltaBytes = entry.Bytes
			entry.DeltaKnown = true
			entry.DeltaSince = previous.GeneratedAt
		}
	}
}

func folderMapDeltaActivity(entries []FolderMapEntry, previous *FolderMap) (growth, shrink int64, known bool) {
	known = previous != nil && previous.SizeKnown && previous.SizeComplete
	currentPaths := make(map[string]struct{}, len(entries))
	for _, entry := range entries {
		currentPaths[entry.Path] = struct{}{}
		if entry.Directory {
			if entry.DeltaActivityKnown {
				growth += entry.DeltaGrowthBytes
				shrink += entry.DeltaShrinkBytes
				continue
			}
			// A complete directory may have a known net delta even when its
			// older snapshot lacks the gross activity fields.
			if entry.DeltaKnown {
				if entry.DeltaBytes > 0 {
					growth += entry.DeltaBytes
				} else if entry.DeltaBytes < 0 {
					shrink -= entry.DeltaBytes
				}
				continue
			}
			known = false
			continue
		}
		if !entry.DeltaKnown {
			known = false
			continue
		}
		if entry.DeltaBytes > 0 {
			growth += entry.DeltaBytes
		} else if entry.DeltaBytes < 0 {
			shrink -= entry.DeltaBytes
		}
	}
	if previous != nil {
		for _, entry := range previous.Children {
			if _, exists := currentPaths[entry.Path]; exists {
				continue
			}
			if !entry.SizeKnown || !entry.SizeComplete {
				known = false
				continue
			}
			shrink += entry.Bytes
		}
	}
	known = known || growth > 0 || shrink > 0
	return growth, shrink, known
}

// Check the cached layer's direct entries before using it. Deep changes are
// picked up by the explicit size refresh action, avoiding a cold volume walk.
func folderMapSnapshotFresh(snapshot *FolderMap) bool {
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
		if err != nil || info.IsDir() != previous.Directory || (info.Mode()&os.ModeSymlink != 0) != previous.Symlink || info.ModTime().UnixNano() != previous.StatModTime || info.Size() != previous.StatSize {
			return false
		}
	}
	return true
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
