package main

import (
	"context"

	"safeshed/internal/core"
)

// App struct
type App struct {
	ctx context.Context
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

// Scan performs a read-only scan. The native UI does not expose deletion APIs.
func (a *App) Scan(root string) (*core.Result, error) {
	return core.Scan(root)
}

func (a *App) Rules() []core.Rule {
	return core.Rules()
}
