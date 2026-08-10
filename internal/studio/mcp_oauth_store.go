package studio

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
)

const (
	mcpOAuthCredentialService  = "com.gokin-studio.mcp.oauth"
	maxMCPOAuthCredentialBytes = 64 << 10
)

var errMCPOAuthCredentialNotFound = errors.New("MCP OAuth credential not found")

func mcpOAuthCredentialKey(name string) string {
	sum := sha256.Sum256([]byte(strings.ToLower(strings.TrimSpace(name))))
	return "connector-" + hex.EncodeToString(sum[:])
}

func saveMCPOAuthCredential(name string, data []byte) error {
	if strings.TrimSpace(name) == "" {
		return fmt.Errorf("connector name cannot be empty")
	}
	if len(data) == 0 || len(data) > maxMCPOAuthCredentialBytes {
		return fmt.Errorf("OAuth credential must be between 1 byte and %d KiB", maxMCPOAuthCredentialBytes>>10)
	}
	return platformSaveMCPOAuthCredential(mcpOAuthCredentialKey(name), data)
}

func loadMCPOAuthCredential(name string) ([]byte, error) {
	if strings.TrimSpace(name) == "" {
		return nil, fmt.Errorf("connector name cannot be empty")
	}
	data, err := platformLoadMCPOAuthCredential(mcpOAuthCredentialKey(name))
	if err != nil {
		return nil, err
	}
	if len(data) == 0 || len(data) > maxMCPOAuthCredentialBytes {
		return nil, fmt.Errorf("stored OAuth credential has invalid size")
	}
	return data, nil
}

func deleteMCPOAuthCredential(name string) error {
	if strings.TrimSpace(name) == "" {
		return fmt.Errorf("connector name cannot be empty")
	}
	err := platformDeleteMCPOAuthCredential(mcpOAuthCredentialKey(name))
	if errors.Is(err, errMCPOAuthCredentialNotFound) {
		return nil
	}
	return err
}
