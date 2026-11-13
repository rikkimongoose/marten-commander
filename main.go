package main

import (
	"embed"
	"log"
	"os"

	"github.com/rikkimongoose/marten-commander/internal/app"
	"github.com/rikkimongoose/marten-commander/internal/desktop"
	"github.com/rikkimongoose/marten-commander/internal/localfs"
	"github.com/rikkimongoose/marten-commander/internal/operations"
	"github.com/rikkimongoose/marten-commander/internal/settings"
	"github.com/rikkimongoose/marten-commander/internal/volumes"
	"github.com/wailsapp/wails/v3/pkg/application"
)

//go:embed all:frontend/dist
var assets embed.FS

func init() {
	application.RegisterEvent[operations.Event]("operations:updated")
}

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
	editorConfig, editorConfigError := settings.LoadEditorConfig()
	editor := app.NewEditorService(filesystem, localfs.SystemEditorLauncher{}, editorConfig, editorConfigError)
	terminalConfig, terminalConfigError := settings.LoadTerminalConfig()
	terminal := app.NewTerminalService(localfs.SystemTerminalLauncher{}, terminalConfig, terminalConfigError)
	operationManager := operations.NewManager()
	copyService := app.NewCopyService(filesystem, operationManager)
	moveService := app.NewMoveService(filesystem, operationManager)
	operationService := desktop.NewOperationService(operationManager)
	desktopApp := application.New(application.Options{
		Name: "Marten Commander", Description: "Двухпанельный файловый менеджер",
		Services: []application.Service{
			application.NewService(desktop.NewDirectoryService(directories, initialIsHome)),
			application.NewService(desktop.NewCommandServiceWithOperations(files, editor, copyService, moveService, terminal)),
			application.NewService(desktop.NewVolumeService(volumes.NewService())),
			application.NewService(operationService),
		},
		Assets: application.AssetOptions{Handler: application.AssetFileServerFS(assets)},
		Mac:    application.MacOptions{ApplicationShouldTerminateAfterLastWindowClosed: true},
	})
	if err := operationManager.SetPublisher(func(event operations.Event) {
		desktopApp.Event.Emit(desktop.OperationUpdatedEvent, event)
	}); err != nil {
		log.Fatal(err)
	}
	desktopApp.Window.NewWithOptions(application.WebviewWindowOptions{
		Title: "Marten Commander", Width: 1200, Height: 720, URL: "/",
	})
	if err := desktopApp.Run(); err != nil {
		log.Fatal(err)
	}
}
