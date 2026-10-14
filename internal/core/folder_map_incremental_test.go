package core

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

func TestFolderMapIncrementalRefreshMatchesFullWalk(t *testing.T) {
	for _, operation := range []string{"unchanged", "partial-root", "modify", "add", "delete", "rename", "directory-move"} {
		t.Run(operation, func(t *testing.T) {
			root, err := filepath.EvalSymlinks(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			changed := filepath.Join(root, "changed")
			clean := filepath.Join(root, "clean", "nested")
			for _, dir := range []string{changed, clean} {
				if err := os.MkdirAll(dir, 0755); err != nil {
					t.Fatal(err)
				}
			}
			write := func(path string, size int) {
				t.Helper()
				if err := os.WriteFile(path, make([]byte, size), 0644); err != nil {
					t.Fatal(err)
				}
			}
			file := filepath.Join(changed, "data")
			write(file, 4096)
			write(filepath.Join(clean, "untouched"), 4096)
			measure := func(plan *folderMapRefreshPlan) (measuredFolder, int) {
				t.Helper()
				count := 0
				// Capacity zero keeps callbacks sequential for this accounting test.
				got, err := measureFolderEntry(context.Background(), root, root, folderMapAggregate{}, nil, plan, make(chan struct{}), func(string) { count++ })
				if err != nil {
					t.Fatal(err)
				}
				return got, count
			}
			initial, _ := measure(nil)
			info, _ := os.Lstat(root)
			for path, aggregate := range initial.Aggregates {
				aggregate.Journal = journalCursor{Kind: "fsevents", ID: 10}
				aggregate.JournalDevice = filesystemIdentity(root, info)
				initial.Aggregates[path] = aggregate
			}
			if operation == "partial-root" {
				aggregate := initial.Aggregates[root]
				aggregate.Complete = false
				initial.Aggregates[root] = aggregate
			}
			store := folderMapIndexStoreForRoot(root)
			store.loaded = true
			store.index = &folderMapAggregateIndex{Root: root, Directories: initial.Aggregates}
			defer folderMapIndexStores.Delete(root)
			changes := []journalChange{}
			switch operation {
			case "modify":
				write(file, 32768)
				changes = append(changes, journalChange{Path: file})
			case "add":
				added := filepath.Join(changed, "new")
				write(added, 16384)
				changes = append(changes, journalChange{Path: added})
			case "delete":
				if err := os.Remove(file); err != nil {
					t.Fatal(err)
				}
				changes = append(changes, journalChange{Path: file})
			case "rename":
				moved := filepath.Join(changed, "renamed")
				if err := os.Rename(file, moved); err != nil {
					t.Fatal(err)
				}
				changes = append(changes, journalChange{Path: file}, journalChange{Path: moved})
			case "directory-move":
				moved := filepath.Join(root, "moved")
				if err := os.Rename(changed, moved); err != nil {
					t.Fatal(err)
				}
				changes = append(changes, journalChange{Path: changed, Recursive: true}, journalChange{Path: moved, Recursive: true})
			}
			read := func(_ string, cursor journalCursor) (journalCursor, []journalChange, bool, error) {
				if cursor.ID == 0 {
					return journalCursor{Kind: "fsevents", ID: 20}, nil, true, nil
				}
				return journalCursor{Kind: "fsevents", ID: 21}, changes, true, nil
			}
			plan, checkpoint, _ := prepareFolderMapRefresh(root, root, read)
			if plan == nil {
				t.Fatal("expected incremental refresh plan")
			}
			if checkpoint.ID != 20 {
				t.Fatalf("checkpoint must precede scan, got %d", checkpoint.ID)
			}
			incremental, visited := measure(plan)
			full, fullVisited := measure(nil)
			if incremental.Bytes != full.Bytes || incremental.Files != full.Files {
				t.Fatalf("incremental %d/%d differs from full %d/%d", incremental.Bytes, incremental.Files, full.Bytes, full.Files)
			}
			if len(incremental.Aggregates) != len(full.Aggregates) {
				t.Fatalf("directory count mismatch: %d vs %d", len(incremental.Aggregates), len(full.Aggregates))
			}
			for path, want := range full.Aggregates {
				got, exists := incremental.Aggregates[path]
				if !exists || got.Bytes != want.Bytes || got.Files != want.Files {
					t.Errorf("wrong aggregate for %s: %+v vs %+v", path, got, want)
				}
			}
			if visited >= fullVisited {
				t.Fatalf("unchanged branch was traversed: incremental=%d full=%d", visited, fullVisited)
			}
			if operation == "unchanged" && visited != 0 {
				t.Fatalf("unchanged refresh visited %d entries", visited)
			}
		})
	}
}

func TestFolderMapRefreshFallsBackForIncompleteJournal(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	info, _ := os.Lstat(root)
	store := folderMapIndexStoreForRoot(root)
	store.loaded = true
	store.index = &folderMapAggregateIndex{Root: root, Directories: map[string]folderMapAggregate{root: {
		Known: true, Complete: true, Journal: journalCursor{Kind: "fsevents", ID: 10}, JournalDevice: filesystemIdentity(root, info),
	}}}
	defer folderMapIndexStores.Delete(root)
	for _, kind := range []string{"gap", "unresolved", "device-change"} {
		t.Run(kind, func(t *testing.T) {
			read := func(_ string, cursor journalCursor) (journalCursor, []journalChange, bool, error) {
				if cursor.ID == 0 {
					return journalCursor{Kind: "fsevents", ID: 20}, nil, true, nil
				}
				if kind == "gap" {
					return cursor, nil, false, fmt.Errorf("history expired")
				}
				return cursor, []journalChange{{}}, true, nil
			}
			if kind == "device-change" {
				value := store.index.Directories[root]
				value.JournalDevice = "another-volume"
				store.index.Directories[root] = value
			}
			plan, _, _ := prepareFolderMapRefresh(root, root, read)
			if plan != nil {
				t.Fatal("unsafe journal must not reuse cached measurements")
			}
		})
	}
}
