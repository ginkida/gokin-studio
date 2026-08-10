//go:build darwin && cgo

package studio

/*
#cgo LDFLAGS: -framework Carbon -framework ApplicationServices -framework CoreFoundation
#include <Carbon/Carbon.h>
#include <ApplicationServices/ApplicationServices.h>
#include <CoreFoundation/CoreFoundation.h>
#include <stdbool.h>
#include <stdatomic.h>
#include <stdlib.h>

extern void goQuickEntryDarwinTriggered(unsigned int token);
extern void goQuickEntryDarwinGestureEvent(unsigned int token, unsigned int eventType, unsigned int keyCode, unsigned long long flags, unsigned long long timestamp);

typedef struct {
	EventHotKeyRef hotKey;
	EventHandlerRef handler;
	unsigned int token;
} GokinQuickEntryHotKey;

static OSStatus gokinQuickEntryHandler(
	EventHandlerCallRef nextHandler,
	EventRef event,
	void *userData
) {
	GokinQuickEntryHotKey *shortcut = (GokinQuickEntryHotKey *)userData;
	if (shortcut != NULL) {
		goQuickEntryDarwinTriggered(shortcut->token);
	}
	return noErr;
}

static GokinQuickEntryHotKey *gokinRegisterQuickEntry(
	unsigned int token,
	unsigned int keyCode,
	unsigned int modifiers,
	OSStatus *statusOut
) {
	GokinQuickEntryHotKey *shortcut = (GokinQuickEntryHotKey *)calloc(1, sizeof(GokinQuickEntryHotKey));
	if (shortcut == NULL) {
		*statusOut = memFullErr;
		return NULL;
	}
	shortcut->token = token;
	EventTypeSpec eventType = { kEventClassKeyboard, kEventHotKeyPressed };
	OSStatus status = InstallApplicationEventHandler(
		gokinQuickEntryHandler,
		1,
		&eventType,
		shortcut,
		&shortcut->handler
	);
	if (status != noErr) {
		free(shortcut);
		*statusOut = status;
		return NULL;
	}
	EventHotKeyID identifier;
	identifier.signature = 0x474F4B49;
	identifier.id = token;
	status = RegisterEventHotKey(
		keyCode,
		modifiers,
		identifier,
		GetApplicationEventTarget(),
		0,
		&shortcut->hotKey
	);
	if (status != noErr) {
		RemoveEventHandler(shortcut->handler);
		free(shortcut);
		*statusOut = status;
		return NULL;
	}
	*statusOut = noErr;
	return shortcut;
}

static OSStatus gokinUnregisterQuickEntry(GokinQuickEntryHotKey *shortcut) {
	if (shortcut == NULL) {
		return noErr;
	}
	OSStatus hotKeyStatus = noErr;
	OSStatus handlerStatus = noErr;
	if (shortcut->hotKey != NULL) {
		hotKeyStatus = UnregisterEventHotKey(shortcut->hotKey);
	}
	if (shortcut->handler != NULL) {
		handlerStatus = RemoveEventHandler(shortcut->handler);
	}
	free(shortcut);
	return hotKeyStatus != noErr ? hotKeyStatus : handlerStatus;
}

typedef struct {
	CFMachPortRef tap;
	CFRunLoopSourceRef source;
	CFRunLoopRef runLoop;
	unsigned int token;
	bool consumeCapsLock;
	_Atomic bool stopped;
} GokinQuickEntryGesture;

static CGEventRef gokinQuickEntryGestureHandler(
	CGEventTapProxy proxy,
	CGEventType type,
	CGEventRef event,
	void *userData
) {
	GokinQuickEntryGesture *gesture = (GokinQuickEntryGesture *)userData;
	if (gesture == NULL) {
		return event;
	}
	if (type == kCGEventTapDisabledByTimeout || type == kCGEventTapDisabledByUserInput) {
		if (gesture->tap != NULL) {
			CGEventTapEnable(gesture->tap, true);
		}
		return event;
	}
	if (type != kCGEventFlagsChanged && type != kCGEventKeyDown) {
		return event;
	}
	unsigned int keyCode = (unsigned int)CGEventGetIntegerValueField(event, kCGKeyboardEventKeycode);
	goQuickEntryDarwinGestureEvent(
		gesture->token,
		(unsigned int)type,
		keyCode,
		(unsigned long long)CGEventGetFlags(event),
		(unsigned long long)CGEventGetTimestamp(event)
	);
	// Caps Lock is an explicit opt-in voice control. Suppress its ordinary
	// toggle while active so dictated text is not unexpectedly uppercased.
	if (gesture->consumeCapsLock && type == kCGEventFlagsChanged && keyCode == 57) {
		return NULL;
	}
	return event;
}

static bool gokinRequestAccessibilityTrust(void) {
	const void *keys[] = { kAXTrustedCheckOptionPrompt };
	const void *values[] = { kCFBooleanTrue };
	CFDictionaryRef options = CFDictionaryCreate(
		kCFAllocatorDefault,
		keys,
		values,
		1,
		&kCFCopyStringDictionaryKeyCallBacks,
		&kCFTypeDictionaryValueCallBacks
	);
	if (options == NULL) {
		return AXIsProcessTrusted();
	}
	bool trusted = AXIsProcessTrustedWithOptions(options);
	CFRelease(options);
	return trusted;
}

static GokinQuickEntryGesture *gokinCreateQuickEntryGesture(
	unsigned int token,
	bool consumeCapsLock
) {
	GokinQuickEntryGesture *gesture = (GokinQuickEntryGesture *)calloc(1, sizeof(GokinQuickEntryGesture));
	if (gesture == NULL) {
		return NULL;
	}
	gesture->token = token;
	gesture->consumeCapsLock = consumeCapsLock;
	CGEventMask mask = CGEventMaskBit(kCGEventFlagsChanged) | CGEventMaskBit(kCGEventKeyDown);
	gesture->tap = CGEventTapCreate(
		kCGSessionEventTap,
		kCGHeadInsertEventTap,
		kCGEventTapOptionDefault,
		mask,
		gokinQuickEntryGestureHandler,
		gesture
	);
	if (gesture->tap == NULL) {
		free(gesture);
		return NULL;
	}
	gesture->source = CFMachPortCreateRunLoopSource(kCFAllocatorDefault, gesture->tap, 0);
	if (gesture->source == NULL) {
		CFRelease(gesture->tap);
		free(gesture);
		return NULL;
	}
	gesture->runLoop = CFRunLoopGetCurrent();
	CFRetain(gesture->runLoop);
	CFRunLoopAddSource(gesture->runLoop, gesture->source, kCFRunLoopCommonModes);
	CGEventTapEnable(gesture->tap, true);
	return gesture;
}

static void gokinRunQuickEntryGesture(GokinQuickEntryGesture *gesture) {
	if (gesture != NULL) {
		while (!atomic_load(&gesture->stopped)) {
			CFRunLoopRunInMode(kCFRunLoopDefaultMode, 10.0, true);
		}
	}
}

static void gokinStopQuickEntryGesture(GokinQuickEntryGesture *gesture) {
	if (gesture == NULL) {
		return;
	}
	atomic_store(&gesture->stopped, true);
	if (gesture->tap != NULL) {
		CGEventTapEnable(gesture->tap, false);
	}
	if (gesture->runLoop != NULL) {
		CFRunLoopStop(gesture->runLoop);
	}
}

static void gokinDestroyQuickEntryGesture(GokinQuickEntryGesture *gesture) {
	if (gesture == NULL) {
		return;
	}
	if (gesture->runLoop != NULL && gesture->source != NULL) {
		CFRunLoopRemoveSource(gesture->runLoop, gesture->source, kCFRunLoopCommonModes);
	}
	if (gesture->source != NULL) {
		CFRelease(gesture->source);
	}
	if (gesture->tap != NULL) {
		CFRelease(gesture->tap);
	}
	if (gesture->runLoop != NULL) {
		CFRelease(gesture->runLoop);
	}
	free(gesture);
}
*/
import "C"

