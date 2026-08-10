package tools

import "testing"

func TestNormalizeComputerAppID(t *testing.T) {
	if got := NormalizeComputerAppID(` C:\Program Files\App\App.EXE `); got != "c:/program files/app/app.exe" {
		t.Fatalf("normalized app ID = %q", got)
	}
}

func TestSensitiveComputerApplicationIsFailClosed(t *testing.T) {
	for _, app := range []ComputerApplication{
		{ID: "com.apple.keychainaccess", Name: "Keychain Access"},
		{ID: "com.1password.1password", Name: "1Password"},
		{ID: "c:/program files/bitwarden/bitwarden.exe", Name: "Bitwarden"},
		{ID: "com.ledger.live", Name: "Ledger Live"},
	} {
		if !IsSensitiveComputerApplication(app) {
			t.Errorf("sensitive application not blocked: %#v", app)
		}
	}
	if IsSensitiveComputerApplication(ComputerApplication{ID: "com.apple.Safari", Name: "Safari"}) {
		t.Fatal("ordinary browser classified as sensitive")
	}
}
