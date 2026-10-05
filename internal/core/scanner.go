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
	Action        string   `json:"action,omitempty"`
	Markers       []string `json:"markers,omitempty"`
	Targets       []string `json:"targets,omitempty"`
	Tier          Tier     `json:"tier"`
	Category      string   `json:"category"`
	Rebuild       string   `json:"rebuild"`
	Native        string   `json:"native,omitempty"`
	Cost          string   `json:"cost"`
	MinBytes      int64    `json:"minBytes,omitempty"`
	MinFiles      int      `json:"minFiles,omitempty"`
	OlderThanDays int      `json:"olderThanDays,omitempty"`
	Command       []string `json:"-"`
	Probe         []string `json:"-"`
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
	Action      string   `json:"action,omitempty"`
	Labels      []string `json:"labels,omitempty"`
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

type DirectorySummary struct {
	Path           string   `json:"path"`
	Name           string   `json:"name"`
	Bytes          int64    `json:"bytes"`
	Files          int      `json:"files"`
	CandidateBytes int64    `json:"candidateBytes"`
	CandidateFiles int      `json:"candidateFiles"`
	Labels         []string `json:"labels,omitempty"`
}

type Progress struct {
	Phase        string   `json:"phase"`
	RequestID    string   `json:"requestID,omitempty"`
	Scanned      int64    `json:"scanned"`
	Candidates   int64    `json:"candidates"`
	Found        int64    `json:"found"`
	FilesScanned int64    `json:"filesScanned,omitempty"`
	Path         string   `json:"path,omitempty"`
	Storage      *Storage `json:"storage,omitempty"`
	Item         *Item    `json:"item,omitempty"`
}

type Result struct {
	GeneratedAt string                 `json:"generatedAt"`
	Roots       []string               `json:"roots"`
	Items       []Item                 `json:"items"`
	Directories []DirectorySummary     `json:"directories,omitempty"`
	Summary     map[string]TierSummary `json:"summary"`
	Groups      []Group                `json:"groups"`
	DurationMS  int64                  `json:"durationMs"`
}

var projectRules = []Rule{
	{ID: "node.modules", Name: "Node.js dependencies", Kind: "project", Action: "delete", Markers: []string{"package.json"}, Targets: []string{"node_modules"}, Tier: Safe, Category: "Project output", Rebuild: "npm ci", Cost: "low"},
	{ID: "node.build", Name: "Node.js build cache", Kind: "project", Action: "delete", Markers: []string{"package.json"}, Targets: []string{".next", ".nuxt", ".turbo", ".parcel-cache"}, Tier: Safe, Category: "Project output", Rebuild: "npm run build", Cost: "low"},
	{ID: "rust.target", Name: "Rust build output", Kind: "project", Action: "delete", Markers: []string{"Cargo.toml"}, Targets: []string{"target"}, Tier: Safe, Category: "Project output", Rebuild: "cargo build", Cost: "low"},
	{ID: "python.cache", Name: "Python build cache", Kind: "project", Action: "delete", Markers: []string{"pyproject.toml", "requirements.txt"}, Targets: []string{"__pycache__", ".pytest_cache", ".mypy_cache", ".ruff_cache"}, Tier: Safe, Category: "Project output", Rebuild: "python -m pip install", Cost: "low"},
	{ID: "python.venv", Name: "Python virtual environment", Kind: "project", Action: "delete", Markers: []string{"pyproject.toml", "requirements.txt"}, Targets: []string{".venv", "venv"}, Tier: Caution, Category: "Project output", Rebuild: "python -m venv .venv", Cost: "medium"},
	{ID: "java.build", Name: "Java/Kotlin build output", Kind: "project", Action: "delete", Markers: []string{"build.gradle", "build.gradle.kts", "pom.xml"}, Targets: []string{"build", ".gradle", "target"}, Tier: Safe, Category: "Project output", Rebuild: "./gradlew build or mvn package", Cost: "medium"},
	{ID: "swift.build", Name: "Swift package build output", Kind: "project", Action: "delete", Markers: []string{"Package.swift"}, Targets: []string{".build"}, Tier: Safe, Category: "Project output", Rebuild: "swift build", Cost: "low"},
	{ID: "dotnet.build", Name: ".NET build output", Kind: "project", Action: "delete", Markers: []string{"*.csproj", "*.sln"}, Targets: []string{"bin", "obj"}, Tier: Safe, Category: "Project output", Rebuild: "dotnet build", Cost: "low"},
	{ID: "unity.cache", Name: "Unity generated data", Kind: "project", Action: "delete", Markers: []string{"ProjectSettings"}, Targets: []string{"Library", "Temp", "Obj"}, Tier: Safe, Category: "Project output", Rebuild: "Open the project in Unity", Cost: "medium"},
	{ID: "flutter.build", Name: "Flutter build output", Kind: "project", Action: "delete", Markers: []string{"pubspec.yaml"}, Targets: []string{".dart_tool", "build"}, Tier: Safe, Category: "Project output", Rebuild: "flutter pub get", Cost: "low"},
}

