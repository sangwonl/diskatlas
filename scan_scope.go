package main

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
)

type scanScopeSettings struct {
	Path     string `json:"path"`
	Bookmark string `json:"bookmark,omitempty"`
}

func expandScanRoot(path string) string {
	path = strings.TrimSpace(path)
	if path == "~" || strings.HasPrefix(path, "~/") || strings.HasPrefix(path, `~\`) {
		if home, err := os.UserHomeDir(); err == nil {
			if path == "~" {
				return home
			}
			return filepath.Join(home, path[2:])
		}
	}
	return path
}

func scanScopeFile() (string, error) {
	configDir, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(configDir, "Shed", "scan-scope.json"), nil
}

func readScanScope() (scanScopeSettings, error) {
	path, err := scanScopeFile()
	if err != nil {
		return scanScopeSettings{}, err
	}
	contents, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return scanScopeSettings{}, nil
	}
	if err != nil {
		return scanScopeSettings{}, err
	}
	var settings scanScopeSettings
	if err := json.Unmarshal(contents, &settings); err != nil {
		return scanScopeSettings{}, err
	}
	return settings, nil
}

func writeScanScope(settings scanScopeSettings) error {
	path, err := scanScopeFile()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	contents, err := json.Marshal(settings)
	if err != nil {
		return err
	}
	temporary := path + ".tmp"
	if err := os.WriteFile(temporary, contents, 0o600); err != nil {
		return err
	}
	if err := os.Rename(temporary, path); err != nil {
		_ = os.Remove(temporary)
		return err
	}
	return nil
}
