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
	initialIsHome := err == nil
	if err != nil {
		initial, err = os.Getwd()
		if err != nil {
			log.Fatal(err)
		}
	}
	filesystem := localfs.LocalFileSystem{}
	directories := app.NewDirectoryService(filesystem, filesystem, initial)
	files := app.NewFileService(filesystem, localfs.SystemFileLauncher{})
	desktopApp := application.New(application.Options{
		Name: "Marten Commander", Description: "Двухпанельный файловый менеджер",
		Services: []application.Service{
			application.NewService(desktop.NewDirectoryService(directories, initialIsHome)),
			application.NewService(desktop.NewCommandService(files)),
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