func Rules() []Rule {
	result := append([]Rule{}, projectRules...)
	result = append(result, globalRules()...)
	result = append(result, appRules()...)
	result = append(result, generalRules()...)
	return result
}

func generalRules() []Rule {
	return []Rule{
		// Older files take precedence so the UI can explain both signals without
		// emitting two candidates for the same path. The lower general threshold
		// keeps useful personal files visible while the collapsed groups prevent
		// the result from becoming a wall of rows.
		{ID: "general.old-files", Name: "Large files not opened recently", Kind: "general", Tier: Review, Category: "Personal files", Rebuild: "Review before deleting or archive externally", Cost: "high", MinBytes: 10 * 1024 * 1024, OlderThanDays: 180},
		{ID: "general.large-files", Name: "Large personal files", Kind: "general", Tier: Review, Category: "Personal files", Rebuild: "Keep, archive, or move to external storage", Cost: "high", MinBytes: 10 * 1024 * 1024},
		{ID: "general.large-directory", Name: "Large folder", Kind: "directory", Action: "delete", Tier: Review, Category: "Personal files", Rebuild: "Review its contents before deleting the folder", Cost: "high", MinBytes: largeDirectoryBytes, MinFiles: 32},
	}
}

func globalRules() []Rule {
	home, _ := os.UserHomeDir()
	cache := func(defaultPath string) string { return filepath.Join(home, defaultPath) }
	rules := []Rule{
		{ID: "npm.cache", Name: "npm 캐시", Kind: "command", Action: "command", Tier: Safe, Category: "Package cache", Rebuild: "npm install", Native: "npm cache clean --force", Cost: "low"},
		{ID: "pnpm.store", Name: "pnpm 저장소", Kind: "command", Action: "command", Tier: Safe, Category: "Package cache", Rebuild: "pnpm install", Native: "pnpm store prune", Cost: "low"},
		{ID: "yarn.cache", Name: "Yarn 캐시", Kind: "command", Action: "command", Tier: Safe, Category: "Package cache", Rebuild: "yarn install", Native: "yarn cache clean", Cost: "low"},
		{ID: "bun.cache", Name: "Bun 캐시", Kind: "command", Action: "command", Tier: Safe, Category: "Package cache", Rebuild: "bun install", Native: "bun pm cache rm", Cost: "low"},
		{ID: "pip.cache", Name: "pip 캐시", Kind: "command", Action: "command", Tier: Safe, Category: "Package cache", Rebuild: "python -m pip install", Native: "python -m pip cache purge", Cost: "low"},
		{ID: "uv.cache", Name: "uv 캐시", Kind: "command", Action: "command", Tier: Safe, Category: "Package cache", Rebuild: "uv sync", Native: "uv cache clean", Cost: "low"},
		{ID: "go.build-cache", Name: "Go 빌드 캐시", Kind: "command", Action: "command", Tier: Safe, Category: "Build cache", Rebuild: "go build ./...", Native: "go clean -cache", Cost: "low"},
		{ID: "go.mod-cache", Name: "Go 모듈 캐시", Kind: "command", Action: "command", Tier: Caution, Category: "Package cache", Rebuild: "go mod download", Native: "go clean -modcache", Cost: "medium"},
		{ID: "cargo.autoclean", Name: "Cargo 미사용 소스 캐시", Kind: "command", Action: "command", Tier: Safe, Category: "Package cache", Rebuild: "cargo fetch", Native: "cargo cache --autoclean (cargo-cache 필요)", Cost: "medium"},
		{ID: "rustup.toolchain", Name: "rustup 툴체인", Kind: "command", Action: "command", Tier: Caution, Category: "Rust 환경", Rebuild: "rustup toolchain install <toolchain>", Native: "rustup toolchain uninstall <toolchain>", Cost: "high"},
		{ID: "rbenv.version", Name: "rbenv Ruby 버전", Kind: "command", Action: "command", Tier: Caution, Category: "Ruby 환경", Rebuild: "rbenv install <version>", Native: "rbenv uninstall <version>", Cost: "high"},
		{ID: "asdf.version", Name: "asdf 관리 런타임", Kind: "command", Action: "command", Tier: Caution, Category: "언어 런타임", Rebuild: "asdf install <tool> <version>", Native: "asdf uninstall <tool> <version>", Cost: "high"},
		{ID: "mise.version", Name: "mise 관리 런타임", Kind: "command", Action: "command", Tier: Caution, Category: "언어 런타임", Rebuild: "mise install <tool>@<version>", Native: "mise uninstall <tool>@<version>", Cost: "high"},
		{ID: "cargo.registry", Name: "Cargo registry", Kind: "global", Tier: Safe, Category: "Package cache", Rebuild: "cargo fetch", Native: "cargo cache --autoclean", Cost: "medium"},
		{ID: "maven.repository", Name: "Maven 로컬 저장소", Kind: "global", Tier: Caution, Category: "Package cache", Rebuild: "mvn dependency:resolve", Native: "프로젝트 단위 정리: mvn dependency:purge-local-repository -DreResolve=false", Cost: "high"},
		{ID: "gradle.cache", Name: "Gradle cache", Kind: "global", Tier: Safe, Category: "Build cache", Rebuild: "./gradlew build", Native: "./gradlew --stop 후 Gradle 캐시 정리", Cost: "medium"},
		{ID: "xcode.derived-data", Name: "Xcode DerivedData", Kind: "global", Tier: Safe, Category: "IDE cache", Rebuild: "Build the Xcode project again", Native: "Close Xcode before cleanup", Cost: "medium"},
		{ID: "xcode.device-support", Name: "iOS DeviceSupport", Kind: "global", Tier: Caution, Category: "IDE cache", Rebuild: "Reconnect the device in Xcode", Native: "Reconnect the device in Xcode if needed", Cost: "high"},
		{ID: "xcode.archives", Name: "Xcode Archives", Kind: "global", Tier: Review, Category: "IDE data", Rebuild: "Archives are not automatically reproducible", Native: "Export or back up archives before cleanup", Cost: "high"},
		{ID: "android.avd", Name: "Android virtual device", Kind: "command", Action: "command", Tier: Caution, Category: "Emulator data", Rebuild: "Create the virtual device again; its apps, settings, files, and snapshots will be lost", Native: "avdmanager delete avd -n <name> · removes this virtual device and its data", Cost: "high"},
		{ID: "ios.simulator", Name: "iOS Simulator", Kind: "command", Action: "command", Tier: Caution, Category: "Emulator data", Rebuild: "Create the simulator again in Xcode; its apps, settings, files, and snapshots will be lost", Native: "xcrun simctl delete <udid> · removes this simulator and its data", Cost: "high"},
		{ID: "huggingface.models", Name: "Hugging Face model cache", Kind: "global", Tier: Caution, Category: "AI model", Rebuild: "huggingface-cli download <model>", Native: "Use the Hugging Face cache command to remove unused models", Cost: "high"},
		{ID: "lmstudio.models", Name: "LM Studio models", Kind: "global", Tier: Caution, Category: "AI model", Rebuild: "Download the model again in LM Studio", Cost: "high"},
		{ID: "homebrew.cleanup", Name: "Homebrew 다운로드 캐시", Kind: "command", Action: "command", Tier: Safe, Category: "Package manager", Rebuild: "brew install <formula>", Native: "brew cleanup --prune=all", Cost: "low", Command: []string{"brew", "cleanup", "--prune=all"}},
		{ID: "conda.cleanup", Name: "Conda 패키지 캐시", Kind: "command", Action: "command", Tier: Safe, Category: "Package manager", Rebuild: "conda install <package>", Native: "conda clean --all --yes", Cost: "medium", Command: []string{"conda", "clean", "--all", "--yes"}},
		{ID: "pyenv.version", Name: "pyenv Python 버전", Kind: "command", Action: "command", Tier: Caution, Category: "Python 환경", Rebuild: "pyenv install <version>", Native: "pyenv uninstall <version>", Cost: "high"},
		{ID: "ollama.models", Name: "Ollama models", Kind: "global", Tier: Caution, Category: "AI model", Rebuild: "ollama pull <model>", Cost: "high"},
		{ID: "docker.system-prune", Name: "Docker unused resources", Kind: "command", Action: "command", Tier: Caution, Category: "Container data", Rebuild: "docker pull/build again", Native: "docker system prune --force", Cost: "high", Command: []string{"docker", "system", "prune", "--force"}, Probe: []string{"docker", "system", "df", "--format", "{{json .}}"}},
	}
	for i := range rules {
		switch rules[i].ID {
		case "cargo.registry":
			rules[i].Targets = []string{cache(filepath.Join(".cargo", "registry"))}
		case "maven.repository":
			rules[i].Targets = []string{cache(filepath.Join(".m2", "repository"))}
		case "gradle.cache":
			rules[i].Targets = []string{cache(filepath.Join(".gradle", "caches")), cache(filepath.Join(".gradle", "wrapper", "dists"))}
		case "xcode.derived-data":
			if runtime.GOOS == "darwin" {
				rules[i].Targets = []string{cache(filepath.Join("Library", "Developer", "Xcode", "DerivedData"))}
			}
		case "xcode.device-support":
			if runtime.GOOS == "darwin" {
				rules[i].Targets = []string{cache(filepath.Join("Library", "Developer", "Xcode", "iOS DeviceSupport"))}
			}
		case "xcode.archives":
			if runtime.GOOS == "darwin" {
				rules[i].Targets = xcodeArchiveTargets(cache(filepath.Join("Library", "Developer", "Xcode", "Archives")))
			}
		case "android.avd":
			// Individual AVDs are discovered through avdmanager and removed one
			// device at a time. Never offer direct deletion of the containing
			// directory, which could erase every emulator in one action.
		case "ios.simulator":
			// Individual simulators are discovered through simctl and removed one
			// device at a time. Never expose CoreSimulator/Devices as one row.
		case "huggingface.models":
			// The Hub cache can share blob storage across repositories. Don't
			// expose the cache root as one direct-delete candidate.
		case "lmstudio.models":
			// This is user-managed model data; wait for a per-model manager API.
		case "ollama.models":
			// Model manifests can share blob layers; don't delete the store root.
		}
	}
	return rules
}

