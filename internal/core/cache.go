package core

import (
	"compress/gzip"
	"crypto/sha256"
	"encoding/gob"
	"encoding/hex"
	"os"
	"path/filepath"
	"time"
)

const (
	// Bump this when the cache semantics change. Version 22 adds aggregate
	// large-directory candidates. Version 21 adds iOS Simulator device
	// candidates. Version 20 marks rebuildable
	// project outputs as directly deletable. Version 19 corrects command
	// candidate explanations. Version 18 removes stale
	// Docker managed-storage informational rows. Version 17 narrows user-state
	// collection candidates. Version 16 stores individual
	// Android AVD cleanup candidates. Version 15 fixes the final
	// classified storage totals. Version 14 stores manager-backed
	// package cleanup candidates. Version 13 stores the complete
	// same-filesystem inventory before classification. Version 12 stopped persisting
	// files below rebuildable and protected subtrees. Version 11 stores one cache per
	// scan root so switching between / and ~/Documents does not invalidate the
	// other root. Version 10 lowers the manifest
	// floor so the UI can offer a configurable minimum file size. Version 9 adds
	// directory summaries. Version 8 broadens general
	// personal-file candidates and gives the old-file rule precedence. Version 7
	// added path labels and command-backed cleanup candidates on top of the file
	// manifest used to apply journal paths incrementally.
	analysisCacheVersion = 22
	analysisFastReuseTTL = time.Hour
)

// analysisIndex is a compact local snapshot of the file manifest and
// candidate measurements. The manifest lets a filesystem journal update only
// affected paths between analyses.
type analysisIndex struct {
	Version     int
	Root        string
	SavedAt     time.Time
	Candidates  map[string]cachedCandidate
	Files       map[string]cachedFile
	Directories map[string]directoryAggregate
	Journal     journalCursor
	Snapshot    *analysisSnapshot
}

type journalCursor struct {
	Kind      string
	Volume    string
	JournalID uint64
	ID        uint64
}

type analysisSnapshot struct {
	Result  *Result
	Storage Storage
}

// analysisCacheSummary keeps the small part needed to decide whether a full
// manifest has to be decoded. A root scan can contain millions of file rows,
// while the journal cursor and rendered result are comparatively small.
type analysisCacheSummary struct {
	Version  int
	Root     string
	SavedAt  time.Time
	Journal  journalCursor
	Snapshot *analysisSnapshot
}

type cachedCandidate struct {
	RuleID         string
	Target         string
	Project        string
	TargetModTime  int64
	TargetSize     int64
	TargetIdentity string
	Item           *Item
}

type cachedFile struct {
	Category string
	Labels   []string
	Bytes    int64
	ModTime  int64
	Size     int64
	Identity string
}

func newAnalysisIndex(root string) *analysisIndex {
	return &analysisIndex{
		Version:    analysisCacheVersion,
		Root:       root,
		Candidates: map[string]cachedCandidate{},
		Files:      map[string]cachedFile{},
	}
}

func loadAnalysisIndex(root string) *analysisIndex {
	path, err := analysisIndexPath(root)
	if err != nil {
		return nil
	}
	file, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer file.Close()
	reader, err := gzip.NewReader(file)
	if err != nil {
		return nil
	}
	defer reader.Close()
	var index analysisIndex
	if err := gob.NewDecoder(reader).Decode(&index); err != nil {
		return nil
	}
	if index.Version != analysisCacheVersion || filepath.Clean(index.Root) != filepath.Clean(root) || index.SavedAt.IsZero() {
		return nil
	}
	if index.Candidates == nil {
		index.Candidates = map[string]cachedCandidate{}
	}
	if index.Files == nil {
		index.Files = map[string]cachedFile{}
	}
	return &index
}

func loadAnalysisSummary(root string) *analysisCacheSummary {
	path, err := analysisSummaryPath(root)
	if err != nil {
		return nil
	}
	file, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer file.Close()
	reader, err := gzip.NewReader(file)
	if err != nil {
		return nil
	}
	defer reader.Close()
	var summary analysisCacheSummary
	if err := gob.NewDecoder(reader).Decode(&summary); err != nil {
		return nil
	}
	if summary.Version != analysisCacheVersion || filepath.Clean(summary.Root) != filepath.Clean(root) || summary.SavedAt.IsZero() || summary.Snapshot == nil || summary.Snapshot.Result == nil {
		return nil
	}
	return &summary
}

// CachedAnalysis returns the last completed analysis without walking the
// filesystem. It is used to rehydrate the UI after a frontend reload or app
// restart; an explicit Analyze call is still required to refresh the data.
func CachedAnalysis(root string) (*Analysis, bool) {
	normalized, err := analysisRoot(root)
	if err != nil {
		return nil, false
	}
	summary := loadAnalysisSummary(normalized)
	if summary == nil || summary.Snapshot == nil || summary.Snapshot.Result == nil {
		return nil, false
	}
	storage := summary.Snapshot.Storage
	if current, err := storageInfoAt(normalized); err == nil {
		storage.Total = current.Total
		storage.Available = current.Available
	}
	storage.Complete = true
	return &Analysis{Result: summary.Snapshot.Result, Storage: storage}, true
}

