//go:build !darwin

package main

import (
	"context"
	"strings"

	wailsruntime "github.com/wailsapp/wails/v2/pkg/runtime"
)

func chooseFolder(ctx context.Context, initial string) (string, string, error) {
	if ctx == nil {
		return "", "", nil
	}
	path, err := wailsruntime.OpenDirectoryDialog(ctx, wailsruntime.OpenDialogOptions{
		Title:                "분석할 폴더 선택",
		DefaultDirectory:     initial,
		CanCreateDirectories: false,
	})
	return strings.TrimSpace(path), "", err
}

func startFolderScope(bookmark string) (string, string, error) {
	return "", "", nil
}

func stopFolderScope() {}
