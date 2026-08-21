package studio

import (
	"testing"
)

// Save() marshals exactly the struct it is handed — it never merges with the
// file already on disk — and `groups` carries omitempty. So a config rebuilt
// without the Groups field does not leave the stored groups alone: it deletes
// them, silently, while the in-memory copy still shows them. Eight call sites
// rebuilt the config that way, including saveConfigAsync, which runs whenever a
// turn bumps lastUsedAt. Any group died on the first message in any chat, and
// a project whose DelegationPolicy is "group" then refuses every caller forever
// with no group left to explain why.
//
// These assertions are on the RELOADED config, because reading s.config would
// pass even with the bug.
func groupsOnDisk(t *testing.T) int {
	t.Helper()
	return len(LoadConfig().Groups)
}

func studioWithAGroup(t *testing.T) *Studio {
	t.Helper()
	t.Setenv("GOKIN_CONFIG_DIR", t.TempDir())
	s := newStudioForTest(t)

	p := NewProject(ProjectConfig{ID: "pid", Name: "Alpha", Directory: t.TempDir()})
	p.studio = s
	s.projects[p.ID] = p
	s.config.Projects = []ProjectConfig{p.ToConfig()}

	if _, err := s.SaveProjectGroup(ProjectGroupConfig{
		Name:          "Backend",
		SharedContext: "postgres 16, k8s staging",
		Members:       []GroupMemberConfig{{ProjectID: "pid"}},
	}); err != nil {
		t.Fatalf("SaveProjectGroup: %v", err)
	}
	if got := groupsOnDisk(t); got != 1 {
		t.Fatalf("the group was not stored in the first place: %d on disk", got)
	}
	return s
}

func TestGroupsSurviveEveryConfigWritingPath(t *testing.T) {
	for name, mutate := range map[string]func(*testing.T, *Studio){
		"saveConfigAsync (runs on every turn)": func(t *testing.T, s *Studio) {
			s.saveConfigAsync()
			waitForAsyncConfigSave(t, s)
		},
		"AddProject": func(t *testing.T, s *Studio) {
			if _, err := s.AddProject("Beta", t.TempDir()); err != nil {
				t.Fatalf("AddProject: %v", err)
			}
		},
		// Shaped exactly like SettingsPage's payload: a settings block and
		// nothing else. Reading Groups off this argument would look correct in
		// a test that echoed the whole config back, and still erase them here.
		"UpdateSettings (frontend payload shape)": func(t *testing.T, s *Studio) {
			settings := s.config.Settings
			settings.DefaultBudgetUSD = 5
			if err := s.UpdateSettings(StudioConfig{Settings: settings}); err != nil {
				t.Fatalf("UpdateSettings: %v", err)
			}
		},
		"RemoveProject": func(t *testing.T, s *Studio) {
			if err := s.RemoveProject("pid"); err != nil {
				t.Fatalf("RemoveProject: %v", err)
			}
		},
	} {
		t.Run(name, func(t *testing.T) {
			s := studioWithAGroup(t)
			mutate(t, s)
			if got := groupsOnDisk(t); got != 1 {
				t.Fatalf("%s erased the stored groups: %d on disk, want 1", name, got)
			}
		})
	}
}

// The in-memory copy kept looking right while the file lost the group, which is
// why nothing surfaced until the next launch. Pin both halves together.
func TestGroupsOnDiskAgreeWithMemoryAfterAWrite(t *testing.T) {
	s := studioWithAGroup(t)
	s.saveConfigAsync()
	waitForAsyncConfigSave(t, s)

	inMemory := len(s.ListProjectGroups())
	onDisk := groupsOnDisk(t)
	if inMemory != onDisk {
		t.Fatalf("memory reports %d group(s), disk has %d — the divergence is invisible until restart",
			inMemory, onDisk)
	}
	reloaded := LoadConfig()
	if len(reloaded.Groups) != 1 || reloaded.Groups[0].SharedContext != "postgres 16, k8s staging" {
		t.Fatalf("the group's contents did not round-trip: %+v", reloaded.Groups)
	}
}

// saveConfigAsync writes on its own goroutine; configSaveMu is what it holds
// while doing so, so taking it is the wait.
func waitForAsyncConfigSave(t *testing.T, s *Studio) {
	t.Helper()
	s.configSaveMu.Lock()
	s.configSaveMu.Unlock() //nolint:staticcheck // acquiring is the synchronisation point
}