func saveAnalysisIndex(index *analysisIndex) error {
	if index == nil {
		return nil
	}
	path, err := analysisIndexPath(index.Root)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	index.SavedAt = time.Now().UTC()
	temporary, err := os.CreateTemp(filepath.Dir(path), ".analysis-index-*.tmp")
	if err != nil {
		return err
	}
	temporaryName := temporary.Name()
	defer os.Remove(temporaryName)
	writer := gzip.NewWriter(temporary)
	encodeErr := gob.NewEncoder(writer).Encode(index)
	if closeErr := writer.Close(); encodeErr == nil {
		encodeErr = closeErr
	}
	if closeErr := temporary.Close(); encodeErr == nil {
		encodeErr = closeErr
	}
	if encodeErr != nil {
		return encodeErr
	}
	if err := os.Rename(temporaryName, path); err != nil {
		return err
	}
	return saveAnalysisSummary(index)
}

func saveAnalysisSummary(index *analysisIndex) error {
	if index == nil || index.Snapshot == nil || index.Snapshot.Result == nil {
		return nil
	}
	summary := analysisCacheSummary{Version: index.Version, Root: index.Root, SavedAt: index.SavedAt, Journal: index.Journal, Snapshot: index.Snapshot}
	return writeAnalysisSummary(summary)
}

// UpdateCachedAnalysisResult keeps the lightweight cache aligned with cleanup
// performed inside the app. The filesystem journal will update the full
// manifest later; immediate re-analysis should not resurrect deleted rows.
func UpdateCachedAnalysisResult(result *Result) {
	if result == nil || len(result.Roots) == 0 {
		return
	}
	summary := loadAnalysisSummary(result.Roots[0])
	if summary == nil || summary.Snapshot == nil {
		return
	}
	summary.Snapshot.Result = result
	_ = writeAnalysisSummary(*summary)
}

func writeAnalysisSummary(summary analysisCacheSummary) error {
	path, err := analysisSummaryPath(summary.Root)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	temporary, err := os.CreateTemp(filepath.Dir(path), ".analysis-summary-*.tmp")
	if err != nil {
		return err
	}
	temporaryName := temporary.Name()
	defer os.Remove(temporaryName)
	writer := gzip.NewWriter(temporary)
	encodeErr := gob.NewEncoder(writer).Encode(summary)
	if closeErr := writer.Close(); encodeErr == nil {
		encodeErr = closeErr
	}
	if closeErr := temporary.Close(); encodeErr == nil {
		encodeErr = closeErr
	}
	if encodeErr != nil {
		return encodeErr
	}
	return os.Rename(temporaryName, path)
}

func analysisIndexPath(scanRoot string) (string, error) {
	cacheRoot, err := os.UserCacheDir()
	if err != nil {
		return "", err
	}
	// The cache is intentionally keyed by the normalized absolute root. A
	// single global cache file made alternating scans of different roots throw
	// away each other's snapshots.
	sum := sha256.Sum256([]byte(filepath.Clean(scanRoot)))
	key := hex.EncodeToString(sum[:8])
	return filepath.Join(cacheRoot, "shed", "analysis-index-"+key+".gob.gz"), nil
}

func analysisSummaryPath(scanRoot string) (string, error) {
	cacheRoot, err := os.UserCacheDir()
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256([]byte(filepath.Clean(scanRoot)))
	key := hex.EncodeToString(sum[:8])
	return filepath.Join(cacheRoot, "shed", "analysis-summary-"+key+".gob.gz"), nil
}

func cacheCandidateKey(ruleID, target string) string {
	return ruleID + ":" + filepath.Clean(target)
}

func targetSignature(path string) (int64, int64, string, bool) {
	info, err := os.Lstat(path)
	// Directory mtimes do not summarize the contents below the directory. A
	// cached measurement for a directory would therefore go stale when a
	// nested file changes, so only regular files use this fast-path signature.
	if err != nil || info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return 0, 0, "", false
	}
	return info.ModTime().UnixNano(), info.Size(), fileIdentity(info), true
}

func cachedItem(current cachedCandidate, target string) (Item, bool) {
	// Project candidates depend on nearby lockfiles and Git state in addition
	// to the target itself. Re-evaluate them so those signals cannot become
	// stale while a file's own timestamp stays unchanged.
	if current.Item == nil || current.Project != "" || current.Item.Action == "command" {
		return Item{}, false
	}
	modified, size, identity, ok := targetSignature(target)
	if !ok || modified != current.TargetModTime || size != current.TargetSize || identity != current.TargetIdentity {
		return Item{}, false
	}
	return *current.Item, true
}
