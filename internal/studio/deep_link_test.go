package studio

import (
	"strings"
	"testing"
)

func TestParseDeepLinkRoutes(t *testing.T) {
	tests := []struct {
		raw  string
		want DeepLinkEvent
	}{
		{"gokin://studio/new", DeepLinkEvent{Action: "new"}},
		{"gokin://studio/new?project=abc-123&q=Review%20this", DeepLinkEvent{Action: "new", ProjectID: "abc-123", Prompt: "Review this"}},
		{"gokin://studio/chat/chat_1?project=project.1&q=Continue", DeepLinkEvent{Action: "chat", ProjectID: "project.1", SessionID: "chat_1", Prompt: "Continue"}},
		{"gokin://studio/project/project-1", DeepLinkEvent{Action: "project", ProjectID: "project-1", View: "chat"}},
		{"gokin://studio/project/project-1?view=artifacts", DeepLinkEvent{Action: "project", ProjectID: "project-1", View: "artifacts"}},
	}
	for _, test := range tests {
		got, err := ParseDeepLink(test.raw)
		if err != nil {
			t.Fatalf("ParseDeepLink(%q): %v", test.raw, err)
		}
		if got != test.want {
			t.Fatalf("ParseDeepLink(%q) = %#v, want %#v", test.raw, got, test.want)
		}
	}
}

func TestParseDeepLinkRejectsUnsafeOrAmbiguousInput(t *testing.T) {
	invalid := []string{
		"https://studio/new",
		"gokin://evil/new",
		"gokin://user@studio/new",
		"gokin://studio:42/new",
		"gokin://studio/new#send",
		"gokin://studio/new?q=one&q=two",
		"gokin://studio/new?send=true",
		"gokin://studio/chat/session",
		"gokin://studio/chat/a%2Fb?project=p",
		"gokin://studio/project/p?q=send",
		"gokin://studio/project/p?view=settings",
		"gokin://studio/unknown",
		"gokin://studio/new?q=" + strings.Repeat("x", deepLinkMaxPromptBytes+1),
	}
	for _, raw := range invalid {
		if got, err := ParseDeepLink(raw); err == nil {
			t.Fatalf("ParseDeepLink(%q) accepted %#v", raw, got)
		}
	}
}

func TestDeepLinkQueueDeliveryAndDedupe(t *testing.T) {
	s := &Studio{}
	s.HandleDeepLink("gokin://studio/new?q=first")
	s.HandleDeepLink("gokin://studio/new?q=first")
	pending := s.StartDeepLinkEvents()
	if len(pending) != 1 || pending[0].Sequence != 1 || pending[0].Prompt != "first" {
		t.Fatalf("pending = %#v", pending)
	}
	if again := s.StartDeepLinkEvents(); len(again) != 0 {
		t.Fatalf("second drain = %#v", again)
	}

	var emitted []DeepLinkEvent
	activated := 0
	s.testDeepLinkEmitter = func(event DeepLinkEvent) { emitted = append(emitted, event) }
	s.testWindowActivator = func() { activated++ }
	s.HandleDeepLink("gokin://studio/project/p?view=files")
	if len(emitted) != 1 || emitted[0].Sequence != 2 || activated != 1 {
		t.Fatalf("emitted=%#v activated=%d", emitted, activated)
	}
}

func TestDeepLinkPendingQueueIsBounded(t *testing.T) {
	s := &Studio{}
	for i := 0; i < deepLinkPendingMax+4; i++ {
		s.HandleDeepLink("gokin://studio/new?q=" + strings.Repeat("x", i+1))
	}
	pending := s.StartDeepLinkEvents()
	if len(pending) != deepLinkPendingMax || pending[0].Sequence != 5 {
		t.Fatalf("bounded pending = %#v", pending)
	}
}

func TestDeepLinkURLsFromArgs(t *testing.T) {
	got := DeepLinkURLsFromArgs([]string{"--flag", "/tmp/file", " GOKIN://studio/new?q=hi "})
	if len(got) != 1 || got[0] != "GOKIN://studio/new?q=hi" {
		t.Fatalf("DeepLinkURLsFromArgs = %#v", got)
	}
}
