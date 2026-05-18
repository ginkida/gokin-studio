package studio

import (
	"testing"
)

// TestSetProjectPinned validates the pinned flag round-trips through Info() +
// ToConfig() + NewProject() so it survives a restart.
func TestSetProjectPinned(t *testing.T) {
	s := newStudioForTest(t)
	info := addTestProject(t, s, "PinnedProj")

	// Default is unpinned.
	if info.Pinned {
		t.Errorf("new project Pinned = true, want false")
	}

	// Unknown project rejected.
	if err := s.SetProjectPinned("no-such-id", true); err == nil {
		t.Error("expected error for unknown project, got nil")
	}

	// Pin → Info reports pinned=true.
	if err := s.SetProjectPinned(info.ID, true); err != nil {
		t.Fatalf("SetProjectPinned(true): %v", err)
	}
	got, err := s.GetProject(info.ID)
	if err != nil {
		t.Fatalf("GetProject: %v", err)
	}
	if !got.Pinned {
		t.Errorf("Pinned after pin = false, want true")
	}

	// Unpin → Info reports pinned=false again.
	if err := s.SetProjectPinned(info.ID, false); err != nil {
		t.Fatalf("SetProjectPinned(false): %v", err)
	}
	got, _ = s.GetProject(info.ID)
	if got.Pinned {
		t.Errorf("Pinned after unpin = true, want false")
	}
}

// TestSetProjectPinned_PersistsToConfig verifies that Pinned is included in
// ToConfig() so it survives a save+reload cycle.
func TestSetProjectPinned_PersistsToConfig(t *testing.T) {
	s := newStudioForTest(t)
	info := addTestProject(t, s, "PersistPin")

	if err := s.SetProjectPinned(info.ID, true); err != nil {
		t.Fatalf("SetProjectPinned: %v", err)
	}

	s.mu.RLock()
	p := s.projects[info.ID]
	s.mu.RUnlock()
	cfg := p.ToConfig()
	if !cfg.Pinned {
		t.Errorf("ToConfig.Pinned = false, want true")
	}

	// Round-trip through NewProject.
	p2 := NewProject(cfg)
	if !p2.Pinned {
		t.Errorf("NewProject.Pinned = false after restart-style round-trip, want true")
	}
}
