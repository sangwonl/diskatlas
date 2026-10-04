package core

import (
	"os"
	"path/filepath"
	"testing"
)

func TestDiscoverAnalysisInventoriesHiddenAndGeneratedDirectories(t *testing.T) {
	root := t.TempDir()
	files := map[string]string{
		"Documents/notes.txt":           "ordinary document",
		".config/tool/settings.json":    "{}",
		"project/.git/objects/pack":     "git object data",
		"project/node_modules/pkg/data": "installed package data",
	}
	for relative, contents := range files {
		path := filepath.Join(root, relative)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(contents), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	_, _, index, err := discoverAnalysis(root, Storage{}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	for relative := range files {
		path := filepath.Join(root, relative)
		if _, ok := index.Files[path]; !ok {
			t.Errorf("file missing from full inventory: %s", relative)
		}
	}
}
