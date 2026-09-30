package core

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

type CleanupRequest struct {
	IDs             []string `json:"ids"`
	Mode            string   `json:"mode"`
	AcknowledgeRisk bool     `json:"acknowledgeRisk"`
	Confirmation    string   `json:"confirmation"`
}

type BlockedItem struct {
	ID     string `json:"id"`
	Path   string `json:"path"`
	Reason string `json:"reason"`
}

type CleanupPreview struct {
	Items             []Item        `json:"items"`
	Blocked           []BlockedItem `json:"blocked"`
	TotalBytes        int64         `json:"totalBytes"`
	ConfirmationToken string        `json:"confirmationToken"`
	Mode              string        `json:"mode"`
	ImmediateReclaim  bool          `json:"immediateReclaim"`
}

type CleanupResult struct {
	Mode           string        `json:"mode"`
	Completed      []Item        `json:"completed"`
	Failed         []BlockedItem `json:"failed"`
	EstimatedBytes int64         `json:"estimatedBytes"`
	ActualBytes    int64         `json:"actualBytes"`
	QuarantineID   string        `json:"quarantineId,omitempty"`
	FinishedAt     string        `json:"finishedAt"`
}

type quarantineManifest struct {
	ID        string `json:"id"`
	CreatedAt string `json:"createdAt"`
	Items     []struct {
		Item           Item   `json:"item"`
		OriginalPath   string `json:"originalPath"`
		QuarantinePath string `json:"quarantinePath"`
	} `json:"items"`
}

func PreviewCleanup(scan *Result, request CleanupRequest) CleanupPreview {
	mode := strings.ToLower(strings.TrimSpace(request.Mode))
	if mode != "delete" && mode != "quarantine" {
		mode = "delete"
	}
	preview := CleanupPreview{Items: []Item{}, Blocked: []BlockedItem{}, Mode: mode, ImmediateReclaim: mode == "delete"}
	if scan == nil {
		preview.Blocked = append(preview.Blocked, BlockedItem{Reason: "Run a scan before cleaning"})
		return preview
	}
	byID := make(map[string]Item, len(scan.Items))
	for _, item := range scan.Items {
		byID[item.ID] = item
	}
	seen := map[string]bool{}
	for _, id := range request.IDs {
		if seen[id] {
			continue
		}
		seen[id] = true
		item, ok := byID[id]
		if !ok {
			preview.Blocked = append(preview.Blocked, BlockedItem{ID: id, Reason: "Item is not part of the latest scan"})
			continue
		}
		if reason := validateCleanupItem(scan, item, request.AcknowledgeRisk); reason != "" {
			preview.Blocked = append(preview.Blocked, BlockedItem{ID: item.ID, Path: item.Path, Reason: reason})
			continue
		}
		preview.Items = append(preview.Items, item)
		preview.TotalBytes += item.Bytes
	}
	preview.ConfirmationToken = fmt.Sprintf("DELETE %d", len(preview.Items))
	if mode == "quarantine" {
		preview.ConfirmationToken = fmt.Sprintf("QUARANTINE %d", len(preview.Items))
	}
	return preview
}

func ExecuteCleanup(scan *Result, request CleanupRequest) (CleanupResult, error) {
	preview := PreviewCleanup(scan, request)
	result := CleanupResult{Mode: preview.Mode, Completed: []Item{}, Failed: append([]BlockedItem{}, preview.Blocked...), FinishedAt: time.Now().UTC().Format(time.RFC3339)}
	if len(preview.Items) == 0 {
		return result, errors.New("no eligible items selected")
	}
	if request.Confirmation != preview.ConfirmationToken {
		return result, fmt.Errorf("confirmation must exactly match %q", preview.ConfirmationToken)
	}
	measurementPath := filepath.Dir(preview.Items[0].Path)
	before, _ := diskFree(measurementPath)
	var manifest quarantineManifest
	var quarantineRoot string
	if preview.Mode == "quarantine" {
		manifest.ID = time.Now().UTC().Format("20060102T150405.000000000Z")
		manifest.CreatedAt = time.Now().UTC().Format(time.RFC3339)
		stateRoot, err := stateDirectory()
		if err != nil {
			return result, err
		}
		quarantineRoot = filepath.Join(stateRoot, "quarantine", manifest.ID)
		if err := os.MkdirAll(quarantineRoot, 0o700); err != nil {
			return result, err
		}
		result.QuarantineID = manifest.ID
	}
	for index, item := range preview.Items {
		if reason := validateCleanupItem(scan, item, request.AcknowledgeRisk); reason != "" {
			result.Failed = append(result.Failed, BlockedItem{ID: item.ID, Path: item.Path, Reason: reason})
			continue
		}
		if preview.Mode == "delete" {
			if err := os.RemoveAll(item.Path); err != nil {
				result.Failed = append(result.Failed, BlockedItem{ID: item.ID, Path: item.Path, Reason: err.Error()})
				continue
			}
		} else {
			destination := filepath.Join(quarantineRoot, fmt.Sprintf("%04d-%s", index+1, filepath.Base(item.Path)))
			if err := os.Rename(item.Path, destination); err != nil {
				result.Failed = append(result.Failed, BlockedItem{ID: item.ID, Path: item.Path, Reason: "quarantine move failed: " + err.Error()})
				continue
			}
			manifest.Items = append(manifest.Items, struct {
				Item           Item   `json:"item"`
				OriginalPath   string `json:"originalPath"`
				QuarantinePath string `json:"quarantinePath"`
			}{Item: item, OriginalPath: item.Path, QuarantinePath: destination})
		}
		result.Completed = append(result.Completed, item)
		result.EstimatedBytes += item.Bytes
	}
	if preview.Mode == "quarantine" {
		if err := writeManifest(quarantineRoot, manifest); err != nil {
			return result, err
		}
	}
	after, _ := diskFree(measurementPath)
	if after > before {
		result.ActualBytes = int64(after - before)
	}
	result.FinishedAt = time.Now().UTC().Format(time.RFC3339)
	_ = appendHistory(result)
	return result, nil
}

