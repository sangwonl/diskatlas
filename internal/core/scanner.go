package core

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

type Tier string

const (
	Safe      Tier = "safe"
	Caution   Tier = "caution"
	Review    Tier = "review"
	Protected Tier = "protected"
)

type Rule struct {
	ID            string   `json:"id"`
	Name          string   `json:"name"`
	Kind          string   `json:"kind"`
	Markers       []string `json:"markers,omitempty"`
	Targets       []string `json:"targets,omitempty"`
	Tier          Tier     `json:"tier"`
	Category      string   `json:"category"`
	Rebuild       string   `json:"rebuild"`
	Native        string   `json:"native,omitempty"`
	Cost          string   `json:"cost"`
	MinBytes      int64    `json:"minBytes,omitempty"`
	OlderThanDays int      `json:"olderThanDays,omitempty"`
}

type Item struct {
	ID          string   `json:"id"`
	RuleID      string   `json:"ruleId"`
	Name        string   `json:"name"`
	Path        string   `json:"path"`
	ProjectPath string   `json:"projectPath,omitempty"`
	Category    string   `json:"category"`
	Tier        Tier     `json:"tier"`
	Bytes       int64    `json:"bytes"`
	Files       int      `json:"files"`
	ModifiedAt  string   `json:"modifiedAt,omitempty"`
	Signals     []string `json:"signals,omitempty"`
	Explanation string   `json:"explanation"`
	Rebuild     string   `json:"rebuild"`
	Native      string   `json:"native,omitempty"`
	Cost        string   `json:"cost"`
	Clean       string   `json:"clean"`
}

type TierSummary struct {
	Bytes int64 `json:"bytes"`
	Count int   `json:"count"`
}

type Group struct {
	ID          string   `json:"id"`
	Title       string   `json:"title"`
	Description string   `json:"description"`
	Tier        Tier     `json:"tier"`
	Category    string   `json:"category"`
	Bytes       int64    `json:"bytes"`
	Count       int      `json:"count"`
	ItemIDs     []string `json:"itemIds"`
}

type Progress struct {
	Phase      string `json:"phase"`
	Scanned    int64  `json:"scanned"`
	Candidates int64  `json:"candidates"`
	Path       string `json:"path,omitempty"`
}

type Result struct {
	GeneratedAt string                 `json:"generatedAt"`
	Roots       []string               `json:"roots"`
	Items       []Item                 `json:"items"`
	Summary     map[string]TierSummary `json:"summary"`
	Groups      []Group                `json:"groups"`
	DurationMS  int64                  `json:"durationMs"`
}

var projectRules = []Rule{
	{ID: "node.modules", Name: "Node.js dependencies", Kind: "project", Markers: []string{"package.json"}, Targets: []string{"node_modules"}, Tier: Safe, Category: "Project output", Rebuild: "npm ci", Cost: "low"},
	{ID: "node.build", Name: "Node.js build cache", Kind: "project", Markers: []string{"package.json"}, Targets: []string{".next", ".nuxt", ".turbo", ".parcel-cache"}, Tier: Safe, Category: "Project output", Rebuild: "npm run build", Cost: "low"},
	{ID: "rust.target", Name: "Rust build output", Kind: "project", Markers: []string{"Cargo.toml"}, Targets: []string{"target"}, Tier: Safe, Category: "Project output", Rebuild: "cargo build", Cost: "low"},
	{ID: "python.cache", Name: "Python build cache", Kind: "project", Markers: []string{"pyproject.toml", "requirements.txt"}, Targets: []string{"__pycache__", ".pytest_cache", ".mypy_cache", ".ruff_cache"}, Tier: Safe, Category: "Project output", Rebuild: "python -m pip install", Cost: "low"},
	{ID: "python.venv", Name: "Python virtual environment", Kind: "project", Markers: []string{"pyproject.toml", "requirements.txt"}, Targets: []string{".venv", "venv"}, Tier: Caution, Category: "Project output", Rebuild: "python -m venv .venv", Cost: "medium"},
	{ID: "swift.build", Name: "Swift package build output", Kind: "project", Markers: []string{"Package.swift"}, Targets: []string{".build"}, Tier: Safe, Category: "Project output", Rebuild: "swift build", Cost: "low"},
}

