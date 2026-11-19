package main

import (
	"embed"
	"log"
	"os"
	"path/filepath"

	"github.com/adrg/xdg"
	"github.com/rikkimongoose/marten-commander/internal/app"
	"github.com/rikkimongoose/marten-commander/internal/desktop"
	"github.com/rikkimongoose/marten-commander/internal/localfs"
	"github.com/rikkimongoose/marten-commander/internal/operations"
	"github.com/rikkimongoose/marten-commander/internal/settings"
	remotesftp "github.com/rikkimongoose/marten-commander/internal/sftp"
	"github.com/rikkimongoose/marten-commander/internal/viewer"
	"github.com/rikkimongoose/marten-commander/internal/volumes"
	"github.com/wailsapp/wails/v3/pkg/application"
	"github.com/wailsapp/wails/v3/pkg/events"
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
	trashService := app.NewTrashService(localfs.SystemTrash{}, operationManager)
	profileStore := remotesftp.NewProfileStore(filepath.Join(xdg.ConfigHome, "marten-commander"))
	knownHostsPath := filepath.Join(xdg.Home, ".ssh", "known_hosts")
	sftpManager := remotesftp.NewManager(profileStore, knownHostsPath)
	sftpService := desktop.NewSFTPService(profileStore, sftpManager)
	viewerService := desktop.NewViewerService(viewer.LocalFileSource{})
	var desktopApp *application.App
	viewerWindows := desktop.NewViewerWindowService(viewerService, func(options desktop.ViewerWindowOptions) (desktop.ViewerWindow, error) {
		window := desktopApp.Window.NewWithOptions(application.WebviewWindowOptions{
			Name: options.Name, Title: options.Title, URL: options.URL, Width: options.Width, Height: options.Height,
			MinWidth: 480, MinHeight: 320, Hidden: true,
		})
		return nativeViewerWindow{window}, nil
	})
	desktopApp = application.New(application.Options{
		Name: "Marten Commander", Description: "Двухпанельный файловый менеджер",
		Services: []application.Service{
			application.NewService(desktop.NewDirectoryService(directories, initialIsHome)),
			application.NewService(desktop.NewCommandServiceWithTrash(files, editor, copyService, moveService, terminal, trashService)),
			application.NewService(viewerService),
			application.NewService(viewerWindows),
			application.NewService(desktop.NewVolumeService(volumes.NewService())),
			application.NewService(operationService),
			application.NewService(sftpService),
		},
		Assets: application.AssetOptions{Handler: application.AssetFileServerFS(assets), Middleware: desktop.NewViewerImageMiddleware(viewerService)},
		Mac:    application.MacOptions{ApplicationShouldTerminateAfterLastWindowClosed: true},
	})
	if err := operationManager.SetPublisher(func(event operations.Event) {
		desktopApp.Event.Emit(desktop.OperationUpdatedEvent, event)
	}); err != nil {
		log.Fatal(err)
	}
	mainWindow := desktopApp.Window.NewWithOptions(application.WebviewWindowOptions{
		Title: "Marten Commander", Width: 1200, Height: 720, URL: "/",
	})
	mainWindow.RegisterHook(events.Common.WindowClosing, func(*application.WindowEvent) {
		// Closing the commander window means quitting the application, even
		// while independent Viewer windows are still open. Quit performs the
		// service shutdown first, which releases every viewer session.
		go desktopApp.Quit()
	})
	if err := desktopApp.Run(); err != nil {
		log.Fatal(err)
	}
}

type nativeViewerWindow struct{ *application.WebviewWindow }

func (w nativeViewerWindow) Show() error {
	w.WebviewWindow.Show()
	return nil
}

func (w nativeViewerWindow) OnClosing(callback func()) {
	w.WebviewWindow.RegisterHook(events.Common.WindowClosing, func(*application.WindowEvent) { callback() })
}
