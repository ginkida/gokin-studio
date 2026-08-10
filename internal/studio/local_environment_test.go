package studio

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/ginkida/gokin-studio/internal/engine/security"
)

func localEnvironmentTestStudio(t *testing.T) (*Studio, *[]byte) {
	t.Helper()
	previous := security.WorkspaceEnvironmentSnapshot()
	t.Cleanup(func() { _ = security.SetWorkspaceEnvironment(previous) })
	if err := security.SetWorkspaceEnvironment(nil); err != nil {
		t.Fatal(err)
	}
	s := NewStudio()
	var stored []byte
	s.testLocalEnvironmentSave = func(data []byte) error {
		stored = append(stored[:0], data...)
		return nil
	}
	s.testLocalEnvironmentLoad = func() ([]byte, error) {
		if len(stored) == 0 {
			return nil, errMCPOAuthCredentialNotFound
		}
		return append([]byte(nil), stored...), nil
	}
	s.testLocalEnvironmentDelete = func() error {
		stored = nil
		return nil
	}
	return s, &stored
}

func TestLocalEnvironmentSaveNeverReturnsOrLogsValues(t *testing.T) {
	s, stored := localEnvironmentTestStudio(t)
	const secret = "secret-value-that-must-not-leak"
	status, err := s.SaveLocalEnvironment([]LocalEnvironmentInput{{Name: "SERVICE_TOKEN", Value: secret}})
	if err != nil {
		t.Fatal(err)
	}
	if len(status.Variables) != 1 || status.Variables[0].Name != "SERVICE_TOKEN" {
		t.Fatalf("unexpected names-only status: %#v", status)
	}
	statusJSON, _ := json.Marshal(status)
	if strings.Contains(string(statusJSON), secret) {
		t.Fatal("plaintext value crossed the frontend status boundary")
	}
	if !strings.Contains(string(*stored), secret) {
		t.Fatal("test secure-store seam did not receive the encoded value")
	}
	for _, entry := range s.eventLog.Snapshot() {
		if strings.Contains(entry.Message, secret) {
			t.Fatal("plaintext value leaked into the event log")
		}
	}
}

func TestLocalEnvironmentPreserveReplaceAndRemove(t *testing.T) {
	s, stored := localEnvironmentTestStudio(t)
	if _, err := s.SaveLocalEnvironment([]LocalEnvironmentInput{
		{Name: "KEEP_ME", Value: "original"},
		{Name: "REPLACE_ME", Value: "old"},
	}); err != nil {
		t.Fatal(err)
	}
	status, err := s.SaveLocalEnvironment([]LocalEnvironmentInput{
		{Name: "KEEP_ME", KeepExisting: true},
		{Name: "REPLACE_ME", Value: "new"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(status.Variables) != 2 {
		t.Fatalf("unexpected status: %#v", status)
	}
	values := security.WorkspaceEnvironmentSnapshot()
	if values["KEEP_ME"] != "original" || values["REPLACE_ME"] != "new" {
		t.Fatalf("unexpected active values: %#v", values)
	}
	if _, err := s.SaveLocalEnvironment(nil); err != nil {
		t.Fatal(err)
	}
	if len(*stored) != 0 || len(security.WorkspaceEnvironmentSnapshot()) != 0 {
		t.Fatal("removing every row did not delete secure and active state")
	}
}

func TestLocalEnvironmentPersistenceFailureKeepsActiveState(t *testing.T) {
	s, _ := localEnvironmentTestStudio(t)
	if _, err := s.SaveLocalEnvironment([]LocalEnvironmentInput{{Name: "STABLE", Value: "before"}}); err != nil {
		t.Fatal(err)
	}
	s.testLocalEnvironmentSave = func([]byte) error { return errors.New("secure store unavailable") }
	if _, err := s.SaveLocalEnvironment([]LocalEnvironmentInput{{Name: "STABLE", Value: "after"}}); err == nil {
		t.Fatal("expected secure-store failure")
	}
	if got := security.WorkspaceEnvironmentSnapshot()["STABLE"]; got != "before" {
		t.Fatalf("active value changed after failed persistence: %q", got)
	}
}

func TestLocalEnvironmentRejectsUnsafeAndStaleInputs(t *testing.T) {
	s, _ := localEnvironmentTestStudio(t)
	for _, input := range [][]LocalEnvironmentInput{
		{{Name: "PATH", Value: "/tmp"}},
		{{Name: "BAD-NAME", Value: "value"}},
		{{Name: "Token", Value: "one"}, {Name: "TOKEN", Value: "two"}},
		{{Name: "MISSING", KeepExisting: true}},
	} {
		if _, err := s.SaveLocalEnvironment(input); err == nil {
			t.Fatalf("expected input to be rejected: %#v", input)
		}
	}
}

func TestLoadLocalEnvironmentActivatesValidPayloadAndFailsClosed(t *testing.T) {
	s, stored := localEnvironmentTestStudio(t)
	payload, err := json.Marshal(localEnvironmentDiskPayload{
		Version: localEnvironmentStorageVersion,
		Values:  map[string]string{"LOADED_VALUE": "ready"},
	})
	if err != nil {
		t.Fatal(err)
	}
	*stored = payload
	if err := s.loadLocalEnvironment(); err != nil {
		t.Fatal(err)
	}
	if got := security.WorkspaceEnvironmentSnapshot()["LOADED_VALUE"]; got != "ready" {
		t.Fatalf("loaded value = %q", got)
	}
	*stored = []byte("not json")
	if err := s.loadLocalEnvironment(); err == nil {
		t.Fatal("expected malformed secure payload to fail")
	}
	if len(security.WorkspaceEnvironmentSnapshot()) != 0 {
		t.Fatal("malformed secure payload left environment active")
	}
	if !strings.Contains(s.ListLocalEnvironment().Error, "decode secure local environment") {
		t.Fatalf("storage error was not surfaced: %#v", s.ListLocalEnvironment())
	}
}

func TestLocalEnvironmentLimitsSerializedPayload(t *testing.T) {
	s, _ := localEnvironmentTestStudio(t)
	inputs := make([]LocalEnvironmentInput, security.MaxWorkspaceEnvironmentVariables+1)
	for index := range inputs {
		inputs[index] = LocalEnvironmentInput{Name: fmt.Sprintf("VAR_%d", index), Value: "value"}
	}
	if _, err := s.SaveLocalEnvironment(inputs); err == nil {
		t.Fatal("expected variable-count limit")
	}
}
