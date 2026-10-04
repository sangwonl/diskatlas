package core

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// classifyPath attaches durable labels while the manifest is being built.
// Labels are evidence used by detectors and future providers; they are not
// themselves permission to delete a path.
func classifyPath(path string) []string {
	p := strings.ToLower(filepath.ToSlash(filepath.Clean(path)))
	labels := []string{}
	if pathHasSegment(p, "node_modules") {
		labels = append(labels, "generated:node-dependencies")
	}
	if pathHasSegment(p, ".git") {
		labels = append(labels, "repository:git")
	}
	if isManagedStoragePath(path) {
		labels = append(labels, "managed:docker")
	}
	if strings.Contains(p, "/jetbrains/") || strings.Contains(p, "intellijidea") || strings.Contains(p, "ideaic") {
		labels = append(labels, "app:jetbrains")
	}
	if strings.Contains(p, "androidstudio") {
		labels = append(labels, "app:android-studio")
	}
	return labels
}

func pathHasSegment(path, segment string) bool {
	return path == "/"+segment || strings.Contains(path, "/"+segment+"/") || strings.HasSuffix(path, "/"+segment)
}

func isManagedStoragePath(path string) bool {
	p := strings.ToLower(filepath.ToSlash(filepath.Clean(path)))
	if strings.HasSuffix(p, "/docker.raw") && strings.Contains(p, "/com.docker.docker/") {
		return true
	}
	if p == "/var/lib/docker" || strings.HasPrefix(p, "/var/lib/docker/") {
		return true
	}
	if strings.Contains(p, "/docker/wsl/") && (strings.HasSuffix(p, ".vhdx") || strings.HasSuffix(p, ".vhd")) {
		return true
	}
	return false
}

func commandCandidates(root string) []scanCandidate {
	rules := globalRules()
	byRule := make([][]scanCandidate, len(rules))
	var workers sync.WaitGroup
	for index, rule := range rules {
		if rule.Action != "command" {
			continue
		}
		workers.Add(1)
		go func(index int, rule Rule) {
			defer workers.Done()
			byRule[index] = commandCandidatesForRule(root, rule)
		}(index, rule)
	}
	workers.Wait()
	result := []scanCandidate{}
	for _, candidates := range byRule {
		result = append(result, candidates...)
	}
	return result
}

func commandCandidatesForRule(root string, rule Rule) []scanCandidate {
	switch rule.ID {
	case "docker.system-prune":
		return dockerCommandCandidates(root, rule)
	case "npm.cache":
		return queriedCacheCandidate(root, rule, "npm", []string{"config", "get", "cache"}, []string{"cache", "clean", "--force"}, "npm", "_cacache")
	case "pnpm.store":
		return queriedCacheCandidate(root, rule, "pnpm", []string{"store", "path"}, []string{"store", "prune"}, "pnpm")
	case "yarn.cache":
		return yarnCacheCandidates(root, rule)
	case "bun.cache":
		return queriedCacheCandidate(root, rule, "bun", []string{"pm", "cache"}, []string{"pm", "cache", "rm"}, "bun")
	case "pip.cache":
		return pipCacheCandidates(root, rule)
	case "uv.cache":
		return queriedCacheCandidate(root, rule, "uv", []string{"cache", "dir"}, []string{"cache", "clean"}, "uv")
	case "go.build-cache":
		return goCacheCandidate(root, rule, "GOCACHE", []string{"clean", "-cache"})
	case "go.mod-cache":
		return goCacheCandidate(root, rule, "GOMODCACHE", []string{"clean", "-modcache"})
	case "cargo.autoclean":
		return cargoAutocleanCandidates(root, rule)
	case "rustup.toolchain":
		return rustupCommandCandidates(root, rule)
	case "rbenv.version":
		return rbenvCommandCandidates(root, rule)
	case "asdf.version":
		return versionManagerCandidates(root, rule, "asdf")
	case "mise.version":
		return versionManagerCandidates(root, rule, "mise")
	case "homebrew.cleanup":
		return homebrewCommandCandidates(root, rule)
	case "conda.cleanup":
		return condaCommandCandidates(root, rule)
	case "pyenv.version":
		return pyenvCommandCandidates(root, rule)
	case "android.avd":
		return androidAVDCandidates(root, rule)
	case "ios.simulator":
		return iosSimulatorCandidates(root, rule)
	default:
		return nil
	}
}

