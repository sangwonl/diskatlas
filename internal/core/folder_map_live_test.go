package core

import (
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func TestLiveFolderMeasurementsAreVisibleWithoutCommittingIndex(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	child := filepath.Join(root, "child")
	if err := os.Mkdir(child, 0755); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(child, "data")
	if err := os.WriteFile(file, make([]byte, 16384), 0644); err != nil {
		t.Fatal(err)
	}
	info, _ := os.Lstat(child)
	fileInfo, _ := os.Lstat(file)
	actual := allocatedSize(fileInfo)
	baseline := time.Now().Add(-24 * time.Hour).UTC().Truncate(time.Second)
	old := folderMapAggregate{
		Bytes: 4096, Files: 1, Known: true, Complete: true,
		BaselineKnown: true, BaselineComplete: true, BaselineBytes: 4096, BaselineAt: baseline,
		StatModTime: info.ModTime().UnixNano(), StatSize: info.Size(), ScannedAt: baseline,
		Journal: journalCursor{Kind: "fsevents", ID: 10},
	}
	store := folderMapIndexStoreForRoot(root)
	store.loaded = true
	store.index = &folderMapAggregateIndex{Root: root, Directories: map[string]folderMapAggregate{root: old, child: old}}
	store.live = map[string]folderMapAggregate{}
	defer folderMapIndexStores.Delete(root)
	measured := old
	measured.Bytes, measured.ScannedAt = actual, time.Now()
	publishFolderMapAggregate(root, child, measured)
	for _, path := range []string{root, child} {
		visible, err := ReadFolderMap(root, path)
		if err != nil {
			t.Fatal(err)
		}
		if visible.Bytes != actual || !visible.DeltaKnown || visible.DeltaBytes != actual-old.BaselineBytes {
			t.Fatalf("live map at %s has wrong measurement: %+v", path, visible)
		}
		if visible.DeltaSince != baseline.Format(time.RFC3339) {
			t.Fatalf("comparison date changed: %s", visible.DeltaSince)
		}
	}
	if store.index.Directories[child].Bytes != old.Bytes || store.index.Directories[child].Journal.ID != 10 {
		t.Fatal("preview modified persistent measurements or checkpoint")
	}
	// Cancellation discards the live overlay; persisted measurements stay valid.
	store.live = nil
	visible, err := ReadFolderMap(root, root)
	if err != nil {
		t.Fatal(err)
	}
	if visible.Bytes != old.Bytes {
		t.Fatalf("discarded live measurement leaked into index: %d", visible.Bytes)
	}
}

func TestLiveFolderReadersCanRunWhileMeasurementsPublish(t *testing.T) {
	root := t.TempDir()
	child := filepath.Join(root, "child")
	store := folderMapIndexStoreForRoot(root)
	store.loaded = true
	store.index = &folderMapAggregateIndex{Root: root, Directories: map[string]folderMapAggregate{}}
	store.live = map[string]folderMapAggregate{}
	defer folderMapIndexStores.Delete(root)
	var tasks sync.WaitGroup
	tasks.Add(2)
	go func() {
		defer tasks.Done()
		for i := 0; i < 100; i++ {
			publishFolderMapAggregate(root, child, folderMapAggregate{Bytes: int64(i), Known: true, Complete: true})
		}
	}()
	go func() {
		defer tasks.Done()
		for i := 0; i < 100; i++ {
			loadVisibleFolderMapAggregates(root, []string{root, child})
		}
	}()
	tasks.Wait()
	got, active := loadVisibleFolderMapAggregates(root, []string{child})
	if !active || got[child].Bytes != 99 {
		t.Fatalf("last completed measurement missing: %+v", got)
	}
}
