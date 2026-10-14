package core

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func isolatedFolderMapDB(t *testing.T) *sqlTestCache {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, "cache"))
	t.Setenv("LocalAppData", filepath.Join(home, "cache"))
	cache, err := os.UserCacheDir()
	if err != nil {
		t.Fatal(err)
	}
	db, err := openFolderMapDB()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		folderMapDatabases.Lock()
		delete(folderMapDatabases.connections, filepath.Join(cache, "diskatlas", "folder-map.sqlite"))
		folderMapDatabases.Unlock()
		db.Close()
	})
	return &sqlTestCache{path: filepath.Join(cache, "diskatlas")}
}

type sqlTestCache struct{ path string }

func testMapScan(t *testing.T, root, target string) *FolderMap {
	t.Helper()
	result, err := MeasureFolderMapContext(context.Background(), root, target, true, nil)
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func TestFolderMapSQLiteSharedComparisonAcrossScansAndRestart(t *testing.T) {
	isolatedFolderMapDB(t)
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer folderMapIndexStores.Delete(root)
	leaf := filepath.Join(root, "parent", "child", "leaf")
	if err := os.MkdirAll(leaf, 0755); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(leaf, "data")
	write := func(size int) {
		t.Helper()
		if err := os.WriteFile(file, make([]byte, size), 0644); err != nil {
			t.Fatal(err)
		}
	}
	write(4096)
	initial := testMapScan(t, root, root)
	if !initial.DeltaKnown || initial.DeltaBytes != 0 {
		t.Fatalf("first scan must establish baseline: %+v", initial)
	}
	firstCheckpoint := loadFolderMapAggregates(root, []string{root})[root].BaselineAt
	write(32768)
	second := testMapScan(t, root, root)
	growth := second.Bytes - initial.Bytes
	if growth <= 0 || second.DeltaBytes != growth {
		t.Fatalf("wrong root growth: %+v", second)
	}
	secondCheckpoint := loadFolderMapAggregates(root, []string{root})[root].BaselineAt
	if !secondCheckpoint.After(firstCheckpoint) {
		t.Fatal("checkpoint did not advance")
	}
	assertTree := func(wantDelta int64, since time.Time) {
		t.Helper()
		for _, path := range []string{root, filepath.Dir(filepath.Dir(leaf)), filepath.Dir(leaf), leaf} {
			got, err := ReadFolderMap(root, path)
			if err != nil {
				t.Fatal(err)
			}
			if !got.DeltaKnown || got.DeltaBytes != wantDelta || got.DeltaSince != since.UTC().Format(time.RFC3339) {
				t.Fatalf("%s: want delta=%d since=%s, got %+v", path, wantDelta, since, got)
			}
			for _, entry := range got.Children {
				if entry.DeltaKnown && entry.DeltaSince != got.DeltaSince {
					t.Fatalf("child baseline differs at %s: %+v", path, entry)
				}
			}
		}
	}
	assertTree(growth, firstCheckpoint)
	folderMapIndexStores.Delete(root)
	assertTree(growth, firstCheckpoint)
	// Local refreshes keep the displayed comparison and the next root checkpoint.
	write(65536)
	local := testMapScan(t, root, leaf)
	totalGrowth := local.Bytes - 4096
	assertTree(totalGrowth, firstCheckpoint)
	if checkpoint := loadFolderMapAggregates(root, []string{root})[root].BaselineAt; !checkpoint.Equal(secondCheckpoint) {
		t.Fatal("local refresh advanced the root checkpoint")
	}
	folderMapIndexStores.Delete(root)
	assertTree(totalGrowth, firstCheckpoint)
	third := testMapScan(t, root, root)
	sinceSecond := third.Bytes - second.Bytes
	assertTree(sinceSecond, secondCheckpoint)
	// A further unchanged scan clears previous growth, including file entries.
	thirdCheckpoint := loadFolderMapAggregates(root, []string{root})[root].BaselineAt
	testMapScan(t, root, root)
	assertTree(0, thirdCheckpoint)
}

func TestFolderMapSQLiteBatchesAndRollsBackFailedScan(t *testing.T) {
	cache := isolatedFolderMapDB(t)
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer folderMapIndexStores.Delete(root)
	for i := 0; i < 150; i++ {
		dir := filepath.Join(root, fmt.Sprintf("folder-%03d", i))
		if err := os.Mkdir(dir, 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "data"), make([]byte, 4096), 0644); err != nil {
			t.Fatal(err)
		}
	}
	initial := testMapScan(t, root, root)
	files, err := os.ReadDir(cache.path)
	if err != nil {
		t.Fatal(err)
	}
	if len(files) > 3 {
		t.Fatalf("expected only SQLite database/WAL/SHM, got %d files", len(files))
	}
	db, err := openFolderMapDB()
	if err != nil {
		t.Fatal(err)
	}
	var staged int
	if err := db.QueryRow(`SELECT count(*) FROM staged_snapshots`).Scan(&staged); err != nil {
		t.Fatal(err)
	}
	if staged != 0 {
		t.Fatalf("committed scan left %d staged rows", staged)
	}
	if _, err := db.Exec(`CREATE TRIGGER fail_scan BEFORE UPDATE ON roots BEGIN SELECT RAISE(ABORT,'injected commit failure'); END;`); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "folder-000", "data"), make([]byte, 32768), 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := MeasureFolderMapContext(context.Background(), root, root, true, nil); err == nil {
		t.Fatal("expected database commit failure")
	}
	if aggregate := loadFolderMapAggregates(root, []string{root})[root]; aggregate.Bytes != initial.Bytes {
		t.Fatal("failed scan leaked into memory")
	}
	folderMapIndexStores.Delete(root)
	if aggregate := loadFolderMapAggregates(root, []string{root})[root]; aggregate.Bytes != initial.Bytes {
		t.Fatal("failed scan changed stored aggregate")
	}
	snapshot, ok := readFolderMapDBSnapshot(root, root, folderMapDisplay)
	if !ok || snapshot.Bytes != initial.Bytes {
		t.Fatal("failed scan changed stored snapshot")
	}
	if err := db.QueryRow(`SELECT count(*) FROM staged_snapshots`).Scan(&staged); err != nil || staged != 0 {
		t.Fatalf("failed scan left staged rows: %d %v", staged, err)
	}
	if _, err := db.Exec(`DROP TRIGGER fail_scan`); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	_, err = MeasureFolderMapContext(ctx, root, root, true, func(progress Progress) {
		if progress.FilesScanned > 0 {
			cancel()
		}
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected cancellation, got %v", err)
	}
	if aggregate := loadFolderMapAggregates(root, []string{root})[root]; aggregate.Bytes != initial.Bytes {
		t.Fatal("cancelled scan changed measurements")
	}
	if _, err := db.Exec(`CREATE TRIGGER fail_staging BEFORE INSERT ON staged_snapshots BEGIN SELECT RAISE(ABORT,'injected staging failure'); END;`); err != nil {
		t.Fatal(err)
	}
	timeout, stop := context.WithTimeout(context.Background(), 10*time.Second)
	defer stop()
	_, err = MeasureFolderMapContext(timeout, root, root, true, nil)
	if err == nil || !strings.Contains(err.Error(), "injected staging failure") {
		t.Fatalf("staging error was swallowed or scanning stalled: %v", err)
	}
	if aggregate := loadFolderMapAggregates(root, []string{root})[root]; aggregate.Bytes != initial.Bytes {
		t.Fatal("staging failure changed measurements")
	}
}

