package studio

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeArtifactForVersionTest(t *testing.T, dir, name, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestArtifactVersionSnapshotDeduplicatesAndRestores(t *testing.T) {
	withTempConfigDir(t)
	s := newStudioForTest(t)
	info := addTestProject(t, s, "Versioned artifacts")
	projectDir := s.projects[info.ID].Directory

	writeArtifactForVersionTest(t, projectDir, "dashboard.html", "<h1>Version one</h1>")
	first, err := s.SaveArtifactVersion(info.ID, "dashboard.html")
	if err != nil {
		t.Fatal(err)
	}
	duplicate, err := s.SaveArtifactVersion(info.ID, "dashboard.html")
	if err != nil {
		t.Fatal(err)
	}
	if duplicate.ID != first.ID {
		t.Fatalf("unchanged snapshot created a duplicate: first=%q duplicate=%q", first.ID, duplicate.ID)
	}

	writeArtifactForVersionTest(t, projectDir, "dashboard.html", "<h1>Version two</h1>")
	second, err := s.SaveArtifactVersion(info.ID, "dashboard.html")
	if err != nil {
		t.Fatal(err)
	}
	if second.ID == first.ID || second.Digest == first.Digest {
		t.Fatalf("changed snapshot was not distinct: first=%#v second=%#v", first, second)
	}
	versions, err := s.ListArtifactVersions(info.ID, "dashboard.html")
	if err != nil || len(versions) != 2 || versions[0].ID != second.ID {
		t.Fatalf("version list = %#v, %v", versions, err)
	}
	old, err := s.ReadArtifactVersion(info.ID, "dashboard.html", first.ID)
	if err != nil || old.Content != "<h1>Version one</h1>" {
		t.Fatalf("old version = %#v, %v", old, err)
	}

	restored, err := s.RestoreArtifactVersion(info.ID, "dashboard.html", first.ID)
	if err != nil {
		t.Fatal(err)
	}
	if restored.Content != "<h1>Version one</h1>" {
		t.Fatalf("restored document = %#v", restored)
	}
	onDisk, err := os.ReadFile(filepath.Join(projectDir, "dashboard.html"))
	if err != nil || string(onDisk) != restored.Content {
		t.Fatalf("restored disk content = %q, %v", onDisk, err)
	}
	versions, err = s.ListArtifactVersions(info.ID, "dashboard.html")
	if err != nil || len(versions) != 3 || versions[0].Digest != first.Digest {
		t.Fatalf("restore was not recorded as a new revision: %#v, %v", versions, err)
	}
}

func TestArtifactVersionHistoryIsBoundedAndCanOutliveSource(t *testing.T) {
	withTempConfigDir(t)
	s := newStudioForTest(t)
	info := addTestProject(t, s, "Bounded artifacts")
	projectDir := s.projects[info.ID].Directory
	path := "timeline.svg"

	var oldestID string
	for i := 0; i < artifactVersionsPerArtifact+3; i++ {
		writeArtifactForVersionTest(t, projectDir, path, "<svg><text>"+strings.Repeat("x", i+1)+"</text></svg>")
		version, err := s.SaveArtifactVersion(info.ID, path)
		if err != nil {
			t.Fatal(err)
		}
		if i == 0 {
			oldestID = version.ID
		}
	}
	versions, err := s.ListArtifactVersions(info.ID, path)
	if err != nil {
		t.Fatal(err)
	}
	if len(versions) != artifactVersionsPerArtifact {
		t.Fatalf("retained %d versions, want %d", len(versions), artifactVersionsPerArtifact)
	}
	if _, err := s.ReadArtifactVersion(info.ID, path, oldestID); err == nil {
		t.Fatal("oldest pruned version remained readable")
	}
	if err := os.Remove(filepath.Join(projectDir, path)); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ListArtifactVersions(info.ID, path); err != nil {
		t.Fatalf("history should remain available after source deletion: %v", err)
	}
	if _, err := s.RestoreArtifactVersion(info.ID, path, versions[0].ID); err != nil {
		t.Fatalf("restore should recreate a deleted source: %v", err)
	}
}

func TestArtifactVersionStorageRejectsSymlinkRedirection(t *testing.T) {
	withTempConfigDir(t)
	s := newStudioForTest(t)
	info := addTestProject(t, s, "Private artifact storage")
	projectDir := s.projects[info.ID].Directory
	writeArtifactForVersionTest(t, projectDir, "private.html", "<p>private</p>")

	outside := t.TempDir()
	storage := filepath.Join(configDir(), "artifact_versions")
	if err := os.Symlink(outside, storage); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if _, err := s.SaveArtifactVersion(info.ID, "private.html"); err == nil ||
		!strings.Contains(err.Error(), "symlink") {
		t.Fatalf("symlink storage error = %v", err)
	}
	entries, err := os.ReadDir(outside)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("version storage escaped through symlink: %#v", entries)
	}
}

