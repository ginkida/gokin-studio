package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestConfigSaveUsesAtomicPrivateFile(t *testing.T) {
	root := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", root)
	cfg := DefaultConfig()
	cfg.API.APIKey = "private-test-key"
	if err := cfg.Save(); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, "gokin", "config.yaml")
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Fatalf("config mode=%#o, want 0600", got)
	}
	loaded := DefaultConfig()
	if err := loadFromFile(loaded, path); err != nil {
		t.Fatal(err)
	}
	if loaded.API.APIKey != cfg.API.APIKey {
		t.Fatalf("loaded API key=%q, want saved value", loaded.API.APIKey)
	}
	if temps, err := filepath.Glob(filepath.Join(filepath.Dir(path), ".gokin-*.tmp")); err != nil || len(temps) != 0 {
		t.Fatalf("config save leaked temps=%v err=%v", temps, err)
	}
}

func TestConfigSaveFailureDoesNotReplaceExistingDirectory(t *testing.T) {
	root := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", root)
	target := filepath.Join(root, "gokin", "config.yaml")
	if err := os.MkdirAll(target, 0o700); err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(target, "marker")
	if err := os.WriteFile(marker, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := DefaultConfig().Save(); err == nil {
		t.Fatal("Config.Save unexpectedly replaced a directory")
	}
	if data, err := os.ReadFile(marker); err != nil || string(data) != "keep" {
		t.Fatalf("failed save changed existing target: %q err=%v", data, err)
	}
}

func TestLoadFromFileRejectsSymlinkAndOversize(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "real.yaml")
	if err := os.WriteFile(target, []byte("api:\n  api_key: secret\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "config.yaml")
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	if err := loadFromFile(DefaultConfig(), link); err == nil || !strings.Contains(err.Error(), "not a regular file") {
		t.Fatalf("symlink load error=%v", err)
	}

	large := filepath.Join(root, "large.yaml")
	f, err := os.OpenFile(large, os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.Truncate(maxConfigFileBytes + 1); err != nil {
		_ = f.Close()
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	if err := loadFromFile(DefaultConfig(), large); err == nil || !strings.Contains(err.Error(), "too large") {
		t.Fatalf("oversized config load error=%v", err)
	}
}

func TestNilConfigSaveFails(t *testing.T) {
	var cfg *Config
	if err := cfg.Save(); err == nil {
		t.Fatal("nil Config.Save succeeded")
	}
}
