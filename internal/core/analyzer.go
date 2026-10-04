package core

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

type Analysis struct {
	Result  *Result `json:"result"`
	Storage Storage `json:"storage"`
}

func AnalyzeWithProgress(root string, onProgress func(Progress)) (*Analysis, error) {
	root, err := analysisRoot(root)
	if err != nil {
		return nil, err
	}
	if onProgress != nil {
		onProgress(Progress{Phase: "discover", Scanned: 0})
	}
	baseStorage, err := storageInfoAt(root)
	if err != nil {
		return nil, err
	}
	watchRoot := analysisWatchRoot(root)
	reuseCachedAnalysis := func(snapshot *analysisSnapshot) *Analysis {
		storage := snapshot.Storage
		storage.Total = baseStorage.Total
		storage.Available = baseStorage.Available
		storage.Complete = true
		if onProgress != nil {
			onProgress(Progress{Phase: "cached", Scanned: 0, Candidates: int64(len(snapshot.Result.Items)), Found: int64(len(snapshot.Result.Items)), Storage: &storage})
			onProgress(Progress{Phase: "done", Scanned: 0, Candidates: int64(len(snapshot.Result.Items)), Found: int64(len(snapshot.Result.Items))})
		}
		return &Analysis{Result: snapshot.Result, Storage: storage}
	}
	summary := loadAnalysisSummary(root)
	var previousIndex *analysisIndex
	var cachedSnapshot *analysisSnapshot
	var cachedJournal journalCursor
	var cachedAt time.Time
	if summary != nil {
		cachedSnapshot = summary.Snapshot
		cachedJournal = summary.Journal
		cachedAt = summary.SavedAt
	} else {
		previousIndex = loadAnalysisIndex(root)
		if previousIndex != nil {
			cachedSnapshot = previousIndex.Snapshot
			cachedJournal = previousIndex.Journal
			cachedAt = previousIndex.SavedAt
			// Migrate an existing full cache to the lightweight summary. Future
			// unchanged analyses no longer decode the complete file manifest.
			_ = saveAnalysisSummary(previousIndex)
		}
	}
	cursor := journalCursor{}
	journalChanges := []journalChange(nil)
	if cachedJournal.Kind != "" {
		cursor = cachedJournal
	}
	if cachedSnapshot != nil && cachedSnapshot.Result != nil && cachedJournal.Kind == "" && time.Since(cachedAt) <= analysisFastReuseTTL {
		return reuseCachedAnalysis(cachedSnapshot), nil
	}
	if cachedSnapshot != nil && cachedSnapshot.Result != nil && cachedJournal.Kind != "" {
		nextCursor, changes, complete, journalErr := readJournal(watchRoot, cachedJournal)
		if journalErr == nil && complete {
			changes = relevantJournalChanges(root, changes)
		}
		canReuse := journalErr == nil && complete && len(changes) == 0
		if journalErr == nil && complete && len(changes) > 0 && journalChangesOnlyRoot(watchRoot, changes) {
			// FSEvents may report a coarse event for the watched root without a
			// changed descendant. Reusing a recent result avoids turning that
			// bookkeeping event into another full-volume walk.
			canReuse = true
		}
		if canReuse && previousIndex == nil && time.Since(cachedAt) > analysisFastReuseTTL {
			previousIndex = loadAnalysisIndex(root)
			canReuse = previousIndex != nil && !timeSensitiveCandidatesNeedRefresh(root, previousIndex)
		} else if canReuse && previousIndex != nil {
			canReuse = !timeSensitiveCandidatesNeedRefresh(root, previousIndex)
		}
		// Some environments temporarily deny access to the native journal. A
		// recent completed analysis is still more useful than immediately
		// repeating a multi-million-file cold walk.
		if journalErr != nil && time.Since(cachedAt) <= analysisFastReuseTTL {
			canReuse = true
		}
		if canReuse {
			if journalErr == nil && complete && (len(changes) == 0 || journalChangesOnlyRoot(watchRoot, changes)) && nextCursor != cachedJournal {
				if summary != nil {
					summary.Journal = nextCursor
					_ = writeAnalysisSummary(*summary)
				} else if previousIndex != nil {
					previousIndex.Journal = nextCursor
					_ = saveAnalysisSummary(previousIndex)
				}
			}
			return reuseCachedAnalysis(cachedSnapshot), nil
		}
		if journalErr == nil && complete {
			cursor = nextCursor
			journalChanges = changes
			if len(journalChanges) > 0 && previousIndex == nil {
				previousIndex = loadAnalysisIndex(root)
			}
		}
	}
	// Persistent journals can establish a boundary before the walk. Linux
	// inotify is initialized after the first walk because recursively adding
	// watches itself requires a directory traversal.
	if cursor.Kind == "" && runtime.GOOS != "linux" {
		if current, _, complete, journalErr := readJournal(watchRoot, journalCursor{}); journalErr == nil && complete {
			cursor = current
		}
	}
	if previousIndex == nil && cachedSnapshot != nil {
		previousIndex = loadAnalysisIndex(root)
	}
	var candidates []scanCandidate
	var storage Storage
	var index *analysisIndex
	if previousIndex != nil && len(journalChanges) > 0 {
		if onProgress != nil {
			onProgress(Progress{Phase: "incremental", Scanned: int64(len(journalChanges)), Candidates: int64(len(previousIndex.Candidates))})
		}
		var incrementalOK bool
		candidates, storage, index, incrementalOK, err = incrementalDiscover(root, baseStorage, previousIndex, journalChanges, onProgress)
		if !incrementalOK {
			candidates, storage, index, err = discoverAnalysis(root, baseStorage, onProgress, previousIndex)
		}
	} else {
		candidates, storage, index, err = discoverAnalysis(root, baseStorage, onProgress, previousIndex)
	}
	if err != nil {
		return nil, err
	}
	result := &Result{
		GeneratedAt: time.Now().UTC().Format(time.RFC3339),
		Roots:       []string{root},
		Items:       []Item{},
		Summary:     map[string]TierSummary{string(Safe): {}, string(Caution): {}, string(Review): {}, string(Protected): {}},
	}
	started := time.Now()
	workerCount := runtime.NumCPU() * 2
	if workerCount < 4 {
		workerCount = 4
	}
	if workerCount > 16 {
		workerCount = 16
	}
	jobs := make(chan scanCandidate)
	items := make(chan Item)
	var workers sync.WaitGroup
	var measured atomic.Int64
	var found atomic.Int64
	var filesScanned atomic.Int64
	var lastMeasureReport atomic.Int64
	var indexMu sync.Mutex
	seen := &sync.Map{}
	reportMeasured := func(current int64, path string, item *Item) {
		if onProgress == nil {
			return
		}
		now := time.Now().UnixNano()
		previous := lastMeasureReport.Load()
		if current != 1 && current != int64(len(candidates)) && now-previous < int64(150*time.Millisecond) {
			return
		}
		if current != int64(len(candidates)) && !lastMeasureReport.CompareAndSwap(previous, now) {
			return
		}
		onProgress(Progress{Phase: "measure", Scanned: current, Candidates: int64(len(candidates)), Found: found.Load(), FilesScanned: filesScanned.Load(), Path: path, Item: item})
	}
	if onProgress != nil {
		onProgress(Progress{Phase: "measure", Candidates: int64(len(candidates))})
	}
	for range workerCount {
		workers.Add(1)
		go func() {
			defer workers.Done()
			for candidate := range jobs {
				var foundItem *Item
				if candidate.cached != nil {
					item := *candidate.cached
					items <- item
					foundItem = &item
					found.Add(1)
					current := measured.Add(1)
					reportMeasured(current, candidate.target, foundItem)
					continue
				}
				cacheKey := cacheCandidateKey(candidate.rule.ID, candidate.target)
				indexMu.Lock()
				cached := index.Candidates[cacheKey]
				indexMu.Unlock()
				if candidate.rule.Kind != "command" {
					if item, ok := cachedItem(cached, candidate.target); ok {
						items <- item
						foundItem = &item
						found.Add(1)
						filesScanned.Add(int64(item.Files))
						current := measured.Add(1)
						reportMeasured(current, candidate.target, foundItem)
						continue
					}
				}
				reportFile := func(path string) {
					currentFiles := filesScanned.Add(1)
					if onProgress == nil {
						return
					}
					now := time.Now().UnixNano()
					previous := lastMeasureReport.Load()
					if currentFiles != 1 && now-previous < int64(150*time.Millisecond) {
						return
					}
					if !lastMeasureReport.CompareAndSwap(previous, now) {
						return
					}
					onProgress(Progress{Phase: "measure", Scanned: measured.Load(), Candidates: int64(len(candidates)), Found: found.Load(), FilesScanned: currentFiles, Path: path})
				}
				if item, ok := inspectCandidate(candidate, seen, reportFile); ok {
					items <- item
					foundItem = &item
					found.Add(1)
				}
				modified, size, identity, _ := targetSignature(candidate.target)
				indexMu.Lock()
				cached = index.Candidates[cacheKey]
				cached.RuleID, cached.Target, cached.Project = candidate.rule.ID, candidate.target, candidate.project
				cached.TargetModTime, cached.TargetSize = modified, size
				cached.TargetIdentity = identity
				// Directory entries are safe to reuse only when the filesystem
				// journal says their subtree was untouched. Persist their measured
				// result so incrementalDiscover can take that fast path.
				if foundItem != nil {
					cached.Item = foundItem
				} else {
					cached.Item = nil
				}
				index.Candidates[cacheKey] = cached
				indexMu.Unlock()
				current := measured.Add(1)
				reportMeasured(current, candidate.target, foundItem)
			}
		}()
	}
	go func() {
		for _, candidate := range candidates {
			jobs <- candidate
		}
		close(jobs)
		workers.Wait()
		close(items)
	}()
	for item := range items {
		result.Items = append(result.Items, item)
	}
	sort.Slice(result.Items, func(i, j int) bool { return result.Items[i].Bytes > result.Items[j].Bytes })
	for _, item := range result.Items {
		summary := result.Summary[string(item.Tier)]
		summary.Bytes += item.Bytes
		summary.Count++
		result.Summary[string(item.Tier)] = summary
	}
	result.Groups = makeGroups(result.Items)
	result.Directories = summarizeDirectories(root, index, result.Items)
	result.DurationMS = time.Since(started).Milliseconds()
	storage.Complete = true
	index.Journal = cursor
	if current, changes, complete, journalErr := readJournal(watchRoot, cursor); journalErr == nil && complete {
		if len(changes) == 0 {
			index.Journal = current
		}
	}
	index.Snapshot = &analysisSnapshot{Result: result, Storage: storage}
	if onProgress != nil {
		onProgress(Progress{Phase: "cache", Scanned: int64(len(candidates)), Candidates: int64(len(candidates)), Found: int64(len(result.Items))})
	}
	_ = saveAnalysisIndex(index)
	if onProgress != nil {
		onProgress(Progress{Phase: "done", Scanned: int64(len(candidates)), Candidates: int64(len(candidates)), Found: int64(len(result.Items))})
	}
	return &Analysis{Result: result, Storage: storage}, nil
}

