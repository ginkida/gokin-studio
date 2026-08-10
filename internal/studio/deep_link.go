package studio

import (
	"crypto/sha256"
	"fmt"
	"net/url"
	"strings"
	"time"
	"unicode/utf8"

	wailsRuntime "github.com/wailsapp/wails/v2/pkg/runtime"
)

const (
	deepLinkMaxURLBytes    = 32 << 10
	deepLinkMaxPromptBytes = 14 << 10
	deepLinkMaxIDBytes     = 128
	deepLinkPendingMax     = 16
	deepLinkDedupeWindow   = 2 * time.Second
)

// ParseDeepLink accepts only documented, local navigation routes. Unknown
// parameters fail closed so a typo cannot silently navigate somewhere else.
// Prompt text is never interpreted as a send instruction.
func ParseDeepLink(raw string) (DeepLinkEvent, error) {
	var result DeepLinkEvent
	if raw == "" || len(raw) > deepLinkMaxURLBytes || !utf8.ValidString(raw) {
		return result, fmt.Errorf("invalid deep link")
	}
	parsed, err := url.Parse(raw)
	if err != nil || !strings.EqualFold(parsed.Scheme, "gokin") || !strings.EqualFold(parsed.Host, "studio") {
		return result, fmt.Errorf("deep link must use gokin://studio")
	}
	if parsed.User != nil || parsed.Port() != "" || parsed.Fragment != "" || parsed.Opaque != "" {
		return result, fmt.Errorf("deep link contains unsupported authority or fragment")
	}
	query, err := url.ParseQuery(parsed.RawQuery)
	if err != nil {
		return result, fmt.Errorf("invalid deep-link query")
	}
	for key, values := range query {
		if len(values) != 1 {
			return result, fmt.Errorf("deep-link parameter %q must appear once", key)
		}
		if key != "q" && key != "project" && key != "view" {
			return result, fmt.Errorf("unsupported deep-link parameter %q", key)
		}
	}
	prompt := query.Get("q")
	if !utf8.ValidString(prompt) || len(prompt) > deepLinkMaxPromptBytes {
		return result, fmt.Errorf("deep-link prompt is invalid or too large")
	}
	projectID := query.Get("project")
	if projectID != "" && !validDeepLinkID(projectID) {
		return result, fmt.Errorf("invalid deep-link project ID")
	}

	parts := strings.Split(strings.Trim(parsed.EscapedPath(), "/"), "/")
	if len(parts) == 1 && parts[0] == "new" {
		if query.Has("view") {
			return result, fmt.Errorf("new-chat links do not accept view")
		}
		return DeepLinkEvent{Action: "new", ProjectID: projectID, Prompt: prompt}, nil
	}
	if len(parts) == 2 && parts[0] == "chat" {
		if projectID == "" || query.Has("view") {
			return result, fmt.Errorf("chat links require project and do not accept view")
		}
		sessionID, decodeErr := url.PathUnescape(parts[1])
		if decodeErr != nil || !validDeepLinkID(sessionID) {
			return result, fmt.Errorf("invalid deep-link session ID")
		}
		return DeepLinkEvent{Action: "chat", ProjectID: projectID, SessionID: sessionID, Prompt: prompt}, nil
	}
	if len(parts) == 2 && parts[0] == "project" {
		if projectID != "" || prompt != "" {
			return result, fmt.Errorf("project links do not accept project or q")
		}
		id, decodeErr := url.PathUnescape(parts[1])
		if decodeErr != nil || !validDeepLinkID(id) {
			return result, fmt.Errorf("invalid deep-link project ID")
		}
		view := query.Get("view")
		if view == "" {
			view = "chat"
		}
		if view != "chat" && view != "files" && view != "artifacts" {
			return result, fmt.Errorf("invalid deep-link project view")
		}
		return DeepLinkEvent{Action: "project", ProjectID: id, View: view}, nil
	}
	return result, fmt.Errorf("unsupported deep-link route")
}

