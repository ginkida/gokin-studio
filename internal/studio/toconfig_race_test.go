package studio

import (
	"sync"
	"testing"
)

// TestToConfig_NoRaceWithSetProjectProvider is a regression guard for the
// iter 980+ data race fix in ToConfig.
//
// Pre-fix: ToConfig() read p.Provider / p.Model / p.Name / ... without
// holding p.mu. SetProjectProvider wrote p.Provider then p.Model under
// p.mu.Lock(). saveConfigAsync (which runs on every agent turn via the
// goroutine in p.SendMessage) called p.ToConfig() — could observe a torn
// state where Provider was updated but Model was still the old value.
// Manifested as "I switched provider but after restart the model went
// back to the previous one" because the YAML on disk was inconsistent.
//
// Post-fix: ToConfig() takes p.mu.RLock() internally. SetProjectProvider's
// p.mu.Lock() blocks ToConfig observation until both fields are updated.
//
// This test is meaningful only with `go test -race`. Without the race
// detector, the worst case is a torn read that may happen to land on a
// consistent value — the test would pass spuriously. CI runs with -race.
func TestToConfig_NoRaceWithSetProjectProvider(t *testing.T) {
	s := newStudioForTest(t)
	info := addTestProject(t, s, "race-test")

	const iters = 200
	var wg sync.WaitGroup

	// Writer: alternate between two providers as fast as it can.
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < iters; i++ {
			provider := "glm"
			model := "glm-5.2"
			if i%2 == 1 {
				provider = "kimi"
				model = "k3"
			}
			_ = s.SetProjectProvider(info.ID, provider, model)
		}
	}()

	// Reader: call ToConfig (the path saveConfig / saveConfigAsync use)
	// in parallel. With race detector enabled this fails the moment the
	// reader observes the writer's mid-update state.
	wg.Add(1)
	go func() {
		defer wg.Done()
		s.mu.RLock()
		p := s.projects[info.ID]
		s.mu.RUnlock()
		for i := 0; i < iters; i++ {
			cfg := p.ToConfig()
			// Also assert the (provider, model) pair is internally
			// consistent — if the reader sees "kimi" + "glm-5.2" the
			// torn read happened and the consistency invariant broke.
			// SetProjectProvider currently writes both fields together
			// under one lock, so a fully-locked reader always sees
			// matching pairs. (The project's initial default model is
			// glm-5.2, so the glm branch accepts that too.)
			provider := cfg.Provider
			model := cfg.Model
			switch provider {
			case "glm":
				if model != "" && model != "glm-5.2" {
					t.Errorf("torn read: provider=glm but model=%q", model)
					return
				}
			case "kimi":
				if model != "" && model != "k3" {
					t.Errorf("torn read: provider=kimi but model=%q", model)
					return
				}
			}
		}
	}()

	wg.Wait()
}

// TestToConfig_NoRaceWithRename — same pattern for the Name field, the
// other most-mutated string under p.mu. Catches future regressions where
// someone accidentally removes the RLock from ToConfig.
func TestToConfig_NoRaceWithRename(t *testing.T) {
	s := newStudioForTest(t)
	info := addTestProject(t, s, "race-rename")

	const iters = 200
	var wg sync.WaitGroup

	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < iters; i++ {
			name := "alpha"
			if i%2 == 1 {
				name = "beta"
			}
			_ = s.RenameProject(info.ID, name)
		}
	}()

	wg.Add(1)
	go func() {
		defer wg.Done()
		s.mu.RLock()
		p := s.projects[info.ID]
		s.mu.RUnlock()
		for i := 0; i < iters; i++ {
			_ = p.ToConfig()
		}
	}()

	wg.Wait()
}

// TestToConfig_SnapshotIsConsistent — non-race-detector smoke test that
// ToConfig returns a value reflecting one specific point in time.
// Important because callers serialise the result to YAML — any drift
// between fields would persist as a corrupt config.
func TestToConfig_SnapshotIsConsistent(t *testing.T) {
	s := newStudioForTest(t)
	info := addTestProject(t, s, "snapshot")

	if err := s.SetProjectProvider(info.ID, "kimi", "kimi-for-coding"); err != nil {
		t.Fatalf("SetProjectProvider: %v", err)
	}
	if err := s.RenameProject(info.ID, "renamed"); err != nil {
		t.Fatalf("RenameProject: %v", err)
	}

	s.mu.RLock()
	p := s.projects[info.ID]
	s.mu.RUnlock()

	cfg := p.ToConfig()
	if cfg.Provider != "kimi" {
		t.Errorf("expected provider=kimi, got %q", cfg.Provider)
	}
	if cfg.Model != "kimi-for-coding" {
		t.Errorf("expected model=kimi-for-coding, got %q", cfg.Model)
	}
	if cfg.Name != "renamed" {
		t.Errorf("expected name=renamed, got %q", cfg.Name)
	}
	if cfg.ID != info.ID {
		t.Errorf("expected id=%q, got %q", info.ID, cfg.ID)
	}
}
