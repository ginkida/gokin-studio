package studio

import (
	"fmt"
	"sync"
)

type wakeLease interface {
	Close() error
}

type WakeStatus struct {
	Supported      bool   `json:"supported"`
	Enabled        bool   `json:"enabled"`
	Active         bool   `json:"active"`
	ActiveRuns     int    `json:"activeRuns"`
	ScheduledTasks bool   `json:"scheduledTasks"`
	Error          string `json:"error,omitempty"`
}

type processWakeLease struct {
	once    sync.Once
	closeFn func() error
	err     error
}

func (l *processWakeLease) Close() error {
	if l == nil {
		return nil
	}
	l.once.Do(func() {
		if l.closeFn != nil {
			l.err = l.closeFn()
		}
	})
	return l.err
}

func (s *Studio) acquireWake(reason string) (wakeLease, error) {
	if s.testWakeAcquire != nil {
		return s.testWakeAcquire(reason)
	}
	return acquirePlatformWakeLease(reason)
}

func (s *Studio) wakeSupported() bool {
	return s.testWakeAcquire != nil || wakePlatformSupported()
}

func (s *Studio) beginWakeRun() func() {
	s.wakeMu.Lock()
	s.wakeRuns++
	s.reconcileWakeLocked()
	s.wakeMu.Unlock()
	var once sync.Once
	return func() {
		once.Do(func() {
			s.wakeMu.Lock()
			if s.wakeRuns > 0 {
				s.wakeRuns--
			}
			s.reconcileWakeLocked()
			s.wakeMu.Unlock()
		})
	}
}

func (s *Studio) setWakeEnabled(enabled bool) {
	s.wakeEnabled.Store(enabled)
	s.wakeMu.Lock()
	s.reconcileWakeLocked()
	s.wakeMu.Unlock()
}

func (s *Studio) reconcileWakeLocked() {
	wanted := s.wakeEnabled.Load() && (s.wakeRuns > 0 || s.wakeScheduled.Load())
	if !wanted {
		if s.wakeLease != nil {
			if err := s.wakeLease.Close(); err != nil {
				s.wakeError = err.Error()
			} else {
				s.wakeError = ""
			}
			s.wakeLease = nil
		}
		return
	}
	if s.wakeLease != nil {
		return
	}
	if !s.wakeSupported() {
		s.wakeError = "sleep inhibition is unavailable on this operating system"
		return
	}
	reason := "GLM/Kimi agent is running"
	if s.wakeScheduled.Load() {
		reason = "Gokin Studio has enabled scheduled tasks"
	}
	lease, err := s.acquireWake(reason)
	if err != nil {
		s.wakeError = err.Error()
		return
	}
	if lease == nil {
		s.wakeError = "sleep inhibitor returned no lease"
		return
	}
	s.wakeLease = lease
	s.wakeError = ""
}

func (s *Studio) refreshScheduledWakeNeed() error {
	scheduledTasksMu.Lock()
	tasks, err := loadScheduledTasksRaw()
	scheduledTasksMu.Unlock()
	if err != nil {
		return err
	}
	needed := false
	for _, task := range tasks {
		// Manual-only tasks never dispatch from the scheduler, so keeping the
		// machine awake for them would waste battery without enabling any work.
		// Archived projects keep their routines intact but are suspended until
		// restore, so they must not hold the machine awake either.
		if task.Enabled && task.Schedule != "manual" && !s.isProjectArchived(task.ProjectID) {
			needed = true
			break
		}
	}
	s.wakeScheduled.Store(needed)
	s.wakeMu.Lock()
	s.reconcileWakeLocked()
	s.wakeMu.Unlock()
	return nil
}

func (s *Studio) GetWakeStatus() WakeStatus {
	s.wakeMu.Lock()
	defer s.wakeMu.Unlock()
	return WakeStatus{
		Supported: s.wakeSupported(),
		Enabled:   s.wakeEnabled.Load(), Active: s.wakeLease != nil,
		ActiveRuns: s.wakeRuns, ScheduledTasks: s.wakeScheduled.Load(),
		Error: s.wakeError,
	}
}

func wakeLeaseCloseProcess(process interface {
	Kill() error
}, wait func() error) wakeLease {
	return &processWakeLease{closeFn: func() error {
		killErr := process.Kill()
		waitErr := wait()
		if killErr != nil {
			return fmt.Errorf("stop sleep inhibitor: %w", killErr)
		}
		if waitErr != nil {
			// A killed helper normally reports an exit error. The inhibitor is
			// nevertheless released, so do not turn normal teardown into an
			// alarming persistent status.
			return nil
		}
		return nil
	}}
}