func validDeepLinkID(value string) bool {
	if value == "" || len(value) > deepLinkMaxIDBytes || !utf8.ValidString(value) {
		return false
	}
	for _, r := range value {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') ||
			(r >= '0' && r <= '9') || r == '-' || r == '_' || r == '.' {
			continue
		}
		return false
	}
	return true
}

// DeepLinkURLsFromArgs extracts protocol arguments without treating ordinary
// filenames or flags as navigation requests.
func DeepLinkURLsFromArgs(args []string) []string {
	result := make([]string, 0, len(args))
	for _, value := range args {
		value = strings.TrimSpace(value)
		if len(value) <= deepLinkMaxURLBytes && strings.HasPrefix(strings.ToLower(value), "gokin://") {
			result = append(result, value)
		}
	}
	return result
}

// HandleDeepLink validates and queues a URL delivered by the OS or another
// application instance. Invalid input is logged without the raw URL/prompt.
func (s *Studio) HandleDeepLink(raw string) {
	raw = strings.TrimSpace(raw)
	link, err := ParseDeepLink(raw)
	if err != nil {
		if s.ctx != nil {
			s.LogEvent("warn", "deep-link", "Rejected an invalid gokin:// navigation request.")
		}
		return
	}
	now := time.Now()
	dedupeKey := sha256.Sum256([]byte(raw))
	s.deepLinkMu.Lock()
	if s.deepLinkRecent == nil {
		s.deepLinkRecent = make(map[[32]byte]time.Time)
	}
	for value, seenAt := range s.deepLinkRecent {
		if now.Sub(seenAt) > deepLinkDedupeWindow {
			delete(s.deepLinkRecent, value)
		}
	}
	if seenAt, duplicate := s.deepLinkRecent[dedupeKey]; duplicate && now.Sub(seenAt) <= deepLinkDedupeWindow {
		s.deepLinkMu.Unlock()
		return
	}
	s.deepLinkRecent[dedupeKey] = now
	s.deepLinkSequence++
	link.Sequence = s.deepLinkSequence
	ready := s.deepLinkReady
	if !ready {
		if len(s.deepLinkPending) == deepLinkPendingMax {
			copy(s.deepLinkPending, s.deepLinkPending[1:])
			s.deepLinkPending = s.deepLinkPending[:deepLinkPendingMax-1]
		}
		s.deepLinkPending = append(s.deepLinkPending, link)
	}
	s.deepLinkMu.Unlock()
	if ready {
		s.emitDeepLink(link)
	}
}

// StartDeepLinkEvents is called after React installs its event listener. It
// atomically enables live delivery and returns cold-start requests exactly once.
func (s *Studio) StartDeepLinkEvents() []DeepLinkEvent {
	s.deepLinkMu.Lock()
	s.deepLinkReady = true
	pending := append([]DeepLinkEvent(nil), s.deepLinkPending...)
	s.deepLinkPending = nil
	s.deepLinkMu.Unlock()
	return pending
}

func (s *Studio) emitDeepLink(link DeepLinkEvent) {
	s.activateStudioWindow()
	if s.testDeepLinkEmitter != nil {
		s.testDeepLinkEmitter(link)
		return
	}
	if s.ctx != nil {
		wailsRuntime.EventsEmit(s.ctx, EventDeepLink, link)
	}
}

func (s *Studio) activateStudioWindow() {
	_ = s.HideQuickEntryWindow(true)
	if s.testWindowActivator != nil {
		s.testWindowActivator()
		return
	}
	if s.ctx == nil {
		return
	}
	wailsRuntime.WindowUnminimise(s.ctx)
	wailsRuntime.WindowShow(s.ctx)
}

// HandleSecondInstanceLaunch routes protocol arguments to the first process;
// an ordinary second launch simply reveals the existing workspace.
func (s *Studio) HandleSecondInstanceLaunch(args []string) {
	links := DeepLinkURLsFromArgs(args)
	if len(links) == 0 {
		s.activateStudioWindow()
		return
	}
	for _, link := range links {
		s.HandleDeepLink(link)
	}
}
