package main

import (
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/ginkida/gokin-studio/internal/studio"
	"github.com/wailsapp/wails/v2/pkg/menu"
	"github.com/wailsapp/wails/v2/pkg/menu/keys"
)

func TestApplicationLifecycleWiresQuitGuardWithoutRemovingMacBackgroundClose(t *testing.T) {
	source, err := os.ReadFile("main.go")
	if err != nil {
		t.Fatal(err)
	}
	text := string(source)
	for _, contract := range []string{
		"OnBeforeClose:    app.BeforeClose",
		`HideWindowOnClose: runtime.GOOS == "darwin"`,
		"OnShutdown:       app.Shutdown",
	} {
		if !strings.Contains(text, contract) {
			t.Errorf("main lifecycle missing %q", contract)
		}
	}
}

func TestMacApplicationMenuIncludesDesktopCommands(t *testing.T) {
	app := studio.NewStudio()
	applicationMenu := macApplicationMenu(app)
	if applicationMenu == nil || len(applicationMenu.Items) != 6 {
		t.Fatalf("application menu = %#v", applicationMenu)
	}
	file := applicationMenu.Items[1]
	if file.Label != "File" || file.SubMenu == nil || len(file.SubMenu.Items) != 5 {
		t.Fatalf("file menu = %#v", file)
	}
	assertMenuCommand(t, file.SubMenu.Items[0], "New Chat", "n", []keys.Modifier{keys.CmdOrCtrlKey})
	assertMenuCommand(t, file.SubMenu.Items[1], "Close Chat", "w", []keys.Modifier{keys.CmdOrCtrlKey})
	assertMenuCommand(t, file.SubMenu.Items[2], "Connect Project Folder…", "o", []keys.Modifier{keys.CmdOrCtrlKey})
	assertMenuCommand(t, file.SubMenu.Items[4], "Settings…", ",", []keys.Modifier{keys.CmdOrCtrlKey})

	view := applicationMenu.Items[3]
	if view.Label != "View" || view.SubMenu == nil || len(view.SubMenu.Items) != 16 {
		t.Fatalf("view menu = %#v", view)
	}
	assertMenuCommand(t, view.SubMenu.Items[0], "Chat", "1", []keys.Modifier{keys.CmdOrCtrlKey})
	assertMenuCommand(t, view.SubMenu.Items[7], "Cycle Transcript View", "o", []keys.Modifier{keys.ControlKey})
	assertMenuCommand(t, view.SubMenu.Items[8], "Side Chat", ";", []keys.Modifier{keys.CmdOrCtrlKey})
	assertMenuCommand(t, view.SubMenu.Items[9], "Diff", "d", []keys.Modifier{keys.CmdOrCtrlKey, keys.ShiftKey})
	assertMenuCommand(t, view.SubMenu.Items[10], "Browser / Preview", "b", []keys.Modifier{keys.CmdOrCtrlKey, keys.ShiftKey})
	assertMenuCommand(t, view.SubMenu.Items[11], "Select Preview Element", "s", []keys.Modifier{keys.CmdOrCtrlKey, keys.ShiftKey})
	assertMenuCommand(t, view.SubMenu.Items[13], "Command Palette…", "k", []keys.Modifier{keys.CmdOrCtrlKey})
	assertMenuCommand(t, view.SubMenu.Items[15], "Search Project Chats…", "f", []keys.Modifier{keys.CmdOrCtrlKey, keys.ShiftKey})

	help := applicationMenu.Items[5]
	if help.Label != "Help" || help.SubMenu == nil {
		t.Fatalf("help menu = %#v", help)
	}
	if len(help.SubMenu.Items) != 3 {
		t.Fatalf("help items = %d, want 3", len(help.SubMenu.Items))
	}
	assertMenuCommand(t, help.SubMenu.Items[0], "Keyboard Shortcuts", "/", []keys.Modifier{keys.CmdOrCtrlKey})
	if got := help.SubMenu.Items[2]; got.Label != "Check for Updates…" || got.Click == nil {
		t.Fatalf("update item = %#v", got)
	}

	// Exercise real callbacks rather than only checking labels. The backend
	// queues them because React has not announced readiness in this unit test.
	file.SubMenu.Items[0].Click(nil)
	file.SubMenu.Items[1].Click(nil)
	view.SubMenu.Items[7].Click(nil)
	view.SubMenu.Items[8].Click(nil)
	view.SubMenu.Items[9].Click(nil)
	view.SubMenu.Items[10].Click(nil)
	view.SubMenu.Items[11].Click(nil)
	view.SubMenu.Items[15].Click(nil)
	if got, want := app.StartNativeMenuEvents(), []string{studio.NativeCommandNewChat, studio.NativeCommandCloseChat, studio.NativeCommandTranscriptMode, studio.NativeCommandSideChat, studio.NativeCommandDiff, studio.NativeCommandPreview, studio.NativeCommandSelectPreview, studio.NativeCommandSearchAll}; !reflect.DeepEqual(got, want) {
		t.Fatalf("menu callback commands = %#v, want %#v", got, want)
	}
}

func assertMenuCommand(t *testing.T, item *menu.MenuItem, label, key string, modifiers []keys.Modifier) {
	t.Helper()
	if item == nil || item.Label != label || item.Click == nil || item.Accelerator == nil {
		t.Fatalf("menu item = %#v, want enabled %q command", item, label)
	}
	if item.Accelerator.Key != key || !reflect.DeepEqual(item.Accelerator.Modifiers, modifiers) {
		t.Fatalf("%s accelerator = %#v, want key %q modifiers %#v", label, item.Accelerator, key, modifiers)
	}
}