import (
	"fmt"
	"runtime"
	"sync"
	"sync/atomic"
	"time"
)

var (
	quickEntryDarwinNextToken atomic.Uint32
	quickEntryDarwinCallbacks sync.Map
	quickEntryDarwinGestures  sync.Map
)

type darwinQuickEntryController struct {
	token    uint32
	shortcut *C.GokinQuickEntryHotKey
	once     sync.Once
	err      error
}

type darwinQuickEntryGestureController struct {
	token   uint32
	gesture *C.GokinQuickEntryGesture
	done    chan struct{}
	once    sync.Once
}

type darwinQuickEntryGestureState struct {
	kind               string
	trigger            func()
	optionDown         bool
	optionKey          uint32
	optionDownAt       uint64
	previousOptionTap  uint64
	previousCapsChange uint64
}

const (
	darwinCGEventKeyDown      = 10
	darwinCGEventFlagsChanged = 12
	darwinOptionFlag          = uint64(1 << 19)
	darwinRelevantFlags       = uint64((1 << 16) | (1 << 17) | (1 << 18) | (1 << 19) | (1 << 20) | (1 << 23))
	darwinOptionTapMax        = uint64(350 * time.Millisecond)
	darwinOptionDoubleGap     = uint64(450 * time.Millisecond)
	darwinCapsLockDebounce    = uint64(200 * time.Millisecond)
)

