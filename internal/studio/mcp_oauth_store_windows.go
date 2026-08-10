//go:build windows

package studio

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"unsafe"

	"golang.org/x/sys/windows"
)

const cryptProtectUIForbidden = 0x1

type windowsDataBlob struct {
	Size uint32
	Data *byte
}

var (
	crypt32DLL           = windows.NewLazySystemDLL("crypt32.dll")
	cryptProtectDataProc = crypt32DLL.NewProc("CryptProtectData")
	cryptUnprotectProc   = crypt32DLL.NewProc("CryptUnprotectData")
)

func windowsOAuthTokenDir() (string, error) {
	dir := filepath.Join(configDir(), "mcp_oauth_tokens")
	info, err := os.Lstat(dir)
	if os.IsNotExist(err) {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return "", err
		}
		return dir, nil
	}
	if err != nil {
		return "", err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return "", fmt.Errorf("MCP OAuth token path must be a real directory")
	}
	return dir, nil
}

func windowsProtectCredentialBytes(data []byte, protect bool, service string) ([]byte, error) {
	if len(data) == 0 {
		return nil, fmt.Errorf("OAuth credential is empty")
	}
	input := windowsDataBlob{Size: uint32(len(data)), Data: &data[0]}
	entropyBytes := []byte(service)
	entropy := windowsDataBlob{Size: uint32(len(entropyBytes)), Data: &entropyBytes[0]}
	var output windowsDataBlob
	procedure := cryptProtectDataProc
	if !protect {
		procedure = cryptUnprotectProc
	}
	result, _, callErr := procedure.Call(
		uintptr(unsafe.Pointer(&input)),
		0,
		uintptr(unsafe.Pointer(&entropy)),
		0,
		0,
		cryptProtectUIForbidden,
		uintptr(unsafe.Pointer(&output)),
	)
	if result == 0 {
		if callErr != nil && !errors.Is(callErr, windows.ERROR_SUCCESS) {
			return nil, callErr
		}
		return nil, fmt.Errorf("Windows DPAPI operation failed")
	}
	if output.Data == nil || output.Size == 0 || output.Size > maxMCPOAuthCredentialBytes*2 {
		if output.Data != nil {
			_, _ = windows.LocalFree(windows.Handle(uintptr(unsafe.Pointer(output.Data))))
		}
		return nil, fmt.Errorf("Windows DPAPI returned an invalid credential")
	}
	defer windows.LocalFree(windows.Handle(uintptr(unsafe.Pointer(output.Data)))) //nolint:errcheck
	protected := unsafe.Slice(output.Data, int(output.Size))
	return append([]byte(nil), protected...), nil
}

func platformSaveMCPOAuthCredential(account string, data []byte) error {
	dir, err := windowsOAuthTokenDir()
	if err != nil {
		return err
	}
	protected, err := windowsProtectCredentialBytes(data, true, mcpOAuthCredentialService)
	if err != nil {
		return fmt.Errorf("protect MCP OAuth token with Windows DPAPI: %w", err)
	}
	return atomicWriteFile(filepath.Join(dir, account+".bin"), protected, 0o600)
}

func platformLoadMCPOAuthCredential(account string) ([]byte, error) {
	dir, err := windowsOAuthTokenDir()
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(filepath.Join(dir, account+".bin"))
	if os.IsNotExist(err) {
		return nil, errMCPOAuthCredentialNotFound
	}
	if err != nil {
		return nil, err
	}
	if len(data) == 0 || len(data) > maxMCPOAuthCredentialBytes*2 {
		return nil, fmt.Errorf("encrypted OAuth credential has invalid size")
	}
	plain, err := windowsProtectCredentialBytes(data, false, mcpOAuthCredentialService)
	if err != nil {
		return nil, fmt.Errorf("unprotect MCP OAuth token with Windows DPAPI: %w", err)
	}
	return plain, nil
}

func platformDeleteMCPOAuthCredential(account string) error {
	dir, err := windowsOAuthTokenDir()
	if err != nil {
		return err
	}
	err = os.Remove(filepath.Join(dir, account+".bin"))
	if os.IsNotExist(err) {
		return errMCPOAuthCredentialNotFound
	}
	return err
}

func windowsLocalEnvironmentPath() string {
	return filepath.Join(configDir(), "local_environment.bin")
}

func platformSaveLocalEnvironmentCredential(data []byte) error {
	if err := os.MkdirAll(configDir(), 0o700); err != nil {
		return err
	}
	protected, err := windowsProtectCredentialBytes(data, true, localEnvironmentCredentialService)
	if err != nil {
		return fmt.Errorf("protect local environment with Windows DPAPI: %w", err)
	}
	return atomicWriteFile(windowsLocalEnvironmentPath(), protected, 0o600)
}

func platformLoadLocalEnvironmentCredential() ([]byte, error) {
	data, err := os.ReadFile(windowsLocalEnvironmentPath())
	if os.IsNotExist(err) {
		return nil, errMCPOAuthCredentialNotFound
	}
	if err != nil {
		return nil, err
	}
	if len(data) == 0 || len(data) > maxMCPOAuthCredentialBytes*2 {
		return nil, fmt.Errorf("encrypted local environment has invalid size")
	}
	plain, err := windowsProtectCredentialBytes(data, false, localEnvironmentCredentialService)
	if err != nil {
		return nil, fmt.Errorf("unprotect local environment with Windows DPAPI: %w", err)
	}
	return plain, nil
}

func platformDeleteLocalEnvironmentCredential() error {
	err := os.Remove(windowsLocalEnvironmentPath())
	if os.IsNotExist(err) {
		return errMCPOAuthCredentialNotFound
	}
	return err
}
