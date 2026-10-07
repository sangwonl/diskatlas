package main

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"

	"diskatlas/internal/core"

	wailsruntime "github.com/wailsapp/wails/v2/pkg/runtime"
)

// App struct
type App struct {
	ctx         context.Context
	mu          sync.RWMutex
	rootMu      sync.RWMutex
	folderMapMu sync.Mutex
	folderMaps  map[string]context.CancelFunc
	lastScan    *core.Result
	defaultRoot string
	rootError   error
	startupDone chan struct{}
}

// NewApp creates a new App application struct
func NewApp() *App {
	return NewAppWithRoot("")
}

func NewAppWithRoot(root string) *App {
	return &App{defaultRoot: expandScanRoot(root), startupDone: make(chan struct{})}
}

func (a *App) effectiveRoot(root string) string {
	if strings.TrimSpace(root) == "" {
		return a.scanRoot()
	}
	return expandScanRoot(root)
}

func (a *App) scanRoot() string {
	a.rootMu.RLock()
	defer a.rootMu.RUnlock()
	return a.defaultRoot
}

// startup is called when the app starts. The context is saved
// so we can call the runtime methods
func (a *App) startup(ctx context.Context) {
	if a.startupDone != nil {
		defer close(a.startupDone)
	}
	a.ctx = ctx
	if a.scanRoot() != "" {
		return
	}

	settings, err := readScanScope()
	if err != nil {
		a.setRootError(err)
		return
	}
	if settings.Path == "" {
		return
	}

	root := settings.Path
	if settings.Bookmark != "" {
		resolved, renewedBookmark, err := startFolderScope(settings.Bookmark)
		if err != nil {
			a.setRootError(err)
			return
		}
		if resolved != "" {
			root = resolved
		}
		if renewedBookmark != "" && renewedBookmark != settings.Bookmark {
			settings.Bookmark = renewedBookmark
			settings.Path = root
			_ = writeScanScope(settings)
		}
	}
	if info, err := os.Stat(root); err != nil || !info.IsDir() {
		if err == nil {
			err = errors.New("saved scan location is not a directory")
		}
		a.setRootError(err)
		return
	}
	a.rootMu.Lock()
	a.defaultRoot = root
	a.rootMu.Unlock()
}

func (a *App) shutdown(context.Context) {
	stopFolderScope()
}

func (a *App) setRootError(err error) {
	a.rootMu.Lock()
	a.rootError = err
	a.rootMu.Unlock()
}

// ScanRoot returns the remembered scan location, if one is available.
func (a *App) ScanRoot() (string, error) {
	if a.startupDone != nil {
		<-a.startupDone
	}
	a.rootMu.RLock()
	defer a.rootMu.RUnlock()
	if a.rootError != nil {
		return a.defaultRoot, a.rootError
	}
	return a.defaultRoot, nil
}

// ChooseScanRoot asks the user to pick a folder and remembers that scope.
func (a *App) ChooseScanRoot() (string, error) {
	if a.startupDone != nil {
		<-a.startupDone
	}
	initial := a.scanRoot()
	if initial == "" {
		initial, _ = os.UserHomeDir()
	}
	path, bookmark, err := chooseFolder(a.ctx, initial)
	if err != nil || path == "" {
		return path, err
	}
	path = expandScanRoot(path)
	absolute, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	info, err := os.Stat(absolute)
	if err != nil {
		return "", err
	}
	if !info.IsDir() {
		return "", errors.New("selected scan location is not a directory")
	}
	if err := writeScanScope(scanScopeSettings{Path: absolute, Bookmark: bookmark}); err != nil {
		return "", err
	}
	a.rootMu.Lock()
	a.defaultRoot = absolute
	a.rootError = nil
	a.rootMu.Unlock()
	return absolute, nil
}

func (a *App) Scan(root string) (*core.Result, error) {
	result, err := core.ScanWithProgress(a.effectiveRoot(root), func(progress core.Progress) {
		if a.ctx != nil {
			wailsruntime.EventsEmit(a.ctx, "scan:progress", progress)
		}
	})
	if err != nil {
		return nil, err
	}
	a.mu.Lock()
	a.lastScan = result
	a.mu.Unlock()
	return result, nil
}

func (a *App) Analyze(root string) (*core.Analysis, error) {
	analysis, err := core.AnalyzeWithProgress(a.effectiveRoot(root), func(progress core.Progress) {
		if a.ctx != nil {
			wailsruntime.EventsEmit(a.ctx, "scan:progress", progress)
		}
	})
	if err != nil {
		return nil, err
	}
	a.mu.Lock()
	a.lastScan = analysis.Result
	a.mu.Unlock()
	return analysis, nil
}

// CachedAnalysis restores the last completed result without scanning. A nil
// result means this root has not been analyzed yet.
func (a *App) CachedAnalysis(root string) (*core.Analysis, error) {
	analysis, ok := core.CachedAnalysis(a.effectiveRoot(root))
	if !ok {
		return nil, nil
	}
	a.mu.Lock()
	a.lastScan = analysis.Result
	a.mu.Unlock()
	return analysis, nil
}

