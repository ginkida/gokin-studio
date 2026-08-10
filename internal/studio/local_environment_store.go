package studio

import (
	"errors"
	"fmt"
)

const (
	localEnvironmentCredentialService = "com.gokin-studio.local-environment"
	localEnvironmentCredentialAccount = "global"
)

func saveLocalEnvironmentCredential(data []byte) error {
	if len(data) == 0 || len(data) > maxMCPOAuthCredentialBytes {
		return fmt.Errorf("local environment payload must be between 1 byte and %d KiB", maxMCPOAuthCredentialBytes>>10)
	}
	if err := platformSaveLocalEnvironmentCredential(data); err != nil {
		return fmt.Errorf("save local environment in secure storage: %w", err)
	}
	return nil
}

func loadLocalEnvironmentCredential() ([]byte, error) {
	data, err := platformLoadLocalEnvironmentCredential()
	if err != nil {
		return nil, err
	}
	return data, nil
}

func deleteLocalEnvironmentCredential() error {
	if err := platformDeleteLocalEnvironmentCredential(); err != nil && !errors.Is(err, errMCPOAuthCredentialNotFound) {
		return fmt.Errorf("delete local environment from secure storage: %w", err)
	}
	return nil
}