func Rules() []Rule {
	result := append([]Rule{}, projectRules...)
	result = append(result, globalRules()...)
	result = append(result, generalRules()...)
	return result
}

func generalRules() []Rule {
	return []Rule{
		{ID: "general.large-files", Name: "Large files", Kind: "general", Tier: Review, Category: "Personal files", Rebuild: "Keep, archive, or move to external storage", Cost: "high", MinBytes: 500 * 1024 * 1024},
		{ID: "general.old-files", Name: "Large files not opened recently", Kind: "general", Tier: Review, Category: "Personal files", Rebuild: "Review before deleting or archive externally", Cost: "high", MinBytes: 100 * 1024 * 1024, OlderThanDays: 180},
	}
}

func globalRules() []Rule {
	home, _ := os.UserHomeDir()
	cache := func(defaultPath string) string { return filepath.Join(home, defaultPath) }
	rules := []Rule{
		{ID: "npm.cache", Name: "npm cache", Kind: "global", Tier: Safe, Category: "Package cache", Rebuild: "npm install", Native: "npm cache clean --force", Cost: "low"},
		{ID: "pip.cache", Name: "pip cache", Kind: "global", Tier: Safe, Category: "Package cache", Rebuild: "python -m pip install", Native: "pip cache purge", Cost: "low"},
		{ID: "go.build-cache", Name: "Go build cache", Kind: "global", Tier: Safe, Category: "Build cache", Rebuild: "go build ./...", Native: "go clean -cache", Cost: "low"},
		{ID: "cargo.registry", Name: "Cargo registry", Kind: "global", Tier: Safe, Category: "Package cache", Rebuild: "cargo fetch", Native: "cargo cache --autoclean", Cost: "medium"},
		{ID: "xcode.derived-data", Name: "Xcode DerivedData", Kind: "global", Tier: Safe, Category: "IDE cache", Rebuild: "Build the Xcode project again", Native: "Close Xcode before cleanup", Cost: "medium"},
		{ID: "ollama.models", Name: "Ollama models", Kind: "global", Tier: Caution, Category: "AI model", Rebuild: "ollama pull <model>", Cost: "high"},
	}
	for i := range rules {
		switch rules[i].ID {
		case "npm.cache":
			rules[i].Targets = []string{filepath.Join(home, ".npm", "_cacache")}
		case "pip.cache":
			if runtime.GOOS == "darwin" {
				rules[i].Targets = []string{cache(filepath.Join("Library", "Caches", "pip"))}
			} else {
				rules[i].Targets = []string{cache(filepath.Join(".cache", "pip"))}
			}
		case "go.build-cache":
			if runtime.GOOS == "darwin" {
				rules[i].Targets = []string{cache(filepath.Join("Library", "Caches", "go-build"))}
			} else {
				rules[i].Targets = []string{cache(filepath.Join(".cache", "go-build"))}
			}
		case "cargo.registry":
			rules[i].Targets = []string{cache(filepath.Join(".cargo", "registry"))}
		case "xcode.derived-data":
			if runtime.GOOS == "darwin" {
				rules[i].Targets = []string{cache(filepath.Join("Library", "Developer", "Xcode", "DerivedData"))}
			}
		case "ollama.models":
			rules[i].Targets = []string{cache(".ollama/models")}
		}
	}
	return rules
}

type scanCandidate struct {
	rule    Rule
	target  string
	project string
}

func Scan(root string) (*Result, error) {
	return ScanWithProgress(root, nil)
}

