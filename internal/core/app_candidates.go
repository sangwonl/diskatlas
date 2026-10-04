package core

import (
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
)

// appRoot describes one well-known application data area. Versioned siblings
// are discovered instead of treating every file below the area as an
// independent cleanup candidate.
type appRoot struct {
	ruleID string
	root   string
	filter func(string) bool
}

func appRules() []Rule {
	return []Rule{
		{ID: "app.jetbrains.cache", Name: "JetBrains 이전 버전 캐시", Kind: "app", Tier: Safe, Category: "App data", Rebuild: "JetBrains IDE를 다시 실행하면 캐시가 재생성됩니다.", Native: "IDE를 완전히 종료한 뒤 정리하세요.", Cost: "low"},
		{ID: "app.jetbrains.logs", Name: "JetBrains 이전 버전 로그", Kind: "app", Tier: Safe, Category: "App data", Rebuild: "현재 IDE 사용에는 필요하지 않은 이전 로그입니다.", Native: "IDE를 완전히 종료한 뒤 정리하세요.", Cost: "low"},
		{ID: "app.jetbrains.support", Name: "JetBrains 이전 버전 앱 데이터", Kind: "app", Tier: Caution, Category: "App data", Rebuild: "필요한 설정과 플러그인은 현재 IDE에서 다시 설정할 수 있습니다.", Native: "해당 IDE를 완전히 종료하고 설정·플러그인을 확인한 뒤 정리하세요.", Cost: "medium"},
		{ID: "app.androidstudio.cache", Name: "Android Studio 이전 버전 캐시", Kind: "app", Tier: Safe, Category: "App data", Rebuild: "Android Studio를 다시 실행하면 캐시가 재생성됩니다.", Native: "Android Studio를 완전히 종료한 뒤 정리하세요.", Cost: "low"},
		{ID: "app.androidstudio.support", Name: "Android Studio 이전 버전 앱 데이터", Kind: "app", Tier: Caution, Category: "App data", Rebuild: "필요한 설정과 플러그인은 현재 Android Studio에서 다시 설정할 수 있습니다.", Native: "Android Studio를 완전히 종료하고 설정·플러그인을 확인한 뒤 정리하세요.", Cost: "medium"},
	}
}

func knownAppRoots(home string) []appRoot {
	join := func(parts ...string) string { return filepath.Join(append([]string{home}, parts...)...) }
	jetbrains := func(name string) bool { return name != "" }
	androidStudio := func(name string) bool { return strings.HasPrefix(strings.ToLower(name), "androidstudio") }

	var roots []appRoot
	switch runtime.GOOS {
	case "darwin":
		roots = []appRoot{
			{ruleID: "app.jetbrains.cache", root: join("Library", "Caches", "JetBrains"), filter: jetbrains},
			{ruleID: "app.jetbrains.logs", root: join("Library", "Logs", "JetBrains"), filter: jetbrains},
			{ruleID: "app.jetbrains.support", root: join("Library", "Application Support", "JetBrains"), filter: jetbrains},
			{ruleID: "app.androidstudio.cache", root: join("Library", "Caches", "Google"), filter: androidStudio},
			{ruleID: "app.androidstudio.support", root: join("Library", "Application Support", "Google"), filter: androidStudio},
		}
	case "windows":
		local := os.Getenv("LOCALAPPDATA")
		if local == "" {
			local = join("AppData", "Local")
		}
		roaming := os.Getenv("APPDATA")
		if roaming == "" {
			roaming = join("AppData", "Roaming")
		}
		roots = []appRoot{
			{ruleID: "app.jetbrains.cache", root: filepath.Join(local, "JetBrains"), filter: jetbrains},
			{ruleID: "app.jetbrains.support", root: filepath.Join(roaming, "JetBrains"), filter: jetbrains},
			{ruleID: "app.androidstudio.cache", root: filepath.Join(local, "Google"), filter: androidStudio},
			{ruleID: "app.androidstudio.support", root: filepath.Join(roaming, "Google"), filter: androidStudio},
		}
	default:
		roots = []appRoot{
			{ruleID: "app.jetbrains.cache", root: join(".cache", "JetBrains"), filter: jetbrains},
			{ruleID: "app.jetbrains.support", root: join(".config", "JetBrains"), filter: jetbrains},
			{ruleID: "app.androidstudio.cache", root: join(".cache", "Google"), filter: androidStudio},
			{ruleID: "app.androidstudio.support", root: join(".config", "Google"), filter: androidStudio},
		}
	}
	return roots
}

