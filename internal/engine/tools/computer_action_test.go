package tools

import (
	"context"
	"strings"
	"testing"
)

func TestComputerActionValidation(t *testing.T) {
	tool := NewComputerActionTool()
	valid := []map[string]any{
		{"action": "click", "x": 120, "y": -20},
		{"action": "click", "x": 1.0, "y": 2.0, "button": "double"},
		{"action": "type", "text": "hello\nworld"},
		{"action": "key", "keys": "shift+ctrl+s"},
		{"action": "key", "keys": "CMD+ENTER"},
	}
	for _, args := range valid {
		if err := tool.Validate(args); err != nil {
			t.Errorf("valid action %#v rejected: %v", args, err)
		}
	}
	invalid := []map[string]any{
		{},
		{"action": "click", "x": 1},
		{"action": "click", "x": 1, "y": 2, "button": "middle"},
		{"action": "type", "text": ""},
		{"action": "type", "text": "bad\x00text"},
		{"action": "key", "keys": "CTRL+ALT"},
		{"action": "key", "keys": "CTRL+F12"},
		{"action": "drag"},
	}
	for _, args := range invalid {
		if err := tool.Validate(args); err == nil {
			t.Errorf("invalid action %#v accepted", args)
		}
	}
}

func TestComputerActionExecutesOnlySelectedPrimitive(t *testing.T) {
	tool := NewComputerActionTool()
	var got string
	tool.click = func(_ context.Context, x, y int, button string) error {
		got = "click:" + button
		if x != 10 || y != 20 {
			t.Errorf("coordinates = %d,%d", x, y)
		}
		return nil
	}
	tool.typ = func(_ context.Context, text string) error { got = "type:" + text; return nil }
	tool.key = func(_ context.Context, chord string) error { got = "key:" + chord; return nil }

	result, _ := tool.Execute(context.Background(), map[string]any{"action": "click", "x": 10, "y": 20})
	if !result.Success || got != "click:left" {
		t.Fatalf("click result=%#v got=%q", result, got)
	}
	result, _ = tool.Execute(context.Background(), map[string]any{"action": "type", "text": "hello"})
	if !result.Success || got != "type:hello" {
		t.Fatalf("type result=%#v got=%q", result, got)
	}
	result, _ = tool.Execute(context.Background(), map[string]any{"action": "key", "keys": "shift+ctrl+s"})
	if !result.Success || got != "key:CTRL+SHIFT+S" {
		t.Fatalf("key result=%#v got=%q", result, got)
	}
}

func TestComputerKeyChordCanonicalization(t *testing.T) {
	got, err := normalizeComputerKeyChord(" command + shift + p ")
	if err != nil || got != "SHIFT+META+P" {
		t.Fatalf("canonical chord = %q, %v", got, err)
	}
	if _, err := normalizeComputerKeyChord(strings.Repeat("CTRL+", 6) + "A"); err == nil {
		t.Fatal("accepted oversized chord")
	}
}
