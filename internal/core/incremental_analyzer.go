package core

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync/atomic"
	"time"
)

func timeSensitiveCandidatesNeedRefresh(root string, index *analysisIndex) bool {
	if index == nil {
		return true
	}
	oldRule := Rule{}
	for _, rule := range generalRules() {
		if rule.ID == "general.old-files" {
			oldRule = rule
			break
		}
	}
	if oldRule.ID == "" {
		return false
	}
	now := time.Now()
	for path, file := range index.Files {
		if strings.HasPrefix(filepath.Base(path), ".") || analysisExcluded(root, path) || isProtected(path) {
			continue
		}
		if file.Bytes < oldRule.MinBytes || now.Sub(time.Unix(0, file.ModTime)) < time.Duration(oldRule.OlderThanDays)*24*time.Hour {
			continue
		}
		key := cacheCandidateKey(oldRule.ID, path)
		if _, ok := index.Candidates[key]; !ok {
			return true
		}
	}
	return false
}

// incrementalDiscover applies journal paths to the previous manifest. It
// returns ok=false when the journal cannot describe the change precisely; the
// caller then performs the conservative full metadata walk.
func incrementalDiscover(root string, base Storage, previous *analysisIndex, changes []journalChange, onProgress func(Progress)) ([]scanCandidate, Storage, *analysisIndex, bool, error) {
	if previous == nil || len(previous.Files) == 0 || len(changes) == 0 {
		return nil, Storage{}, nil, false, nil
	}

	paths := normalizeJournalPaths(root, changes)
	if len(paths) == 0 {
		return nil, Storage{}, nil, false, nil
	}
	for _, path := range paths {
		if path == root || !isWithin(root, path) {
			return nil, Storage{}, nil, false, nil
		}
	}

	index := newAnalysisIndex(root)
	for path, file := range previous.Files {
		index.Files[path] = file
	}
	for key, candidate := range previous.Candidates {
		index.Candidates[key] = candidate
	}
	if previous.Directories != nil {
		index.Directories = make(map[string]directoryAggregate, len(previous.Directories))
		for path, aggregate := range previous.Directories {
			index.Directories[path] = aggregate
		}
	}
	managedCandidates := commandCandidates(root)
	rootInfo, rootInfoErr := os.Stat(root)
	if rootInfoErr != nil {
		return nil, Storage{}, nil, false, nil
	}
	rootDevice := filesystemIdentity(root, rootInfo)
	var manifestScanned atomic.Int64
	var lastManifestReport time.Time
	reportManifest := func(path string) {
		if onProgress == nil {
			return
		}
		count := manifestScanned.Add(1)
		now := time.Now()
		if count != 1 && !lastManifestReport.IsZero() && now.Sub(lastManifestReport) < 150*time.Millisecond {
			return
		}
		lastManifestReport = now
		onProgress(Progress{Phase: "discover", Scanned: count, FilesScanned: count, Candidates: int64(len(index.Candidates)), Path: path})
	}

	removeManifestPaths(index.Files, paths, func(path string, file cachedFile) {
		if index.Directories != nil {
			adjustDirectoryAggregate(root, index.Directories, path, -file.Bytes, -1)
		}
	})
	touchedFiles := map[string]bool{}
	for _, path := range paths {
		if err := addManifestPath(index.Files, root, path, rootDevice, func(file string) {
			touchedFiles[file] = true
			if index.Directories != nil {
				adjustDirectoryAggregate(root, index.Directories, file, index.Files[file].Bytes, 1)
			}
			reportManifest(file)
		}); err != nil {
			return nil, Storage{}, nil, false, nil
		}
	}

	rules := map[string]Rule{}
	for _, rule := range Rules() {
		rules[rule.ID] = rule
	}
	entries := map[string]scanCandidate{}
	directoryPrevious := map[string]cachedCandidate{}
	projectDirs := map[string]bool{}
	for key, candidate := range index.Candidates {
		rule, exists := rules[candidate.RuleID]
		if !exists {
			delete(index.Candidates, key)
			continue
		}
		dirty := candidateAffected(candidate, paths)
		if dirty {
			candidate.Item = nil
			index.Candidates[key] = candidate
		}
		if candidate.Project != "" && dirty {
			projectDirs[filepath.Clean(candidate.Project)] = true
		}
		if candidate.RuleID == "" {
			delete(index.Candidates, key)
			continue
		}
		if candidate.RuleID == "general.large-directory" {
			directoryPrevious[key] = candidate
			delete(index.Candidates, key)
			continue
		}
		if rule.Kind == "general" && pathAffected(candidate.Target, paths) {
			delete(index.Candidates, key)
			continue
		}
		entry := scanCandidate{rule: rule, target: candidate.Target, project: candidate.Project, labels: classifyPath(candidate.Target)}
		if !dirty && candidate.Item != nil {
			cached := *candidate.Item
			entry.cached = &cached
		}
		entries[key] = entry
	}

	for _, path := range paths {
		for dir := filepath.Dir(path); isWithin(root, dir); dir = filepath.Dir(dir) {
			if hasProjectMarker(dir) {
				projectDirs[dir] = true
			}
			if dir == root {
				break
			}
		}
	}
	for key, entry := range entries {
		if entry.project != "" && projectDirs[filepath.Clean(entry.project)] {
			delete(entries, key)
			delete(index.Candidates, key)
		}
	}
	for dir := range projectDirs {
		addProjectEntries(dir, entries, index)
	}

	// App version directories and command-backed providers are cheap to
	// enumerate and can appear or disappear outside the exact journal path.
	// Reconcile them on every incremental pass, retaining cached measurements
	// for untouched entries.
	appPrevious := map[string]cachedCandidate{}
	for key, candidate := range index.Candidates {
		if strings.HasPrefix(candidate.RuleID, "app.") || isManagedCommandRule(candidate.RuleID) {
			appPrevious[key] = candidate
			delete(index.Candidates, key)
			delete(entries, key)
		}
	}
	for _, candidate := range appCandidates(root) {
		key := cacheCandidateKey(candidate.rule.ID, candidate.target)
		entry := candidate
		if cached, ok := appPrevious[key]; ok {
			if candidateAffected(cached, paths) {
				cached.Item = nil
			}
			cached.RuleID, cached.Target, cached.Project = candidate.rule.ID, candidate.target, candidate.project
			index.Candidates[key] = cached
			if cached.Item != nil && candidate.rule.Kind != "command" && !candidateAffected(cached, paths) {
				item := *cached.Item
				entry.cached = &item
			}
		} else {
			index.Candidates[key] = cachedCandidate{RuleID: candidate.rule.ID, Target: candidate.target, Project: candidate.project}
		}
		entries[key] = entry
	}
	for _, candidate := range managedCandidates {
		key := cacheCandidateKey(candidate.rule.ID, candidate.target)
		entry := candidate
		if cached, ok := appPrevious[key]; ok {
			if candidateAffected(cached, paths) {
				cached.Item = nil
			}
			cached.RuleID, cached.Target, cached.Project = candidate.rule.ID, candidate.target, candidate.project
			index.Candidates[key] = cached
			if cached.Item != nil && candidate.rule.Kind != "command" && !candidateAffected(cached, paths) {
				item := *cached.Item
				entry.cached = &item
			}
		} else {
			index.Candidates[key] = cachedCandidate{RuleID: candidate.rule.ID, Target: candidate.target, Project: candidate.project}
		}
		entries[key] = entry
	}

	general := generalRules()
	for filePath := range touchedFiles {
		file, exists := index.Files[filePath]
		if !exists || filepath.Base(filePath) == "" {
			continue
		}
		if strings.HasPrefix(filepath.Base(filePath), ".") || analysisExcluded(root, filePath) || commandOwnsPath(managedCandidates, filePath) || isProtected(filePath) {
			continue
		}
		for _, rule := range general {
			if rule.Kind != "general" {
				continue
			}
			if file.Bytes < rule.MinBytes || (rule.OlderThanDays > 0 && time.Since(time.Unix(0, file.ModTime)) < time.Duration(rule.OlderThanDays)*24*time.Hour) {
				continue
			}
			key := cacheCandidateKey(rule.ID, filePath)
			entries[key] = scanCandidate{rule: rule, target: filePath, labels: classifyPath(filePath)}
			index.Candidates[key] = cachedCandidate{RuleID: rule.ID, Target: filePath}
			break
		}
	}
	for _, candidate := range largeDirectoryCandidates(root, index, managedCandidates) {
		key := cacheCandidateKey(candidate.rule.ID, candidate.target)
		entry := candidate
		if cached, ok := directoryPrevious[key]; ok && !candidateAffected(cached, paths) && cached.Item != nil {
			item := *cached.Item
			entry.cached = &item
		}
		entries[key] = entry
		index.Candidates[key] = cachedCandidate{RuleID: candidate.rule.ID, Target: candidate.target, Project: candidate.project, Item: entry.cached}
	}

	for key, entry := range entries {
		if entry.rule.Action == "command" && len(entry.rule.Targets) > 0 {
			entry.estimatedBytes, entry.estimatedFiles = 0, 0
			for _, target := range entry.rule.Targets {
				if aggregate, ok := index.Directories[filepath.Clean(target)]; ok {
					entry.estimatedBytes += aggregate.Bytes
					entry.estimatedFiles += aggregate.Files
				}
			}
			entries[key] = entry
		} else if aggregate, ok := index.Directories[filepath.Clean(entry.target)]; ok {
			entry.estimatedBytes = aggregate.Bytes
			entry.estimatedFiles = aggregate.Files
			entries[key] = entry
		}
		if _, ok := index.Candidates[key]; !ok {
			index.Candidates[key] = cachedCandidate{RuleID: entry.rule.ID, Target: entry.target, Project: entry.project}
		}
	}

	storage := storageFromManifest(base, index.Files)
	if onProgress != nil {
		onProgress(Progress{Phase: "discover", Scanned: int64(len(index.Files)), Candidates: int64(len(entries)), Path: root, Storage: &storage})
	}
	result := make([]scanCandidate, 0, len(entries))
	for _, entry := range entries {
		result = append(result, entry)
	}
	return result, storage, index, true, nil
}

