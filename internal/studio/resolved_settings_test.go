package studio

import (
	"os"
	"strings"
	"testing"
)

// withEnv sets an environment variable for the duration of the test and
// restores the previous value on cleanup. Safe across multiple vars.
func withEnv(t *testing.T, key, val string) {
	t.Helper()
	prev, hadPrev := os.LookupEnv(key)
	_ = os.Setenv(key, val)
	t.Cleanup(func() {
		if hadPrev {
			_ = os.Setenv(key, prev)
		} else {
			_ = os.Unsetenv(key)
		}
	})
}

// clearEnv removes a variable for the duration of the test.
func clearEnv(t *testing.T, key string) {
	t.Helper()
	prev, hadPrev := os.LookupEnv(key)
	_ = os.Unsetenv(key)
	t.Cleanup(func() {
		if hadPrev {
			_ = os.Setenv(key, prev)
		}
	})
}

func TestResolveProviderKey_SettingWins(t *testing.T) {
	// When YAML key is set, env var must be ignored.
	withEnv(t, "GLM_API_KEY", "from-env-should-not-win")
	s := Settings{GLMKey: "from-setting"}
	key, src := ResolveProviderKey("glm", s)
	if key != "from-setting" {
		t.Errorf("key=%q, want 'from-setting'", key)
	}
	if src != KeySourceSetting {
		t.Errorf("src=%q, want %q", src, KeySourceSetting)
	}
}

func TestResolveProviderKey_EnvFallback(t *testing.T) {
	withEnv(t, "GLM_API_KEY", "from-env")
	s := Settings{GLMKey: ""}
	key, src := ResolveProviderKey("glm", s)
	if key != "from-env" {
		t.Errorf("key=%q, want 'from-env'", key)
	}
	if src != KeySourceEnv {
		t.Errorf("src=%q, want %q", src, KeySourceEnv)
	}
}

func TestResolveProviderKey_BothEmpty(t *testing.T) {
	clearEnv(t, "GLM_API_KEY")
	clearEnv(t, "MINIMAX_API_KEY")
	clearEnv(t, "KIMI_API_KEY")
	s := Settings{}
	for _, prov := range []string{"glm", "minimax", "kimi"} {
		key, src := ResolveProviderKey(prov, s)
		if key != "" {
			t.Errorf("%s: key=%q, want empty", prov, key)
		}
		if src != KeySourceNone {
			t.Errorf("%s: src=%q, want %q", prov, src, KeySourceNone)
		}
	}
}

func TestResolveProviderKey_AllProviders(t *testing.T) {
	cases := []struct {
		prov   string
		envVar string
	}{
		{"glm", "GLM_API_KEY"},
		{"minimax", "MINIMAX_API_KEY"},
		{"kimi", "KIMI_API_KEY"},
	}
	for _, c := range cases {
		t.Run(c.prov, func(t *testing.T) {
			withEnv(t, c.envVar, "env-value-"+c.prov)
			key, src := ResolveProviderKey(c.prov, Settings{})
			if key != "env-value-"+c.prov {
				t.Errorf("key=%q, want env-value-%s", key, c.prov)
			}
			if src != KeySourceEnv {
				t.Errorf("src=%q, want env", src)
			}
		})
	}
}

func TestResolveProviderKey_Ollama_DefaultFallback(t *testing.T) {
	clearEnv(t, "OLLAMA_HOST")
	s := Settings{OllamaURL: ""}
	url, src := ResolveProviderKey("ollama", s)
	if url != "http://localhost:11434" {
		t.Errorf("url=%q, want default localhost", url)
	}
	if src != KeySourceDefault {
		t.Errorf("src=%q, want default", src)
	}
}

func TestResolveProviderKey_Ollama_EnvOverride(t *testing.T) {
	withEnv(t, "OLLAMA_HOST", "http://my-ollama:11434")
	s := Settings{OllamaURL: ""}
	url, src := ResolveProviderKey("ollama", s)
	if url != "http://my-ollama:11434" {
		t.Errorf("url=%q, want from-env value", url)
	}
	if src != KeySourceEnv {
		t.Errorf("src=%q, want env", src)
	}
}

func TestResolveProviderKey_Ollama_SettingWins(t *testing.T) {
	withEnv(t, "OLLAMA_HOST", "http://from-env:11434")
	s := Settings{OllamaURL: "http://from-setting:11434"}
	url, src := ResolveProviderKey("ollama", s)
	if url != "http://from-setting:11434" {
		t.Errorf("url=%q, want from-setting value", url)
	}
	if src != KeySourceSetting {
		t.Errorf("src=%q, want setting", src)
	}
}

