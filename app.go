package main

import (
	"context"
	"errors"
	"sync"

	"safeshed/internal/core"

	wailsruntime "github.com/wailsapp/wails/v2/pkg/runtime"
)

// App struct
type App struct {
	ctx      context.Context
	mu       sync.RWMutex
	lastScan *core.Result
}

// NewApp creates a new App application struct
func NewApp() *App {
	return &App{}
}

// startup is called when the app starts. The context is saved
// so we can call the runtime methods
func (a *App) startup(ctx context.Context) {
	a.ctx = ctx
}

func (a *App) Scan(root string) (*core.Result, error) {
	result, err := core.ScanWithProgress(root, func(progress core.Progress) {
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

func (a *App) Rules() []core.Rule {
	return core.Rules()
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
	a.lastScan = nil
	a.mu.Unlock()
	return result, nil
}