func ScanWithProgress(root string, onProgress func(Progress)) (*Result, error) {
	started := time.Now()
	if root == "" || root == "~" {
		root, _ = os.UserHomeDir()
	}
	if strings.HasPrefix(root, "~/") {
		home, _ := os.UserHomeDir()
		root = filepath.Join(home, root[2:])
	}
	root, err := filepath.Abs(root)
	if err != nil {
		return nil, err
	}
	info, err := os.Stat(root)
	if err != nil {
		return nil, err
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("scan root is not a directory: %s", root)
	}
	result := &Result{GeneratedAt: time.Now().UTC().Format(time.RFC3339), Roots: []string{root}, Items: []Item{}, Summary: map[string]TierSummary{string(Safe): {}, string(Caution): {}, string(Review): {}, string(Protected): {}}}
	candidates := make([]scanCandidate, 0, 128)
	for _, rule := range globalRules() {
		for _, target := range rule.Targets {
			if target == "" || !isWithin(root, target) {
				continue
			}
			candidates = append(candidates, scanCandidate{rule: rule, target: target})
		}
	}
	projectCandidates, err := discoverProjects(root, onProgress)
	if err != nil {
		return nil, err
	}
	candidates = append(candidates, projectCandidates...)
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
	seen := &sync.Map{}
	for range workerCount {
		workers.Add(1)
		go func() {
			defer workers.Done()
			for candidate := range jobs {
				if item, ok := inspect(candidate.rule, candidate.target, candidate.project, seen); ok {
					items <- item
				}
				current := measured.Add(1)
				if onProgress != nil {
					onProgress(Progress{Phase: "measure", Scanned: current, Candidates: int64(len(candidates)), Path: candidate.target})
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
		s := result.Summary[string(item.Tier)]
		s.Bytes += item.Bytes
		s.Count++
		result.Summary[string(item.Tier)] = s
	}
	result.Groups = makeGroups(result.Items)
	result.DurationMS = time.Since(started).Milliseconds()
	if onProgress != nil {
		onProgress(Progress{Phase: "done", Scanned: int64(len(candidates)), Candidates: int64(len(result.Items))})
	}
	return result, nil
}

func discoverProjects(root string, onProgress func(Progress)) ([]scanCandidate, error) {
	candidates := []scanCandidate{}
	seenCandidates := map[string]bool{}
	general := generalRules()
	now := time.Now()
	var scanned atomic.Int64
	err := filepath.WalkDir(root, func(dir string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return nil
		}
		if entry.IsDir() && entry.Name() != filepath.Base(root) {
			for _, skip := range []string{".git", "node_modules", "target", ".next", ".nuxt", ".turbo", ".parcel-cache", ".venv", "venv", ".npm", ".cache", ".cargo", ".ollama", "Library", "Applications"} {
				if entry.Name() == skip {
					return filepath.SkipDir
				}
			}
		}
		if !entry.IsDir() {
			if entry.Type()&os.ModeSymlink == 0 && !strings.HasPrefix(entry.Name(), ".") {
				info, infoErr := entry.Info()
				if infoErr == nil && info.Mode().IsRegular() {
					for _, rule := range general {
						if info.Size() < rule.MinBytes {
							continue
						}
						if rule.OlderThanDays > 0 && now.Sub(info.ModTime()) < time.Duration(rule.OlderThanDays)*24*time.Hour {
							continue
						}
						key := rule.ID + ":" + dir
						if !seenCandidates[key] {
							seenCandidates[key] = true
							candidates = append(candidates, scanCandidate{rule: rule, target: dir})
						}
						break
					}
				}
			}
			return nil
		}
		count := scanned.Add(1)
		if onProgress != nil && (count == 1 || count%200 == 0) {
			onProgress(Progress{Phase: "discover", Scanned: count, Candidates: int64(len(candidates)), Path: dir})
		}
		entries, err := os.ReadDir(dir)
		if err != nil {
			return nil
		}
		names := map[string]bool{}
		for _, child := range entries {
			names[child.Name()] = true
		}
		for _, rule := range projectRules {
			matched := false
			for _, marker := range rule.Markers {
				if names[marker] {
					matched = true
					break
				}
			}
			if !matched {
				continue
			}
			for _, target := range rule.Targets {
				targetPath := filepath.Join(dir, target)
				key := rule.ID + ":" + targetPath
				if !seenCandidates[key] {
					seenCandidates[key] = true
					candidates = append(candidates, scanCandidate{rule: rule, target: targetPath, project: dir})
				}
			}
		}
		return nil
	})
	return candidates, err
}

func inspect(rule Rule, target, project string, seen *sync.Map) (Item, bool) {
	info, err := os.Lstat(target)
	if err != nil || info.Mode()&os.ModeSymlink != 0 {
		return Item{}, false
	}
	bytes, files := measure(target, seen)
	if bytes == 0 {
		return Item{}, false
	}
	tier := rule.Tier
	signals := []string{}
	if isProtected(target) {
		tier = Protected
		signals = append(signals, "Protected system, credential, or application path")
	}
	if project != "" && rule.ID == "node.modules" {
		if !hasAny(project, []string{"package-lock.json", "pnpm-lock.yaml", "yarn.lock", "bun.lockb"}) {
			tier = Caution
			signals = append(signals, "No lockfile was found")
		} else {
			signals = append(signals, "A lockfile can reproduce exact dependency versions")
		}
	}
	if project != "" {
		if gitTracked(project, target) {
			if tier != Protected {
				tier = Review
			}
			signals = append(signals, "Git tracks files in this path; explicit review is required")
		} else if gitIgnored(project, target) {
			signals = append(signals, "Git ignores this generated path")
		}
	}
	explanation := fmt.Sprintf("%s can be recreated with %s.", rule.Name, rule.Rebuild)
	if rule.Kind == "general" {
		if rule.ID == "general.large-files" {
			signals = append(signals, fmt.Sprintf("Regular file larger than %s", formatBytes(rule.MinBytes)))
			explanation = "This is a large personal file, not a rebuildable cache. Review its contents before deleting or archive it elsewhere."
		} else {
			signals = append(signals, fmt.Sprintf("No modification in the last %d days", rule.OlderThanDays))
			explanation = "This large personal file has not changed recently. Confirm that it is still needed before deleting or archiving it."
		}
	}
	if tier == Review {
		if rule.Kind != "general" {
			explanation = fmt.Sprintf("%s contains user-managed data and requires manual review.", rule.Name)
		}
	}
	if tier == Protected {
		explanation = "Protected by Shed safety rules and cannot be selected."
	}
	return Item{ID: rule.ID + ":" + target, RuleID: rule.ID, Name: rule.Name, Path: target, ProjectPath: project, Category: rule.Category, Tier: tier, Bytes: bytes, Files: files, ModifiedAt: info.ModTime().UTC().Format(time.RFC3339), Signals: signals, Explanation: explanation, Rebuild: rule.Rebuild, Native: rule.Native, Cost: rule.Cost, Clean: map[bool]string{true: "blocked", false: "quarantine"}[tier == Protected]}, true
}

func formatBytes(bytes int64) string {
	value := float64(bytes)
	units := []string{"B", "KB", "MB", "GB", "TB"}
	index := 0
	for value >= 1024 && index < len(units)-1 {
		value /= 1024
		index++
	}
	return fmt.Sprintf("%.1f %s", value, units[index])
}

func measure(target string, seen *sync.Map) (int64, int) {
	info, err := os.Lstat(target)
	if err != nil || info.Mode()&os.ModeSymlink != 0 {
		return 0, 0
	}
	if !info.IsDir() {
		if key := fileIdentity(info); key != "" {
			if _, loaded := seen.LoadOrStore(key, true); loaded {
				return 0, 0
			}
		}
		return info.Size(), 1
	}
	var bytes int64
	files := 0
	entries, err := os.ReadDir(target)
	if err != nil {
		return 0, 0
	}
	for _, entry := range entries {
		child := filepath.Join(target, entry.Name())
		b, f := measure(child, seen)
		bytes += b
		files += f
	}
	return bytes, files
}

func fileIdentity(info os.FileInfo) string {
	value := reflect.Indirect(reflect.ValueOf(info.Sys()))
	if !value.IsValid() || value.Kind() != reflect.Struct {
		return ""
	}
	dev, ino := value.FieldByName("Dev"), value.FieldByName("Ino")
	if !dev.IsValid() || !ino.IsValid() {
		return ""
	}
	devNumber, inoNumber := reflectNumber(dev), reflectNumber(ino)
	if devNumber == "" || inoNumber == "" {
		return ""
	}
	return devNumber + ":" + inoNumber
}

func reflectNumber(value reflect.Value) string {
	switch value.Kind() {
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return strconv.FormatInt(value.Int(), 10)
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		return strconv.FormatUint(value.Uint(), 10)
	default:
		return ""
	}
}

func gitTracked(project, target string) bool {
	relative, err := filepath.Rel(project, target)
	if err != nil {
		return false
	}
	output, err := exec.Command("git", "-C", project, "ls-files", "--", relative).Output()
	return err == nil && strings.TrimSpace(string(output)) != ""
}

func gitIgnored(project, target string) bool {
	relative, err := filepath.Rel(project, target)
	if err != nil {
		return false
	}
	return exec.Command("git", "-C", project, "check-ignore", "-q", "--", relative).Run() == nil
}

func makeGroups(items []Item) []Group {
	groups := map[string]*Group{}
	for _, item := range items {
		if item.Tier == Protected {
			continue
		}
		key := string(item.Tier) + ":" + item.RuleID
		group := groups[key]
		if group == nil {
			group = &Group{ID: key, Title: item.Name, Description: groupDescription(item), Tier: item.Tier, Category: item.Category, ItemIDs: []string{}}
			groups[key] = group
		}
		group.Bytes += item.Bytes
		group.Count++
		group.ItemIDs = append(group.ItemIDs, item.ID)
	}
	result := make([]Group, 0, len(groups))
	for _, group := range groups {
		result = append(result, *group)
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].Tier != result[j].Tier {
			return tierRank(result[i].Tier) < tierRank(result[j].Tier)
		}
		return result[i].Bytes > result[j].Bytes
	})
	return result
}

