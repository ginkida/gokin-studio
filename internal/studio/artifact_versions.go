package studio

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/google/uuid"
)

const (
	artifactVersionSchema          = 1
	artifactVersionsPerArtifact    = 20
	artifactHistoriesPerProject    = 64
	artifactVersionManifestMaxSize = 128 << 10
	artifactVersionProjectMaxBytes = 128 << 20
)

// ArtifactVersion is immutable metadata for one saved HTML/SVG revision.
// Content is stored separately and deduplicated by Digest.
type ArtifactVersion struct {
	ID               string `json:"id"`
	Digest           string `json:"digest"`
	SessionID        string `json:"sessionID,omitempty"`
	Path             string `json:"path"`
	Name             string `json:"name"`
	MIMEType         string `json:"mimeType"`
	Size             int64  `json:"size"`
	CreatedAt        int64  `json:"createdAt"`
	SourceModifiedAt int64  `json:"sourceModifiedAt"`
}

type artifactVersionManifest struct {
	Schema   int               `json:"schema"`
	Path     string            `json:"path"`
	Versions []ArtifactVersion `json:"versions"`
}

var artifactVersionsMu sync.Mutex

type artifactLibraryVersionMetadata struct {
	Count    int
	LatestAt int64
}

func artifactVersionLibraryMetadata(projectID string, paths []string) map[string]artifactLibraryVersionMetadata {
	return artifactVersionLibraryMetadataForScope(projectID, "", paths)
}

func artifactVersionLibraryMetadataForScope(projectID, sessionID string, paths []string) map[string]artifactLibraryVersionMetadata {
	out := make(map[string]artifactLibraryVersionMetadata)
	if len(paths) == 0 {
		return out
	}
	artifactVersionsMu.Lock()
	defer artifactVersionsMu.Unlock()
	root, err := openArtifactVersionStorage()
	if err != nil {
		return out
	}
	defer root.Close()
	for _, artifactPath := range paths {
		relDir := artifactVersionRelativeDirForScope(projectID, sessionID, artifactPath)
		manifest, err := loadArtifactVersionManifest(root, relDir, artifactPath)
		if err != nil || len(manifest.Versions) == 0 {
			continue
		}
		out[artifactPath] = artifactLibraryVersionMetadata{
			Count:    len(manifest.Versions),
			LatestAt: manifest.Versions[len(manifest.Versions)-1].CreatedAt,
		}
	}
	return out
}

func normalizeArtifactVersionPath(subPath string) (string, error) {
	rel, err := normalizeProjectSubPath(subPath)
	if err != nil {
		return "", err
	}
	if rel == "." {
		return "", fmt.Errorf("artifact path is required")
	}
	switch strings.ToLower(filepath.Ext(rel)) {
	case ".html", ".htm", ".svg":
	default:
		return "", fmt.Errorf("artifact versions support only HTML and SVG files")
	}
	return filepath.ToSlash(rel), nil
}

func artifactVersionRelativeDir(projectID, artifactPath string) string {
	return artifactVersionRelativeDirForScope(projectID, "", artifactPath)
}

func artifactVersionRelativeDirForScope(projectID, sessionID, artifactPath string) string {
	digestInput := artifactPath
	if sessionID != "" {
		digestInput = "session\x00" + sessionID + "\x00" + artifactPath
	}
	sum := sha256.Sum256([]byte(digestInput))
	return filepath.Join(
		"artifact_versions",
		safeStorageKey(projectID),
		hex.EncodeToString(sum[:16]),
	)
}

func artifactVersionContentName(digest string) string {
	return "content-" + digest + ".bin"
}

func openArtifactVersionStorage() (*os.Root, error) {
	if err := os.MkdirAll(configDir(), 0o700); err != nil {
		return nil, fmt.Errorf("create config directory: %w", err)
	}
	root, err := os.OpenRoot(configDir())
	if err != nil {
		return nil, fmt.Errorf("open config directory: %w", err)
	}
	if err := ensureRootDirectory(root, "artifact_versions"); err != nil {
		root.Close()
		return nil, err
	}
	return root, nil
}

