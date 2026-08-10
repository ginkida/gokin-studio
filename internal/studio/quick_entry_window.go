package studio

import "fmt"

// QuickEntryWindowStatus describes the compact native surface independently
// from the global-shortcut registration status. Native is true only when the
// main Wails WebView can be hosted in a dedicated OS panel without starting a
// second backend or duplicating application state.
type QuickEntryWindowStatus struct {
	Supported bool `json:"supported"`
	Native    bool `json:"native"`
	Open      bool `json:"open"`
}

// ShowQuickEntryWindow moves the existing application surface into a native
// floating panel where supported. Returning Supported=false is intentional:
// the frontend then uses its geometry-preserving single-window fallback.
func (s *Studio) ShowQuickEntryWindow() (QuickEntryWindowStatus, error) {
	s.quickEntryWindowMu.Lock()
	defer s.quickEntryWindowMu.Unlock()

	supported := nativeQuickEntryWindowSupported() || s.testQuickEntryWindowShow != nil
	if !supported {
		return QuickEntryWindowStatus{}, nil
	}
	if s.quickEntryWindow {
		if err := showNativeOrTestQuickEntryWindow(s); err != nil {
			return QuickEntryWindowStatus{Supported: true, Native: true, Open: true}, err
		}
		return QuickEntryWindowStatus{Supported: true, Native: true, Open: true}, nil
	}
	if err := showNativeOrTestQuickEntryWindow(s); err != nil {
		return QuickEntryWindowStatus{Supported: true, Native: true}, fmt.Errorf("show native Quick Entry window: %w", err)
	}
	s.quickEntryWindow = true
	return QuickEntryWindowStatus{Supported: true, Native: true, Open: true}, nil
}

func showNativeOrTestQuickEntryWindow(s *Studio) error {
	if s.testQuickEntryWindowShow != nil {
		return s.testQuickEntryWindowShow()
	}
	return showNativeQuickEntryWindow()
}

// HideQuickEntryWindow restores the Wails surface to its original window.
// activateStudio controls whether that window becomes foreground (opening a
// chat) or merely returns to its previous visibility (dismissing the panel).
func (s *Studio) HideQuickEntryWindow(activateStudio bool) error {
	s.quickEntryWindowMu.Lock()
	defer s.quickEntryWindowMu.Unlock()
	if !s.quickEntryWindow {
		return nil
	}
	var err error
	if s.testQuickEntryWindowHide != nil {
		err = s.testQuickEntryWindowHide(activateStudio)
	} else {
		err = hideNativeQuickEntryWindow(activateStudio)
	}
	if err != nil {
		return fmt.Errorf("hide native Quick Entry window: %w", err)
	}
	s.quickEntryWindow = false
	return nil
}

func (s *Studio) closeQuickEntryWindowForShutdown() {
	if err := s.HideQuickEntryWindow(false); err != nil {
		s.LogEvent("warn", "quick-entry", err.Error())
	}
}
