package main

import (
	"embed"
	"os"
	"strings"

	"github.com/wailsapp/wails/v2"
	"github.com/wailsapp/wails/v2/pkg/options"
	"github.com/wailsapp/wails/v2/pkg/options/assetserver"
)

//go:embed all:frontend/dist
var assets embed.FS

func main() {
	// Create an instance of the app structure
	app := NewAppWithRoot(scanRootFromArgs(os.Args[1:]))

	// Create application with options
	err := wails.Run(&options.App{
		Title:  "DiskAtlas",
		Width:  1024,
		Height: 768,
		AssetServer: &assetserver.Options{
			Assets: assets,
		},
		BackgroundColour: &options.RGBA{R: 245, G: 245, B: 247, A: 1},
		OnStartup:        app.startup,
		OnShutdown:       app.shutdown,
		Bind: []interface{}{
			app,
		},
	})

	if err != nil {
		println("Error:", err.Error())
	}
}

// scanRootFromArgs reads arguments forwarded by `wails dev -appargs ...`.
// A positional directory is accepted for quick fixture runs, while the
// named form is clearer in scripts: --root /tmp/diskatlas-fixture.
func scanRootFromArgs(args []string) string {
	positional := ""
	for index := 0; index < len(args); index++ {
		argument := strings.TrimSpace(args[index])
		if argument == "--" || argument == "" {
			continue
		}
		if argument == "--root" || argument == "--scan-root" || argument == "--base-dir" {
			if index+1 < len(args) && strings.TrimSpace(args[index+1]) != "" {
				return strings.TrimSpace(args[index+1])
			}
			continue
		}
		for _, prefix := range []string{"--root=", "--scan-root=", "--base-dir="} {
			if strings.HasPrefix(argument, prefix) {
				if value := strings.TrimSpace(strings.TrimPrefix(argument, prefix)); value != "" {
					return value
				}
			}
		}
		if positional == "" && !strings.HasPrefix(argument, "-") {
			positional = argument
		}
	}
	if positional != "" {
		return positional
	}
	return ""
}