// ensureRootDirectory creates a private relative directory without accepting
// symlink components. os.Root already prevents outward traversal; rejecting
// links also prevents a tampered config tree from redirecting version data to
// an unexpected location elsewhere inside the config directory.
func ensureRootDirectory(root *os.Root, rel string) error {
	clean := filepath.Clean(rel)
	if clean == "." || clean == "" {
		return nil
	}
	current := ""
	for _, part := range strings.Split(clean, string(filepath.Separator)) {
		if part == "" || part == "." || part == ".." {
			return fmt.Errorf("invalid private storage path")
		}
		current = filepath.Join(current, part)
		info, err := root.Lstat(current)
		if errors.Is(err, fs.ErrNotExist) {
			if err := root.Mkdir(current, 0o700); err != nil && !errors.Is(err, fs.ErrExist) {
				return fmt.Errorf("create artifact version directory: %w", err)
			}
			info, err = root.Lstat(current)
		}
		if err != nil {
			return fmt.Errorf("inspect artifact version directory: %w", err)
		}
		if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
			return fmt.Errorf("artifact version storage contains a non-directory or symlink component")
		}
	}
	return nil
}

func readRootRegularFileLimited(root *os.Root, rel string, maxBytes int64) ([]byte, error) {
	info, err := root.Lstat(rel)
	if err != nil {
		return nil, err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return nil, fmt.Errorf("artifact version storage path is not a regular file")
	}
	if info.Size() > maxBytes {
		return nil, fmt.Errorf("artifact version storage file exceeds %d bytes", maxBytes)
	}
	file, err := root.Open(rel)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	opened, err := file.Stat()
	if err != nil {
		return nil, err
	}
	if !os.SameFile(info, opened) || !opened.Mode().IsRegular() {
		return nil, fmt.Errorf("artifact version storage file changed while opening")
	}
	data, err := io.ReadAll(io.LimitReader(file, maxBytes+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > maxBytes {
		return nil, fmt.Errorf("artifact version storage file exceeds %d bytes", maxBytes)
	}
	return data, nil
}

func writeRootFileAtomic(root *os.Root, rel string, data []byte, mode os.FileMode) error {
	dir := filepath.Dir(rel)
	if err := ensureRootDirectory(root, dir); err != nil {
		return err
	}
	temp := filepath.Join(dir, "."+filepath.Base(rel)+".tmp-"+uuid.NewString())
	file, err := root.OpenFile(temp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode)
	if err != nil {
		return err
	}
	cleanup := true
	defer func() {
		if cleanup {
			_ = root.Remove(temp)
		}
	}()
	if _, err := file.Write(data); err != nil {
		_ = file.Close()
		return err
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	if err := root.Rename(temp, rel); err != nil {
		return err
	}
	cleanup = false
	return nil
}

func validArtifactVersionDigest(value string) bool {
	if len(value) != sha256.Size*2 {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}

func loadArtifactVersionManifest(root *os.Root, relDir, expectedPath string) (artifactVersionManifest, error) {
	manifest := artifactVersionManifest{Schema: artifactVersionSchema, Path: expectedPath}
	data, err := readRootRegularFileLimited(
		root,
		filepath.Join(relDir, "manifest.json"),
		artifactVersionManifestMaxSize,
	)
	if errors.Is(err, fs.ErrNotExist) {
		return manifest, nil
	}
	if err != nil {
		return artifactVersionManifest{}, err
	}
	if err := json.Unmarshal(data, &manifest); err != nil {
		return artifactVersionManifest{}, fmt.Errorf("parse artifact version manifest: %w", err)
	}
	if manifest.Schema != artifactVersionSchema {
		return artifactVersionManifest{}, fmt.Errorf("unsupported artifact version manifest schema")
	}
	if expectedPath != "" && manifest.Path != expectedPath {
		return artifactVersionManifest{}, fmt.Errorf("artifact version manifest path mismatch")
	}
	if len(manifest.Versions) > artifactVersionsPerArtifact {
		return artifactVersionManifest{}, fmt.Errorf("artifact version manifest exceeds the %d-version limit", artifactVersionsPerArtifact)
	}
	seenIDs := make(map[string]bool, len(manifest.Versions))
	expectedSessionID := ""
	for index, version := range manifest.Versions {
		if index == 0 {
			expectedSessionID = version.SessionID
		}
		if version.ID == "" || len(version.ID) > 128 || seenIDs[version.ID] ||
			version.Path != manifest.Path || version.Size < 0 || version.Size > artifactPreviewMaxBytes ||
			len(version.SessionID) > 128 || !utf8.ValidString(version.SessionID) || version.SessionID != expectedSessionID ||
			!validArtifactVersionDigest(version.Digest) || version.CreatedAt <= 0 ||
			version.Name == "" || len(version.Name) > 256 || !utf8.ValidString(version.Name) ||
			strings.IndexFunc(version.Name, unicode.IsControl) >= 0 ||
			(version.MIMEType != "text/html" && version.MIMEType != "image/svg+xml") {
			return artifactVersionManifest{}, fmt.Errorf("artifact version manifest contains invalid metadata")
		}
		seenIDs[version.ID] = true
	}
	return manifest, nil
}

func saveArtifactVersionManifest(root *os.Root, relDir string, manifest artifactVersionManifest) error {
	data, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	if len(data) > artifactVersionManifestMaxSize {
		return fmt.Errorf("artifact version manifest exceeds %d bytes", artifactVersionManifestMaxSize)
	}
	return writeRootFileAtomic(root, filepath.Join(relDir, "manifest.json"), data, 0o600)
}

func artifactVersionProjectUsage(root *os.Root, projectRel, excludeDir string) (int64, int, error) {
	dir, err := root.Open(projectRel)
	if errors.Is(err, fs.ErrNotExist) {
		return 0, 0, nil
	}
	if err != nil {
		return 0, 0, err
	}
	defer dir.Close()
	histories := 0
	var total int64
	inspected := 0
	for {
		entries, readErr := dir.ReadDir(32)
		for _, entry := range entries {
			inspected++
			if inspected > artifactHistoriesPerProject*4 {
				return 0, histories, fmt.Errorf("artifact history directory contains too many entries")
			}
			if !entry.IsDir() {
				continue
			}
			histories++
			if histories > artifactHistoriesPerProject {
				return 0, histories, fmt.Errorf("project exceeds the %d-artifact history limit", artifactHistoriesPerProject)
			}
			relDir := filepath.Join(projectRel, entry.Name())
			info, err := root.Lstat(relDir)
			if err != nil {
				return 0, 0, err
			}
			if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
				return 0, 0, fmt.Errorf("artifact history is not a private directory")
			}
			if relDir == excludeDir {
				continue
			}
			manifest, err := loadArtifactVersionManifest(root, relDir, "")
			if err != nil {
				return 0, 0, err
			}
			seenDigests := make(map[string]bool, len(manifest.Versions))
			for _, version := range manifest.Versions {
				if !seenDigests[version.Digest] {
					total += version.Size
					seenDigests[version.Digest] = true
				}
			}
		}
		if errors.Is(readErr, io.EOF) {
			break
		}
		if readErr != nil {
			return 0, 0, readErr
		}
	}
	return total, histories, nil
}

func readStoredArtifactVersion(root *os.Root, relDir string, version ArtifactVersion) ([]byte, error) {
	data, err := readRootRegularFileLimited(
		root,
		filepath.Join(relDir, artifactVersionContentName(version.Digest)),
		artifactPreviewMaxBytes,
	)
	if err != nil {
		return nil, err
	}
	sum := sha256.Sum256(data)
	if hex.EncodeToString(sum[:]) != version.Digest || int64(len(data)) != version.Size {
		return nil, fmt.Errorf("artifact version content failed integrity verification")
	}
	if !utf8.Valid(data) || bytes.IndexByte(data, 0) >= 0 {
		return nil, fmt.Errorf("artifact version content is not valid UTF-8 text")
	}
	return data, nil
}

// SaveArtifactVersion snapshots the current artifact when its content differs
// from the latest revision. Repeated live-preview polling is therefore cheap
// and does not create duplicate history entries.
func (s *Studio) SaveArtifactVersion(projectID, subPath string) (*ArtifactVersion, error) {
	document, err := s.ReadArtifactContent(projectID, subPath)
	if err != nil {
		return nil, err
	}
	return saveArtifactVersionDocument(projectID, "", document)
}

// SaveSessionArtifactVersion snapshots the file visible inside one chat's
// worktree. Histories are namespaced per session so identical relative paths
// in independent checkouts cannot overwrite or restore each other's content.
func (s *Studio) SaveSessionArtifactVersion(projectID, sessionID, subPath string) (*ArtifactVersion, error) {
	document, err := s.ReadSessionArtifactContent(projectID, sessionID, subPath)
	if err != nil {
		return nil, err
	}
	if sessionID == "" {
		sessionID = "default"
	}
	return saveArtifactVersionDocument(projectID, sessionID, document)
}

func saveArtifactVersionDocument(projectID, sessionID string, document *ArtifactDocument) (*ArtifactVersion, error) {
	artifactPath, err := normalizeArtifactVersionPath(document.Path)
	if err != nil {
		return nil, err
	}
	sum := sha256.Sum256([]byte(document.Content))
	digest := hex.EncodeToString(sum[:])
	relDir := artifactVersionRelativeDirForScope(projectID, sessionID, artifactPath)
	projectRel := filepath.Dir(relDir)

	artifactVersionsMu.Lock()
	defer artifactVersionsMu.Unlock()
	root, err := openArtifactVersionStorage()
	if err != nil {
		return nil, err
	}
	defer root.Close()
	createdDir := false
	if info, statErr := root.Lstat(relDir); errors.Is(statErr, fs.ErrNotExist) {
		createdDir = true
	} else if statErr != nil {
		return nil, statErr
	} else if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return nil, fmt.Errorf("artifact version history is not a private directory")
	}
	if err := ensureRootDirectory(root, relDir); err != nil {
		return nil, err
	}
	committed := false
	defer func() {
		if createdDir && !committed {
			_ = root.Remove(relDir)
		}
	}()
	manifest, err := loadArtifactVersionManifest(root, relDir, artifactPath)
	if err != nil {
		return nil, err
	}
	if len(manifest.Versions) > 0 && manifest.Versions[len(manifest.Versions)-1].Digest == digest {
		version := manifest.Versions[len(manifest.Versions)-1]
		return &version, nil
	}

	version := ArtifactVersion{
		ID:               uuid.NewString(),
		Digest:           digest,
		SessionID:        sessionID,
		Path:             artifactPath,
		Name:             document.Name,
		MIMEType:         document.MIMEType,
		Size:             int64(len(document.Content)),
		CreatedAt:        time.Now().UnixMilli(),
		SourceModifiedAt: document.ModifiedAt,
	}
	manifest.Versions = append(manifest.Versions, version)
	if len(manifest.Versions) > artifactVersionsPerArtifact {
		manifest.Versions = append([]ArtifactVersion(nil), manifest.Versions[len(manifest.Versions)-artifactVersionsPerArtifact:]...)
	}

	otherUsage, histories, err := artifactVersionProjectUsage(root, projectRel, relDir)
	if err != nil {
		return nil, err
	}
	if histories > artifactHistoriesPerProject {
		return nil, fmt.Errorf("project may retain history for at most %d artifacts", artifactHistoriesPerProject)
	}
	total := otherUsage
	currentDigests := make(map[string]bool, len(manifest.Versions))
	for _, item := range manifest.Versions {
		if !currentDigests[item.Digest] {
			total += item.Size
			currentDigests[item.Digest] = true
		}
	}
	if total > artifactVersionProjectMaxBytes {
		return nil, fmt.Errorf("project artifact history exceeds the %d MiB limit", artifactVersionProjectMaxBytes>>20)
	}

	contentRel := filepath.Join(relDir, artifactVersionContentName(digest))
	wroteContent := false
	if existing, err := readRootRegularFileLimited(root, contentRel, artifactPreviewMaxBytes); err == nil {
		if !bytes.Equal(existing, []byte(document.Content)) {
			return nil, fmt.Errorf("artifact version digest collision or corrupt content")
		}
	} else if errors.Is(err, fs.ErrNotExist) {
		if err := writeRootFileAtomic(root, contentRel, []byte(document.Content), 0o600); err != nil {
			return nil, err
		}
		wroteContent = true
	} else {
		return nil, err
	}
	if err := saveArtifactVersionManifest(root, relDir, manifest); err != nil {
		if wroteContent {
			_ = root.Remove(contentRel)
		}
		return nil, err
	}
	committed = true

	keep := make(map[string]bool, len(manifest.Versions))
	for _, item := range manifest.Versions {
		keep[item.Digest] = true
	}
	dir, err := root.Open(relDir)
	if err == nil {
		if entries, readErr := dir.ReadDir(artifactVersionsPerArtifact*2 + 16); readErr == nil || errors.Is(readErr, io.EOF) {
			for _, entry := range entries {
				name := entry.Name()
				if strings.HasPrefix(name, "content-") && strings.HasSuffix(name, ".bin") {
					digest := strings.TrimSuffix(strings.TrimPrefix(name, "content-"), ".bin")
					if !keep[digest] {
						_ = root.Remove(filepath.Join(relDir, name))
					}
				}
			}
		}
		dir.Close()
	}
	return &version, nil
}

func (s *Studio) ListArtifactVersions(projectID, subPath string) ([]ArtifactVersion, error) {
	if _, err := s.projectDirectoryForArtifact(projectID); err != nil {
		return nil, err
	}
	return listArtifactVersionsForScope(projectID, "", subPath)
}

func (s *Studio) ListSessionArtifactVersions(projectID, sessionID, subPath string) ([]ArtifactVersion, error) {
	if _, _, err := s.projectSession(projectID, sessionID); err != nil {
		return nil, err
	}
	if sessionID == "" {
		sessionID = "default"
	}
	return listArtifactVersionsForScope(projectID, sessionID, subPath)
}

func listArtifactVersionsForScope(projectID, sessionID, subPath string) ([]ArtifactVersion, error) {
	artifactPath, err := normalizeArtifactVersionPath(subPath)
	if err != nil {
		return nil, err
	}
	relDir := artifactVersionRelativeDirForScope(projectID, sessionID, artifactPath)

	artifactVersionsMu.Lock()
	defer artifactVersionsMu.Unlock()
	root, err := openArtifactVersionStorage()
	if err != nil {
		return nil, err
	}
	defer root.Close()
	manifest, err := loadArtifactVersionManifest(root, relDir, artifactPath)
	if err != nil {
		return nil, err
	}
	out := make([]ArtifactVersion, len(manifest.Versions))
	for i := range manifest.Versions {
		out[i] = manifest.Versions[len(manifest.Versions)-1-i]
	}
	return out, nil
}

func (s *Studio) ReadArtifactVersion(projectID, subPath, versionID string) (*ArtifactDocument, error) {
	if _, err := s.projectDirectoryForArtifact(projectID); err != nil {
		return nil, err
	}
	return readArtifactVersionForScope(projectID, "", subPath, versionID)
}

func (s *Studio) ReadSessionArtifactVersion(projectID, sessionID, subPath, versionID string) (*ArtifactDocument, error) {
	if _, _, err := s.projectSession(projectID, sessionID); err != nil {
		return nil, err
	}
	if sessionID == "" {
		sessionID = "default"
	}
	return readArtifactVersionForScope(projectID, sessionID, subPath, versionID)
}

func readArtifactVersionForScope(projectID, sessionID, subPath, versionID string) (*ArtifactDocument, error) {
	artifactPath, err := normalizeArtifactVersionPath(subPath)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(versionID) == "" || len(versionID) > 128 {
		return nil, fmt.Errorf("invalid artifact version ID")
	}
	relDir := artifactVersionRelativeDirForScope(projectID, sessionID, artifactPath)

	artifactVersionsMu.Lock()
	defer artifactVersionsMu.Unlock()
	root, err := openArtifactVersionStorage()
	if err != nil {
		return nil, err
	}
	defer root.Close()
	manifest, err := loadArtifactVersionManifest(root, relDir, artifactPath)
	if err != nil {
		return nil, err
	}
	for _, version := range manifest.Versions {
		if version.ID != versionID {
			continue
		}
		data, err := readStoredArtifactVersion(root, relDir, version)
		if err != nil {
			return nil, err
		}
		return &ArtifactDocument{
			Path:        version.Path,
			Name:        version.Name,
			MIMEType:    version.MIMEType,
			Content:     string(data),
			PreviewKind: "web",
			Size:        version.Size,
			ModifiedAt:  version.SourceModifiedAt,
		}, nil
	}
	return nil, fmt.Errorf("artifact version not found")
}

func (s *Studio) projectDirectoryForArtifact(projectID string) (string, error) {
	s.mu.RLock()
	project := s.projects[projectID]
	s.mu.RUnlock()
	if project == nil {
		return "", fmt.Errorf("project not found: %s", projectID)
	}
	project.mu.RLock()
	directory := project.Directory
	project.mu.RUnlock()
	return directory, nil
}

func (s *Studio) beginArtifactRestore(projectID string) (func(), error) {
	s.mu.RLock()
	project := s.projects[projectID]
	s.mu.RUnlock()
	if project == nil {
		return nil, fmt.Errorf("project not found: %s", projectID)
	}
	project.mu.Lock()
	if project.artifactRestoreActive {
		project.mu.Unlock()
		return nil, fmt.Errorf("another artifact restore is already running")
	}
	for _, session := range project.sessions {
		session.mu.RLock()
		active := session.active
		session.mu.RUnlock()
		if active {
			project.mu.Unlock()
			return nil, fmt.Errorf("an agent is running in this project; stop it before restoring an artifact")
		}
	}
	project.artifactRestoreActive = true
	project.mu.Unlock()
	var once sync.Once
	return func() {
		once.Do(func() {
			project.mu.Lock()
			project.artifactRestoreActive = false
			project.mu.Unlock()
		})
	}, nil
}

func removeArtifactVersionsFor(projectID string) error {
	artifactVersionsMu.Lock()
	defer artifactVersionsMu.Unlock()
	root, err := openArtifactVersionStorage()
	if err != nil {
		return err
	}
	defer root.Close()
	rel := filepath.Join("artifact_versions", safeStorageKey(projectID))
	info, err := root.Lstat(rel)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return fmt.Errorf("project artifact version storage is not a private directory")
	}
	return root.RemoveAll(rel)
}

func removeArtifactVersionsForSession(projectID, sessionID string) error {
	if sessionID == "" {
		sessionID = "default"
	}
	artifactVersionsMu.Lock()
	defer artifactVersionsMu.Unlock()
	root, err := openArtifactVersionStorage()
	if err != nil {
		return err
	}
	defer root.Close()
	projectRel := filepath.Join("artifact_versions", safeStorageKey(projectID))
	dir, err := root.Open(projectRel)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	defer dir.Close()
	entries, err := dir.ReadDir(artifactHistoriesPerProject*4 + 1)
	if err != nil && !errors.Is(err, io.EOF) {
		return err
	}
	if len(entries) > artifactHistoriesPerProject*4 {
		return fmt.Errorf("artifact history directory contains too many entries")
	}
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		relDir := filepath.Join(projectRel, entry.Name())
		manifest, err := loadArtifactVersionManifest(root, relDir, "")
		if err != nil {
			return err
		}
		if len(manifest.Versions) > 0 && manifest.Versions[0].SessionID == sessionID {
			if err := root.RemoveAll(relDir); err != nil {
				return err
			}
		}
	}
	return nil
}