func TestFolderMapSQLiteDoesNotRewriteRowsForSharedDates(t *testing.T) {
	isolatedFolderMapDB(t)
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer folderMapIndexStores.Delete(root)
	if err := os.Mkdir(filepath.Join(root, "child"), 0755); err != nil {
		t.Fatal(err)
	}
	testMapScan(t, root, root)
	db, err := openFolderMapDB()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`CREATE TABLE updates(n INTEGER); INSERT INTO updates VALUES(0); CREATE TRIGGER count_updates AFTER UPDATE ON aggregates BEGIN UPDATE updates SET n=n+1; END;`); err != nil {
		t.Fatal(err)
	}
	var before, after int
	if err := db.QueryRow(`SELECT total_changes()`).Scan(&before); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 5; i++ {
		if _, err := ReadFolderMap(root, root); err != nil {
			t.Fatal(err)
		}
	}
	if err := db.QueryRow(`SELECT total_changes()`).Scan(&after); err != nil {
		t.Fatal(err)
	}
	if after != before {
		t.Fatal("browsing wrote to the measurement database")
	}
	if _, err := db.Exec(`CREATE TABLE staged_writes(n INTEGER); INSERT INTO staged_writes VALUES(0); CREATE TRIGGER count_staged AFTER INSERT ON staged_snapshots BEGIN UPDATE staged_writes SET n=n+1; END;`); err != nil {
		t.Fatal(err)
	}
	testMapScan(t, root, root)
	var stagedWrites int
	if err := db.QueryRow(`SELECT n FROM staged_writes`).Scan(&stagedWrites); err != nil {
		t.Fatal(err)
	}
	if stagedWrites != 0 {
		t.Fatalf("unchanged scan rewrote %d snapshots", stagedWrites)
	}
	// Ignore the intentionally updated root measurement timestamp above.
	if _, err := db.Exec(`UPDATE updates SET n=0`); err != nil {
		t.Fatal(err)
	}
	index := readFolderMapAggregateIndex(root)
	for path, aggregate := range index.Directories {
		aggregate.BaselineAt = aggregate.BaselineAt.Add(24 * time.Hour)
		aggregate.DeltaSince = aggregate.DeltaSince.Add(24 * time.Hour)
		index.Directories[path] = aggregate
	}
	tx, err := db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if err := writeFolderMapIndexTx(tx, root, index, nil); err != nil {
		t.Fatal(err)
	}
	var updates int
	if err := tx.QueryRow(`SELECT n FROM updates`).Scan(&updates); err != nil {
		t.Fatal(err)
	}
	if updates != 0 {
		t.Fatalf("advancing shared dates rewrote %d unchanged aggregate rows", updates)
	}
}