var cargoAutocleanSizePattern = regexp.MustCompile(`(?i)Size changed .*?\(([+-]?[\d,.]+)\s*([kmgt]?i?b)`)

func cargoAutocleanCandidates(root string, rule Rule) []scanCandidate {
	tool := findManagerBinary("cargo-cache")
	if tool == "" {
		return nil
	}
	home := os.Getenv("CARGO_HOME")
	if home == "" {
		userHome, _ := os.UserHomeDir()
		home = filepath.Join(userHome, ".cargo")
	}
	target := filepath.Clean(home)
	if !isWithin(root, target) || !onRootFilesystem(root, target) {
		return nil
	}
	if info, err := os.Stat(target); err != nil || !info.IsDir() {
		return nil
	}
	output, err := runManager(tool, "--autoclean", "--dry-run")
	if err != nil {
		return nil
	}
	match := cargoAutocleanSizePattern.FindSubmatch(output)
	if len(match) != 3 {
		return nil
	}
	amount := strings.TrimLeft(strings.TrimSpace(string(match[1])), "+-") + " " + strings.TrimSpace(string(match[2]))
	bytes := parseHumanBytes(amount)
	if bytes <= 0 {
		return nil
	}
	rule.Kind = "command"
	rule.Action = "command"
	rule.Command = []string{tool, "--autoclean"}
	return []scanCandidate{{rule: rule, target: target, estimatedBytes: bytes, estimatedFiles: 1, labels: []string{"managed:cargo", "provider:cargo"}}}
}

func queriedCacheCandidate(root string, rule Rule, binary string, query, cleanup []string, provider string, suffix ...string) []scanCandidate {
	tool := findManagerBinary(binary)
	if tool == "" {
		return nil
	}
	output, err := runManager(tool, query...)
	if err != nil {
		return nil
	}
	target := filepath.Clean(strings.TrimSpace(string(output)))
	if len(suffix) > 0 {
		target = filepath.Join(target, suffix[0])
	}
	if target == "." || !isWithin(root, target) || !onRootFilesystem(root, target) {
		return nil
	}
	info, err := os.Stat(target)
	if err != nil || !info.IsDir() {
		return nil
	}
	rule.Command = append([]string{tool}, cleanup...)
	rule.Targets = []string{target}
	return []scanCandidate{{rule: rule, target: target, labels: []string{"managed:" + provider, "provider:" + provider}}}
}

func versionManagerCandidates(root string, rule Rule, manager string) []scanCandidate {
	dataRoot := ""
	var binary string
	switch manager {
	case "asdf":
		dataRoot = os.Getenv("ASDF_DATA_DIR")
		if dataRoot == "" {
			home, _ := os.UserHomeDir()
			dataRoot = filepath.Join(home, ".asdf")
		}
		binary = findManagerBinary("asdf", filepath.Join(dataRoot, "bin", "asdf"))
	case "mise":
		dataRoot = os.Getenv("MISE_DATA_DIR")
		if dataRoot == "" {
			home, _ := os.UserHomeDir()
			dataRoot = filepath.Join(home, ".local", "share", "mise")
		}
		home, _ := os.UserHomeDir()
		binary = findManagerBinary("mise", filepath.Join(home, ".local", "bin", "mise"))
	}
	installs := filepath.Join(dataRoot, "installs")
	if binary == "" || !isWithin(root, installs) || !onRootFilesystem(root, installs) {
		return nil
	}
	tools, err := os.ReadDir(installs)
	if err != nil {
		return nil
	}
	result := []scanCandidate{}
	for _, tool := range tools {
		if !tool.IsDir() {
			continue
		}
		toolPath := filepath.Join(installs, tool.Name())
		versions, err := os.ReadDir(toolPath)
		if err != nil {
			continue
		}
		for _, version := range versions {
			if !version.IsDir() {
				continue
			}
			target := filepath.Join(toolPath, version.Name())
			ruleCopy := rule
			if manager == "asdf" {
				ruleCopy.Command = []string{binary, "uninstall", tool.Name(), version.Name()}
				ruleCopy.Native = "asdf uninstall " + tool.Name() + " " + version.Name()
			} else {
				ruleCopy.Command = []string{binary, "uninstall", tool.Name() + "@" + version.Name()}
				ruleCopy.Native = "mise uninstall " + tool.Name() + "@" + version.Name()
			}
			ruleCopy.Targets = []string{target}
			result = append(result, scanCandidate{rule: ruleCopy, target: target, labels: []string{"managed:" + manager, "provider:" + manager}})
		}
	}
	return result
}

