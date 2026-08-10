//go:build !darwin && !windows

package studio

import "fmt"

func platformSaveMCPOAuthCredential(string, []byte) error {
	return fmt.Errorf("MCP OAuth secure storage is currently supported on macOS and Windows")
}

func platformLoadMCPOAuthCredential(string) ([]byte, error) {
	return nil, fmt.Errorf("MCP OAuth secure storage is currently supported on macOS and Windows")
}

// Deleting is a successful no-op here. Saving is impossible on this platform,
// so no credential can exist to remove — and returning an error instead would
// abort RemoveMCPServer / DisconnectMCPServerOAuth before they write the
// pruned config, stranding the connector with no way to delete it from the UI.
func platformDeleteMCPOAuthCredential(string) error {
	return errMCPOAuthCredentialNotFound
}

func platformSaveLocalEnvironmentCredential([]byte) error {
	return fmt.Errorf("local environment secure storage is currently supported on macOS and Windows")
}

func platformLoadLocalEnvironmentCredential() ([]byte, error) {
	return nil, fmt.Errorf("local environment secure storage is currently supported on macOS and Windows")
}

// Same reasoning as platformDeleteMCPOAuthCredential: nothing can be stored on
// this platform, so a clear must not fail and block the caller's own cleanup.
func platformDeleteLocalEnvironmentCredential() error {
	return errMCPOAuthCredentialNotFound
}