func isManagedCommandRule(ruleID string) bool {
	switch ruleID {
	case "docker.system-prune", "npm.cache", "pnpm.store", "yarn.cache", "bun.cache", "pip.cache", "uv.cache", "go.build-cache", "go.mod-cache", "cargo.autoclean", "homebrew.cleanup", "conda.cleanup", "pyenv.version", "rustup.toolchain", "rbenv.version", "asdf.version", "mise.version", "android.avd", "ios.simulator":
		return true
	default:
		return false
	}
}

func normalizeJournalPaths(root string, changes []journalChange) []string {
	seen := map[string]bool{}
	rescan := map[string]bool{}
	paths := []string{}
	for _, change := range changes {
		if change.Path == "" {
			continue
		}
		path := filepath.Clean(change.Path)
		if change.Flags&1 != 0 {
			rescan[path] = true
		}
		if !isWithin(root, path) || seen[path] {
			continue
		}
		seen[path] = true
		paths = append(paths, path)
	}
	if len(paths) > 1 {
		filtered := paths[:0]
		for _, path := range paths {
			if rescan[path] || (path != filepath.Clean(root) && path != filepath.Clean(analysisWatchRoot(root))) {
				filtered = append(filtered, path)
			}
		}
		paths = filtered
	}
	return paths
}

