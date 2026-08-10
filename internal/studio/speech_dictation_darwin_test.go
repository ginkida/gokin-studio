//go:build darwin && cgo

package studio

import "testing"

func TestDarwinSpeechAuthorizationLabelsMatchFrameworkEnums(t *testing.T) {
	if got := speechAuthorizationLabel(1); got != "denied" {
		t.Fatalf("speech status 1 = %q, want denied", got)
	}
	if got := speechAuthorizationLabel(2); got != "restricted" {
		t.Fatalf("speech status 2 = %q, want restricted", got)
	}
	if got := microphoneAuthorizationLabel(1); got != "restricted" {
		t.Fatalf("microphone status 1 = %q, want restricted", got)
	}
	if got := microphoneAuthorizationLabel(2); got != "denied" {
		t.Fatalf("microphone status 2 = %q, want denied", got)
	}
	if speechAuthorizationLabel(3) != "authorized" || microphoneAuthorizationLabel(3) != "authorized" {
		t.Fatal("authorized enum mapping changed")
	}
}
