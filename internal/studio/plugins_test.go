package studio

import (
	"archive/zip"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPluginBundleReviewInstallEnableCommandsAndRemove(t *testing.T) {
	withTempConfigDir(t)
	s := newStudioForTest(t)
	addTestProject(t, s, "Plugins")
	archivePath := filepath.Join(t.TempDir(), "reporting.zip")
	writeTestPluginZIP(t, archivePath, map[string]string{
		"reporting/.claude-plugin/plugin.json": `{
			"name":"reporting","version":"1.2.3","description":"Build reviewed reports",
			"author":{"name":"Example Team"}
		}`,
		"reporting/skills/daily-report/SKILL.md":             "---\nname: daily-report\ndescription: Build a concise daily report from project evidence.\n---\n\n# Daily report\nRead the references first.",
		"reporting/skills/daily-report/references/layout.md": "# Layout\nUse an executive summary.",
		"reporting/commands/review.md":                       "---\ndescription: Review current changes\n---\nReview the project changes and summarize risks: $ARGUMENTS",
		"reporting/agents/researcher.md":                     "# Researcher",
		"reporting/.mcp.json":                                `{"mcpServers":{}}`,
		"reporting/scripts/build.sh":                         "#!/bin/sh\necho retained-but-not-executed",
	})

	preview, err := s.previewPluginBundle(archivePath)
	if err != nil {
		t.Fatal(err)
	}
	if preview.Name != "reporting" || preview.Version != "1.2.3" ||
		len(preview.Skills) != 1 || len(preview.Commands) != 1 || len(preview.Agents) != 1 ||
		!preview.HasMCP || !preview.HasScripts {
		t.Fatalf("preview = %#v", preview)
	}
	if preview.Commands[0].SlashName != "reporting-review" ||
		!strings.Contains(preview.Commands[0].Body, "$ARGUMENTS") {
		t.Fatalf("command preview = %#v", preview.Commands[0])
	}
	if preview.Agents[0].Description != "Researcher" {
		t.Fatalf("agent metadata = %#v", preview.Agents[0])
	}

	if _, err := s.InstallPluginBundle(archivePath, strings.Repeat("0", 64)); err == nil {
		t.Fatal("digest mismatch was accepted")
	}
	installed, err := s.InstallPluginBundle(archivePath, preview.Digest)
	if err != nil {
		t.Fatal(err)
	}
	if installed.Enabled {
		t.Fatal("new plugin enabled before explicit review")
	}
	scriptInfo, err := os.Stat(filepath.Join(pluginsDir(), "reporting", "scripts", "build.sh"))
	if err != nil {
		t.Fatal(err)
	}
	if scriptInfo.Mode().Perm()&0o111 != 0 {
		t.Fatalf("plugin script kept executable bits: %o", scriptInfo.Mode().Perm())
	}
	if commands, err := s.ListPluginCommands(); err != nil || len(commands) != 0 {
		t.Fatalf("disabled commands = %#v, %v", commands, err)
	}

	if err := s.SetPluginEnabled("reporting", true); err != nil {
		t.Fatal(err)
	}
	agentSpecs := enabledPluginAgentSpecs()
	if len(agentSpecs) != 1 || agentSpecs[0].ID != "reporting:researcher" ||
		agentSpecs[0].Description != "Researcher" {
		t.Fatalf("enabled agent catalog = %#v", agentSpecs)
	}
	commands, err := s.ListPluginCommands()
	if err != nil || len(commands) != 1 || commands[0].SlashName != "reporting-review" {
		t.Fatalf("enabled commands = %#v, %v", commands, err)
	}
	ctx := enabledPluginTurnContext()
	if !strings.Contains(ctx, "reporting:daily-report") ||
		!strings.Contains(ctx, "reporting/skills/daily-report/skill.md") {
		t.Fatalf("plugin skill context = %q", ctx)
	}
	list, err := s.ListInstalledPlugins()
	if err != nil || len(list) != 1 || !list[0].Enabled {
		t.Fatalf("installed plugins = %#v, %v", list, err)
	}
	if err := s.RemovePlugin("reporting"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(pluginsDir(), "reporting")); !os.IsNotExist(err) {
		t.Fatalf("plugin directory survived removal: %v", err)
	}
}

func TestPluginBundleRejectsTraversalAndSymlinks(t *testing.T) {
	withTempConfigDir(t)
	for name, configure := range map[string]func(*zip.Writer){
		"traversal": func(zw *zip.Writer) {
			writeZipText(t, zw, ".claude-plugin/plugin.json", `{"name":"safe"}`)
			writeZipText(t, zw, "../escape.txt", "no")
		},
		"symlink": func(zw *zip.Writer) {
			writeZipText(t, zw, ".claude-plugin/plugin.json", `{"name":"safe"}`)
			header := &zip.FileHeader{Name: "skills/link"}
			header.SetMode(os.ModeSymlink | 0o777)
			entry, err := zw.CreateHeader(header)
			if err != nil {
				t.Fatal(err)
			}
			_, _ = entry.Write([]byte("../../outside"))
		},
	} {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), name+".zip")
			file, err := os.Create(path)
			if err != nil {
				t.Fatal(err)
			}
			zw := zip.NewWriter(file)
			configure(zw)
			if err := zw.Close(); err != nil {
				t.Fatal(err)
			}
			if err := file.Close(); err != nil {
				t.Fatal(err)
			}
			if _, err := openPluginBundle(path); err == nil {
				t.Fatal("unsafe plugin archive was accepted")
			}
		})
	}
}

func TestInstalledPluginStateRejectsTraversal(t *testing.T) {
	withTempConfigDir(t)
	plugins := []InstalledPlugin{{
		Name: "safe", Digest: strings.Repeat("a", 64), InstalledAt: 1,
		Skills: []PluginComponentInfo{{Name: "skill", Description: "desc", Path: "../escape/SKILL.md"}},
	}}
	if err := saveInstalledPluginsRaw(plugins); err == nil {
		t.Fatal("unsafe plugin state path was accepted")
	}
}

func TestInstalledPluginStateRejectsForgedSlashName(t *testing.T) {
	withTempConfigDir(t)
	plugins := []InstalledPlugin{{
		Name: "safe", Digest: strings.Repeat("a", 64), InstalledAt: 1,
		Commands: []PluginCommandInfo{{
			Name: "review", SlashName: "clear", Body: "Do work",
			Path: "commands/review.md", Plugin: "safe",
		}},
	}}
	if err := saveInstalledPluginsRaw(plugins); err == nil {
		t.Fatal("forged plugin slash name was accepted")
	}
}

func TestPluginSkillAllowsClaudeCompatibleNames(t *testing.T) {
	meta, err := parsePluginSkillFrontmatter("---\nname: claude-review\ndescription: Review changes using a compatible workflow.\n---\n")
	if err != nil {
		t.Fatal(err)
	}
	if meta.Name != "claude-review" {
		t.Fatalf("name = %q", meta.Name)
	}
}

func writeTestPluginZIP(t *testing.T, filename string, entries map[string]string) {
	t.Helper()
	file, err := os.Create(filename)
	if err != nil {
		t.Fatal(err)
	}
	zw := zip.NewWriter(file)
	for name, content := range entries {
		writeZipText(t, zw, name, content)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
}

func writeZipText(t *testing.T, zw *zip.Writer, name, content string) {
	t.Helper()
	entry, err := zw.Create(name)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := entry.Write([]byte(content)); err != nil {
		t.Fatal(err)
	}
}
