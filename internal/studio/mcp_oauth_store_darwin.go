//go:build darwin && cgo

package studio

/*
#cgo LDFLAGS: -framework Security -framework CoreFoundation
#include <Security/Security.h>
#include <stdlib.h>
#include <string.h>
#pragma clang diagnostic ignored "-Wdeprecated-declarations"

static OSStatus gokinOAuthSave(const char *service, const char *account, const unsigned char *data, long length) {
	SecKeychainItemRef item = NULL;
	UInt32 existingLength = 0;
	void *existingData = NULL;
	OSStatus status = SecKeychainFindGenericPassword(
		NULL,
		(UInt32)strlen(service), service,
		(UInt32)strlen(account), account,
		&existingLength, &existingData, &item
	);
	if (status == errSecSuccess) {
		SecKeychainItemFreeContent(NULL, existingData);
		status = SecKeychainItemModifyAttributesAndData(item, NULL, (UInt32)length, data);
		CFRelease(item);
		return status;
	}
	if (status != errSecItemNotFound) return status;
	return SecKeychainAddGenericPassword(
		NULL,
		(UInt32)strlen(service), service,
		(UInt32)strlen(account), account,
		(UInt32)length, data,
		NULL
	);
}

static OSStatus gokinOAuthLoad(const char *service, const char *account, unsigned char **out, long *outLength) {
	*out = NULL;
	*outLength = 0;
	UInt32 length = 0;
	void *passwordData = NULL;
	SecKeychainItemRef item = NULL;
	OSStatus status = SecKeychainFindGenericPassword(
		NULL,
		(UInt32)strlen(service), service,
		(UInt32)strlen(account), account,
		&length, &passwordData, &item
	);
	if (status != errSecSuccess) return status;
	if (length == 0 || passwordData == NULL) {
		SecKeychainItemFreeContent(NULL, passwordData);
		if (item != NULL) CFRelease(item);
		return errSecDecode;
	}
	unsigned char *copy = malloc((size_t)length);
	if (copy == NULL) {
		SecKeychainItemFreeContent(NULL, passwordData);
		if (item != NULL) CFRelease(item);
		return errSecAllocate;
	}
	memcpy(copy, passwordData, (size_t)length);
	SecKeychainItemFreeContent(NULL, passwordData);
	if (item != NULL) CFRelease(item);
	*out = copy;
	*outLength = (long)length;
	return errSecSuccess;
}

static OSStatus gokinOAuthDelete(const char *service, const char *account) {
	SecKeychainItemRef item = NULL;
	UInt32 length = 0;
	void *passwordData = NULL;
	OSStatus status = SecKeychainFindGenericPassword(
		NULL,
		(UInt32)strlen(service), service,
		(UInt32)strlen(account), account,
		&length, &passwordData, &item
	);
	if (status != errSecSuccess) return status;
	SecKeychainItemFreeContent(NULL, passwordData);
	status = SecKeychainItemDelete(item);
	CFRelease(item);
	return status;
}
*/
import "C"

import (
	"fmt"
	"unsafe"
)

func platformSaveKeychainCredential(service, account, description string, data []byte) error {
	serviceCString := C.CString(service)
	accountCString := C.CString(account)
	defer C.free(unsafe.Pointer(serviceCString))
	defer C.free(unsafe.Pointer(accountCString))
	status := C.gokinOAuthSave(
		serviceCString,
		accountCString,
		(*C.uchar)(unsafe.Pointer(&data[0])),
		C.long(len(data)),
	)
	if status != C.errSecSuccess {
		return fmt.Errorf("save %s in macOS Keychain: status %d", description, int32(status))
	}
	return nil
}

func platformLoadKeychainCredential(service, account, description string) ([]byte, error) {
	serviceCString := C.CString(service)
	accountCString := C.CString(account)
	defer C.free(unsafe.Pointer(serviceCString))
	defer C.free(unsafe.Pointer(accountCString))
	var data *C.uchar
	var length C.long
	status := C.gokinOAuthLoad(serviceCString, accountCString, &data, &length)
	if status == C.errSecItemNotFound {
		return nil, errMCPOAuthCredentialNotFound
	}
	if status != C.errSecSuccess {
		return nil, fmt.Errorf("load %s from macOS Keychain: status %d", description, int32(status))
	}
	defer C.free(unsafe.Pointer(data))
	if length <= 0 || length > C.long(maxMCPOAuthCredentialBytes) {
		return nil, fmt.Errorf("stored OAuth credential has invalid size")
	}
	return C.GoBytes(unsafe.Pointer(data), C.int(length)), nil
}

func platformDeleteKeychainCredential(service, account, description string) error {
	serviceCString := C.CString(service)
	accountCString := C.CString(account)
	defer C.free(unsafe.Pointer(serviceCString))
	defer C.free(unsafe.Pointer(accountCString))
	status := C.gokinOAuthDelete(serviceCString, accountCString)
	if status == C.errSecItemNotFound {
		return errMCPOAuthCredentialNotFound
	}
	if status != C.errSecSuccess {
		return fmt.Errorf("delete %s from macOS Keychain: status %d", description, int32(status))
	}
	return nil
}

func platformSaveMCPOAuthCredential(account string, data []byte) error {
	return platformSaveKeychainCredential(mcpOAuthCredentialService, account, "OAuth token", data)
}

func platformLoadMCPOAuthCredential(account string) ([]byte, error) {
	return platformLoadKeychainCredential(mcpOAuthCredentialService, account, "OAuth token")
}

func platformDeleteMCPOAuthCredential(account string) error {
	return platformDeleteKeychainCredential(mcpOAuthCredentialService, account, "OAuth token")
}

func platformSaveLocalEnvironmentCredential(data []byte) error {
	return platformSaveKeychainCredential(localEnvironmentCredentialService, localEnvironmentCredentialAccount, "local environment", data)
}

func platformLoadLocalEnvironmentCredential() ([]byte, error) {
	return platformLoadKeychainCredential(localEnvironmentCredentialService, localEnvironmentCredentialAccount, "local environment")
}

func platformDeleteLocalEnvironmentCredential() error {
	return platformDeleteKeychainCredential(localEnvironmentCredentialService, localEnvironmentCredentialAccount, "local environment")
}
