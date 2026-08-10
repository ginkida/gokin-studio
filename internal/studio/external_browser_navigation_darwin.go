//go:build darwin && cgo

package studio

/*
#cgo CFLAGS: -x objective-c
#cgo LDFLAGS: -framework Foundation -framework WebKit
#include <stdbool.h>
#include <stdlib.h>

bool gokinExternalNavigationPolicyAvailable(void);
bool gokinExternalNavigationPolicyTestInstall(void);
bool gokinExternalNavigationPolicyAllows(
	const char *sourceScheme,
	const char *sourceHost,
	long sourcePort,
	bool sourceIsMainFrame,
	const char *targetScheme,
	const char *targetHost,
	long targetPort,
	bool targetIsMainFrame,
	bool hasTargetFrame
);
*/
import "C"

import "unsafe"

func externalBrowserActiveScriptsSupported() bool {
	return bool(C.gokinExternalNavigationPolicyAvailable())
}

func darwinExternalNavigationPolicyInstalls() bool {
	return bool(C.gokinExternalNavigationPolicyTestInstall())
}

func darwinExternalNavigationPolicyAllows(sourceScheme, sourceHost string, sourcePort int, sourceIsMain bool, targetScheme, targetHost string, targetPort int, targetIsMain, hasTarget bool) bool {
	cSourceScheme := C.CString(sourceScheme)
	cSourceHost := C.CString(sourceHost)
	cTargetScheme := C.CString(targetScheme)
	cTargetHost := C.CString(targetHost)
	defer C.free(unsafe.Pointer(cSourceScheme))
	defer C.free(unsafe.Pointer(cSourceHost))
	defer C.free(unsafe.Pointer(cTargetScheme))
	defer C.free(unsafe.Pointer(cTargetHost))
	return bool(C.gokinExternalNavigationPolicyAllows(
		cSourceScheme, cSourceHost, C.long(sourcePort), C.bool(sourceIsMain),
		cTargetScheme, cTargetHost, C.long(targetPort), C.bool(targetIsMain), C.bool(hasTarget),
	))
}