// appCandidates returns only older versioned siblings. The newest sibling for
// each product is kept out of the cleanup list because it is the likely active
// installation. A single version is also treated as current and is not
// guessed to be stale.
func appCandidates(root string) []scanCandidate {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil
	}
	rules := map[string]Rule{}
	for _, rule := range appRules() {
		rules[rule.ID] = rule
	}
	result := []scanCandidate{}
	for _, descriptor := range knownAppRoots(home) {
		if !isWithin(root, descriptor.root) && !isWithin(descriptor.root, root) {
			continue
		}
		entries, err := os.ReadDir(descriptor.root)
		if err != nil {
			continue
		}
		byProduct := map[string][]appVersionedDir{}
		for _, entry := range entries {
			if !entry.IsDir() || (descriptor.filter != nil && !descriptor.filter(entry.Name())) {
				continue
			}
			product, version, ok := parseAppVersion(entry.Name())
			if !ok {
				continue
			}
			byProduct[product] = append(byProduct[product], appVersionedDir{name: entry.Name(), version: version})
		}
		rule, ok := rules[descriptor.ruleID]
		if !ok {
			continue
		}
		for _, dirs := range byProduct {
			if len(dirs) < 2 {
				continue
			}
			sort.Slice(dirs, func(i, j int) bool { return compareAppVersions(dirs[i].version, dirs[j].version) > 0 })
			for _, old := range dirs[1:] {
				if compareAppVersions(old.version, dirs[0].version) == 0 {
					continue
				}
				target := filepath.Join(descriptor.root, old.name)
				result = append(result, scanCandidate{rule: rule, target: target})
			}
		}
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].rule.ID != result[j].rule.ID {
			return result[i].rule.ID < result[j].rule.ID
		}
		return result[i].target < result[j].target
	})
	return result
}

type appVersionedDir struct {
	name    string
	version []int
}

func parseAppVersion(name string) (string, []int, bool) {
	firstDigit := -1
	for i, character := range name {
		if character >= '0' && character <= '9' {
			firstDigit = i
			break
		}
	}
	if firstDigit <= 0 || firstDigit >= len(name) {
		return "", nil, false
	}
	product := name[:firstDigit]
	parts := strings.Split(name[firstDigit:], ".")
	if len(parts) < 2 {
		return "", nil, false
	}
	version := make([]int, len(parts))
	for i, part := range parts {
		if part == "" {
			return "", nil, false
		}
		value, err := strconv.Atoi(part)
		if err != nil {
			return "", nil, false
		}
		version[i] = value
	}
	product = strings.ToLower(product)
	// IntelliJ IDEA Community (IdeaIC) and Ultimate (IntelliJIdea) use
	// different directory prefixes but belong to the same version family for
	// cleanup purposes. This lets an older edition directory be surfaced when
	// a newer IDEA directory is present beside it.
	if strings.HasPrefix(product, "ideai") || strings.HasPrefix(product, "intellijidea") {
		product = "idea"
	}
	return product, version, true
}

func compareAppVersions(left, right []int) int {
	for i := 0; i < len(left) || i < len(right); i++ {
		var l, r int
		if i < len(left) {
			l = left[i]
		}
		if i < len(right) {
			r = right[i]
		}
		if l > r {
			return 1
		}
		if l < r {
			return -1
		}
	}
	return 0
}