func TestFolderMapIncrementalReuseClearsPreviousScanGrowth(t *testing.T) {
	root := t.TempDir()
	info, err := os.Lstat(root)
	if err != nil {
		t.Fatal(err)
	}
	checkpoint := time.Now().UTC()
	child := filepath.Join(root, "child")
	old := folderMapAggregate{Bytes: 8192, Files: 1, Known: true, Complete: true, DeltaBytes: 4096, DeltaKnown: true, DeltaGrowthBytes: 4096, DeltaActivityKnown: true, StatModTime: info.ModTime().UnixNano(), StatSize: info.Size()}
	plan := &folderMapRefreshPlan{rootScan: true, baselineAt: checkpoint, aggregates: map[string]folderMapAggregate{root: old, child: old}, children: map[string][]string{root: {child}}}
	reused, ok := plan.reuse(root, info)
	if !ok || !reused.DeltaKnown || reused.DeltaBytes != 0 || reused.DeltaGrowthBytes != 0 || reused.DeltaSince != checkpoint.Format(time.RFC3339) {
		t.Fatalf("unchanged branch retained previous growth: %+v", reused)
	}
	for path, aggregate := range reused.Aggregates {
		if !aggregate.DeltaKnown || aggregate.DeltaBytes != 0 || aggregate.DeltaGrowthBytes != 0 || !aggregate.DeltaSince.Equal(checkpoint) {
			t.Fatalf("reused descendant %s retained previous comparison: %+v", path, aggregate)
		}
	}
}