func TestResolveProviderKey_UnknownProvider(t *testing.T) {
	key, src := ResolveProviderKey("unknown-thing", Settings{})
	if key != "" || src != KeySourceNone {
		t.Errorf("unknown provider: key=%q src=%q, want empty/none", key, src)
	}
}

func TestResolveProviderKey_TrimsWhitespace(t *testing.T) {
	// Whitespace-only setting AND whitespace-only env should fall through
	// to None, not be treated as a valid key.
	withEnv(t, "GLM_API_KEY", "   \t  ")
	s := Settings{GLMKey: "   "}
	key, src := ResolveProviderKey("glm", s)
	if key != "" {
		t.Errorf("key=%q, want empty after whitespace-only", key)
	}
	if src != KeySourceNone {
		t.Errorf("src=%q, want none", src)
	}

	// Now with a real env value but only whitespace setting.
	withEnv(t, "GLM_API_KEY", "sk-real-key")
	key, src = ResolveProviderKey("glm", Settings{GLMKey: "   "})
	if key != "sk-real-key" {
		t.Errorf("key=%q, want 'sk-real-key' from env when setting is whitespace", key)
	}
	if src != KeySourceEnv {
		t.Errorf("src=%q, want env", src)
	}
}

func TestEnvVarForProvider(t *testing.T) {
	cases := map[string]string{
		"glm":     "GLM_API_KEY",
		"minimax": "MINIMAX_API_KEY",
		"kimi":    "KIMI_API_KEY",
		"ollama":  "OLLAMA_HOST",
		"GLM":     "GLM_API_KEY", // case-insensitive
		"  kimi ": "KIMI_API_KEY",
		"":        "",
		"unknown": "",
	}
	for in, want := range cases {
		t.Run("provider="+in, func(t *testing.T) {
			if got := envVarForProvider(in); got != want {
				t.Errorf("envVarForProvider(%q)=%q, want %q", in, got, want)
			}
		})
	}
}

// Diagnostics integration: env-only setup should report OK, not error.
func TestCheckAPIKeys_EnvOnlyReportsOK(t *testing.T) {
	_ = withTempHistoryDir(t)
	withEnv(t, "GLM_API_KEY", "sk-from-env")

	s := NewStudio()
	s.config = defaultConfig()
	s.config.Settings.GLMKey = "" // no setting, only env
	if _, err := s.AddProject("P", t.TempDir()); err != nil {
		t.Fatal(err)
	}

	d := s.GetDiagnostics()
	foundOK := false
	mentionsEnv := false
	for _, c := range d.Checks {
		if c.Category == "providers" && strings.Contains(c.Name, "GLM") {
			if c.Status == "ok" {
				foundOK = true
			}
			if strings.Contains(c.Message, "GLM_API_KEY") {
				mentionsEnv = true
			}
		}
	}
	if !foundOK {
		t.Errorf("env-only setup should report OK; got: %+v", d.Checks)
	}
	if !mentionsEnv {
		t.Errorf("OK message should mention $GLM_API_KEY source; got: %+v", d.Checks)
	}
}

func TestCheckAPIKeys_EnvAndSettingBothMissingReportsError(t *testing.T) {
	_ = withTempHistoryDir(t)
	clearEnv(t, "GLM_API_KEY")

	s := NewStudio()
	s.config = defaultConfig()
	s.config.Settings.GLMKey = ""
	if _, err := s.AddProject("P", t.TempDir()); err != nil {
		t.Fatal(err)
	}

	d := s.GetDiagnostics()
	foundErr := false
	for _, c := range d.Checks {
		if c.Category == "providers" && strings.Contains(c.Name, "GLM") && c.Status == "error" {
			foundErr = true
			break
		}
	}
	if !foundErr {
		t.Errorf("no key anywhere should report error; got: %+v", d.Checks)
	}
}

func TestCheckAPIKeys_OllamaEnvFallbackReportsOK(t *testing.T) {
	_ = withTempHistoryDir(t)
	withEnv(t, "OLLAMA_HOST", "http://my-ollama:11434")

	s := NewStudio()
	s.config = defaultConfig()
	s.config.Settings.OllamaURL = ""
	p, err := s.AddProject("P", t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := s.SetProjectProvider(p.ID, "ollama", "llama3.1"); err != nil {
		t.Fatal(err)
	}

	d := s.GetDiagnostics()
	foundOK := false
	for _, c := range d.Checks {
		if c.Category == "providers" && strings.Contains(c.Name, "Ollama") && c.Status == "ok" {
			foundOK = true
			break
		}
	}
	if !foundOK {
		t.Errorf("Ollama env-fallback should report OK; got: %+v", d.Checks)
	}
}