func nativeQuickEntrySupported() bool { return true }

func startNativeQuickEntry(shortcutValue string, trigger func()) (quickEntryController, error) {
	if trigger == nil {
		return nil, fmt.Errorf("Quick Entry trigger is required")
	}
	if shortcutValue == quickEntryDoubleOption || shortcutValue == quickEntryCapsLock {
		return startDarwinQuickEntryGesture(shortcutValue, trigger)
	}
	spec, err := parseQuickEntryShortcut(shortcutValue)
	if err != nil {
		return nil, err
	}
	keyCode, ok := darwinQuickEntryKeyCode(spec.Key)
	if !ok {
		return nil, fmt.Errorf("Quick Entry key %s is unavailable on macOS", spec.Key)
	}
	var modifiers C.uint
	if spec.Control {
		modifiers |= C.controlKey
	}
	if spec.Alt {
		modifiers |= C.optionKey
	}
	if spec.Shift {
		modifiers |= C.shiftKey
	}
	if spec.Meta {
		modifiers |= C.cmdKey
	}
	token := quickEntryDarwinNextToken.Add(1)
	if token == 0 {
		token = quickEntryDarwinNextToken.Add(1)
	}
	quickEntryDarwinCallbacks.Store(token, trigger)
	var status C.OSStatus
	shortcut := C.gokinRegisterQuickEntry(C.uint(token), C.uint(keyCode), modifiers, &status)
	if shortcut == nil || status != 0 {
		quickEntryDarwinCallbacks.Delete(token)
		return nil, fmt.Errorf("register %s (OSStatus %d)", formatQuickEntryShortcut(spec), int32(status))
	}
	return &darwinQuickEntryController{token: token, shortcut: shortcut}, nil
}

func startDarwinQuickEntryGesture(kind string, trigger func()) (quickEntryController, error) {
	if kind != quickEntryDoubleOption && kind != quickEntryCapsLock {
		return nil, fmt.Errorf("unsupported macOS shortcut %q", kind)
	}
	if C.gokinRequestAccessibilityTrust() == C.bool(false) {
		return nil, fmt.Errorf("register %s: enable Gokin Studio in macOS Privacy & Security > Accessibility", kind)
	}
	token := quickEntryDarwinNextToken.Add(1)
	if token == 0 {
		token = quickEntryDarwinNextToken.Add(1)
	}
	state := &darwinQuickEntryGestureState{kind: kind, trigger: trigger}
	quickEntryDarwinGestures.Store(token, state)
	started := make(chan *C.GokinQuickEntryGesture, 1)
	done := make(chan struct{})
	go func() {
		runtime.LockOSThread()
		defer runtime.UnlockOSThread()
		defer close(done)
		gesture := C.gokinCreateQuickEntryGesture(C.uint(token), C.bool(kind == quickEntryCapsLock))
		started <- gesture
		if gesture == nil {
			return
		}
		C.gokinRunQuickEntryGesture(gesture)
		// Deliberately NOT destroyed here. Stop() sets the stopped flag and
		// then still dereferences gesture->tap and gesture->runLoop; this loop
		// wakes on every keystroke and on the 10s timeout, so freeing from
		// this thread could release the struct out from under that call —
		// a use-after-free on quit or on any shortcut change. Ownership of the
		// teardown belongs to Stop(), which frees after joining `done`.
	}()
	gesture := <-started
	if gesture == nil {
		quickEntryDarwinGestures.Delete(token)
		<-done
		return nil, fmt.Errorf("register %s: macOS could not create the global keyboard monitor", kind)
	}
	return &darwinQuickEntryGestureController{token: token, gesture: gesture, done: done}, nil
}

func (c *darwinQuickEntryController) Stop() error {
	c.once.Do(func() {
		quickEntryDarwinCallbacks.Delete(c.token)
		if status := C.gokinUnregisterQuickEntry(c.shortcut); status != 0 {
			c.err = fmt.Errorf("unregister Quick Entry shortcut (OSStatus %d)", int32(status))
		}
		c.shortcut = nil
	})
	return c.err
}

