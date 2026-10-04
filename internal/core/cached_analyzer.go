package core

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

func discoverAnalysis(root string, base Storage, onProgress func(Progress), previous *analysisIndex) ([]scanCandidate, Storage, *analysisIndex, error) {
	index := newAnalysisIndex(root)
	candidates := []scanCandidate{}
	seenCandidates := map[string]bool{}
	totals := map[string]*StorageCategory{}
	managedCandidates := commandCandidates(root)
	var projectMarkers sync.Map
	rootInfo, err := os.Stat(root)
	if err != nil {
		return nil, Storage{}, nil, err
	}
	rootDevice := filesystemIdentity(root, rootInfo)
	var scanned atomic.Int64
	var filesScanned atomic.Int64
	var skipped atomic.Int64
	var stateMu sync.Mutex
	var reportMu sync.Mutex
	var lastReport time.Time
	general := generalRules()

	addTotal := func(category StorageCategory) {
		current := totals[category.ID]
		if current == nil {
			current = &StorageCategory{ID: category.ID}
			totals[category.ID] = current
		}
		current.Bytes += category.Bytes
		current.Files += category.Files
	}
	addFile := func(path string, info os.FileInfo) {
		filesScanned.Add(1)
		stateMu.Lock()
		index.Files[path] = cachedFile{
			Bytes:    allocatedSize(info),
			ModTime:  info.ModTime().UnixNano(),
			Size:     info.Size(),
			Identity: fileIdentity(info),
		}
		stateMu.Unlock()
	}
	storageSnapshot := func() Storage {
		snapshot := base
		stateMu.Lock()
		defer stateMu.Unlock()
		snapshot.Categories = []StorageCategory{}
		for _, id := range []string{"video", "photos", "audio", "documents", "archives", "development", "other"} {
			if category := totals[id]; category != nil {
				snapshot.Categories = append(snapshot.Categories, *category)
			}
		}
		snapshot.Skipped = int(skipped.Load())
		return snapshot
	}
	emitProgress := func(path string, force bool) {
		if onProgress == nil {
			return
		}
		reportMu.Lock()
		if !force && !lastReport.IsZero() && time.Since(lastReport) < 150*time.Millisecond {
			reportMu.Unlock()
			return
		}
		lastReport = time.Now()
		reportMu.Unlock()
		snapshot := storageSnapshot()
		candidateCount := len(candidates)
		onProgress(Progress{
			Phase: "discover", Scanned: scanned.Load(), Candidates: int64(candidateCount), FilesScanned: filesScanned.Load(), Path: path,
			Storage: &snapshot,
		})
	}
	addCandidateEntry := func(candidate scanCandidate) {
		if candidate.rule.Action != "command" && commandOwnsPath(managedCandidates, candidate.target) {
			return
		}
		if candidate.rule.Action == "command" && len(candidate.rule.Targets) > 0 {
			candidate.estimatedBytes, candidate.estimatedFiles = 0, 0
			for _, target := range candidate.rule.Targets {
				if aggregate, ok := index.Directories[filepath.Clean(target)]; ok {
					candidate.estimatedBytes += aggregate.Bytes
					candidate.estimatedFiles += aggregate.Files
				}
			}
		} else if aggregate, ok := index.Directories[filepath.Clean(candidate.target)]; ok && candidate.estimatedBytes == 0 {
			candidate.estimatedBytes = aggregate.Bytes
			candidate.estimatedFiles = aggregate.Files
		}
		candidate.labels = mergeLabels(candidate.labels, classifyPath(candidate.target))
		key := cacheCandidateKey(candidate.rule.ID, candidate.target)
		if seenCandidates[key] {
			return
		}
		seenCandidates[key] = true
		candidates = append(candidates, candidate)
		if previous != nil {
			if cached, ok := previous.Candidates[key]; ok {
				index.Candidates[key] = cached
			}
		}
		cached := index.Candidates[key]
		cached.RuleID, cached.Target, cached.Project = candidate.rule.ID, candidate.target, candidate.project
		index.Candidates[key] = cached
	}
	processDirectory := func(path string) []string {
		if isDataVolumeAlias(path) {
			return nil
		}
		info, err := os.Lstat(path)
		if err != nil {
			return nil
		}
		if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() || isOtherFilesystem(rootDevice, path, info) {
			return nil
		}

		scanned.Add(1)
		emitProgress(path, scanned.Load() == 1)
		entries, err := os.ReadDir(path)
		if err != nil {
			skipped.Add(1)
			return nil
		}
		children := make([]string, 0, len(entries))
		for _, entry := range entries {
			child := filepath.Join(path, entry.Name())
			if isProjectMarker(entry.Name()) {
				projectMarkers.Store(child, struct{}{})
			}
			if entry.IsDir() {
				if !isDataVolumeAlias(child) {
					children = append(children, child)
				}
				continue
			}
			if entry.Type()&os.ModeSymlink != 0 {
				continue
			}
			fileInfo, infoErr := entry.Info()
			if infoErr != nil {
				skipped.Add(1)
				continue
			}
			if !fileInfo.Mode().IsRegular() {
				continue
			}
			addFile(child, fileInfo)
			emitProgress(child, false)
		}
		return children
	}

	// Directory enumeration is I/O-bound. Keep a bounded number of readers so
	// a root scan can use the machine's parallel filesystem queues without
	// creating one goroutine per directory.
	workerCount := runtime.NumCPU()
	if workerCount < 4 {
		workerCount = 4
	}
	if workerCount > 16 {
		workerCount = 16
	}
	directoryJobs := make(chan string)
	discovered := make(chan string)
	finished := make(chan struct{})
	var workers sync.WaitGroup
	for range workerCount {
		workers.Add(1)
		go func() {
			defer workers.Done()
			for path := range directoryJobs {
				for _, child := range processDirectory(path) {
					discovered <- child
				}
				finished <- struct{}{}
			}
		}()
	}
	queue := []string{root}
	active := 0
	for len(queue) > 0 || active > 0 {
		var nextJob chan string
		var nextPath string
		if len(queue) > 0 && active < workerCount {
			nextJob = directoryJobs
			nextPath = queue[0]
		}
		select {
		case nextJob <- nextPath:
			queue = queue[1:]
			active++
		case child := <-discovered:
			queue = append(queue, child)
		case <-finished:
			active--
		}
	}
	close(directoryJobs)
	workers.Wait()
	emitProgress(root, true)
	index.Directories = make(map[string]directoryAggregate)
	for path, file := range index.Files {
		adjustDirectoryAggregate(root, index.Directories, path, file.Bytes, 1)
	}
	if onProgress != nil {
		onProgress(Progress{Phase: "classify", Scanned: 0, Candidates: int64(len(index.Files))})
	}
	uniqueStorageFiles := map[string]struct{}{}
	classified := int64(0)
	lastClassifyReport := time.Now()
	for path, file := range index.Files {
		classified++
		file.Category = FileCategory(path)
		file.Labels = classifyPath(path)
		index.Files[path] = file
		if file.Identity == "" {
			addTotal(StorageCategory{ID: file.Category, Bytes: file.Bytes, Files: 1})
		} else if _, loaded := uniqueStorageFiles[file.Identity]; !loaded {
			uniqueStorageFiles[file.Identity] = struct{}{}
			addTotal(StorageCategory{ID: file.Category, Bytes: file.Bytes, Files: 1})
		}
		if !strings.HasPrefix(filepath.Base(path), ".") && !analysisExcluded(root, path) && !isProtected(path) && !commandOwnsPath(managedCandidates, path) {
			for _, rule := range general {
				if rule.Kind != "general" {
					continue
				}
				if file.Bytes < rule.MinBytes {
					continue
				}
				if rule.OlderThanDays > 0 && time.Since(time.Unix(0, file.ModTime)) < time.Duration(rule.OlderThanDays)*24*time.Hour {
					continue
				}
				addCandidateEntry(scanCandidate{rule: rule, target: path, labels: file.Labels})
				break
			}
		}
		if onProgress != nil && time.Since(lastClassifyReport) >= 150*time.Millisecond {
			snapshot := storageSnapshot()
			onProgress(Progress{Phase: "classify", Scanned: classified, Candidates: int64(len(index.Files)), Found: int64(len(candidates)), Path: path, Storage: &snapshot})
			lastClassifyReport = time.Now()
		}
	}
	for _, candidate := range largeDirectoryCandidates(root, index, managedCandidates) {
		addCandidateEntry(candidate)
	}
	projectMarkerPaths := []string{}
	projectMarkers.Range(func(path, _ any) bool {
		projectMarkerPaths = append(projectMarkerPaths, path.(string))
		return true
	})
	for _, markerPath := range projectMarkerPaths {
		projectDir := filepath.Dir(markerPath)
		if analysisExcluded(root, projectDir) || isProtected(projectDir) || commandOwnsPath(managedCandidates, projectDir) {
			continue
		}
		for _, rule := range projectRules {
			for _, marker := range rule.Markers {
				if markerMatches(marker, filepath.Base(markerPath)) {
					for _, target := range rule.Targets {
						addCandidateEntry(scanCandidate{rule: rule, target: filepath.Join(projectDir, target), project: projectDir})
					}
				}
			}
		}
	}
	for _, rule := range globalRules() {
		if rule.Action == "command" {
			continue
		}
		for _, target := range rule.Targets {
			if target != "" && isWithin(root, target) {
				addCandidateEntry(scanCandidate{rule: rule, target: target})
			}
		}
	}
	for _, candidate := range appCandidates(root) {
		addCandidateEntry(candidate)
	}
	for _, candidate := range managedCandidates {
		addCandidateEntry(candidate)
	}
	if onProgress != nil {
		storage := storageSnapshot()
		onProgress(Progress{Phase: "classify", Scanned: int64(len(index.Files)), Candidates: int64(len(index.Files)), Found: int64(len(candidates)), Path: root, Storage: &storage})
	}
	return candidates, storageSnapshot(), index, nil
}

func analysisWalkRoots(root string) []string {
	return []string{filepath.Clean(root)}
}

func isProjectMarker(name string) bool {
	for _, rule := range projectRules {
		for _, marker := range rule.Markers {
			if markerMatches(marker, name) {
				return true
			}
		}
	}
	return false
}

func isDataVolumeAlias(path string) bool {
	if runtime.GOOS != "darwin" {
		return false
	}
	dataVolume := filepath.Join(string(filepath.Separator), "System", "Volumes", "Data")
	return filepath.Clean(path) == dataVolume || isWithin(dataVolume, filepath.Clean(path))
}