func removeManifestPaths(files map[string]cachedFile, paths []string, onRemove func(string, cachedFile)) {
	directories := make([]string, 0, len(paths))
	for _, path := range paths {
		if file, exists := files[path]; exists {
			onRemove(path, file)
			delete(files, path)
			continue
		}
		info, err := os.Lstat(path)
		if err == nil && !info.IsDir() {
			delete(files, path)
		}
		// A deleted path may have been a directory. Remove its old descendants
		// in one pass along with all other changed directories.
		directories = append(directories, filepath.Clean(path)+string(filepath.Separator))
	}
	if len(directories) == 0 {
		return
	}
	sort.Strings(directories)
	compact := directories[:0]
	for _, directory := range directories {
		if len(compact) == 0 || !strings.HasPrefix(directory, compact[len(compact)-1]) {
			compact = append(compact, directory)
		}
	}
	for path := range files {
		position := sort.SearchStrings(compact, path)
		if position > 0 && strings.HasPrefix(path, compact[position-1]) {
			onRemove(path, files[path])
			delete(files, path)
		}
	}
}

func addManifestPath(files map[string]cachedFile, root, path, rootDevice string, onFile func(string)) error {
	if isDataVolumeAlias(path) {
		return nil
	}
	info, err := os.Lstat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return nil
	}
	if isOtherFilesystem(rootDevice, path, info) {
		return nil
	}
	if !info.IsDir() {
		if info.Mode().IsRegular() {
			files[path] = cachedFile{Category: FileCategory(path), Labels: classifyPath(path), Bytes: allocatedSize(info), ModTime: info.ModTime().UnixNano(), Size: info.Size(), Identity: fileIdentity(info)}
			if onFile != nil {
				onFile(path)
			}
		}
		return nil
	}
	return filepath.WalkDir(path, func(current string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.Type()&os.ModeSymlink != 0 {
			if entry.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if entry.IsDir() {
			info, err := entry.Info()
			if err != nil || isDataVolumeAlias(current) || isOtherFilesystem(rootDevice, current, info) {
				return filepath.SkipDir
			}
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if info.Mode().IsRegular() {
			files[current] = cachedFile{Category: FileCategory(current), Labels: classifyPath(current), Bytes: allocatedSize(info), ModTime: info.ModTime().UnixNano(), Size: info.Size(), Identity: fileIdentity(info)}
			if onFile != nil {
				onFile(current)
			}
		}
		return nil
	})
}

func storageFromManifest(base Storage, files map[string]cachedFile) Storage {
	totals := map[string]StorageCategory{}
	seen := map[string]bool{}
	for _, file := range files {
		if file.Identity != "" && seen[file.Identity] {
			continue
		}
		if file.Identity != "" {
			seen[file.Identity] = true
		}
		category := totals[file.Category]
		category.ID = file.Category
		category.Bytes += file.Bytes
		category.Files++
		totals[file.Category] = category
	}
	result := base
	result.Categories = []StorageCategory{}
	for _, id := range []string{"video", "photos", "audio", "documents", "archives", "development", "other"} {
		if category, ok := totals[id]; ok {
			result.Categories = append(result.Categories, category)
		}
	}
	return result
}

func pathAffected(path string, roots []string) bool {
	for _, root := range roots {
		rootPrefix := strings.TrimRight(root, string(filepath.Separator)) + string(filepath.Separator)
		pathPrefix := strings.TrimRight(path, string(filepath.Separator)) + string(filepath.Separator)
		if path == root || strings.HasPrefix(path, rootPrefix) || strings.HasPrefix(root, pathPrefix) {
			return true
		}
	}
	return false
}

func candidateAffected(candidate cachedCandidate, roots []string) bool {
	if pathAffected(candidate.Target, roots) {
		return true
	}
	return candidate.Project != "" && pathAffected(candidate.Project, roots)
}

func hasProjectMarker(dir string) bool {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return false
	}
	for _, entry := range entries {
		for _, rule := range projectRules {
			for _, marker := range rule.Markers {
				if markerMatches(marker, entry.Name()) {
					return true
				}
			}
		}
	}
	return false
}

func addProjectEntries(dir string, entries map[string]scanCandidate, index *analysisIndex) {
	if isProtected(dir) {
		return
	}
	items, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	names := map[string]bool{}
	for _, entry := range items {
		names[entry.Name()] = true
	}
	for _, rule := range projectRules {
		matched := false
		for _, marker := range rule.Markers {
			for name := range names {
				if markerMatches(marker, name) {
					matched = true
					break
				}
			}
			if matched {
				break
			}
		}
		if !matched {
			continue
		}
		for _, target := range rule.Targets {
			path := filepath.Join(dir, target)
			key := cacheCandidateKey(rule.ID, path)
			entries[key] = scanCandidate{rule: rule, target: path, project: dir, labels: classifyPath(path)}
			cached := index.Candidates[key]
			cached.RuleID, cached.Target, cached.Project = rule.ID, path, dir
			index.Candidates[key] = cached
		}
	}
}