func TestFolderMapTrashUpdatesAncestorsWithoutAdvancingCheckpoint(t *testing.T) {
	isolatedFolderMapDB(t)
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer folderMapIndexStores.Delete(root)
	leaf := filepath.Join(root, "parent", "child", "leaf")
	if err := os.MkdirAll(leaf, 0755); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(leaf, "data")
	keep := filepath.Join(root, "keep")
	for _, path := range []string{file, keep} {
		if err := os.WriteFile(path, make([]byte, 4096), 0644); err != nil {
			t.Fatal(err)
		}
	}
	initial := testMapScan(t, root, root)
	initialLeaf := testMapScan(t, root, leaf)
	if err := os.WriteFile(file, make([]byte, 32768), 0644); err != nil {
		t.Fatal(err)
	}
	testMapScan(t, root, root)
	rootBefore := loadFolderMapAggregates(root, []string{root})[root]
	db, err := openFolderMapDB()
	if err != nil {
		t.Fatal(err)
	}
	// Local deletions must not traverse/prune unrelated historical snapshots.
	if _, err := db.Exec(`INSERT INTO snapshots(root,path,kind,payload) VALUES(?,?,2,?)`, root, filepath.Join(root, "untouched-history"), []byte("{}")); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`CREATE TRIGGER guard_history BEFORE DELETE ON snapshots WHEN old.path LIKE '%/untouched-history' BEGIN SELECT RAISE(ABORT,'unrelated snapshot pruning'); END;`); err != nil {
		t.Fatal(err)
	}
	// Simulate an OS trash move without touching the user's actual trash.
	if err := os.Rename(file, filepath.Join(t.TempDir(), "trashed-data")); err != nil {
		t.Fatal(err)
	}
	if err := recordFolderMapRemoval(root, file); err != nil {
		t.Fatal(err)
	}
	check := func() {
		t.Helper()
		for _, path := range []string{leaf, filepath.Dir(leaf), filepath.Dir(filepath.Dir(leaf)), root} {
			got, err := ReadFolderMap(root, path)
			if err != nil {
				t.Fatal(err)
			}
			if !got.DeltaKnown || got.DeltaBytes != -initialLeaf.Bytes || got.DeltaSince != rootBefore.DeltaSince.UTC().Format(time.RFC3339) {
				t.Fatalf("removal not propagated to %s: want delta=%d since=%s, got %+v", path, -initialLeaf.Bytes, rootBefore.DeltaSince.UTC().Format(time.RFC3339), got)
			}
		}
		current := loadFolderMapAggregates(root, []string{root})[root]
		if current.Bytes != initial.Bytes-initialLeaf.Bytes {
			t.Fatalf("wrong root size: %d", current.Bytes)
		}
		if !current.BaselineAt.Equal(rootBefore.BaselineAt) {
			t.Fatal("deletion advanced comparison checkpoint")
		}
	}
	check()
	folderMapIndexStores.Delete(root)
	check()
	// Directory removal also subtracts its contents and prunes descendants.
	if err := os.WriteFile(filepath.Join(leaf, "new-data"), make([]byte, 16384), 0644); err != nil {
		t.Fatal(err)
	}
	testMapScan(t, root, leaf)
	target := filepath.Join(root, "parent")
	if err := os.Rename(target, filepath.Join(t.TempDir(), "trashed-parent")); err != nil {
		t.Fatal(err)
	}
	if err := recordFolderMapRemoval(root, target); err != nil {
		t.Fatal(err)
	}
	folderMapIndexStores.Delete(root)
	aggregates := loadFolderMapAggregates(root, []string{root, target, leaf})
	if _, exists := aggregates[target]; exists {
		t.Fatal("trashed directory remains indexed")
	}
	if _, exists := aggregates[leaf]; exists {
		t.Fatal("trashed descendants remain indexed")
	}
	if aggregates[root].Bytes != initial.Bytes-initialLeaf.Bytes || aggregates[root].DeltaBytes != -initialLeaf.Bytes {
		t.Fatal("directory trash changed unrelated measurements")
	}
	rows, err := db.Query(`EXPLAIN QUERY PLAN DELETE FROM snapshots WHERE root=? AND kind IN (0,1,2) AND path=?`, root, target)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var id, parent, unused int
		var detail string
		if err := rows.Scan(&id, &parent, &unused, &detail); err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(detail, "SEARCH snapshots USING PRIMARY KEY") || !strings.Contains(detail, "path=?") {
			t.Fatalf("snapshot deletion must use a point lookup: %s", detail)
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}

}