func journalChangesOnlyRoot(root string, changes []journalChange) bool {
	if len(changes) == 0 {
		return false
	}
	cleanRoot := filepath.Clean(root)
	for _, change := range changes {
		if filepath.Clean(change.Path) != cleanRoot || (runtime.GOOS == "darwin" && change.Flags&1 != 0) {
			return false
		}
	}
	return true
}

// Restrict journal deltas to the inventory root before loading and mutating
// the full index. The root inventory itself applies mount-boundary rules.
func relevantJournalChanges(root string, changes []journalChange) []journalChange {
	if len(changes) == 0 || filepath.Clean(root) != string(filepath.Separator) {
		return changes
	}
	coverage := analysisWalkRoots(root)
	for _, rule := range globalRules() {
		for _, target := range rule.Targets {
			if target != "" && isWithin(root, target) {
				coverage = append(coverage, filepath.Clean(target))
			}
		}
	}
	if target := dockerStorageTarget(); target != "" && isWithin(root, target) {
		coverage = append(coverage, filepath.Clean(target))
	}
	home := analysisWatchRoot(root)
	appRoots := knownAppRoots(home)
	result := make([]journalChange, 0, len(changes))
	for _, change := range changes {
		path := filepath.Clean(change.Path)
		if path == home {
			result = append(result, change)
			continue
		}
		if filepath.Dir(path) == home && !strings.HasPrefix(filepath.Base(path), ".") && filepath.Base(path) != "Library" && filepath.Base(path) != "Applications" {
			result = append(result, change)
			continue
		}
		matched := false
		for _, covered := range coverage {
			if isWithin(covered, path) || isWithin(path, covered) {
				result = append(result, change)
				matched = true
				break
			}
		}
		if matched {
			continue
		}
		for _, app := range appRoots {
			if isWithin(path, app.root) {
				result = append(result, change)
				break
			}
			if !isWithin(app.root, path) {
				continue
			}
			relative, err := filepath.Rel(app.root, path)
			if err != nil {
				continue
			}
			name := strings.SplitN(relative, string(filepath.Separator), 2)[0]
			if app.filter == nil || app.filter(name) {
				result = append(result, change)
				break
			}
		}
	}
	return result
}