// RestoreArtifactVersion atomically replaces the project artifact after first
// preserving its current content. A deleted artifact can be recreated.
func (s *Studio) RestoreArtifactVersion(projectID, subPath, versionID string) (*ArtifactDocument, error) {
	projectDir, err := s.projectDirectoryForArtifact(projectID)
	if err != nil {
		return nil, err
	}
	return s.restoreArtifactVersionForScope(projectID, "", projectDir, subPath, versionID)
}

// RestoreSessionArtifactVersion updates only the selected chat's checkout.
// The project-wide restore barrier still prevents a concurrent agent turn
// from racing an atomic replacement in any worktree.
func (s *Studio) RestoreSessionArtifactVersion(projectID, sessionID, subPath, versionID string) (*ArtifactDocument, error) {
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
	return s.restoreArtifactVersionForScope(projectID, sessionID, workDir, subPath, versionID)
}

func (s *Studio) restoreArtifactVersionForScope(projectID, sessionID, workDir, subPath, versionID string) (*ArtifactDocument, error) {
	artifactPath, err := normalizeArtifactVersionPath(subPath)
	if err != nil {
		return nil, err
	}
	finishRestore, err := s.beginArtifactRestore(projectID)
	if err != nil {
		return nil, err
	}
	defer finishRestore()
	var preserveErr error
	if sessionID == "" {
		_, preserveErr = s.SaveArtifactVersion(projectID, artifactPath)
	} else {
		_, preserveErr = s.SaveSessionArtifactVersion(projectID, sessionID, artifactPath)
	}
	if preserveErr != nil && !errors.Is(preserveErr, fs.ErrNotExist) {
		return nil, fmt.Errorf("preserve current artifact before restore: %w", preserveErr)
	}
	var versionDocument *ArtifactDocument
	if sessionID == "" {
		versionDocument, err = s.ReadArtifactVersion(projectID, artifactPath, versionID)
	} else {
		versionDocument, err = s.ReadSessionArtifactVersion(projectID, sessionID, artifactPath, versionID)
	}
	if err != nil {
		return nil, err
	}

	root, rel, err := openProjectPath(workDir, artifactPath)
	if err != nil {
		return nil, err
	}
	defer root.Close()
	parent := filepath.Dir(rel)
	if err := ensureRootDirectory(root, parent); err != nil {
		return nil, fmt.Errorf("prepare artifact restore path: %w", err)
	}
	mode := os.FileMode(0o600)
	if info, statErr := root.Lstat(rel); statErr == nil {
		if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
			return nil, fmt.Errorf("artifact restore target is not a regular file")
		}
		mode = info.Mode().Perm()
	} else if !errors.Is(statErr, fs.ErrNotExist) {
		return nil, statErr
	}
	if err := writeRootFileAtomic(root, rel, []byte(versionDocument.Content), mode); err != nil {
		return nil, fmt.Errorf("restore artifact: %w", err)
	}
	var restored *ArtifactDocument
	if sessionID == "" {
		restored, err = s.ReadArtifactContent(projectID, artifactPath)
	} else {
		restored, err = s.ReadSessionArtifactContent(projectID, sessionID, artifactPath)
	}
	if err != nil {
		return nil, err
	}
	if sessionID == "" {
		_, err = s.SaveArtifactVersion(projectID, artifactPath)
	} else {
		_, err = s.SaveSessionArtifactVersion(projectID, sessionID, artifactPath)
	}
	if err != nil {
		return nil, fmt.Errorf("record restored artifact version: %w", err)
	}
	return restored, nil
}