func xcodeArchiveTargets(archiveRoot string) []string {
	entries, err := os.ReadDir(archiveRoot)
	if err != nil {
		return nil
	}
	targets := []string{}
	for _, entry := range entries {
		if entry.IsDir() && strings.EqualFold(filepath.Ext(entry.Name()), ".xcarchive") {
			targets = append(targets, filepath.Join(archiveRoot, entry.Name()))
		}
	}
	sort.Strings(targets)
	return targets
}

type scanCandidate struct {
	rule           Rule
	target         string
	project        string
	cached         *Item
	estimatedBytes int64
	estimatedFiles int
	labels         []string
}

func Scan(root string) (*Result, error) {
	return ScanWithProgress(root, nil)
}

func ScanWithProgress(root string, onProgress func(Progress)) (*Result, error) {
	started := time.Now()
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
	managedCandidates := commandCandidates(root)
	for _, rule := range globalRules() {
		if rule.Action == "command" {
			continue
		}
		for _, target := range rule.Targets {
			if target == "" || !isWithin(root, target) || commandOwnsPath(managedCandidates, target) {
				continue
			}
			candidates = append(candidates, scanCandidate{rule: rule, target: target})
		}
	}
	for _, candidate := range managedCandidates {
		if candidate.rule.Action == "command" && candidate.estimatedBytes == 0 {
			targets := candidate.rule.Targets
			if len(targets) == 0 {
				targets = []string{candidate.target}
			}
			seenProviderFiles := &sync.Map{}
			for _, target := range targets {
				bytes, files := measure(target, seenProviderFiles, nil)
				candidate.estimatedBytes += bytes
				candidate.estimatedFiles += files
			}
		}
		candidates = append(candidates, candidate)
	}
	projectCandidates, err := discoverProjects(root, onProgress)
	if err != nil {
		return nil, err
	}
	candidates = append(candidates, projectCandidates...)
	if onProgress != nil {
		onProgress(Progress{Phase: "measure", Scanned: 0, Candidates: int64(len(candidates)), Found: 0})
	}
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
	for range workerCount {
		workers.Add(1)
		go func() {
			defer workers.Done()
			for candidate := range jobs {
				var foundItem *Item
				if item, ok := inspectCandidate(candidate, seen, nil); ok {
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
		s := result.Summary[string(item.Tier)]
		s.Bytes += item.Bytes
		s.Count++
		result.Summary[string(item.Tier)] = s
	}
	result.Groups = makeGroups(result.Items)
	result.DurationMS = time.Since(started).Milliseconds()
	if onProgress != nil {
		onProgress(Progress{Phase: "done", Scanned: int64(len(candidates)), Candidates: int64(len(candidates)), Found: int64(len(result.Items))})
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
						if rule.Kind != "general" {
							continue
						}
						if allocatedSize(info) < rule.MinBytes {
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

func inspect(rule Rule, target, project string, seen *sync.Map, onFile func(string)) (Item, bool) {
	return inspectCandidate(scanCandidate{rule: rule, target: target, project: project}, seen, onFile)
}

func inspectCandidate(candidate scanCandidate, seen *sync.Map, onFile func(string)) (Item, bool) {
	rule, target, project := candidate.rule, candidate.target, candidate.project
	info, err := os.Lstat(target)
	if err != nil || info.Mode()&os.ModeSymlink != 0 {
		return Item{}, false
	}
	if rule.Action == "command" {
		if candidate.estimatedBytes <= 0 {
			return Item{}, false
		}
		labels := mergeLabels(candidate.labels, classifyPath(target))
		explanation := "관리 도구의 정리 명령으로 처리합니다. 저장소 파일을 직접 삭제하지 않습니다."
		if rule.ID == "docker.system-prune" {
			explanation = "Docker가 사용하지 않는 리소스를 자체적으로 정리합니다. Docker 저장소 파일을 직접 지우지 않습니다."
		} else if rule.ID == "ios.simulator" {
			explanation = "선택한 iOS 시뮬레이터와 내부 앱·설정·스냅샷을 simctl로 삭제합니다. 필요하면 Xcode에서 다시 만들 수 있습니다."
		}
		return Item{
			ID:          rule.ID + ":" + target,
			RuleID:      rule.ID,
			Name:        rule.Name,
			Path:        target,
			Category:    rule.Category,
			Tier:        rule.Tier,
			Bytes:       candidate.estimatedBytes,
			Files:       candidate.estimatedFiles,
			ModifiedAt:  info.ModTime().UTC().Format(time.RFC3339),
			Signals:     []string{"관리 도구가 회수 가능 용량을 계산했습니다.", "저장소 파일을 직접 삭제하지 않습니다."},
			Labels:      labels,
			Explanation: explanation,
			Rebuild:     rule.Rebuild,
			Native:      rule.Native,
			Cost:        rule.Cost,
			Clean:       "command",
			Action:      "command",
		}, true
	}
	bytes, files := int64(0), 0
	if info.IsDir() && candidate.estimatedBytes > 0 {
		bytes, files = candidate.estimatedBytes, candidate.estimatedFiles
	} else {
		bytes, files = measure(target, seen, onFile)
	}
	if bytes == 0 {
		return Item{}, false
	}
	tier := rule.Tier
	signals := []string{}
	if rule.Kind == "command" && rule.Action != "command" {
		signals = append(signals, "Docker 관리 도구에 연결되지 않아 직접 정리할 수 없습니다")
	}
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
	if rule.Kind == "directory" {
		signals = append(signals, fmt.Sprintf("Folder total exceeds %s across %d files", formatBytes(rule.MinBytes), files))
		explanation = "Many smaller files add up to a large folder. Review the folder contents before deleting it."
	}
	if rule.Kind == "app" {
		signals = append(signals, "잘 알려진 앱의 이전 버전 디렉터리")
		if rule.Tier == Safe {
			explanation = fmt.Sprintf("%s는 최신 버전과 분리된 이전 버전 데이터입니다. %s", rule.Name, rule.Rebuild)
		} else {
			explanation = fmt.Sprintf("%s에는 설정이나 플러그인이 남아 있을 수 있습니다. 삭제 전에 내용을 확인하세요.", rule.Name)
		}
	}
	if tier == Review {
		if rule.Kind != "general" {
			explanation = fmt.Sprintf("%s contains user-managed data and requires manual review.", rule.Name)
		}
	}
	if tier == Protected {
		explanation = "Protected by DiskAtlas safety rules and cannot be selected."
	}
	labels := mergeLabels(candidate.labels, classifyPath(target))
	name := rule.Name
	switch rule.ID {
	case "pyenv.version", "rustup.toolchain", "rbenv.version":
		name += " · " + filepath.Base(target)
	case "asdf.version", "mise.version":
		name += " · " + filepath.Base(filepath.Dir(target)) + " " + filepath.Base(target)
	}
	return Item{ID: rule.ID + ":" + target, RuleID: rule.ID, Name: name, Path: target, ProjectPath: project, Category: rule.Category, Tier: tier, Bytes: bytes, Files: files, ModifiedAt: info.ModTime().UTC().Format(time.RFC3339), Signals: signals, Labels: labels, Explanation: explanation, Rebuild: rule.Rebuild, Native: rule.Native, Cost: rule.Cost, Clean: map[bool]string{true: "blocked", false: "quarantine"}[tier == Protected], Action: rule.Action}, true
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

func measure(target string, seen *sync.Map, onFile func(string)) (int64, int) {
	info, err := os.Lstat(target)
	if err != nil || info.Mode()&os.ModeSymlink != 0 {
		return 0, 0
	}
	if !info.IsDir() {
		if onFile != nil {
			onFile(target)
		}
		if key := fileIdentity(info); key != "" {
			if _, loaded := seen.LoadOrStore(key, true); loaded {
				return 0, 0
			}
		}
		return allocatedSize(info), 1
	}
	var bytes int64
	files := 0
	entries, err := os.ReadDir(target)
	if err != nil {
		return 0, 0
	}
	for _, entry := range entries {
		child := filepath.Join(target, entry.Name())
		b, f := measure(child, seen, onFile)
		bytes += b
		files += f
	}
	return bytes, files
}

// allocatedSize reports the space occupied on disk instead of the logical file
// length. This matters for sparse VM and disk image files on macOS.
func allocatedSize(info os.FileInfo) int64 {
	value := reflect.Indirect(reflect.ValueOf(info.Sys()))
	if value.IsValid() && value.Kind() == reflect.Struct {
		if blocks := value.FieldByName("Blocks"); blocks.IsValid() {
			if raw := reflectNumber(blocks); raw != "" {
				if count, err := strconv.ParseInt(raw, 10, 64); err == nil && count >= 0 {
					return count * 512
				}
			}
		}
	}
	return info.Size()
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

func filesystemIdentity(path string, info os.FileInfo) string {
	if runtime.GOOS == "windows" {
		return strings.ToLower(filepath.VolumeName(path))
	}
	value := reflect.Indirect(reflect.ValueOf(info.Sys()))
	if !value.IsValid() || value.Kind() != reflect.Struct {
		return ""
	}
	device := value.FieldByName("Dev")
	if !device.IsValid() {
		return ""
	}
	return reflectNumber(device)
}

func isOtherFilesystem(rootDevice, path string, info os.FileInfo) bool {
	if rootDevice == "" {
		return false
	}
	device := filesystemIdentity(path, info)
	return device != "" && device != rootDevice
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

type directoryAggregate struct {
	Bytes int64
	Files int
}

const largeDirectoryBytes = 500 * 1024 * 1024

func largeDirectoryCandidates(root string, index *analysisIndex, managed []scanCandidate) []scanCandidate {
	if index == nil || len(index.Directories) == 0 {
		return nil
	}
	var rule Rule
	for _, candidateRule := range generalRules() {
		if candidateRule.ID == "general.large-directory" {
			rule = candidateRule
			break
		}
	}
	if rule.ID == "" {
		return nil
	}
	home, _ := os.UserHomeDir()
	eligible := map[string]directoryAggregate{}
	for path, aggregate := range index.Directories {
		path = filepath.Clean(path)
		if aggregate.Bytes < rule.MinBytes || aggregate.Files < rule.MinFiles || isRootScanContainer(root, path) || analysisExcluded(root, path) || isProtected(path) || commandOwnsPath(managed, path) {
			continue
		}
		if home != "" && filepath.Clean(path) == filepath.Clean(home) {
			continue
		}
		if hasProjectMarker(path) {
			continue
		}
		eligible[path] = aggregate
	}
	result := []scanCandidate{}
	for path, aggregate := range eligible {
		childEligible := false
		for child := range eligible {
			if child != path && directoryDepth(child) > directoryDepth(path) && isWithin(path, child) {
				childEligible = true
				break
			}
		}
		if childEligible {
			continue
		}
		ruleCopy := rule
		ruleCopy.Targets = []string{path}
		result = append(result, scanCandidate{
			rule:           ruleCopy,
			target:         path,
			estimatedBytes: aggregate.Bytes,
			estimatedFiles: aggregate.Files,
			labels:         []string{"directory:aggregate", "signal:many-small-files"},
		})
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].estimatedBytes != result[j].estimatedBytes {
			return result[i].estimatedBytes > result[j].estimatedBytes
		}
		return result[i].target < result[j].target
	})
	return result
}

func directoryDepth(path string) int {
	return len(strings.Split(filepath.Clean(path), string(filepath.Separator)))
}

// summarizeDirectories builds the navigable directory hierarchy from the
// complete manifest. Every meaningful aggregate is kept, including parents,
// so the UI can start at the largest folders and narrow down before showing
// file-level cleanup candidates. Directory summaries are review-only; the
// actual cleanup list still contains individual paths and managed commands.
func summarizeDirectories(root string, index *analysisIndex, items []Item) []DirectorySummary {
	if index == nil || len(index.Files) == 0 {
		return nil
	}
	root = filepath.Clean(root)
	if index.Directories == nil {
		index.Directories = map[string]directoryAggregate{}
		for path, file := range index.Files {
			adjustDirectoryAggregate(root, index.Directories, path, file.Bytes, 1)
		}
	}
	aggregates := index.Directories
	qualifies := func(path string) bool {
		aggregate, ok := aggregates[path]
		return ok && aggregate.Bytes >= largeDirectoryBytes && aggregate.Files >= 5
	}
	result := []DirectorySummary{}
	for path, aggregate := range aggregates {
		if !qualifies(path) {
			continue
		}
		result = append(result, DirectorySummary{
			Path:   path,
			Name:   filepath.Base(path),
			Bytes:  aggregate.Bytes,
			Files:  aggregate.Files,
			Labels: classifyPath(path),
		})
	}
	for index := range result {
		for _, item := range items {
			if isWithin(result[index].Path, item.Path) {
				result[index].CandidateBytes += item.Bytes
				result[index].CandidateFiles++
			}
		}
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].Bytes != result[j].Bytes {
			return result[i].Bytes > result[j].Bytes
		}
		return result[i].Path < result[j].Path
	})
	return result
}

func adjustDirectoryAggregate(root string, aggregates map[string]directoryAggregate, path string, bytes int64, files int) {
	for dir := filepath.Dir(path); dir != root && isWithin(root, dir); dir = filepath.Dir(dir) {
		current := aggregates[dir]
		current.Bytes += bytes
		current.Files += files
		if current.Files <= 0 {
			delete(aggregates, dir)
		} else {
			aggregates[dir] = current
		}
	}
}

func isRootScanContainer(root, path string) bool {
	if root != string(filepath.Separator) || filepath.Dir(path) != root {
		return false
	}
	switch strings.ToLower(filepath.Base(path)) {
	case "system", "library", "applications", "private", "usr", "bin", "sbin", "var", "users", "volumes", "opt":
		return true
	default:
		return false
	}
}

func hasAny(dir string, names []string) bool {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return false
	}
	for _, entry := range entries {
		for _, name := range names {
			if markerMatches(name, entry.Name()) {
				return true
			}
		}
	}
	return false
}

func markerMatches(marker, name string) bool {
	if strings.HasPrefix(marker, "*") {
		return strings.HasSuffix(name, strings.TrimPrefix(marker, "*"))
	}
	return marker == name
}
func isWithin(root, target string) bool {
	rel, err := filepath.Rel(root, target)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}
func isProtected(target string) bool {
	clean := filepath.Clean(target)
	if isManagedStoragePath(clean) {
		return true
	}
	slashed := "/" + strings.Trim(filepath.ToSlash(clean), "/") + "/"
	if clean == string(filepath.Separator) || strings.Contains(slashed, "/.git/") {
		return true
	}
	// A root scan must not turn operating-system files into personal-file
	// candidates just because they happen to be larger than the review limit.
	// These are absolute system locations on Unix; a user's ~/Library remains
	// eligible for the explicit app and cache rules above.
	for _, prefix := range []string{"/System/", "/usr/", "/bin/", "/sbin/", "/private/", "/var/", "/Library/", "/Applications/"} {
		if strings.HasPrefix(slashed, prefix) {
			return true
		}
	}
	for _, marker := range []string{"/.ssh/", "/.gnupg/", "/System/", "/Library/Keychains/"} {
		if strings.Contains(slashed, marker) {
			return true
		}
	}
	trimmed := strings.TrimSuffix(slashed, "/")
	return strings.HasSuffix(trimmed, ".app") || strings.Contains(slashed, ".app/")
}