func analysisWatchRoot(root string) string {
	return filepath.Clean(root)
}

func analysisRoot(root string) (string, error) {
	if root == "" {
		root = string(filepath.Separator)
	}
	if root == "~" {
		root, _ = os.UserHomeDir()
	}
	if strings.HasPrefix(root, "~/") {
		home, _ := os.UserHomeDir()
		root = filepath.Join(home, root[2:])
	}
	root, err := filepath.Abs(root)
	if err != nil {
		return "", err
	}
	if resolved, resolveErr := filepath.EvalSymlinks(root); resolveErr == nil {
		root = resolved
	}
	info, err := os.Stat(root)
	if err != nil {
		return "", err
	}
	if !info.IsDir() {
		return "", fmt.Errorf("scan root is not a directory: %s", root)
	}
	return root, nil
}

func discoverAnalysisLegacy(root string, base Storage, onProgress func(Progress)) ([]scanCandidate, Storage, error) {
	candidates := []scanCandidate{}
	seenCandidates := map[string]bool{}
	seenFiles := &sync.Map{}
	totals := map[string]*StorageCategory{}
	general := generalRules()
	managedCandidates := commandCandidates(root)
	now := time.Now()
	var scanned atomic.Int64
	var lastDiscoverReport time.Time
	addCategory := func(path string, info os.FileInfo) {
		id := FileCategory(path)
		category := totals[id]
		if category == nil {
			category = &StorageCategory{ID: id}
			totals[id] = category
		}
		category.Bytes += allocatedSize(info)
		category.Files++
	}
	storageSnapshot := func() Storage {
		snapshot := base
		snapshot.Categories = []StorageCategory{}
		for _, id := range []string{"video", "photos", "audio", "documents", "archives", "development", "other"} {
			if category := totals[id]; category != nil {
				snapshot.Categories = append(snapshot.Categories, *category)
			}
		}
		return snapshot
	}
	emitProgress := func(path string, force bool) {
		if onProgress == nil {
			return
		}
		if !force && !lastDiscoverReport.IsZero() && time.Since(lastDiscoverReport) < 150*time.Millisecond {
			return
		}
		lastDiscoverReport = time.Now()
		snapshot := storageSnapshot()
		onProgress(Progress{Phase: "discover", Scanned: scanned.Load(), Candidates: int64(len(candidates)), Path: path, Storage: &snapshot})
	}
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			base.Skipped++
			return nil
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return nil
		}
		if entry.IsDir() {
			count := scanned.Add(1)
			emitProgress(path, count == 1)
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			base.Skipped++
			return nil
		}
		if !info.Mode().IsRegular() {
			return nil
		}
		if key := fileIdentity(info); key != "" {
			if _, loaded := seenFiles.LoadOrStore(key, true); loaded {
				return nil
			}
		}
		addCategory(path, info)
		emitProgress(path, false)
		projectDir := filepath.Dir(path)
		if !analysisExcluded(root, projectDir) && !commandOwnsPath(managedCandidates, projectDir) && !isProtected(projectDir) {
			for _, rule := range projectRules {
				for _, marker := range rule.Markers {
					if !markerMatches(marker, entry.Name()) {
						continue
					}
					for _, target := range rule.Targets {
						targetPath := filepath.Join(projectDir, target)
						key := rule.ID + ":" + targetPath
						if !seenCandidates[key] {
							seenCandidates[key] = true
							candidates = append(candidates, scanCandidate{rule: rule, target: targetPath, project: projectDir})
						}
					}
				}
			}
		}
		if strings.HasPrefix(entry.Name(), ".") || analysisExcluded(root, path) || commandOwnsPath(managedCandidates, path) || isProtected(path) {
			return nil
		}
		for _, rule := range general {
			if rule.Kind != "general" {
				continue
			}
			if allocatedSize(info) < rule.MinBytes {
				continue
			}
			if rule.OlderThanDays > 0 && now.Sub(info.ModTime()) < time.Duration(rule.OlderThanDays)*24*time.Hour {
				continue
			}
			key := rule.ID + ":" + path
			if !seenCandidates[key] {
				seenCandidates[key] = true
				candidates = append(candidates, scanCandidate{rule: rule, target: path})
			}
			break
		}
		return nil
	})
	for _, candidate := range managedCandidates {
		key := candidate.rule.ID + ":" + candidate.target
		if !seenCandidates[key] {
			seenCandidates[key] = true
			candidates = append(candidates, candidate)
		}
	}
	storage := storageSnapshot()
	emitProgress(root, true)
	return candidates, storage, err
}

func analysisExcluded(root, path string) bool {
	// These paths are excluded from personal-file and project-marker rules,
	// but the analysis still traverses them so build/cache usage is counted and
	// the corresponding cleanup candidates can be measured.
	relative, err := filepath.Rel(root, path)
	if err != nil {
		return true
	}
	for _, part := range strings.Split(relative, string(filepath.Separator)) {
		switch part {
		case ".git", "node_modules", "target", ".next", ".nuxt", ".turbo", ".parcel-cache", ".venv", "venv", ".npm", ".cache", ".cargo", ".ollama":
			return true
		}
	}
	return false
}