func yarnCacheCandidates(root string, rule Rule) []scanCandidate {
	yarn := findManagerBinary("yarn")
	if yarn == "" {
		return nil
	}
	output, err := runManager(yarn, "cache", "dir")
	if err != nil || strings.TrimSpace(string(output)) == "" {
		output, err = runManager(yarn, "config", "get", "cacheFolder")
	}
	if err != nil {
		return nil
	}
	target := filepath.Clean(strings.TrimSpace(string(output)))
	if target == "." || !isWithin(root, target) || !onRootFilesystem(root, target) {
		output, err = runManager(yarn, "config", "get", "cacheFolder")
		if err != nil {
			return nil
		}
		target = filepath.Clean(strings.TrimSpace(string(output)))
		if target == "." || !isWithin(root, target) || !onRootFilesystem(root, target) {
			return nil
		}
	}
	info, err := os.Stat(target)
	if err != nil || !info.IsDir() {
		return nil
	}
	rule.Command = []string{yarn, "cache", "clean"}
	rule.Targets = []string{target}
	return []scanCandidate{{rule: rule, target: target, labels: []string{"managed:yarn", "provider:yarn"}}}
}

func pipCacheCandidates(root string, rule Rule) []scanCandidate {
	for _, candidate := range []struct {
		binary string
		prefix []string
	}{
		{binary: "pip3"}, {binary: "pip"}, {binary: "python3", prefix: []string{"-m", "pip"}}, {binary: "python", prefix: []string{"-m", "pip"}}, {binary: "py", prefix: []string{"-m", "pip"}},
	} {
		tool := findManagerBinary(candidate.binary)
		if tool == "" {
			continue
		}
		query := append(append([]string{}, candidate.prefix...), "cache", "dir")
		output, err := runManager(tool, query...)
		if err != nil {
			continue
		}
		target := filepath.Clean(strings.TrimSpace(string(output)))
		if target == "." || !isWithin(root, target) || !onRootFilesystem(root, target) {
			continue
		}
		info, err := os.Stat(target)
		if err != nil || !info.IsDir() {
			continue
		}
		cleanup := append(append([]string{}, candidate.prefix...), "cache", "purge")
		rule.Command = append([]string{tool}, cleanup...)
		rule.Targets = []string{target}
		return []scanCandidate{{rule: rule, target: target, labels: []string{"managed:pip", "provider:pip"}}}
	}
	return nil
}

func goCacheCandidate(root string, rule Rule, envName string, cleanup []string) []scanCandidate {
	tool := findManagerBinary("go")
	if tool == "" {
		return nil
	}
	output, err := runManager(tool, "env", envName)
	if err != nil {
		return nil
	}
	target := filepath.Clean(strings.TrimSpace(string(output)))
	if target == "." || !isWithin(root, target) || !onRootFilesystem(root, target) {
		return nil
	}
	info, err := os.Stat(target)
	if err != nil || !info.IsDir() {
		return nil
	}
	rule.Command = append([]string{tool}, cleanup...)
	rule.Targets = []string{target}
	return []scanCandidate{{rule: rule, target: target, labels: []string{"managed:go", "provider:go"}}}
}

