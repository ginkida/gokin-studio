package studio

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
)

// SessionFileApplication is one fixed, locally installed editor that can be
// launched by an explicit user click. Command details never cross the Wails
// boundary, so the frontend cannot turn this into arbitrary process execution.
type SessionFileApplication struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

type SessionFileActions struct {
	Path         string                   `json:"path"`
	AbsolutePath string                   `json:"absolutePath"`
	IsDirectory  bool                     `json:"isDirectory"`
	Applications []SessionFileApplication `json:"applications"`
}

type sessionFileApplicationCommand struct {
	SessionFileApplication
	Command string
	Args    []string
}

var discoverSessionFileApplications = defaultSessionFileApplications

func sessionActionPath(s *Studio, projectID, sessionID, subPath string) (string, string, bool, error) {
	project, session, err := s.projectSession(projectID, sessionID)
	if err != nil {
		return "", "", false, err
	}
	workDir, err := sessionWorkingDirectory(project, session)
	if err != nil {
		return "", "", false, err
	}
	rel, err := editableSessionPath(subPath)
	if err != nil {
		return "", "", false, err
	}
	root, err := os.OpenRoot(workDir)
	if err != nil {
		return "", "", false, fmt.Errorf("open session workspace: %w", err)
	}
	defer root.Close()
	if err := rejectSessionEditorSymlinkComponents(root, rel); err != nil {
		return "", "", false, err
	}
	info, err := root.Lstat(rel)
	if err != nil {
		return "", "", false, fmt.Errorf("stat session path: %w", err)
	}
	if !info.Mode().IsRegular() && !info.IsDir() {
		return "", "", false, fmt.Errorf("session path is not a regular file or directory")
	}
	return filepath.ToSlash(rel), filepath.Join(workDir, rel), info.IsDir(), nil
}

func sessionFileActionPath(s *Studio, projectID, sessionID, subPath string) (string, string, error) {
	rel, absolute, isDirectory, err := sessionActionPath(s, projectID, sessionID, subPath)
	if err != nil {
		return "", "", err
	}
	if isDirectory {
		return "", "", fmt.Errorf("session path is not a regular file")
	}
	return rel, absolute, nil
}

func defaultSessionFileApplications() []sessionFileApplicationCommand {
	type candidate struct {
		id, name, executable, darwinBundle string
	}
	candidates := []candidate{
		{id: "vscode", name: "Visual Studio Code", executable: "code", darwinBundle: "Visual Studio Code.app"},
		{id: "cursor", name: "Cursor", executable: "cursor", darwinBundle: "Cursor.app"},
		{id: "zed", name: "Zed", executable: "zed", darwinBundle: "Zed.app"},
	}
	result := make([]sessionFileApplicationCommand, 0, len(candidates))
	for _, candidate := range candidates {
		if runtime.GOOS == "darwin" {
			if !darwinApplicationExists(candidate.darwinBundle) {
				continue
			}
			result = append(result, sessionFileApplicationCommand{
				SessionFileApplication: SessionFileApplication{ID: candidate.id, Name: candidate.name},
				Command:                "open",
				Args:                   []string{"-a", candidate.name},
			})
			continue
		}
		executable, err := exec.LookPath(candidate.executable)
		if err != nil {
			continue
		}
		result = append(result, sessionFileApplicationCommand{
			SessionFileApplication: SessionFileApplication{ID: candidate.id, Name: candidate.name},
			Command:                executable,
		})
	}
	return result
}

func darwinApplicationExists(bundle string) bool {
	locations := []string{filepath.Join("/Applications", bundle), filepath.Join("/System/Applications", bundle)}
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		locations = append(locations, filepath.Join(home, "Applications", bundle))
	}
	for _, location := range locations {
		if info, err := os.Stat(location); err == nil && info.IsDir() {
			return true
		}
	}
	return false
}

// ListSessionFileActions resolves the exact chat worktree path and lists only
// fixed editor applications detected on this machine.
func (s *Studio) ListSessionFileActions(projectID, sessionID, subPath string) (*SessionFileActions, error) {
	rel, absolute, err := sessionFileActionPath(s, projectID, sessionID, subPath)
	if err != nil {
		return nil, err
	}
	commands := discoverSessionFileApplications()
	applications := make([]SessionFileApplication, 0, len(commands))
	seen := make(map[string]bool, len(commands))
	for _, command := range commands {
		if command.ID == "" || command.Name == "" || seen[command.ID] {
			continue
		}
		seen[command.ID] = true
		applications = append(applications, command.SessionFileApplication)
	}
	return &SessionFileActions{Path: rel, AbsolutePath: absolute, Applications: applications}, nil
}

// ListSessionPathActions resolves a regular file or directory for the shared
// context menu. Directories intentionally expose no editor applications.
func (s *Studio) ListSessionPathActions(projectID, sessionID, subPath string) (*SessionFileActions, error) {
	rel, absolute, isDirectory, err := sessionActionPath(s, projectID, sessionID, subPath)
	if err != nil {
		return nil, err
	}
	actions := &SessionFileActions{Path: rel, AbsolutePath: absolute, IsDirectory: isDirectory}
	if isDirectory {
		actions.Applications = []SessionFileApplication{}
		return actions, nil
	}
	commands := discoverSessionFileApplications()
	actions.Applications = make([]SessionFileApplication, 0, len(commands))
	seen := make(map[string]bool, len(commands))
	for _, command := range commands {
		if command.ID == "" || command.Name == "" || seen[command.ID] {
			continue
		}
		seen[command.ID] = true
		actions.Applications = append(actions.Applications, command.SessionFileApplication)
	}
	return actions, nil
}

// OpenSessionFileInApplication re-resolves both the session file and installed
// app at click time. appID is matched against a fixed catalog, never executed.
func (s *Studio) OpenSessionFileInApplication(projectID, sessionID, subPath, appID string) error {
	rel, absolute, err := sessionFileActionPath(s, projectID, sessionID, subPath)
	if err != nil {
		return err
	}
	appID = strings.TrimSpace(appID)
	for _, application := range discoverSessionFileApplications() {
		if application.ID != appID {
			continue
		}
		args := append(append([]string(nil), application.Args...), absolute)
		if err := execCommand(application.Command, args...); err != nil {
			return fmt.Errorf("open session file in %s: %w", application.Name, err)
		}
		s.logf("info", "file-actions", "opened session file %q in %s", rel, application.Name)
		return nil
	}
	return fmt.Errorf("file editor application is unavailable")
}

func sessionFileRevealCommand(path string) (string, []string, error) {
	switch runtime.GOOS {
	case "darwin":
		return "open", []string{"-R", path}, nil
	case "windows":
		return "explorer.exe", []string{"/select," + path}, nil
	case "linux":
		return "xdg-open", []string{filepath.Dir(path)}, nil
	default:
		return "", nil, fmt.Errorf("unsupported OS for file reveal: %s", runtime.GOOS)
	}
}

// ShowSessionFileInFileManager reveals (rather than executes) one validated
// regular file from the selected chat checkout.
func (s *Studio) ShowSessionFileInFileManager(projectID, sessionID, subPath string) error {
	rel, absolute, err := sessionFileActionPath(s, projectID, sessionID, subPath)
	if err != nil {
		return err
	}
	command, args, err := sessionFileRevealCommand(absolute)
	if err != nil {
		return err
	}
	if err := execCommand(command, args...); err != nil {
		return fmt.Errorf("reveal session file: %w", err)
	}
	s.logf("info", "file-actions", "revealed session file %q", rel)
	return nil
}
