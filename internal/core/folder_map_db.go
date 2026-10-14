package core

import (
	"bytes"
	"compress/flate"
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	_ "modernc.org/sqlite"
)

const (
	folderMapDisplay    = 0
	folderMapCheckpoint = 1
	folderMapComparison = 2
)

var folderMapDatabases = struct {
	sync.Mutex
	connections map[string]*sql.DB
}{connections: make(map[string]*sql.DB)}

func openFolderMapDB() (*sql.DB, error) {
	cache, err := os.UserCacheDir()
	if err != nil {
		return nil, err
	}
	path := filepath.Join(cache, "diskatlas", "folder-map.sqlite")
	folderMapDatabases.Lock()
	defer folderMapDatabases.Unlock()
	if db := folderMapDatabases.connections[path]; db != nil {
		return db, nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return nil, err
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	_, err = db.Exec(`PRAGMA journal_mode=WAL;
 PRAGMA synchronous=NORMAL;
 PRAGMA journal_size_limit=8388608;
 PRAGMA busy_timeout=5000;
 CREATE TABLE IF NOT EXISTS roots (root TEXT PRIMARY KEY, checkpoint_at TEXT NOT NULL, comparison_at TEXT NOT NULL);
 CREATE TABLE IF NOT EXISTS aggregates (root TEXT NOT NULL, path TEXT NOT NULL, payload BLOB NOT NULL, PRIMARY KEY(root,path)) WITHOUT ROWID;
 CREATE TABLE IF NOT EXISTS snapshots (root TEXT NOT NULL, path TEXT NOT NULL, kind INTEGER NOT NULL, payload BLOB NOT NULL, PRIMARY KEY(root,kind,path)) WITHOUT ROWID;
 CREATE TABLE IF NOT EXISTS staged_snapshots (scan TEXT NOT NULL, root TEXT NOT NULL, path TEXT NOT NULL, payload BLOB NOT NULL, started_at INTEGER NOT NULL, PRIMARY KEY(scan,path)) WITHOUT ROWID;
 DELETE FROM staged_snapshots WHERE started_at < unixepoch()-86400;`)
	if err != nil {
		db.Close()
		return nil, err
	}
	folderMapDatabases.connections[path] = db
	return db, nil
}

func readFolderMapDBSnapshot(root, path string, kind int) (*FolderMap, bool) {
	db, err := openFolderMapDB()
	if err != nil {
		return nil, false
	}
	var payload []byte
	if err := db.QueryRow(`SELECT payload FROM snapshots WHERE root=? AND kind=? AND path=?`, root, kind, path).Scan(&payload); err != nil {
		return nil, false
	}
	var snapshot FolderMap
	if decodeFolderMapPayload(payload, &snapshot) != nil || snapshot.Root != root || snapshot.Path != path {
		return nil, false
	}
	return &snapshot, true
}

var folderMapCompressors = sync.Pool{New: func() any { writer, _ := flate.NewWriter(nil, flate.BestSpeed); return writer }}

func encodeFolderMapPayload(snapshot *FolderMap) ([]byte, error) {
	// Dates and deltas are supplied by the root metadata and aggregate rows.
	// Keeping them out of snapshots lets unchanged measurements share the same
	// payload across display, comparison, and checkpoint generations.
	measurement := *snapshot
	measurement.Children = append([]FolderMapEntry(nil), snapshot.Children...)
	measurement.GeneratedAt = ""
	measurement.DeltaBytes = 0
	measurement.DeltaKnown = false
	measurement.DeltaSince = ""
	measurement.DeltaGrowthBytes = 0
	measurement.DeltaShrinkBytes = 0
	measurement.DeltaActivityKnown = false
	for i := range measurement.Children {
		entry := &measurement.Children[i]
		entry.DeltaBytes = 0
		entry.DeltaKnown = false
		entry.DeltaSince = ""
		entry.DeltaGrowthBytes = 0
		entry.DeltaShrinkBytes = 0
		entry.DeltaActivityKnown = false
	}
	payload, err := json.Marshal(&measurement)
	if err != nil {
		return nil, err
	}
	if len(payload) >= 4096 {
		var compressed bytes.Buffer
		compressed.WriteByte(1)
		writer := folderMapCompressors.Get().(*flate.Writer)
		writer.Reset(&compressed)
		_, writeErr := writer.Write(payload)
		closeErr := writer.Close()
		folderMapCompressors.Put(writer)
		if writeErr != nil {
			return nil, writeErr
		}
		if closeErr != nil {
			return nil, closeErr
		}
		if compressed.Len() < len(payload) {
			return compressed.Bytes(), nil
		}
	}
	return append([]byte{0}, payload...), nil
}

func decodeFolderMapPayload(payload []byte, snapshot *FolderMap) error {
	if len(payload) == 0 {
		return fmt.Errorf("empty folder snapshot")
	}
	switch payload[0] {
	case 0:
		return json.Unmarshal(payload[1:], snapshot)
	case 1:
		reader := flate.NewReader(bytes.NewReader(payload[1:]))
		defer reader.Close()
		return json.NewDecoder(reader).Decode(snapshot)
	default:
		return json.Unmarshal(payload, snapshot)
	}
}

func readFolderMapAggregateIndex(root string) *folderMapAggregateIndex {
	db, err := openFolderMapDB()
	if err != nil {
		return nil
	}
	tx, err := db.Begin()
	if err != nil {
		return nil
	}
	defer tx.Rollback()
	var checkpoint, comparison string
	if err := tx.QueryRow(`SELECT checkpoint_at,comparison_at FROM roots WHERE root=?`, root).Scan(&checkpoint, &comparison); err != nil {
		return nil
	}
	checkpointAt, _ := time.Parse(time.RFC3339Nano, checkpoint)
	comparisonAt, _ := time.Parse(time.RFC3339Nano, comparison)
	rows, err := tx.Query(`SELECT path,payload FROM aggregates WHERE root=?`, root)
	if err != nil {
		return nil
	}
	defer rows.Close()
	index := &folderMapAggregateIndex{Version: folderMapIndexVersion, Root: root, Directories: make(map[string]folderMapAggregate)}
	for rows.Next() {
		var path string
		var payload []byte
		var aggregate folderMapAggregate
		if rows.Scan(&path, &payload) != nil || json.Unmarshal(payload, &aggregate) != nil {
			return nil
		}
		aggregate.BaselineAt = checkpointAt
		aggregate.DeltaSince = comparisonAt
		index.Directories[path] = aggregate
	}
	if rows.Err() != nil {
		return nil
	}
	return index
}

// The checkpoint times are root metadata, so advancing a scan does not rewrite
// every unchanged directory row just to change its date.
func writeFolderMapIndexTx(tx *sql.Tx, root string, index, previous *folderMapAggregateIndex) error {
	var removed []string
	if previous != nil {
		for path := range previous.Directories {
			if _, exists := index.Directories[path]; !exists {
				removed = append(removed, path)
			}
		}
	} else {
		rows, err := tx.Query(`SELECT path FROM aggregates WHERE root=?`, root)
		if err != nil {
			return err
		}
		for rows.Next() {
			var path string
			if err := rows.Scan(&path); err != nil {
				rows.Close()
				return err
			}
			if _, exists := index.Directories[path]; !exists {
				removed = append(removed, path)
			}
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return err
		}
	}

	for _, path := range removed {
		if _, err := tx.Exec(`DELETE FROM aggregates WHERE root=? AND path=?`, root, path); err != nil {
			return err
		}
		// Include kind so the composite primary key performs three point lookups,
		// rather than scanning every snapshot belonging to this root.
		if _, err := tx.Exec(`DELETE FROM snapshots WHERE root=? AND kind IN (0,1,2) AND path=?`, root, path); err != nil {
			return err
		}
	}
	stmt, err := tx.Prepare(`INSERT INTO aggregates(root,path,payload) VALUES(?,?,?) ON CONFLICT(root,path) DO UPDATE SET payload=excluded.payload WHERE aggregates.payload<>excluded.payload`)
	if err != nil {
		return err
	}
	defer stmt.Close()
	for path, aggregate := range index.Directories {
		aggregate = persistedFolderMapAggregate(aggregate)
		if previous != nil {
			if old, exists := previous.Directories[path]; exists && persistedFolderMapAggregate(old) == aggregate {
				continue
			}
		}
		payload, err := json.Marshal(aggregate)
		if err != nil {
			return err
		}
		if _, err := stmt.Exec(root, path, payload); err != nil {
			return err
		}
	}
	return nil
}

func persistedFolderMapAggregate(aggregate folderMapAggregate) folderMapAggregate {
	aggregate.BaselineAt = time.Time{}
	aggregate.DeltaSince = time.Time{}
	aggregate.ScannedAt = aggregate.ScannedAt.Round(0).UTC()
	return aggregate
}

type folderMapSnapshotBatch struct {
	mu                 sync.Mutex
	db                 *sql.DB
	id                 string
	rootScan           bool
	initializeBaseline bool
	pending            map[string][]byte
	pendingBytes       int
	err                error
	previous           *folderMapAggregateIndex
}

func newFolderMapSnapshotBatch(initializeBaseline bool) (*folderMapSnapshotBatch, error) {
	db, err := openFolderMapDB()
	if err != nil {
		return nil, err
	}
	var id [16]byte
	if _, err := rand.Read(id[:]); err != nil {
		return nil, err
	}
	return &folderMapSnapshotBatch{db: db, id: hex.EncodeToString(id[:]), initializeBaseline: initializeBaseline, pending: make(map[string][]byte)}, nil
}

func (batch *folderMapSnapshotBatch) stage(snapshot *FolderMap) error {
	payload, err := encodeFolderMapPayload(snapshot)
	if err != nil {
		return err
	}
	batch.mu.Lock()
	defer batch.mu.Unlock()
	if batch.err != nil {
		return batch.err
	}
	batch.pendingBytes -= len(batch.pending[snapshot.Path])
	batch.pending[snapshot.Path] = payload
	batch.pendingBytes += len(payload)
	if len(batch.pending) >= 128 || batch.pendingBytes >= 2*1024*1024 {
		batch.err = batch.flushLocked(snapshot.Root)
	}
	return batch.err
}

func (batch *folderMapSnapshotBatch) flushLocked(root string) error {
	if len(batch.pending) == 0 {
		return nil
	}
	tx, err := batch.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	stmt, err := tx.Prepare(`INSERT INTO staged_snapshots(scan,root,path,payload,started_at) SELECT ?,?,?,?,unixepoch()
 WHERE NOT EXISTS(SELECT 1 FROM snapshots WHERE root=? AND kind=0 AND path=? AND payload=?)
 ON CONFLICT(scan,path) DO UPDATE SET payload=excluded.payload`)
	if err != nil {
		return err
	}
	defer stmt.Close()
	for path, payload := range batch.pending {
		if _, err := stmt.Exec(batch.id, root, path, payload, root, path, payload); err != nil {
			return err
		}
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	clear(batch.pending)
	batch.pendingBytes = 0
	return nil
}

func (batch *folderMapSnapshotBatch) commitScan(ctx context.Context, root string, index *folderMapAggregateIndex, checkpoint, comparison time.Time) error {
	batch.mu.Lock()
	defer batch.mu.Unlock()
	if batch.err != nil {
		return batch.err
	}
	if err := batch.flushLocked(root); err != nil {
		return err
	}
	tx, err := batch.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if batch.rootScan && !batch.initializeBaseline {
		if _, err := tx.ExecContext(ctx, `DELETE FROM snapshots WHERE root=? AND kind=2 AND path NOT IN (SELECT path FROM snapshots WHERE root=? AND kind=1)`, root, root); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO snapshots(root,path,kind,payload) SELECT root,path,2,payload FROM snapshots WHERE root=? AND kind=1 ON CONFLICT(root,kind,path) DO UPDATE SET payload=excluded.payload WHERE snapshots.payload<>excluded.payload`, root); err != nil {
			return err
		}
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO snapshots(root,path,kind,payload) SELECT root,path,0,payload FROM staged_snapshots WHERE scan=? ON CONFLICT(root,kind,path) DO UPDATE SET payload=excluded.payload WHERE snapshots.payload<>excluded.payload`, batch.id); err != nil {
		return err
	}
	if err := writeFolderMapIndexTx(tx, root, index, batch.previous); err != nil {
		return err
	}
	if batch.rootScan {
		if _, err := tx.ExecContext(ctx, `INSERT INTO snapshots(root,path,kind,payload) SELECT root,path,1,payload FROM snapshots WHERE root=? AND kind=0 ON CONFLICT(root,kind,path) DO UPDATE SET payload=excluded.payload WHERE snapshots.payload<>excluded.payload`, root); err != nil {
			return err
		}
		if batch.initializeBaseline {
			if _, err := tx.ExecContext(ctx, `INSERT INTO snapshots(root,path,kind,payload) SELECT root,path,2,payload FROM snapshots WHERE root=? AND kind=1 ON CONFLICT(root,kind,path) DO UPDATE SET payload=excluded.payload WHERE snapshots.payload<>excluded.payload`, root); err != nil {
				return err
			}
		}
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO roots VALUES(?,?,?) ON CONFLICT(root) DO UPDATE SET checkpoint_at=excluded.checkpoint_at,comparison_at=excluded.comparison_at`, root, checkpoint.Format(time.RFC3339Nano), comparison.Format(time.RFC3339Nano)); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM staged_snapshots WHERE scan=?`, batch.id); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("폴더 측정 저장: %w", err)
	}
	return nil
}

func (batch *folderMapSnapshotBatch) cleanup() {
	if batch == nil {
		return
	}
	batch.mu.Lock()
	defer batch.mu.Unlock()
	clear(batch.pending)
	_, _ = batch.db.Exec(`DELETE FROM staged_snapshots WHERE scan=?`, batch.id)
}