func rustupCommandCandidates(root string, rule Rule) []scanCandidate {
	cargoHome := os.Getenv("CARGO_HOME")
	if cargoHome == "" {
		home, _ := os.UserHomeDir()
		cargoHome = filepath.Join(home, ".cargo")
	}
	rustup := findManagerBinary("rustup", filepath.Join(cargoHome, "bin", "rustup"))
	if rustup == "" {
		return nil
	}
	rustupHome := os.Getenv("RUSTUP_HOME")
	if rustupHome == "" {
		home, _ := os.UserHomeDir()
		rustupHome = filepath.Join(home, ".rustup")
	}
	toolchains := filepath.Join(rustupHome, "toolchains")
	if !isWithin(root, toolchains) || !onRootFilesystem(root, toolchains) {
		return nil
	}
	entries, err := os.ReadDir(toolchains)
	if err != nil {
		return nil
	}
	result := []scanCandidate{}
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		target := filepath.Join(toolchains, entry.Name())
		ruleCopy := rule
		ruleCopy.Command = []string{rustup, "toolchain", "uninstall", entry.Name()}
		ruleCopy.Native = "rustup toolchain uninstall " + entry.Name()
		ruleCopy.Targets = []string{target}
		result = append(result, scanCandidate{rule: ruleCopy, target: target, labels: []string{"managed:rustup", "provider:rustup"}})
	}
	return result
}

func rbenvCommandCandidates(root string, rule Rule) []scanCandidate {
	rbenvRoot := os.Getenv("RBENV_ROOT")
	if rbenvRoot == "" {
		home, _ := os.UserHomeDir()
		rbenvRoot = filepath.Join(home, ".rbenv")
	}
	rbenv := findManagerBinary("rbenv", filepath.Join(rbenvRoot, "bin", "rbenv"))
	if rbenv == "" {
		return nil
	}
	commands, err := runManager(rbenv, "commands")
	if err != nil || !strings.Contains("\n"+string(commands)+"\n", "\nuninstall\n") {
		return nil
	}
	versionsPath := filepath.Join(rbenvRoot, "versions")
	if !isWithin(root, versionsPath) || !onRootFilesystem(root, versionsPath) {
		return nil
	}
	entries, err := os.ReadDir(versionsPath)
	if err != nil {
		return nil
	}
	result := []scanCandidate{}
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		target := filepath.Join(versionsPath, entry.Name())
		ruleCopy := rule
		ruleCopy.Command = []string{rbenv, "uninstall", "--force", entry.Name()}
		ruleCopy.Native = "rbenv uninstall " + entry.Name()
		ruleCopy.Targets = []string{target}
		result = append(result, scanCandidate{rule: ruleCopy, target: target, labels: []string{"managed:rbenv", "provider:rbenv"}})
	}
	return result
}

func runManager(binary string, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
	defer cancel()
	return exec.CommandContext(ctx, binary, args...).Output()
}

func dockerCommandCandidates(root string, rule Rule) []scanCandidate {
	target := dockerStorageTarget()
	if target == "" || !isWithin(root, target) {
		return nil
	}
	if _, err := os.Stat(target); err != nil {
		return nil
	}
	if _, bytes, ok := dockerCandidate(root, rule); ok && bytes > 0 {
		return []scanCandidate{{rule: rule, target: target, estimatedBytes: bytes, estimatedFiles: 1, labels: []string{"managed:docker", "provider:docker"}}}
	}
	// Docker.raw / VHDX is an opaque storage image. If Docker cannot report
	// reclaimable resources, do not surface the image as a cleanup candidate.
	// Showing an informational row here made the same warning appear on every
	// scan even when Docker was stopped or had nothing to prune.
	return nil
}

