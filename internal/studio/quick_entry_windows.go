//go:build windows

package studio

import (
	"fmt"
	"runtime"
	"sync"
	"sync/atomic"
	"unsafe"

	"golang.org/x/sys/windows"
)

const (
	windowsQuickEntryHotKeyID = 0x474B
	windowsWMHotKey           = 0x0312
	windowsWMQuit             = 0x0012
	windowsMODAlt             = 0x0001
	windowsMODControl         = 0x0002
	windowsMODShift           = 0x0004
	windowsMODWin             = 0x0008
	windowsMODNoRepeat        = 0x4000
)

var (
	windowsUser32            = windows.NewLazySystemDLL("user32.dll")
	windowsRegisterHotKey    = windowsUser32.NewProc("RegisterHotKey")
	windowsUnregisterHotKey  = windowsUser32.NewProc("UnregisterHotKey")
	windowsGetMessage        = windowsUser32.NewProc("GetMessageW")
	windowsPeekMessage       = windowsUser32.NewProc("PeekMessageW")
	windowsPostThreadMessage = windowsUser32.NewProc("PostThreadMessageW")
)

type windowsQuickEntryPoint struct {
	X int32
	Y int32
}

type windowsQuickEntryMessage struct {
	Window  uintptr
	Message uint32
	WParam  uintptr
	LParam  uintptr
	Time    uint32
	Point   windowsQuickEntryPoint
	Private uint32
}

type windowsQuickEntryController struct {
	threadID uint32
	done     chan struct{}
	once     sync.Once
	err      error
	stopped  atomic.Bool
}

type windowsQuickEntryStart struct {
	threadID uint32
	err      error
}

func nativeQuickEntrySupported() bool { return true }

func startNativeQuickEntry(shortcutValue string, trigger func()) (quickEntryController, error) {
	if trigger == nil {
		return nil, fmt.Errorf("Quick Entry trigger is required")
	}
	spec, err := parseQuickEntryShortcut(shortcutValue)
	if err != nil {
		return nil, err
	}
	virtualKey, ok := windowsQuickEntryVirtualKey(spec.Key)
	if !ok {
		return nil, fmt.Errorf("Quick Entry key %s is unavailable on Windows", spec.Key)
	}
	modifiers := uintptr(windowsMODNoRepeat)
	if spec.Control {
		modifiers |= windowsMODControl
	}
	if spec.Alt {
		modifiers |= windowsMODAlt
	}
	if spec.Shift {
		modifiers |= windowsMODShift
	}
	if spec.Meta {
		modifiers |= windowsMODWin
	}
	shortcutLabel := formatQuickEntryShortcut(spec)
	started := make(chan windowsQuickEntryStart, 1)
	controller := &windowsQuickEntryController{done: make(chan struct{})}
	go func() {
		runtime.LockOSThread()
		defer runtime.UnlockOSThread()
		defer close(controller.done)
		threadID := windows.GetCurrentThreadId()
		// A thread must own a Win32 message queue before PostThreadMessage can
		// wake it during shutdown. PM_NOREMOVE creates the queue without
		// consuming anything.
		var initialMessage windowsQuickEntryMessage
		windowsPeekMessage.Call(uintptr(unsafe.Pointer(&initialMessage)), 0, 0, 0, 0)
		registered, _, registerErr := windowsRegisterHotKey.Call(
			0,
			windowsQuickEntryHotKeyID,
			modifiers,
			uintptr(virtualKey),
		)
		if registered == 0 {
			started <- windowsQuickEntryStart{err: fmt.Errorf("register %s: %w", shortcutLabel, registerErr)}
			return
		}
		defer windowsUnregisterHotKey.Call(0, windowsQuickEntryHotKeyID)
		controller.threadID = threadID
		started <- windowsQuickEntryStart{threadID: threadID}
		for {
			var message windowsQuickEntryMessage
			result, _, messageErr := windowsGetMessage.Call(
				uintptr(unsafe.Pointer(&message)),
				0,
				0,
				0,
			)
			if int32(result) == -1 {
				controller.err = fmt.Errorf("read Quick Entry shortcut messages: %w", messageErr)
				return
			}
			if result == 0 || message.Message == windowsWMQuit {
				return
			}
			if message.Message == windowsWMHotKey && message.WParam == windowsQuickEntryHotKeyID {
				if !controller.stopped.Load() {
					trigger()
				}
			}
		}
	}()
	result := <-started
	if result.err != nil {
		return nil, result.err
	}
	controller.threadID = result.threadID
	return controller, nil
}

func (c *windowsQuickEntryController) Stop() error {
	c.once.Do(func() {
		c.stopped.Store(true)
		posted, _, err := windowsPostThreadMessage.Call(uintptr(c.threadID), windowsWMQuit, 0, 0)
		if posted == 0 {
			c.err = fmt.Errorf("stop Quick Entry shortcut listener: %w", err)
			return
		}
		<-c.done
	})
	return c.err
}

func windowsQuickEntryVirtualKey(key string) (uint32, bool) {
	if len(key) == 1 {
		value := key[0]
		if (value >= 'A' && value <= 'Z') || (value >= '0' && value <= '9') {
			return uint32(value), true
		}
	}
	keys := map[string]uint32{
		"Space": 0x20, "Enter": 0x0D, "Tab": 0x09, "Escape": 0x1B,
		"Left": 0x25, "Up": 0x26, "Right": 0x27, "Down": 0x28,
	}
	if value, ok := keys[key]; ok {
		return value, true
	}
	if len(key) >= 2 && key[0] == 'F' {
		var number int
		if _, err := fmt.Sscanf(key, "F%d", &number); err == nil && number >= 1 && number <= 12 {
			return uint32(0x70 + number - 1), true
		}
	}
	return 0, false
}
