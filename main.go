package main

import (
	"embed"
	"log"
	"os"

	"github.com/rikkimongoose/marten-commander/internal/app"
	"github.com/rikkimongoose/marten-commander/internal/desktop"
	"github.com/rikkimongoose/marten-commander/internal/localfs"
	"github.com/rikkimongoose/marten-commander/internal/volumes"
	"github.com/wailsapp/wails/v3/pkg/application"
)

//go:embed all:frontend/dist
var assets embed.FS

func main() {
	initial, err := os.UserHomeDir()
	if err != nil {
		initial, err = os.Getwd()
		if err != nil {
			log.Fatal(err)
		}
	}
	filesystem := localfs.LocalFileSystem{}
	directories := app.NewDirectoryService(filesystem, filesystem, initial)
	desktopApp := application.New(application.Options{
		Name: "marten-commander", Description: "Двухпанельный файловый менеджер",
		Services: []application.Service{
			application.NewService(desktop.NewDirectoryService(directories)),
			application.NewService(&desktop.CommandService{}),
			application.NewService(desktop.NewVolumeService(volumes.NewService())),
		},
		Assets: application.AssetOptions{Handler: application.AssetFileServerFS(assets)},
		Mac:    application.MacOptions{ApplicationShouldTerminateAfterLastWindowClosed: true},
	})
	desktopApp.Window.NewWithOptions(application.WebviewWindowOptions{
		Title: "Marten Commander", Width: 1200, Height: 720, URL: "/",
	})
	if err := desktopApp.Run(); err != nil {
		log.Fatal(err)
	}
}
