package studio

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeTestSkill(t *testing.T, projectDir, root, folder, name, description, body string) string {
	t.Helper()
	dir := filepath.Join(projectDir, root, folder)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	manifest := "---\nname: " + name + "\ndescription: " + description + "\n---\n" + body
	path := filepath.Join(dir, "SKILL.md")
	if err := os.WriteFile(path, []byte(manifest), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestDiscoverProjectSkillsProgressiveDisclosureAndCompatibility(t *testing.T) {
	dir := t.TempDir()
	writeTestSkill(t, dir, filepath.Join(".gokin", "skills"), "release-notes", "release-notes", "Create release notes when preparing a release", "SECRET INSTRUCTIONS")
	writeTestSkill(t, dir, filepath.Join(".claude", "skills"), "reviewing_code", "reviewing-code", "Review code when a user asks for review", "OTHER BODY")

	got := discoverProjectSkills(dir)
	if len(got.Issues) != 0 || len(got.Skills) != 2 {
		t.Fatalf("inventory = %#v", got)
	}
	if got.Skills[0].Name != "release-notes" || got.Skills[1].Name != "reviewing-code" {
		t.Fatalf("skills not sorted or normalized: %#v", got.Skills)
	}
	catalog := projectSkillsTurnContext(dir)
	for _, want := range []string{"release-notes", "Create release notes", ".gokin/skills/release-notes/SKILL.md", ".claude/skills/reviewing_code/SKILL.md"} {
		if !strings.Contains(catalog, want) {
			t.Errorf("catalog missing %q: %q", want, catalog)
		}
	}
	for _, body := range []string{"SECRET INSTRUCTIONS", "OTHER BODY"} {
		if strings.Contains(catalog, body) {
			t.Errorf("catalog eagerly loaded skill body %q", body)
		}
	}
}

func TestDiscoverProjectSkillsRejectsInvalidDuplicateAndSymlink(t *testing.T) {
	dir := t.TempDir()
	writeTestSkill(t, dir, filepath.Join(".gokin", "skills"), "safe-skill", "safe-skill", "Use for safe work", "# Safe")
	writeTestSkill(t, dir, filepath.Join(".claude", "skills"), "safe-skill", "safe-skill", "Duplicate", "# Duplicate")
	writeTestSkill(t, dir, filepath.Join(".gokin", "skills"), "wrong-folder", "different-name", "Mismatch", "# Bad")
	writeTestSkill(t, dir, filepath.Join(".gokin", "skills"), "bad-name", "Claude-helper", "Reserved and uppercase", "# Bad")

	outside := t.TempDir()
	writeTestSkill(t, outside, "", "linked", "linked", "Linked skill", "# Linked")
	link := filepath.Join(dir, ".gokin", "skills", "linked")
	if err := os.Symlink(filepath.Join(outside, "linked"), link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}

	got := discoverProjectSkills(dir)
	if len(got.Skills) != 1 || got.Skills[0].Name != "safe-skill" {
		t.Fatalf("valid skills = %#v", got.Skills)
	}
	issues := ""
	for _, issue := range got.Issues {
		issues += issue.Path + ": " + issue.Error + "\n"
	}
	for _, want := range []string{"duplicate skill name", "must match skill name", "lowercase letters", "symlinked skill directories"} {
		if !strings.Contains(issues, want) {
			t.Errorf("issues missing %q: %s", want, issues)
		}
	}
}

func TestDiscoverProjectSkillsRejectsSymlinkedRootComponent(t *testing.T) {
	dir := t.TempDir()
	outside := t.TempDir()
	writeTestSkill(t, outside, "skills", "outside-skill", "outside-skill", "Must not load through a root symlink", "# Outside")
	if err := os.Symlink(outside, filepath.Join(dir, ".claude")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	got := discoverProjectSkills(dir)
	if len(got.Skills) != 0 {
		t.Fatalf("loaded skill through symlinked .claude root: %#v", got.Skills)
	}
	if len(got.Issues) != 1 || !strings.Contains(got.Issues[0].Error, "real directory") {
		t.Fatalf("missing root-symlink diagnostic: %#v", got.Issues)
	}
}

func TestParseSkillFrontmatterLimitsAndExactClosingFence(t *testing.T) {
	if _, err := parseSkillFrontmatter("---\nname: good-skill\ndescription: useful\n---not-a-fence\nbody"); err == nil {
		t.Fatal("accepted a non-exact closing frontmatter fence")
	}
	if _, err := parseSkillFrontmatter("---\r\nname: good-skill\r\ndescription: useful\r\n---\r\nbody"); err != nil {
		t.Fatalf("rejected CRLF frontmatter: %v", err)
	}
	if _, err := parseSkillFrontmatter("---\nname: claude-helper\ndescription: useful\n---\n"); err == nil {
		t.Fatal("accepted reserved skill name")
	}
}

func TestProjectSkillsCatalogIsDeliveredAsTurnContext(t *testing.T) {
	mc := &mockClient{responses: []mockResp{{text: "done"}}}
	p, _ := newTestProject(t, mc, nil)
	writeTestSkill(t, p.Directory, filepath.Join(".gokin", "skills"), "release-notes", "release-notes", "Use when drafting release notes", "FULL BODY MUST STAY LAZY")
	runAgent(p, "prepare a release")

	if !strings.Contains(mc.lastTurnContext, "release-notes") {
		t.Fatalf("turn context missing skill metadata: %q", mc.lastTurnContext)
	}
	if strings.Contains(mc.lastTurnContext, "FULL BODY MUST STAY LAZY") {
		t.Fatal("turn context eagerly included the SKILL.md body")
	}
}

func TestListProjectSkillsRejectsUnknownProject(t *testing.T) {
	s := NewStudio()
	if _, err := s.ListProjectSkills("missing"); err == nil {
		t.Fatal("ListProjectSkills accepted an unknown project")
	}
}
