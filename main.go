package main

import (
	"embed"
	"os"
	"runtime"

	"github.com/ginkida/gokin-studio/internal/studio"
	"github.com/wailsapp/wails/v2"
	"github.com/wailsapp/wails/v2/pkg/menu"
	"github.com/wailsapp/wails/v2/pkg/menu/keys"
	"github.com/wailsapp/wails/v2/pkg/options"
	"github.com/wailsapp/wails/v2/pkg/options/assetserver"
	"github.com/wailsapp/wails/v2/pkg/options/mac"
)

//go:embed all:frontend/dist
var assets embed.FS

func main() {
	app := studio.NewStudio()
	for _, rawURL := range studio.DeepLinkURLsFromArgs(os.Args[1:]) {
		app.HandleDeepLink(rawURL)
	}

	appOptions := &options.App{
		Title:  "Gokin Studio",
		Width:  1280,
		Height: 800,
		AssetServer: &assetserver.Options{
			Assets: assets,
		},
		BackgroundColour: &options.RGBA{R: 24, G: 24, B: 27, A: 1},
		OnStartup:        app.Startup,
		OnShutdown:       app.Shutdown,
		OnBeforeClose:    app.BeforeClose,
		// macOS apps conventionally remain available after their last window is
		// closed. Quick Entry and local schedules therefore keep running in the
		// background; the application menu's Quit command still terminates them.
		HideWindowOnClose: runtime.GOOS == "darwin",
		SingleInstanceLock: &options.SingleInstanceLock{
			UniqueId: "com.ginkida.gokin-studio",
			OnSecondInstanceLaunch: func(data options.SecondInstanceData) {
				app.HandleSecondInstanceLaunch(data.Args)
			},
		},
		Mac: &mac.Options{
			OnUrlOpen: app.HandleDeepLink,
		},
		Bind: []interface{}{
			app,
		},
	}
	if runtime.GOOS == "darwin" {
		appOptions.Menu = macApplicationMenu(app)
	}
	err := wails.Run(appOptions)
	if err != nil {
		println("Error:", err.Error())
	}
}

func macApplicationMenu(app *studio.Studio) *menu.Menu {
	command := func(name string) menu.Callback {
		return func(*menu.CallbackData) { app.HandleNativeMenuCommand(name) }
	}
	file := menu.NewMenuFromItems(
		menu.Text("New Chat", keys.CmdOrCtrl("n"), command(studio.NativeCommandNewChat)),
		menu.Text("Close Chat", keys.CmdOrCtrl("w"), command(studio.NativeCommandCloseChat)),
		menu.Text("Connect Project Folder…", keys.CmdOrCtrl("o"), command(studio.NativeCommandAddProject)),
		menu.Separator(),
		menu.Text("Settings…", keys.CmdOrCtrl(","), command(studio.NativeCommandSettings)),
	)
	view := menu.NewMenuFromItems(
		menu.Text("Chat", keys.CmdOrCtrl("1"), command(studio.NativeCommandChat)),
		menu.Text("Files", keys.CmdOrCtrl("2"), command(studio.NativeCommandFiles)),
		menu.Text("Artifacts", keys.CmdOrCtrl("4"), command(studio.NativeCommandArtifacts)),
		menu.Separator(),
		menu.Text("Back", keys.CmdOrCtrl("["), command(studio.NativeCommandBack)),
		menu.Text("Forward", keys.CmdOrCtrl("]"), command(studio.NativeCommandForward)),
		menu.Text("Toggle Sidebar", keys.CmdOrCtrl("b"), command(studio.NativeCommandToggleSidebar)),
		menu.Text("Cycle Transcript View", keys.Control("o"), command(studio.NativeCommandTranscriptMode)),
		menu.Text("Side Chat", keys.CmdOrCtrl(";"), command(studio.NativeCommandSideChat)),
		menu.Text("Diff", keys.Combo("d", keys.CmdOrCtrlKey, keys.ShiftKey), command(studio.NativeCommandDiff)),
		menu.Text("Browser / Preview", keys.Combo("b", keys.CmdOrCtrlKey, keys.ShiftKey), command(studio.NativeCommandPreview)),
		menu.Text("Select Preview Element", keys.Combo("s", keys.CmdOrCtrlKey, keys.ShiftKey), command(studio.NativeCommandSelectPreview)),
		menu.Separator(),
		menu.Text("Command Palette…", keys.CmdOrCtrl("k"), command(studio.NativeCommandCommandPalette)),
		menu.Text("Find in Conversation…", keys.CmdOrCtrl("f"), command(studio.NativeCommandFindChat)),
		menu.Text("Search Project Chats…", keys.Combo("f", keys.CmdOrCtrlKey, keys.ShiftKey), command(studio.NativeCommandSearchAll)),
	)
	help := menu.NewMenuFromItems(
		menu.Text("Keyboard Shortcuts", keys.CmdOrCtrl("/"), command(studio.NativeCommandHelp)),
		menu.Separator(),
		menu.Text("Check for Updates…", nil, func(*menu.CallbackData) { app.ShowUpdateCenter() }),
	)
	return menu.NewMenuFromItems(
		menu.AppMenu(),
		menu.SubMenu("File", file),
		menu.EditMenu(),
		menu.SubMenu("View", view),
		menu.WindowMenu(),
		menu.SubMenu("Help", help),
	)
}
