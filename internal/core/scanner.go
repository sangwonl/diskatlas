package core

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
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
	ID       string   `json:"id"`
	Name     string   `json:"name"`
	Kind     string   `json:"kind"`
	Markers  []string `json:"markers,omitempty"`
	Targets  []string `json:"targets,omitempty"`
	Tier     Tier     `json:"tier"`
	Category string   `json:"category"`
	Rebuild  string   `json:"rebuild"`
	Native   string   `json:"native,omitempty"`
	Cost     string   `json:"cost"`
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

type Result struct {
	GeneratedAt string                 `json:"generatedAt"`
	Roots       []string               `json:"roots"`
	Items       []Item                 `json:"items"`
	Summary     map[string]TierSummary `json:"summary"`
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
	return result
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

func Scan(root string) (*Result, error) {
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
	for _, rule := range globalRules() {
		for _, target := range rule.Targets {
			if target == "" || !isWithin(root, target) {
				continue
			}
			if item, ok := inspect(rule, target, ""); ok {
				result.Items = append(result.Items, item)
			}
		}
	}
	if err := walkProjects(root, result); err != nil {
		return nil, err
	}
	sort.Slice(result.Items, func(i, j int) bool { return result.Items[i].Bytes > result.Items[j].Bytes })
	for _, item := range result.Items {
		s := result.Summary[string(item.Tier)]
		s.Bytes += item.Bytes
		s.Count++
		result.Summary[string(item.Tier)] = s
	}
	return result, nil
}

func walkProjects(root string, result *Result) error {
	return filepath.WalkDir(root, func(dir string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return nil
		}
		if entry.IsDir() && entry.Name() != filepath.Base(root) {
			for _, skip := range []string{".git", "node_modules", "target", ".next", ".venv", "venv", "Library", "Applications"} {
				if entry.Name() == skip {
					return filepath.SkipDir
				}
			}
		}
		if !entry.IsDir() {
			return nil
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
				if item, ok := inspect(rule, targetPath, dir); ok {
					result.Items = append(result.Items, item)
				}
			}
		}
		return nil
	})
}

func inspect(rule Rule, target, project string) (Item, bool) {
	info, err := os.Lstat(target)
	if err != nil || info.Mode()&os.ModeSymlink != 0 {
		return Item{}, false
	}
	bytes, files := measure(target, map[string]bool{})
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
	explanation := fmt.Sprintf("%s can be recreated with %s.", rule.Name, rule.Rebuild)
	if tier == Review {
		explanation = fmt.Sprintf("%s contains user-managed data and requires manual review.", rule.Name)
	}
	if tier == Protected {
		explanation = "Protected by Shed safety rules and cannot be selected."
	}
	return Item{ID: rule.ID + ":" + target, RuleID: rule.ID, Name: rule.Name, Path: target, ProjectPath: project, Category: rule.Category, Tier: tier, Bytes: bytes, Files: files, ModifiedAt: info.ModTime().UTC().Format(time.RFC3339), Signals: signals, Explanation: explanation, Rebuild: rule.Rebuild, Native: rule.Native, Cost: rule.Cost, Clean: map[bool]string{true: "blocked", false: "quarantine"}[tier == Protected]}, true
}

func measure(target string, seen map[string]bool) (int64, int) {
	info, err := os.Lstat(target)
	if err != nil || info.Mode()&os.ModeSymlink != 0 {
		return 0, 0
	}
	if !info.IsDir() {
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
	if clean == string(filepath.Separator) || strings.Contains(clean, string(filepath.Separator)+".git"+string(filepath.Separator)) {
		return true
	}
	for _, marker := range []string{"/.ssh/", "/.gnupg/", "/Documents/", "/Desktop/", "/Pictures/", "/System/", "/Library/Keychains/"} {
		if strings.Contains(clean, marker) {
			return true
		}
	}
	return strings.HasSuffix(clean, ".app")
}
