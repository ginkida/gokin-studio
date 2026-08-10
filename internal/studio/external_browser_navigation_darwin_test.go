//go:build darwin && cgo

package studio

import "testing"

func TestDarwinExternalNavigationPolicyConfinesChildFrames(t *testing.T) {
	if !darwinExternalNavigationPolicyInstalls() {
		t.Fatal("Objective-C runtime policy method did not install")
	}
	tests := []struct {
		name                                               string
		sourceScheme, sourceHost, targetScheme, targetHost string
		sourcePort, targetPort                             int
		sourceMain, targetMain, hasTarget, want            bool
	}{
		{name: "main Wails document", sourceScheme: "wails", sourceHost: "wails", sourceMain: true, targetScheme: "wails", targetHost: "wails", targetMain: true, hasTarget: true, want: true},
		{name: "initial Wails document has no source frame", targetScheme: "wails", targetHost: "wails", targetMain: true, hasTarget: true, want: true},
		{name: "initial local dev document has no source frame", targetScheme: "http", targetHost: "localhost", targetPort: 34115, targetMain: true, hasTarget: true, want: true},
		{name: "main cannot leave app", sourceScheme: "wails", sourceHost: "wails", sourceMain: true, targetScheme: "https", targetHost: "example.com", targetPort: 443, targetMain: true, hasTarget: true},
		{name: "main opens reviewed loopback frame", sourceScheme: "wails", sourceHost: "wails", sourceMain: true, targetScheme: "http", targetHost: "gokin-external-token.localhost", targetPort: 43123, hasTarget: true, want: true},
		{name: "main opens in-memory PDF frame", sourceScheme: "wails", sourceHost: "wails", sourceMain: true, targetScheme: "data", hasTarget: true, want: true},
		{name: "child cannot replace itself with data document", sourceScheme: "http", sourceHost: "gokin-external-token.localhost", sourcePort: 43123, targetScheme: "data", hasTarget: true},
		{name: "child stays exact local origin", sourceScheme: "http", sourceHost: "gokin-external-token.localhost", sourcePort: 43123, targetScheme: "http", targetHost: "gokin-external-token.localhost", targetPort: 43123, hasTarget: true, want: true},
		{name: "child cannot reach localhost service", sourceScheme: "http", sourceHost: "gokin-external-token.localhost", sourcePort: 43123, targetScheme: "http", targetHost: "127.0.0.1", targetPort: 8080, hasTarget: true},
		{name: "child cannot change proxy port", sourceScheme: "http", sourceHost: "gokin-external-token.localhost", sourcePort: 43123, targetScheme: "http", targetHost: "gokin-external-token.localhost", targetPort: 9999, hasTarget: true},
		{name: "child cannot navigate public origin directly", sourceScheme: "http", sourceHost: "gokin-external-token.localhost", sourcePort: 43123, targetScheme: "https", targetHost: "other.example", targetPort: 443, hasTarget: true},
		{name: "sandboxed child cannot replace app", sourceScheme: "http", sourceHost: "gokin-external-token.localhost", sourcePort: 43123, targetScheme: "http", targetHost: "127.0.0.1", targetPort: 3000, targetMain: true, hasTarget: true},
		{name: "new window denied", sourceScheme: "http", sourceHost: "gokin-external-token.localhost", sourcePort: 43123, targetScheme: "https", targetHost: "example.com", targetPort: 443},
		{name: "srcdoc remains available", sourceScheme: "wails", sourceHost: "wails", targetScheme: "about", targetHost: "", hasTarget: true, want: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := darwinExternalNavigationPolicyAllows(
				tt.sourceScheme, tt.sourceHost, tt.sourcePort, tt.sourceMain,
				tt.targetScheme, tt.targetHost, tt.targetPort, tt.targetMain, tt.hasTarget,
			)
			if got != tt.want {
				t.Fatalf("policy = %v, want %v", got, tt.want)
			}
		})
	}
}
