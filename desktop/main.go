package main

import (
	"embed"
	"log"
	"os"

	"github.com/wailsapp/wails/v2"
	"github.com/wailsapp/wails/v2/pkg/options"
	"github.com/wailsapp/wails/v2/pkg/options/assetserver"

	"scriptstui/internal/agent"
	"scriptstui/internal/shellenv"
)

//go:embed all:frontend/dist
var assets embed.FS

func main() {
	// Opened from Finder the app gets launchd's bare PATH, where neither
	// `aws` nor the session plugin is found.
	shellenv.EnsurePATH()

	// The same binary is the shared agent both UIs spawn and talk to.
	if len(os.Args) > 1 && os.Args[1] == agent.Arg {
		os.Exit(agent.Main(os.Args[2:]))
	}

	// Create an instance of the app structure. This opens the shared tunnels
	// database at the project root, so a failure here leaves nothing to show.
	app, err := NewApp()
	if err != nil {
		log.Fatalln("Error:", err)
	}

	// Create application with options
	err = wails.Run(&options.App{
		Title:  "scriptstui-desktop",
		Width:  1024,
		Height: 768,
		AssetServer: &assetserver.Options{
			Assets: assets,
		},
		BackgroundColour: &options.RGBA{R: 27, G: 38, B: 54, A: 1},
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
