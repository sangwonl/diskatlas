package core

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestIncrementalDirectoryAggregatesMatchRebuild(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(home, "shed-aggregate-test")
	dir := filepath.Join(root, "Documents")
	index := newAnalysisIndex(root)
	for _, name := range []string{"a", "b", "c", "d", "e"} {
		index.Files[filepath.Join(dir, name)] = cachedFile{Bytes: 120 * 1024 * 1024}
	}
	initial := summarizeDirectories(root, index, nil)
	if len(initial) != 1 || initial[0].Bytes != 600*1024*1024 || initial[0].Files != 5 {
		t.Fatalf("unexpected initial directory summary: %+v", initial)
	}
	removed := filepath.Join(dir, "a")
	adjustDirectoryAggregate(root, index.Directories, removed, -index.Files[removed].Bytes, -1)
	delete(index.Files, removed)
	added := filepath.Join(dir, "nested", "f")
	index.Files[added] = cachedFile{Bytes: 250 * 1024 * 1024}
	adjustDirectoryAggregate(root, index.Directories, added, index.Files[added].Bytes, 1)

	rebuilt := newAnalysisIndex(root)
	rebuilt.Files = index.Files
	got := summarizeDirectories(root, index, nil)
	want := summarizeDirectories(root, rebuilt, nil)
	if !reflect.DeepEqual(index.Directories, rebuilt.Directories) || !reflect.DeepEqual(got, want) {
		t.Fatalf("incremental directory aggregates differ from full rebuild: got=%+v want=%+v", got, want)
	}
}