func TestArtifactVersionRejectsUnsupportedAndUnknownVersion(t *testing.T) {
	withTempConfigDir(t)
	s := newStudioForTest(t)
	info := addTestProject(t, s, "Artifact validation")
	projectDir := s.projects[info.ID].Directory
	writeArtifactForVersionTest(t, projectDir, "notes.txt", "not an artifact")
	if _, err := s.SaveArtifactVersion(info.ID, "notes.txt"); err == nil {
		t.Fatal("saved an unsupported artifact type")
	}
	if _, err := s.ReadArtifactVersion(info.ID, "missing.html", "unknown"); err == nil {
		t.Fatal("read an unknown artifact version")
	}
}

func TestArtifactRestoreRejectsWhileProjectAgentIsActive(t *testing.T) {
	withTempConfigDir(t)
	s := newStudioForTest(t)
	info := addTestProject(t, s, "Active artifact")
	project := s.projects[info.ID]
	writeArtifactForVersionTest(t, project.Directory, "active.html", "<p>saved</p>")
	version, err := s.SaveArtifactVersion(info.ID, "active.html")
	if err != nil {
		t.Fatal(err)
	}
	writeArtifactForVersionTest(t, project.Directory, "active.html", "<p>working</p>")
	session := project.GetSession("default")
	session.mu.Lock()
	session.active = true
	session.mu.Unlock()
	defer func() {
		session.mu.Lock()
		session.active = false
		session.mu.Unlock()
	}()

	if _, err := s.RestoreArtifactVersion(info.ID, "active.html", version.ID); err == nil ||
		!strings.Contains(err.Error(), "agent is running") {
		t.Fatalf("active restore error = %v", err)
	}
	data, err := os.ReadFile(filepath.Join(project.Directory, "active.html"))
	if err != nil || string(data) != "<p>working</p>" {
		t.Fatalf("active artifact was overwritten: %q, %v", data, err)
	}
}

func TestRemoveProjectDeletesPrivateArtifactVersions(t *testing.T) {
	withTempConfigDir(t)
	s := newStudioForTest(t)
	info := addTestProject(t, s, "Disposable artifact")
	project := s.projects[info.ID]
	writeArtifactForVersionTest(t, project.Directory, "delete-me.html", "<p>sensitive</p>")
	if _, err := s.SaveArtifactVersion(info.ID, "delete-me.html"); err != nil {
		t.Fatal(err)
	}
	versionDir := filepath.Join(configDir(), "artifact_versions", safeStorageKey(info.ID))
	if _, err := os.Stat(versionDir); err != nil {
		t.Fatalf("version directory was not created: %v", err)
	}
	if err := s.RemoveProject(info.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(versionDir); !os.IsNotExist(err) {
		t.Fatalf("artifact versions survived project removal: %v", err)
	}
}

func TestArtifactRestoreBarrierRejectsNewAgentTurn(t *testing.T) {
	mc := &mockClient{responses: []mockResp{{text: "must not run"}}}
	project, recorder := newTestProject(t, mc, nil)
	project.mu.Lock()
	project.artifactRestoreActive = true
	project.mu.Unlock()

	runAgent(project, "edit the artifact")

	mc.mu.Lock()
	callCount := mc.callCount
	mc.mu.Unlock()
	if callCount != 0 {
		t.Fatalf("provider called %d times while artifact restore barrier was active", callCount)
	}
	errors := recorder.find(EventChatError)
	if len(errors) != 1 || !strings.Contains(errors[0].data.(ChatTextEvent).Text, "being restored") {
		t.Fatalf("restore barrier errors = %#v", errors)
	}
}
