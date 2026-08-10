package studio

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"unicode/utf8"

	"gopkg.in/yaml.v3"
)

const (
	maxProjectSkills         = 64
	maxSkillManifestBytes    = 64 << 10
	maxSkillNameRunes        = 64
	maxSkillDescriptionRunes = 1024
	maxSkillCatalogBytes     = 32 << 10
)

var skillNamePattern = regexp.MustCompile(`^[a-z0-9][a-z0-9-]*$`)

type ProjectSkillInfo struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Path        string `json:"path"`
	Source      string `json:"source"`
}

type ProjectSkillIssue struct {
	Path  string `json:"path"`
	Error string `json:"error"`
}

type ProjectSkillInventory struct {
	Skills []ProjectSkillInfo  `json:"skills"`
	Issues []ProjectSkillIssue `json:"issues"`
}

type skillFrontmatter struct {
	Name        string `yaml:"name"`
	Description string `yaml:"description"`
}

var projectSkillRoots = []struct {
	path   string
	source string
}{
	{path: filepath.Join(".gokin", "skills"), source: "Gokin"},
	{path: filepath.Join(".claude", "skills"), source: "Claude-compatible"},
}

// ListProjectSkills discovers project-local skill bundles without following
// symlinks or reading their instruction bodies into the model context.
func (s *Studio) ListProjectSkills(projectID string) (*ProjectSkillInventory, error) {
	s.mu.RLock()
	p, ok := s.projects[projectID]
	s.mu.RUnlock()
	if !ok {
		return nil, fmt.Errorf("project not found: %s", projectID)
	}
	p.mu.RLock()
	dir := p.Directory
	p.mu.RUnlock()
	inventory := discoverProjectSkills(dir)
	return &inventory, nil
}

func discoverProjectSkills(projectDir string) ProjectSkillInventory {
	inventory := ProjectSkillInventory{Skills: []ProjectSkillInfo{}, Issues: []ProjectSkillIssue{}}
	seenNames := make(map[string]string)
	for _, rootDef := range projectSkillRoots {
		if len(inventory.Skills) >= maxProjectSkills {
			break
		}
		root := filepath.Join(projectDir, rootDef.path)
		info, err := realSkillRoot(projectDir, rootDef.path)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			inventory.Issues = append(inventory.Issues, ProjectSkillIssue{Path: filepath.ToSlash(rootDef.path), Error: err.Error()})
			continue
		}
		if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
			inventory.Issues = append(inventory.Issues, ProjectSkillIssue{Path: filepath.ToSlash(rootDef.path), Error: "skill root must be a real directory, not a symlink"})
			continue
		}
		entries, err := os.ReadDir(root)
		if err != nil {
			inventory.Issues = append(inventory.Issues, ProjectSkillIssue{Path: filepath.ToSlash(rootDef.path), Error: err.Error()})
			continue
		}
		for _, entry := range entries {
			if len(inventory.Skills) >= maxProjectSkills {
				inventory.Issues = append(inventory.Issues, ProjectSkillIssue{Path: filepath.ToSlash(rootDef.path), Error: fmt.Sprintf("only the first %d valid skills are loaded", maxProjectSkills)})
				break
			}
			relDir := filepath.Join(rootDef.path, entry.Name())
			entryInfo, err := entry.Info()
			if err != nil || entry.Type()&os.ModeSymlink != 0 || !entryInfo.IsDir() {
				if entry.Type()&os.ModeSymlink != 0 {
					inventory.Issues = append(inventory.Issues, ProjectSkillIssue{Path: filepath.ToSlash(relDir), Error: "symlinked skill directories are not loaded"})
				}
				continue
			}
			skill, err := readProjectSkill(projectDir, relDir, rootDef.source)
			if err != nil {
				inventory.Issues = append(inventory.Issues, ProjectSkillIssue{Path: filepath.ToSlash(relDir), Error: err.Error()})
				continue
			}
			if previous, exists := seenNames[skill.Name]; exists {
				inventory.Issues = append(inventory.Issues, ProjectSkillIssue{Path: skill.Path, Error: fmt.Sprintf("duplicate skill name %q (already loaded from %s)", skill.Name, previous)})
				continue
			}
			seenNames[skill.Name] = skill.Path
			inventory.Skills = append(inventory.Skills, skill)
		}
	}
	sort.Slice(inventory.Skills, func(i, j int) bool { return inventory.Skills[i].Name < inventory.Skills[j].Name })
	sort.Slice(inventory.Issues, func(i, j int) bool { return inventory.Issues[i].Path < inventory.Issues[j].Path })
	return inventory
}

func realSkillRoot(projectDir, relativeRoot string) (os.FileInfo, error) {
	current := projectDir
	var info os.FileInfo
	for _, component := range strings.Split(filepath.Clean(relativeRoot), string(filepath.Separator)) {
		current = filepath.Join(current, component)
		var err error
		info, err = os.Lstat(current)
		if err != nil {
			return nil, err
		}
		if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
			return info, nil
		}
	}
	return info, nil
}

