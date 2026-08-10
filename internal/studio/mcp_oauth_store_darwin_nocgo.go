//go:build darwin && !cgo

package studio

import "fmt"

func platformSaveMCPOAuthCredential(string, []byte) error {
	return fmt.Errorf("MCP OAuth secure storage requires a normal macOS desktop build with cgo")
}

func platformLoadMCPOAuthCredential(string) ([]byte, error) {
	return nil, fmt.Errorf("MCP OAuth secure storage requires a normal macOS desktop build with cgo")
}

// Delete succeeds as a no-op: this build cannot store a credential, so none
// can exist, and an error here would abort the caller's config cleanup.
func platformDeleteMCPOAuthCredential(string) error {
	return errMCPOAuthCredentialNotFound
}

func platformSaveLocalEnvironmentCredential([]byte) error {
	return fmt.Errorf("local environment secure storage requires a normal macOS desktop build with cgo")
}

func platformLoadLocalEnvironmentCredential() ([]byte, error) {
	return nil, fmt.Errorf("local environment secure storage requires a normal macOS desktop build with cgo")
}

func platformDeleteLocalEnvironmentCredential() error {
	return errMCPOAuthCredentialNotFound
}
