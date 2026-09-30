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
	baseStorage, err := storageInfoAt(root)
	if err != nil {
		return nil, err
	}
	candidates, storage, err := discoverAnalysis(root, baseStorage, onProgress)
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
	seen := &sync.Map{}
	if onProgress != nil {
		onProgress(Progress{Phase: "measure", Candidates: int64(len(candidates))})
	}
	for range workerCount {
		workers.Add(1)
		go func() {
			defer workers.Done()
			for candidate := range jobs {
				var foundItem *Item
				if item, ok := inspect(candidate.rule, candidate.target, candidate.project, seen); ok {
					items <- item
					foundItem = &item
					found.Add(1)
				}
				current := measured.Add(1)
				if onProgress != nil {
					onProgress(Progress{Phase: "measure", Scanned: current, Candidates: int64(len(candidates)), Found: found.Load(), Path: candidate.target, Item: foundItem})
				}
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
	result.DurationMS = time.Since(started).Milliseconds()
	storage.Complete = true
	if onProgress != nil {
		onProgress(Progress{Phase: "done", Scanned: int64(len(candidates)), Candidates: int64(len(candidates)), Found: int64(len(result.Items))})
	}
	return &Analysis{Result: result, Storage: storage}, nil
}

func analysisRoot(root string) (string, error) {
	if root == "" || root == "~" {
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
	info, err := os.Stat(root)
	if err != nil {
		return "", err
	}
	if !info.IsDir() {
		return "", fmt.Errorf("scan root is not a directory: %s", root)
	}
	return root, nil
}

func discoverAnalysis(root string, base Storage, onProgress func(Progress)) ([]scanCandidate, Storage, error) {
	candidates := []scanCandidate{}
	seenCandidates := map[string]bool{}
	seenFiles := &sync.Map{}
	totals := map[string]*StorageCategory{}
	general := generalRules()
	now := time.Now()
	var scanned atomic.Int64
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
			if onProgress != nil && (count == 1 || count%200 == 0) {
				onProgress(Progress{Phase: "discover", Scanned: count, Candidates: int64(len(candidates)), Path: path})
			}
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
		projectDir := filepath.Dir(path)
		for _, rule := range projectRules {
			for _, marker := range rule.Markers {
				if marker != entry.Name() {
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
		if strings.HasPrefix(entry.Name(), ".") || analysisExcluded(root, path) {
			return nil
		}
		for _, rule := range general {
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
	storage := base
	storage.Categories = []StorageCategory{}
	for _, id := range []string{"video", "photos", "audio", "documents", "archives", "development", "other"} {
		if category := totals[id]; category != nil {
			storage.Categories = append(storage.Categories, *category)
		}
	}
	return candidates, storage, err
}

func analysisExcluded(root, path string) bool {
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