// FolderMap reads only one directory level without descending into folders.
func (a *App) FolderMap(path string) (*core.FolderMap, error) {
	return core.ReadFolderMap(a.scanRoot(), path)
}

// ReloadFolderMap rereads the current directory level and reuses indexed child sizes.
func (a *App) ReloadFolderMap(path string) (*core.FolderMap, error) {
	return core.ReloadFolderMap(a.scanRoot(), path)
}

// MeasureFolderMap recursively measures the opened folder's immediate children.
func (a *App) MeasureFolderMap(path, requestID string) (*core.FolderMap, error) {
	return a.measureFolderMap(path, requestID, false)
}

// RefreshFolderMap discards the saved measurements and recalculates this folder.
func (a *App) RefreshFolderMap(path, requestID string) (*core.FolderMap, error) {
	return a.measureFolderMap(path, requestID, true)
}

func (a *App) measureFolderMap(path, requestID string, refresh bool) (*core.FolderMap, error) {
	ctx, cancel := context.WithCancel(context.Background())
	if requestID != "" {
		a.folderMapMu.Lock()
		if a.folderMaps == nil {
			a.folderMaps = make(map[string]context.CancelFunc)
		}
		previous := a.folderMaps[requestID]
		a.folderMaps[requestID] = cancel
		a.folderMapMu.Unlock()
		if previous != nil {
			previous()
		}
		defer func() {
			a.folderMapMu.Lock()
			delete(a.folderMaps, requestID)
			a.folderMapMu.Unlock()
		}()
	}
	defer cancel()
	return core.MeasureFolderMapContext(ctx, a.scanRoot(), path, refresh, func(progress core.Progress) {
		if a.ctx != nil {
			progress.RequestID = requestID
			wailsruntime.EventsEmit(a.ctx, "scan:progress", progress)
		}
	})
}

// CancelFolderMap stops an active folder measurement started by requestID.
func (a *App) CancelFolderMap(requestID string) {
	if requestID == "" {
		return
	}
	a.folderMapMu.Lock()
	cancel := a.folderMaps[requestID]
	a.folderMapMu.Unlock()
	if cancel != nil {
		cancel()
	}
}

func (a *App) Rules() []core.Rule {
	return core.Rules()
}

func (a *App) StorageInfo() (core.Storage, error) {
	root := a.scanRoot()
	if root == "" {
		return core.StorageInfo()
	}
	return core.StorageInfoAt(root)
}

func (a *App) TrashPath(path string, estimatedBytes int64) (core.TrashMoveResult, error) {
	return core.MoveToTrash(a.scanRoot(), path, estimatedBytes)
}

func (a *App) OpenTrash() error {
	if runtime.GOOS == "darwin" {
		home, err := os.UserHomeDir()
		if err != nil {
			return err
		}
		return exec.Command("open", filepath.Join(home, ".Trash")).Run()
	}
	if runtime.GOOS == "windows" {
		return exec.Command("explorer.exe", "shell:RecycleBinFolder").Run()
	}
	return exec.Command("xdg-open", "trash:///").Run()
}

func (a *App) ScanStorage() (core.Storage, error) {
	return core.ScanStorage(func(storage core.Storage) {
		if a.ctx != nil {
			wailsruntime.EventsEmit(a.ctx, "storage:progress", storage)
		}
	})
}

func (a *App) RevealPath(path string) error {
	clean, err := filepath.Abs(path)
	if err != nil {
		return err
	}
	info, err := os.Stat(clean)
	if err != nil {
		return err
	}
	if runtime.GOOS == "darwin" {
		if strings.EqualFold(filepath.Ext(clean), ".app") {
			return exec.Command("open", "-R", clean).Run()
		}
		if info.IsDir() {
			return exec.Command("open", clean).Run()
		}
		return exec.Command("open", "-R", clean).Run()
	}
	if runtime.GOOS == "windows" {
		if info.IsDir() {
			return exec.Command("explorer.exe", clean).Run()
		}
		return exec.Command("explorer.exe", "/select,"+clean).Run()
	}
	return exec.Command("xdg-open", filepath.Dir(clean)).Run()
}

func (a *App) PreviewCleanup(request core.CleanupRequest) (core.CleanupPreview, error) {
	a.mu.RLock()
	scan := a.lastScan
	a.mu.RUnlock()
	if scan == nil {
		return core.CleanupPreview{}, errors.New("run a scan before cleaning")
	}
	return core.PreviewCleanup(scan, request), nil
}

func (a *App) ExecuteCleanup(request core.CleanupRequest) (core.CleanupResult, error) {
	a.mu.RLock()
	scan := a.lastScan
	a.mu.RUnlock()
	if scan == nil {
		return core.CleanupResult{}, errors.New("run a scan before cleaning")
	}
	result, err := core.ExecuteCleanup(scan, request)
	if err != nil {
		return result, err
	}
	a.mu.Lock()
	updated := false
	if a.lastScan == scan {
		core.RemoveCompleted(scan, result.Completed)
		updated = true
	}
	a.mu.Unlock()
	if updated {
		core.UpdateCachedAnalysisResult(scan)
	}
	return result, nil
}
