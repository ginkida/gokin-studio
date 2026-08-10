package studio

import (
	"errors"
	"testing"
)

func TestQuickEntryWindowLifecycle(t *testing.T) {
	s := &Studio{}
	shown := 0
	hidden := []bool{}
	s.testQuickEntryWindowShow = func() error { shown++; return nil }
	s.testQuickEntryWindowHide = func(activate bool) error {
		hidden = append(hidden, activate)
		return nil
	}

	status, err := s.ShowQuickEntryWindow()
	if err != nil || !status.Supported || !status.Native || !status.Open {
		t.Fatalf("ShowQuickEntryWindow() = %+v, %v", status, err)
	}
	status, err = s.ShowQuickEntryWindow()
	if err != nil || !status.Open || shown != 2 {
		t.Fatalf("idempotent show = %+v, %v; calls=%d", status, err, shown)
	}
	if err := s.HideQuickEntryWindow(true); err != nil {
		t.Fatal(err)
	}
	if len(hidden) != 1 || !hidden[0] || s.quickEntryWindow {
		t.Fatalf("hide calls=%v open=%v", hidden, s.quickEntryWindow)
	}
	if err := s.HideQuickEntryWindow(false); err != nil || len(hidden) != 1 {
		t.Fatalf("second hide = %v, calls=%v", err, hidden)
	}
}

func TestQuickEntryWindowFailuresKeepRecoverableState(t *testing.T) {
	s := &Studio{}
	s.testQuickEntryWindowShow = func() error { return errors.New("show failed") }
	status, err := s.ShowQuickEntryWindow()
	if err == nil || status.Open || s.quickEntryWindow {
		t.Fatalf("failed show = %+v, %v; open=%v", status, err, s.quickEntryWindow)
	}

	s.testQuickEntryWindowShow = func() error { return nil }
	if _, err := s.ShowQuickEntryWindow(); err != nil {
		t.Fatal(err)
	}
	s.testQuickEntryWindowHide = func(bool) error { return errors.New("hide failed") }
	if err := s.HideQuickEntryWindow(false); err == nil || !s.quickEntryWindow {
		t.Fatalf("failed hide = %v; open=%v", err, s.quickEntryWindow)
	}
}
