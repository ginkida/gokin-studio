package studio

import (
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"path/filepath"
	"sort"
	"strings"
	"unicode/utf8"
)

const (
	artifactPreviewMaxBytes       = 2 << 20
	binaryArtifactPreviewMaxBytes = 30 << 20
	artifactLibraryMaxScanned     = 25_000
	artifactLibraryMaxItems       = 1_000
)

// ArtifactDocument is a bounded project-local HTML/SVG source document for
// the sandboxed desktop preview. The frontend never navigates to the project
// path directly; it renders Content through an origin-isolated iframe.
type ArtifactDocument struct {
	Path        string `json:"path"`
	Name        string `json:"name"`
	MIMEType    string `json:"mimeType"`
	Content     string `json:"content"`
	DataBase64  string `json:"dataBase64,omitempty"`
	PreviewKind string `json:"previewKind"`
	Size        int64  `json:"size"`
	ModifiedAt  int64  `json:"modifiedAt"`
}

// ArtifactSummary is lightweight metadata for the project-wide Artifacts
// library. Content is intentionally loaded only after the user opens an item.
type ArtifactSummary struct {
	Path            string `json:"path"`
	Name            string `json:"name"`
	Directory       string `json:"directory,omitempty"`
	MIMEType        string `json:"mimeType"`
	PreviewKind     string `json:"previewKind"`
	Size            int64  `json:"size"`
	ModifiedAt      int64  `json:"modifiedAt"`
	VersionCount    int    `json:"versionCount,omitempty"`
	LatestVersionAt int64  `json:"latestVersionAt,omitempty"`
	Previewable     bool   `json:"previewable"`
	Issue           string `json:"issue,omitempty"`
}

type ArtifactLibraryResult struct {
	Artifacts []ArtifactSummary `json:"artifacts"`
	Truncated bool              `json:"truncated"`
}

func artifactPreviewProperties(path string) (mimeType, previewKind string, maxBytes int64, ok bool) {
	maxBytes = artifactPreviewMaxBytes
	switch strings.ToLower(filepath.Ext(path)) {
	case ".html", ".htm":
		return "text/html", "web", maxBytes, true
	case ".svg":
		return "image/svg+xml", "web", maxBytes, true
	case ".docx":
		return "application/vnd.openxmlformats-officedocument.wordprocessingml.document", "document", binaryArtifactPreviewMaxBytes, true
	case ".xlsx":
		return "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet", "spreadsheet", binaryArtifactPreviewMaxBytes, true
	case ".pptx":
		return "application/vnd.openxmlformats-officedocument.presentationml.presentation", "presentation", binaryArtifactPreviewMaxBytes, true
	case ".pdf":
		return "application/pdf", "pdf", binaryArtifactPreviewMaxBytes, true
	default:
		return "", "", 0, false
	}
}

// ListProjectArtifacts discovers supported preview files without reading their
// contents. The walk is bounded, skips symlinks/private dependency trees, and
// returns newest-first metadata suitable for a responsive desktop library.
func (s *Studio) ListProjectArtifacts(projectID string) (*ArtifactLibraryResult, error) {
	if strings.TrimSpace(projectID) == "" {
		return nil, fmt.Errorf("projectID cannot be empty")
	}
	s.mu.RLock()
	project := s.projects[projectID]
	s.mu.RUnlock()
	if project == nil {
		return nil, fmt.Errorf("project not found: %s", projectID)
	}
	project.mu.RLock()
	projectDir := project.Directory
	project.mu.RUnlock()
	return s.listArtifactsAt(projectID, "", projectDir)
}

// ListSessionArtifacts discovers artifacts in the exact checkout used by one
// chat. Isolated Git sessions must never silently fall back to the shared
// project root, because that would show stale files from another workspace.
func (s *Studio) ListSessionArtifacts(projectID, sessionID string) (*ArtifactLibraryResult, error) {
	project, session, err := s.projectSession(projectID, sessionID)
	if err != nil {
		return nil, err
	}
	workDir, err := sessionWorkingDirectory(project, session)
	if err != nil {
		return nil, err
	}
	if sessionID == "" {
		sessionID = "default"
	}
	return s.listArtifactsAt(projectID, sessionID, workDir)
}