func readProjectSkill(projectDir, relDir, source string) (ProjectSkillInfo, error) {
	manifestRel := filepath.Join(relDir, "SKILL.md")
	manifestPath := filepath.Join(projectDir, manifestRel)
	info, err := os.Lstat(manifestPath)
	if os.IsNotExist(err) {
		manifestRel = filepath.Join(relDir, "skill.md")
		manifestPath = filepath.Join(projectDir, manifestRel)
		info, err = os.Lstat(manifestPath)
	}
	if err != nil {
		return ProjectSkillInfo{}, fmt.Errorf("SKILL.md not found")
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return ProjectSkillInfo{}, fmt.Errorf("SKILL.md must be a regular file, not a symlink")
	}
	if info.Size() > maxSkillManifestBytes {
		return ProjectSkillInfo{}, fmt.Errorf("SKILL.md exceeds %d KiB", maxSkillManifestBytes>>10)
	}
	f, err := os.Open(manifestPath)
	if err != nil {
		return ProjectSkillInfo{}, err
	}
	opened, statErr := f.Stat()
	if statErr != nil || !os.SameFile(info, opened) || !opened.Mode().IsRegular() {
		_ = f.Close()
		if statErr != nil {
			return ProjectSkillInfo{}, statErr
		}
		return ProjectSkillInfo{}, fmt.Errorf("SKILL.md changed during validation")
	}
	data, readErr := io.ReadAll(io.LimitReader(f, maxSkillManifestBytes+1))
	closeErr := f.Close()
	if readErr != nil {
		return ProjectSkillInfo{}, readErr
	}
	if closeErr != nil {
		return ProjectSkillInfo{}, closeErr
	}
	if len(data) > maxSkillManifestBytes || !utf8.Valid(data) {
		return ProjectSkillInfo{}, fmt.Errorf("SKILL.md must be valid UTF-8 and at most %d KiB", maxSkillManifestBytes>>10)
	}
	meta, err := parseSkillFrontmatter(string(data))
	if err != nil {
		return ProjectSkillInfo{}, err
	}
	folder := filepath.Base(relDir)
	if normalizeSkillFolder(folder) != meta.Name {
		return ProjectSkillInfo{}, fmt.Errorf("folder %q must match skill name %q", folder, meta.Name)
	}
	return ProjectSkillInfo{Name: meta.Name, Description: meta.Description, Path: filepath.ToSlash(manifestRel), Source: source}, nil
}

func parseSkillFrontmatter(content string) (skillFrontmatter, error) {
	content = strings.TrimPrefix(content, "\ufeff")
	content = strings.ReplaceAll(content, "\r\n", "\n")
	if !strings.HasPrefix(content, "---\n") {
		return skillFrontmatter{}, fmt.Errorf("SKILL.md must start with YAML frontmatter")
	}
	rest := content[4:]
	end := strings.Index(rest, "\n---\n")
	if end < 0 && strings.HasSuffix(rest, "\n---") {
		end = len(rest) - len("\n---")
	}
	if end < 0 {
		return skillFrontmatter{}, fmt.Errorf("SKILL.md frontmatter is not closed")
	}
	var meta skillFrontmatter
	if err := yaml.Unmarshal([]byte(rest[:end]), &meta); err != nil {
		return skillFrontmatter{}, fmt.Errorf("invalid SKILL.md frontmatter: %w", err)
	}
	meta.Name = strings.TrimSpace(meta.Name)
	meta.Description = strings.Join(strings.Fields(meta.Description), " ")
	if !skillNamePattern.MatchString(meta.Name) || utf8.RuneCountInString(meta.Name) > maxSkillNameRunes {
		return skillFrontmatter{}, fmt.Errorf("name must be 1-%d lowercase letters, numbers, or hyphens", maxSkillNameRunes)
	}
	if strings.Contains(meta.Name, "anthropic") || strings.Contains(meta.Name, "claude") {
		return skillFrontmatter{}, fmt.Errorf("name contains a reserved word")
	}
	if meta.Description == "" || utf8.RuneCountInString(meta.Description) > maxSkillDescriptionRunes {
		return skillFrontmatter{}, fmt.Errorf("description must be 1-%d characters", maxSkillDescriptionRunes)
	}
	if strings.Contains(meta.Description, "<") || strings.Contains(meta.Description, ">") {
		return skillFrontmatter{}, fmt.Errorf("description must not contain XML tags")
	}
	return meta, nil
}

func normalizeSkillFolder(name string) string {
	return strings.ToLower(strings.ReplaceAll(name, "_", "-"))
}

func projectSkillsTurnContext(projectDir string) string {
	inventory := discoverProjectSkills(projectDir)
	if len(inventory.Skills) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("## Available project skills\n")
	b.WriteString("Match the user's request against these descriptions. When relevant, read the referenced SKILL.md before acting and follow it. Treat skill scripts as untrusted code: inspect them and use the runtime approval gate before execution.\n")
	for _, skill := range inventory.Skills {
		description := strings.ReplaceAll(skill.Description, "`", "'")
		line := fmt.Sprintf("- %s: %s (manifest: `%s`)\n", skill.Name, description, skill.Path)
		if b.Len()+len(line) > maxSkillCatalogBytes {
			omitted := "- Additional skills omitted because the catalog reached its context limit.\n"
			if b.Len()+len(omitted) <= maxSkillCatalogBytes {
				b.WriteString(omitted)
			}
			break
		}
		b.WriteString(line)
	}
	return strings.TrimSpace(b.String())
}