func (c *darwinQuickEntryGestureController) Stop() error {
	c.once.Do(func() {
		quickEntryDarwinGestures.Delete(c.token)
		C.gokinStopQuickEntryGesture(c.gesture)
		// Join first: the run-loop thread must have left gokinRunQuickEntryGesture
		// before the Core Foundation objects behind `gesture` are released.
		<-c.done
		C.gokinDestroyQuickEntryGesture(c.gesture)
		c.gesture = nil
	})
	return nil
}

func darwinQuickEntryKeyCode(key string) (uint32, bool) {
	// Carbon virtual key codes are hardware-position identifiers. These values
	// are stable across macOS keyboard layouts; the displayed chord remains
	// the user-facing logical key configured in Settings.
	codes := map[string]uint32{
		"A": 0, "S": 1, "D": 2, "F": 3, "H": 4, "G": 5, "Z": 6, "X": 7,
		"C": 8, "V": 9, "B": 11, "Q": 12, "W": 13, "E": 14, "R": 15, "Y": 16,
		"T": 17, "1": 18, "2": 19, "3": 20, "4": 21, "6": 22, "5": 23,
		"9": 25, "7": 26, "8": 28, "0": 29, "O": 31, "U": 32, "I": 34,
		"P": 35, "Enter": 36, "L": 37, "J": 38, "K": 40, "Tab": 48,
		"N": 45, "M": 46, "Space": 49, "Escape": 53, "F1": 122, "F2": 120, "F3": 99, "F4": 118,
		"F5": 96, "F6": 97, "F7": 98, "F8": 100, "F9": 101, "F10": 109,
		"F11": 103, "F12": 111, "Left": 123, "Right": 124, "Down": 125, "Up": 126,
	}
	code, ok := codes[key]
	return code, ok
}

//export goQuickEntryDarwinTriggered
func goQuickEntryDarwinTriggered(token C.uint) {
	value, ok := quickEntryDarwinCallbacks.Load(uint32(token))
	if !ok {
		return
	}
	if trigger, ok := value.(func()); ok {
		trigger()
	}
}

func (s *darwinQuickEntryGestureState) resetOption() {
	s.optionDown = false
	s.optionKey = 0
	s.optionDownAt = 0
	s.previousOptionTap = 0
}

func (s *darwinQuickEntryGestureState) handle(eventType, keyCode uint32, flags, timestamp uint64) {
	if s.kind == quickEntryCapsLock {
		if eventType != darwinCGEventFlagsChanged || keyCode != 57 {
			return
		}
		if s.previousCapsChange != 0 && timestamp-s.previousCapsChange < darwinCapsLockDebounce {
			return
		}
		s.previousCapsChange = timestamp
		s.trigger()
		return
	}
	if eventType == darwinCGEventKeyDown {
		s.resetOption()
		return
	}
	if eventType != darwinCGEventFlagsChanged || (keyCode != 58 && keyCode != 61) {
		s.resetOption()
		return
	}
	otherFlags := flags & darwinRelevantFlags &^ darwinOptionFlag
	optionPressed := flags&darwinOptionFlag != 0
	if otherFlags != 0 {
		s.resetOption()
		return
	}
	if optionPressed {
		if s.optionDown || (s.previousOptionTap != 0 && timestamp-s.previousOptionTap > darwinOptionDoubleGap) {
			s.resetOption()
		}
		s.optionDown = true
		s.optionKey = keyCode
		s.optionDownAt = timestamp
		return
	}
	if !s.optionDown || s.optionKey != keyCode || timestamp < s.optionDownAt || timestamp-s.optionDownAt > darwinOptionTapMax {
		s.resetOption()
		return
	}
	s.optionDown = false
	s.optionKey = 0
	s.optionDownAt = 0
	if s.previousOptionTap != 0 && timestamp >= s.previousOptionTap && timestamp-s.previousOptionTap <= darwinOptionDoubleGap {
		s.previousOptionTap = 0
		s.trigger()
		return
	}
	s.previousOptionTap = timestamp
}

//export goQuickEntryDarwinGestureEvent
func goQuickEntryDarwinGestureEvent(token C.uint, eventType C.uint, keyCode C.uint, flags C.ulonglong, timestamp C.ulonglong) {
	value, ok := quickEntryDarwinGestures.Load(uint32(token))
	if !ok {
		return
	}
	if state, ok := value.(*darwinQuickEntryGestureState); ok {
		state.handle(uint32(eventType), uint32(keyCode), uint64(flags), uint64(timestamp))
	}
}
