//go:build darwin && cgo

package studio

/*
#cgo CFLAGS: -x objective-c -fblocks
#cgo LDFLAGS: -framework AppKit -framework Foundation
#include <stdbool.h>

bool gokinQuickEntryPanelShow(char **errorOut);
bool gokinQuickEntryPanelHide(bool activateStudio, char **errorOut);
void gokinQuickEntryPanelFree(char *value);
*/
import "C"

import "fmt"

func nativeQuickEntryWindowSupported() bool { return true }

func showNativeQuickEntryWindow() error {
	var nativeError *C.char
	if C.gokinQuickEntryPanelShow(&nativeError) {
		return nil
	}
	return quickEntryPanelError(nativeError)
}

func hideNativeQuickEntryWindow(activateStudio bool) error {
	var nativeError *C.char
	if C.gokinQuickEntryPanelHide(C.bool(activateStudio), &nativeError) {
		return nil
	}
	return quickEntryPanelError(nativeError)
}

func quickEntryPanelError(value *C.char) error {
	if value == nil {
		return fmt.Errorf("native window operation failed")
	}
	defer C.gokinQuickEntryPanelFree(value)
	message := C.GoString(value)
	if message == "" {
		message = "native window operation failed"
	}
	return fmt.Errorf("%s", message)
}