func tierRank(tier Tier) int {
	switch tier {
	case Safe:
		return 0
	case Caution:
		return 1
	case Review:
		return 2
	default:
		return 3
	}
}

func groupDescription(item Item) string {
	if item.Tier == Safe {
		return "Rebuildable items that can be cleaned together with low risk."
	}
	if item.Tier == Caution {
		return "Rebuildable, but may require significant download or setup time."
	}
	return "User-managed content requiring an item-by-item review."
}

func hasAny(dir string, names []string) bool {
	for _, name := range names {
		if _, err := os.Stat(filepath.Join(dir, name)); err == nil {
			return true
		}
	}
	return false
}
func isWithin(root, target string) bool {
	rel, err := filepath.Rel(root, target)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}
func isProtected(target string) bool {
	clean := filepath.Clean(target)
	slashed := "/" + strings.Trim(filepath.ToSlash(clean), "/") + "/"
	if clean == string(filepath.Separator) || strings.Contains(slashed, "/.git/") {
		return true
	}
	for _, marker := range []string{"/.ssh/", "/.gnupg/", "/System/", "/Library/Keychains/"} {
		if strings.Contains(slashed, marker) {
			return true
		}
	}
	trimmed := strings.TrimSuffix(slashed, "/")
	return strings.HasSuffix(trimmed, ".app") || strings.Contains(slashed, ".app/")
}
