package main

import (
	"embed"
	"log"

	"github.com/wailsapp/wails/v2"
	"github.com/wailsapp/wails/v2/pkg/options"
	"github.com/wailsapp/wails/v2/pkg/options/assetserver"
)

//go:embed all:frontend/dist
var assets embed.FS

// The build fails if the reviewed host-side Wails overlay is missing.
var _ = options.AteneaSSHBridgeGuard

func main() {
	app := &App{}
	if err := wails.Run(&options.App{
		Title:            "Atenea SSH",
		Width:            1100,
		Height:           720,
		MinWidth:         760,
		MinHeight:        540,
		AssetServer:      &assetserver.Options{Assets: assets},
		BackgroundColour: &options.RGBA{R: 246, G: 247, B: 249, A: 1},
		Bind:             []interface{}{app},
	}); err != nil {
		log.Fatal(err)
	}
}
