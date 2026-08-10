package studio

import (
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/ginkida/gokin-studio/internal/engine/security"
)

const localEnvironmentStorageVersion = 1

type LocalEnvironmentVariable struct {
	Name string `json:"name"`
}

type LocalEnvironmentInput struct {
	Name         string `json:"name"`
	Value        string `json:"value"`
	KeepExisting bool   `json:"keepExisting"`
}

type LocalEnvironmentStatus struct {
	Variables []LocalEnvironmentVariable `json:"variables"`
	Error     string                     `json:"error,omitempty"`
}

type localEnvironmentDiskPayload struct {
	Version int               `json:"version"`
	Values  map[string]string `json:"values"`
}

func localEnvironmentVariables(values map[string]string) []LocalEnvironmentVariable {
	names := make([]string, 0, len(values))
	for name := range values {
		names = append(names, name)
	}
	sort.Strings(names)
	result := make([]LocalEnvironmentVariable, 0, len(names))
	for _, name := range names {
		result = append(result, LocalEnvironmentVariable{Name: name})
	}
	return result
}

func (s *Studio) localEnvironmentStatusLocked() LocalEnvironmentStatus {
	return LocalEnvironmentStatus{
		Variables: localEnvironmentVariables(security.WorkspaceEnvironmentSnapshot()),
		Error:     s.localEnvironmentError,
	}
}

// ListLocalEnvironment intentionally exposes names only. Stored values never
// cross the Wails bridge after being committed to secure storage.
func (s *Studio) ListLocalEnvironment() LocalEnvironmentStatus {
	s.localEnvironmentMu.Lock()
	defer s.localEnvironmentMu.Unlock()
	return s.localEnvironmentStatusLocked()
}

// SaveLocalEnvironment replaces the complete desired list. Existing rows may
// use KeepExisting so the frontend can save without ever receiving plaintext.
func (s *Studio) SaveLocalEnvironment(inputs []LocalEnvironmentInput) (LocalEnvironmentStatus, error) {
	s.localEnvironmentMu.Lock()
	defer s.localEnvironmentMu.Unlock()

	current := security.WorkspaceEnvironmentSnapshot()
	desired := make(map[string]string, len(inputs))
	seen := make(map[string]string, len(inputs))
	for _, input := range inputs {
		upper := strings.ToUpper(input.Name)
		if previous, exists := seen[upper]; exists {
			return s.localEnvironmentStatusLocked(), fmt.Errorf("environment variable names %q and %q conflict", previous, input.Name)
		}
		seen[upper] = input.Name
		if input.KeepExisting {
			if input.Value != "" {
				return s.localEnvironmentStatusLocked(), fmt.Errorf("kept environment variable %q cannot include a replacement value", input.Name)
			}
			value, exists := current[input.Name]
			if !exists {
				return s.localEnvironmentStatusLocked(), fmt.Errorf("environment variable %q no longer exists; enter its value again", input.Name)
			}
			desired[input.Name] = value
			continue
		}
		desired[input.Name] = input.Value
	}
	if err := security.ValidateWorkspaceEnvironment(desired); err != nil {
		return s.localEnvironmentStatusLocked(), err
	}

	if len(desired) == 0 {
		deleteCredential := s.testLocalEnvironmentDelete
		if deleteCredential == nil {
			deleteCredential = deleteLocalEnvironmentCredential
		}
		if err := deleteCredential(); err != nil {
			return s.localEnvironmentStatusLocked(), err
		}
	} else {
		payload, err := json.Marshal(localEnvironmentDiskPayload{Version: localEnvironmentStorageVersion, Values: desired})
		if err != nil {
			return s.localEnvironmentStatusLocked(), fmt.Errorf("encode local environment: %w", err)
		}
		saveCredential := s.testLocalEnvironmentSave
		if saveCredential == nil {
			saveCredential = saveLocalEnvironmentCredential
		}
		if err := saveCredential(payload); err != nil {
			return s.localEnvironmentStatusLocked(), err
		}
	}
	if err := security.SetWorkspaceEnvironment(desired); err != nil {
		return s.localEnvironmentStatusLocked(), err
	}
	s.localEnvironmentError = ""
	s.logLocalEnvironmentChange(current, desired)
	return s.localEnvironmentStatusLocked(), nil
}

func (s *Studio) loadLocalEnvironment() error {
	s.localEnvironmentMu.Lock()
	defer s.localEnvironmentMu.Unlock()
	loadCredential := s.testLocalEnvironmentLoad
	if loadCredential == nil {
		loadCredential = loadLocalEnvironmentCredential
	}
	data, err := loadCredential()
	if errors.Is(err, errMCPOAuthCredentialNotFound) {
		s.localEnvironmentError = ""
		return security.SetWorkspaceEnvironment(nil)
	}
	if err != nil {
		s.localEnvironmentError = err.Error()
		_ = security.SetWorkspaceEnvironment(nil)
		return err
	}
	var payload localEnvironmentDiskPayload
	if err := json.Unmarshal(data, &payload); err != nil {
		err = fmt.Errorf("decode secure local environment: %w", err)
		s.localEnvironmentError = err.Error()
		_ = security.SetWorkspaceEnvironment(nil)
		return err
	}
	if payload.Version != localEnvironmentStorageVersion {
		err = fmt.Errorf("unsupported secure local environment version %d", payload.Version)
		s.localEnvironmentError = err.Error()
		_ = security.SetWorkspaceEnvironment(nil)
		return err
	}
	if err := security.SetWorkspaceEnvironment(payload.Values); err != nil {
		err = fmt.Errorf("invalid secure local environment: %w", err)
		s.localEnvironmentError = err.Error()
		_ = security.SetWorkspaceEnvironment(nil)
		return err
	}
	s.localEnvironmentError = ""
	return nil
}

func (s *Studio) logLocalEnvironmentChange(before, after map[string]string) {
	added, updated, removed := make([]string, 0), make([]string, 0), make([]string, 0)
	for name, value := range after {
		previous, exists := before[name]
		if !exists {
			added = append(added, name)
		} else if previous != value {
			updated = append(updated, name)
		}
	}
	for name := range before {
		if _, exists := after[name]; !exists {
			removed = append(removed, name)
		}
	}
	for _, names := range [][]string{added, updated, removed} {
		sort.Strings(names)
	}
	parts := make([]string, 0, 3)
	if len(added) > 0 {
		parts = append(parts, "added "+strings.Join(added, ", "))
	}
	if len(updated) > 0 {
		parts = append(parts, "updated "+strings.Join(updated, ", "))
	}
	if len(removed) > 0 {
		parts = append(parts, "removed "+strings.Join(removed, ", "))
	}
	if len(parts) > 0 {
		s.LogEvent("info", "local-environment", strings.Join(parts, "; "))
	}
}