func (s *Studio) listArtifactsAt(projectID, sessionID, workDir string) (*ArtifactLibraryResult, error) {
	if strings.TrimSpace(workDir) == "" {
		return &ArtifactLibraryResult{Artifacts: []ArtifactSummary{}}, nil
	}

	result := &ArtifactLibraryResult{Artifacts: make([]ArtifactSummary, 0, 64)}
	scanned := 0
	errArtifactLibraryLimit := errors.New("artifact library scan limit reached")
	walkErr := filepath.WalkDir(workDir, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			if entry != nil && entry.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if path == workDir {
			return nil
		}
		scanned++
		if scanned > artifactLibraryMaxScanned {
			result.Truncated = true
			return errArtifactLibraryLimit
		}
		name := entry.Name()
		if entry.IsDir() {
			if strings.HasPrefix(name, ".") || artifactLibraryNoiseDirNames[name] {
				return filepath.SkipDir
			}
			return nil
		}
		if entry.Type()&fs.ModeSymlink != 0 {
			return nil
		}
		mimeType, previewKind, maxBytes, supported := artifactPreviewProperties(name)
		if !supported {
			return nil
		}
		info, err := entry.Info()
		if err != nil || !info.Mode().IsRegular() {
			return nil
		}
		rel, err := filepath.Rel(workDir, path)
		if err != nil {
			return nil
		}
		rel = filepath.ToSlash(rel)
		directory := filepath.ToSlash(filepath.Dir(rel))
		if directory == "." {
			directory = ""
		}
		item := ArtifactSummary{
			Path:        rel,
			Name:        filepath.Base(rel),
			Directory:   directory,
			MIMEType:    mimeType,
			PreviewKind: previewKind,
			Size:        info.Size(),
			ModifiedAt:  info.ModTime().UnixMilli(),
			Previewable: info.Size() <= maxBytes,
		}
		if !item.Previewable {
			item.Issue = fmt.Sprintf("exceeds the %d MiB preview limit", maxBytes>>20)
		}
		result.Artifacts = append(result.Artifacts, item)
		if len(result.Artifacts) >= artifactLibraryMaxItems {
			result.Truncated = true
			return errArtifactLibraryLimit
		}
		return nil
	})
	if walkErr != nil && !errors.Is(walkErr, errArtifactLibraryLimit) {
		return nil, fmt.Errorf("scan project artifacts: %w", walkErr)
	}

	sort.Slice(result.Artifacts, func(i, j int) bool {
		if result.Artifacts[i].ModifiedAt == result.Artifacts[j].ModifiedAt {
			return result.Artifacts[i].Path < result.Artifacts[j].Path
		}
		return result.Artifacts[i].ModifiedAt > result.Artifacts[j].ModifiedAt
	})
	versionPaths := make([]string, 0, len(result.Artifacts))
	for _, item := range result.Artifacts {
		if item.PreviewKind == "web" {
			versionPaths = append(versionPaths, item.Path)
		}
	}
	versionMetadata := artifactVersionLibraryMetadataForScope(projectID, sessionID, versionPaths)
	for i := range result.Artifacts {
		if metadata, ok := versionMetadata[result.Artifacts[i].Path]; ok {
			result.Artifacts[i].VersionCount = metadata.Count
			result.Artifacts[i].LatestVersionAt = metadata.LatestAt
		}
	}
	return result, nil
}

var artifactLibraryNoiseDirNames = map[string]bool{
	".git":         true,
	".gokin":       true,
	"node_modules": true,
	"vendor":       true,
	"target":       true,
	".venv":        true,
	"venv":         true,
	"__pycache__":  true,
}

// ReadArtifactContent reads a complete supported artifact beneath a project
// root. HTML/SVG stay text-only and origin-isolated; native Office/PDF files
// are bounded to 30 MiB and returned with a safe generated preview plus the
// original bytes for download/opening.
func (s *Studio) ReadArtifactContent(projectID, subPath string) (*ArtifactDocument, error) {
	s.mu.RLock()
	project := s.projects[projectID]
	s.mu.RUnlock()
	if project == nil {
		return nil, fmt.Errorf("project not found: %s", projectID)
	}
	project.mu.RLock()
	projectDir := project.Directory
	project.mu.RUnlock()
	return readArtifactContentAt(projectDir, subPath)
}

// ReadSessionArtifactContent reads from the same fail-closed worktree as the
// session's agent tools, terminal, Git review, and live preview server.
func (s *Studio) ReadSessionArtifactContent(projectID, sessionID, subPath string) (*ArtifactDocument, error) {
	project, session, err := s.projectSession(projectID, sessionID)
	if err != nil {
		return nil, err
	}
	workDir, err := sessionWorkingDirectory(project, session)
	if err != nil {
		return nil, err
	}
	return readArtifactContentAt(workDir, subPath)
}

func readArtifactContentAt(workDir, subPath string) (*ArtifactDocument, error) {
	root, rel, err := openProjectPath(workDir, subPath)
	if err != nil {
		return nil, err
	}
	defer root.Close()

	mimeType, previewKind, maxBytes, supported := artifactPreviewProperties(rel)
	if !supported {
		return nil, fmt.Errorf("artifact preview supports HTML, SVG, DOCX, XLSX, PPTX, and PDF files")
	}
	info, err := root.Stat(rel)
	if err != nil {
		return nil, fmt.Errorf("stat artifact: %w", err)
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("artifact is not a regular file")
	}
	if info.Size() > maxBytes {
		return nil, fmt.Errorf("artifact exceeds the %d MiB preview limit", maxBytes>>20)
	}
	file, err := root.Open(rel)
	if err != nil {
		return nil, fmt.Errorf("open artifact: %w", err)
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, maxBytes+1))
	if err != nil {
		return nil, fmt.Errorf("read artifact: %w", err)
	}
	if int64(len(data)) > maxBytes {
		return nil, fmt.Errorf("artifact exceeds the %d MiB preview limit", maxBytes>>20)
	}
	content := ""
	dataBase64 := ""
	if previewKind == "web" {
		if !utf8.Valid(data) || strings.IndexByte(string(data), 0) >= 0 {
			return nil, fmt.Errorf("artifact is not valid UTF-8 text")
		}
		content = string(data)
	} else {
		dataBase64 = base64.StdEncoding.EncodeToString(data)
		if previewKind != "pdf" {
			content, err = buildOfficeArtifactPreview(previewKind, data)
			if err != nil {
				return nil, fmt.Errorf("build %s preview: %w", previewKind, err)
			}
		}
	}
	return &ArtifactDocument{
		Path:        filepath.ToSlash(rel),
		Name:        filepath.Base(rel),
		MIMEType:    mimeType,
		Content:     content,
		DataBase64:  dataBase64,
		PreviewKind: previewKind,
		Size:        info.Size(),
		ModifiedAt:  info.ModTime().UnixMilli(),
	}, nil
}