func homebrewCommandCandidates(root string, rule Rule) []scanCandidate {
	home, _ := os.UserHomeDir()
	brew := findManagerBinary("brew", "/opt/homebrew/bin/brew", "/usr/local/bin/brew", filepath.Join(home, ".linuxbrew", "bin", "brew"))
	if brew == "" {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	output, err := exec.CommandContext(ctx, brew, "--cache").Output()
	if err != nil {
		return nil
	}
	target := filepath.Clean(strings.TrimSpace(string(output)))
	if target == "." || !isWithin(root, target) || !onRootFilesystem(root, target) {
		return nil
	}
	info, err := os.Stat(target)
	if err != nil || !info.IsDir() {
		return nil
	}
	rule.Command = []string{brew, "cleanup", "--prune=all"}
	rule.Targets = []string{target}
	return []scanCandidate{{rule: rule, target: target, labels: []string{"managed:homebrew", "provider:homebrew"}}}
}

func condaCommandCandidates(root string, rule Rule) []scanCandidate {
	home, _ := os.UserHomeDir()
	conda := findManagerBinary("conda", filepath.Join(home, "miniconda3", "bin", "conda"), filepath.Join(home, "anaconda3", "bin", "conda"), filepath.Join(home, "miniforge3", "bin", "conda"), filepath.Join(home, "mambaforge", "bin", "conda"))
	if conda == "" {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
	defer cancel()
	output, err := exec.CommandContext(ctx, conda, "info", "--json").Output()
	if err != nil {
		return nil
	}
	var info struct {
		PackageDirs []string `json:"pkgs_dirs"`
	}
	if json.Unmarshal(output, &info) != nil {
		return nil
	}
	paths := []string{}
	for _, path := range info.PackageDirs {
		path = filepath.Clean(path)
		stat, statErr := os.Stat(path)
		if statErr != nil {
			continue
		}
		if !isWithin(root, path) || !stat.IsDir() || !onRootFilesystem(root, path) {
			return nil
		}
		paths = append(paths, path)
	}
	if len(paths) == 0 {
		return nil
	}
	sort.Strings(paths)
	rule.Command = []string{conda, "clean", "--all", "--yes"}
	rule.Targets = paths
	return []scanCandidate{{rule: rule, target: paths[0], labels: []string{"managed:conda", "provider:conda"}}}
}

func onRootFilesystem(root, target string) bool {
	rootInfo, err := os.Stat(root)
	if err != nil {
		return false
	}
	targetInfo, err := os.Stat(target)
	if err != nil {
		return false
	}
	return !isOtherFilesystem(filesystemIdentity(root, rootInfo), target, targetInfo)
}

func pyenvCommandCandidates(root string, rule Rule) []scanCandidate {
	pyenvRoot := os.Getenv("PYENV_ROOT")
	if pyenvRoot == "" {
		home, _ := os.UserHomeDir()
		pyenvRoot = filepath.Join(home, ".pyenv")
	}
	pyenv := findManagerBinary("pyenv", filepath.Join(pyenvRoot, "bin", "pyenv"))
	if pyenv == "" {
		return nil
	}
	versionsPath := filepath.Join(pyenvRoot, "versions")
	if !isWithin(root, versionsPath) || !onRootFilesystem(root, versionsPath) {
		return nil
	}
	entries, err := os.ReadDir(versionsPath)
	if err != nil {
		return nil
	}
	result := []scanCandidate{}
	for _, entry := range entries {
		if !entry.IsDir() || strings.HasPrefix(entry.Name(), ".") {
			continue
		}
		target := filepath.Join(versionsPath, entry.Name())
		ruleCopy := rule
		ruleCopy.Command = []string{pyenv, "uninstall", "--force", entry.Name()}
		ruleCopy.Native = "pyenv uninstall " + entry.Name()
		ruleCopy.Targets = []string{target}
		result = append(result, scanCandidate{rule: ruleCopy, target: target, labels: []string{"managed:pyenv", "provider:pyenv"}})
	}
	return result
}

func androidAVDCandidates(root string, rule Rule) []scanCandidate {
	avdHome := os.Getenv("ANDROID_AVD_HOME")
	if avdHome == "" {
		androidUserHome := os.Getenv("ANDROID_USER_HOME")
		if androidUserHome == "" {
			home, _ := os.UserHomeDir()
			androidUserHome = filepath.Join(home, ".android")
		}
		avdHome = filepath.Join(androidUserHome, "avd")
	}
	if !isWithin(root, avdHome) || !onRootFilesystem(root, avdHome) {
		return nil
	}
	info, err := os.Stat(avdHome)
	if err != nil || !info.IsDir() {
		return nil
	}
	tool := androidAVDManager()
	if tool == "" {
		return nil
	}
	entries, err := os.ReadDir(avdHome)
	if err != nil {
		return nil
	}
	result := []scanCandidate{}
	for _, entry := range entries {
		if entry.IsDir() || !strings.EqualFold(filepath.Ext(entry.Name()), ".ini") {
			continue
		}
		name := strings.TrimSuffix(entry.Name(), filepath.Ext(entry.Name()))
		target := filepath.Join(avdHome, name+".avd")
		contents, readErr := os.ReadFile(filepath.Join(avdHome, entry.Name()))
		if readErr == nil {
			for _, line := range strings.Split(string(contents), "\n") {
				key, value, ok := strings.Cut(strings.TrimSpace(line), "=")
				if ok && key == "path" && strings.TrimSpace(value) != "" {
					target = filepath.Clean(strings.TrimSpace(value))
					break
				}
			}
		}
		if !isWithin(root, target) || !onRootFilesystem(root, target) {
			continue
		}
		deviceInfo, statErr := os.Stat(target)
		if statErr != nil || !deviceInfo.IsDir() {
			continue
		}
		deviceRule := rule
		deviceRule.Name = "Android 가상 기기 · " + name
		deviceRule.Command = []string{tool, "delete", "avd", "-n", name}
		deviceRule.Native = "avdmanager delete avd -n " + name
		deviceRule.Targets = []string{target}
		result = append(result, scanCandidate{rule: deviceRule, target: target, labels: []string{"managed:android-avd", "provider:avdmanager"}})
	}
	sort.Slice(result, func(i, j int) bool { return result[i].target < result[j].target })
	return result
}

type simctlDevice struct {
	Name              string `json:"name"`
	UDID              string `json:"udid"`
	State             string `json:"state"`
	IsAvailable       bool   `json:"isAvailable"`
	AvailabilityError string `json:"availabilityError"`
}

func iosSimulatorCandidates(root string, rule Rule) []scanCandidate {
	if runtime.GOOS != "darwin" {
		return nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return nil
	}
	deviceRoot := filepath.Join(home, "Library", "Developer", "CoreSimulator", "Devices")
	if !isWithin(root, deviceRoot) || !onRootFilesystem(root, deviceRoot) {
		return nil
	}
	info, err := os.Stat(deviceRoot)
	if err != nil || !info.IsDir() {
		return nil
	}
	xcrun := findManagerBinary("xcrun", "/usr/bin/xcrun")
	if xcrun == "" {
		return nil
	}
	output, err := runManager(xcrun, "simctl", "list", "devices", "--json")
	if err != nil {
		return iosSimulatorDirectoryCandidates(root, rule, deviceRoot)
	}
	var payload struct {
		Devices map[string][]simctlDevice `json:"devices"`
	}
	if json.Unmarshal(output, &payload) != nil {
		return iosSimulatorDirectoryCandidates(root, rule, deviceRoot)
	}
	result := []scanCandidate{}
	for runtimeID, devices := range payload.Devices {
		for _, device := range devices {
			if device.UDID == "" || strings.EqualFold(device.State, "Booted") {
				continue
			}
			target := filepath.Join(deviceRoot, device.UDID)
			deviceInfo, statErr := os.Stat(target)
			if statErr != nil || !deviceInfo.IsDir() || !isWithin(root, target) || !onRootFilesystem(root, target) {
				continue
			}
			bytes, files := measure(target, &sync.Map{}, nil)
			if bytes <= 0 {
				continue
			}
			deviceRule := rule
			deviceRule.Name = "iOS 시뮬레이터 · " + device.Name
			deviceRule.Command = []string{xcrun, "simctl", "delete", device.UDID}
			deviceRule.Native = "xcrun simctl delete " + device.UDID
			deviceRule.Targets = []string{target}
			labels := []string{"managed:ios-simulator", "provider:simctl", "simulator:ios"}
			if !device.IsAvailable || device.AvailabilityError != "" {
				labels = append(labels, "simulator:unavailable")
			}
			if runtimeID != "" {
				labels = append(labels, "runtime:"+runtimeID)
			}
			result = append(result, scanCandidate{rule: deviceRule, target: target, estimatedBytes: bytes, estimatedFiles: files, labels: labels})
		}
	}
	sort.Slice(result, func(i, j int) bool { return result[i].target < result[j].target })
	return result
}

// CoreSimulatorService can be unavailable while Xcode is updating runtimes or
// when the app has no permission to talk to launchd. Keep the storage visible
// in that case, but expose location only until simctl can identify each device
// safely enough to offer a delete command.
func iosSimulatorDirectoryCandidates(root string, rule Rule, deviceRoot string) []scanCandidate {
	entries, err := os.ReadDir(deviceRoot)
	if err != nil {
		return nil
	}
	result := []scanCandidate{}
	for _, entry := range entries {
		if !entry.IsDir() || len(entry.Name()) != 36 || strings.Count(entry.Name(), "-") != 4 {
			continue
		}
		target := filepath.Join(deviceRoot, entry.Name())
		if !isWithin(root, target) || !onRootFilesystem(root, target) {
			continue
		}
		bytes, files := measure(target, &sync.Map{}, nil)
		if bytes <= 0 {
			continue
		}
		deviceRule := rule
		deviceRule.Name = "iOS 시뮬레이터 · " + entry.Name()[:8]
		deviceRule.Action = ""
		deviceRule.Command = nil
		deviceRule.Native = "Xcode의 Devices and Simulators에서 이 시뮬레이터를 확인하세요."
		deviceRule.Targets = []string{target}
		result = append(result, scanCandidate{rule: deviceRule, target: target, estimatedBytes: bytes, estimatedFiles: files, labels: []string{"managed:ios-simulator", "provider:simctl", "simulator:unknown"}})
	}
	sort.Slice(result, func(i, j int) bool { return result[i].target < result[j].target })
	return result
}

func androidAVDManager() string {
	roots := []string{}
	for _, env := range []string{"ANDROID_HOME", "ANDROID_SDK_ROOT"} {
		if value := os.Getenv(env); value != "" {
			roots = append(roots, value)
		}
	}
	home, _ := os.UserHomeDir()
	switch runtime.GOOS {
	case "darwin":
		roots = append(roots, filepath.Join(home, "Library", "Android", "sdk"))
	case "windows":
		local := os.Getenv("LOCALAPPDATA")
		if local != "" {
			roots = append(roots, filepath.Join(local, "Android", "Sdk"))
		}
	default:
		roots = append(roots, filepath.Join(home, "Android", "Sdk"))
	}
	alternatives := []string{}
	for _, sdk := range roots {
		cmdlineTools := filepath.Join(sdk, "cmdline-tools")
		versions, err := os.ReadDir(cmdlineTools)
		if err == nil {
			sort.Slice(versions, func(i, j int) bool {
				if versions[i].Name() == "latest" {
					return true
				}
				if versions[j].Name() == "latest" {
					return false
				}
				return versions[i].Name() > versions[j].Name()
			})
			for _, version := range versions {
				alternatives = append(alternatives, filepath.Join(cmdlineTools, version.Name(), "bin", "avdmanager"))
			}
		}
		alternatives = append(alternatives, filepath.Join(sdk, "tools", "bin", "avdmanager"))
	}
	return findManagerBinary("avdmanager", alternatives...)
}

func findManagerBinary(name string, alternatives ...string) string {
	if path, err := exec.LookPath(name); err == nil {
		return path
	}
	for _, path := range alternatives {
		if path == "" {
			continue
		}
		info, err := os.Stat(path)
		if err == nil && !info.IsDir() {
			return path
		}
	}
	return ""
}

// commandOwnsPath reports whether a command-backed provider is responsible
// for a path. When it is, the normal file rules must not emit a second,
// direct-delete candidate for the same storage.
func commandOwnsPath(candidates []scanCandidate, path string) bool {
	clean := filepath.Clean(path)
	for _, candidate := range candidates {
		if candidate.rule.Kind != "command" {
			continue
		}
		targets := candidate.rule.Targets
		if len(targets) == 0 {
			targets = []string{candidate.target}
		}
		for _, target := range targets {
			target = filepath.Clean(target)
			if clean == target || isWithin(target, clean) {
				return true
			}
		}
	}
	return false
}

func mergeLabels(groups ...[]string) []string {
	seen := map[string]bool{}
	result := []string{}
	for _, group := range groups {
		for _, label := range group {
			label = strings.TrimSpace(label)
			if label == "" || seen[label] {
				continue
			}
			seen[label] = true
			result = append(result, label)
		}
	}
	return result
}

func dockerCandidate(root string, rule Rule) (string, int64, bool) {
	if _, err := exec.LookPath(rule.Probe[0]); err != nil {
		return "", 0, false
	}
	target := dockerStorageTarget()
	if target == "" || !isWithin(root, target) {
		return "", 0, false
	}
	if _, err := os.Stat(target); err != nil {
		return "", 0, false
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	output, err := exec.CommandContext(ctx, rule.Probe[0], rule.Probe[1:]...).Output()
	if err != nil {
		return "", 0, false
	}
	return target, dockerReclaimableBytes(output), true
}

func dockerStorageTarget() string {
	home, _ := os.UserHomeDir()
	switch runtime.GOOS {
	case "darwin":
		for _, path := range []string{
			filepath.Join(home, "Library", "Containers", "com.docker.docker", "Data", "vms", "0", "data", "Docker.raw"),
			filepath.Join(home, "Library", "Containers", "com.docker.docker", "Data", "vms", "0", "data", "Docker.raw.vhdx"),
		} {
			if _, err := os.Stat(path); err == nil {
				return path
			}
		}
	case "windows":
		local := os.Getenv("LOCALAPPDATA")
		for _, path := range []string{
			filepath.Join(local, "Docker", "wsl", "data", "ext4.vhdx"),
			filepath.Join(local, "Docker", "wsl", "disk", "docker_data.vhdx"),
		} {
			if path != "" {
				if _, err := os.Stat(path); err == nil {
					return path
				}
			}
		}
	default:
		if _, err := os.Stat("/var/lib/docker"); err == nil {
			return "/var/lib/docker"
		}
	}
	return ""
}

func dockerReclaimableBytes(output []byte) int64 {
	var total int64
	for _, line := range strings.Split(string(output), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var row struct {
			Reclaimable string `json:"Reclaimable"`
		}
		if json.Unmarshal([]byte(line), &row) != nil {
			continue
		}
		value := strings.TrimSpace(strings.Split(row.Reclaimable, "(")[0])
		total += parseHumanBytes(value)
	}
	return total
}

func parseHumanBytes(value string) int64 {
	normalized := strings.ReplaceAll(strings.TrimSpace(value), ",", "")
	if normalized == "" {
		return 0
	}
	end := 0
	for end < len(normalized) && ((normalized[end] >= '0' && normalized[end] <= '9') || normalized[end] == '.') {
		end++
	}
	number, err := strconv.ParseFloat(normalized[:end], 64)
	if err != nil || number <= 0 {
		return 0
	}
	unit := strings.ToUpper(strings.TrimSpace(normalized[end:]))
	multiplier := float64(1)
	switch unit {
	case "KB", "KIB":
		multiplier = 1 << 10
	case "MB", "MIB":
		multiplier = 1 << 20
	case "GB", "GIB":
		multiplier = 1 << 30
	case "TB", "TIB":
		multiplier = 1 << 40
	}
	return int64(number * multiplier)
}