func validateCleanupItem(scan *Result, item Item, acknowledgeRisk bool) string {
	clean, err := filepath.Abs(item.Path)
	if err != nil || clean != filepath.Clean(item.Path) {
		return "Path could not be normalized safely"
	}
	if item.Tier == Protected || isProtected(clean) {
		return "Protected paths cannot be cleaned by Shed"
	}
	if (item.Tier == Caution || item.Tier == Review) && !acknowledgeRisk {
		return "Caution and review items require explicit risk acknowledgement"
	}
	home, _ := os.UserHomeDir()
	if clean == home || clean == filepath.VolumeName(clean)+string(filepath.Separator) {
		return "A home or filesystem root can never be cleaned"
	}
	insideRoot := false
	for _, root := range scan.Roots {
		if isWithin(root, clean) {
			insideRoot = true
			break
		}
	}
	if !insideRoot {
		return "Path escaped the scanned root"
	}
	info, err := os.Lstat(clean)
	if err != nil {
		return "Path no longer exists"
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return "Symbolic links are never cleaned"
	}
	resolved, err := filepath.EvalSymlinks(clean)
	if err != nil || resolved != clean {
		return "Path contains a symbolic-link escape"
	}
	if !matchesRule(item) {
		return "Path no longer matches its discovery rule"
	}
	if item.ProjectPath != "" && gitTracked(item.ProjectPath, item.Path) && !acknowledgeRisk {
		return "Git now tracks this path; explicit risk acknowledgement is required"
	}
	if pathInUse(clean) {
		return "Path is currently in use by another process"
	}
	return ""
}

func matchesRule(item Item) bool {
	for _, rule := range Rules() {
		if rule.ID != item.RuleID {
			continue
		}
		if rule.Kind == "global" {
			for _, target := range rule.Targets {
				if filepath.Clean(target) == filepath.Clean(item.Path) {
					return true
				}
			}
			return false
		}
		if item.ProjectPath == "" || !hasAny(item.ProjectPath, rule.Markers) {
			return false
		}
		for _, target := range rule.Targets {
			if filepath.Clean(filepath.Join(item.ProjectPath, target)) == filepath.Clean(item.Path) {
				return true
			}
		}
	}
	return false
}

func pathInUse(path string) bool {
	ctx, cancel := context.WithTimeout(context.Background(), 1500*time.Millisecond)
	defer cancel()
	output, err := exec.CommandContext(ctx, "lsof", "-t", "+D", path).Output()
	return err == nil && strings.TrimSpace(string(output)) != ""
}

func stateDirectory() (string, error) {
	config, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	state := filepath.Join(config, "Shed")
	if err := os.MkdirAll(state, 0o700); err != nil {
		return "", err
	}
	return state, nil
}

func writeManifest(root string, manifest quarantineManifest) error {
	data, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(root, "manifest.json"), data, 0o600)
}

func appendHistory(result CleanupResult) error {
	state, err := stateDirectory()
	if err != nil {
		return err
	}
	data, err := json.Marshal(result)
	if err != nil {
		return err
	}
	file, err := os.OpenFile(filepath.Join(state, "history.jsonl"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer file.Close()
	_, err = file.Write(append(data, '\n'))
	return err
}
