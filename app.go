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

	"safeshed/internal/core"

	wailsruntime "github.com/wailsapp/wails/v2/pkg/runtime"
)

// App struct
type App struct {
	ctx         context.Context
	mu          sync.RWMutex
	lastScan    *core.Result
	defaultRoot string
}

// NewApp creates a new App application struct
func NewApp() *App {
	return NewAppWithRoot(string(os.PathSeparator))
}

func NewAppWithRoot(root string) *App {
	if strings.TrimSpace(root) == "" {
		root = string(os.PathSeparator)
	}
	return &App{defaultRoot: root}
}

func (a *App) effectiveRoot(root string) string {
	if strings.TrimSpace(root) == "" {
		return a.defaultRoot
	}
	return root
}

// startup is called when the app starts. The context is saved
// so we can call the runtime methods
func (a *App) startup(ctx context.Context) {
	a.ctx = ctx
	core.WarmFolderMap(a.defaultRoot)
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
	core.ResetFolderMap(a.effectiveRoot(root))
	core.WarmFolderMap(a.effectiveRoot(root))
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

// FolderMap returns one directory level from the cached file manifest. It is
// intentionally separate from Analyze so browsing never triggers a rescan.
func (a *App) FolderMap(path string) (*core.FolderMap, error) {
	return core.CachedFolderMap(a.defaultRoot, path)
}

func (a *App) Rules() []core.Rule {
	return core.Rules()
}

func (a *App) StorageInfo() (core.Storage, error) {
	return core.StorageInfo()
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
