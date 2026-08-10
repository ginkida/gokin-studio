package studio

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"
)

const (
	projectFilePreviewMaxBytes = 100 * 1024
	projectDirectoryMaxEntries = 10_000
	semanticValidatorMaxBytes  = 2 << 20
)

// openProjectPath anchors subsequent operations to an open directory handle.
// os.Root follows relative symlinks only while they remain beneath root, and
// rejects absolute/outward symlinks without a check-then-open race.
func openProjectPath(projectDir, subPath string) (*os.Root, string, error) {
	rel, err := normalizeProjectSubPath(subPath)
	if err != nil {
		return nil, "", err
	}
	root, err := os.OpenRoot(projectDir)
	if err != nil {
		return nil, "", fmt.Errorf("open project directory: %w", err)
	}
	return root, rel, nil
}

// readProjectRegularFile resolves either a workspace-relative path or an
// absolute path beneath projectDir and returns at most maxBytes. It is used by
// internal post-write validators, whose tool arguments may use either form.
func readProjectRegularFile(projectDir, path string, maxBytes int64) ([]byte, string, error) {
	if maxBytes < 0 {
		return nil, "", fmt.Errorf("invalid file size limit")
	}
	relInput := path
	if filepath.IsAbs(path) {
		projectAbs, err := filepath.Abs(projectDir)
		if err != nil {
			return nil, "", err
		}
		relInput, err = filepath.Rel(projectAbs, path)
		if err != nil {
			return nil, "", fmt.Errorf("resolve project file: %w", err)
		}
	}
	root, rel, err := openProjectPath(projectDir, relInput)
	if err != nil {
		return nil, "", err
	}
	defer root.Close()
	info, err := root.Stat(rel)
	if err != nil {
		return nil, "", err
	}
	if !info.Mode().IsRegular() {
		return nil, "", fmt.Errorf("project path is not a regular file")
	}
	if info.Size() > maxBytes {
		return nil, "", fmt.Errorf("project file exceeds %d bytes", maxBytes)
	}
	f, err := root.Open(rel)
	if err != nil {
		return nil, "", err
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, maxBytes+1))
	if err != nil {
		return nil, "", err
	}
	if int64(len(data)) > maxBytes {
		return nil, "", fmt.Errorf("project file exceeds %d bytes", maxBytes)
	}
	return data, filepath.Join(projectDir, rel), nil
}

func normalizeProjectSubPath(subPath string) (string, error) {
	if !utf8.ValidString(subPath) || strings.ContainsRune(subPath, 0) {
		return "", fmt.Errorf("invalid project path")
	}
	if subPath == "" {
		return ".", nil
	}
	rel := filepath.Clean(filepath.FromSlash(subPath))
	if filepath.IsAbs(rel) || filepath.VolumeName(rel) != "" || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("path outside project directory")
	}
	return rel, nil
}

func readProjectTextFile(projectDir, subPath string) (string, error) {
	root, rel, err := openProjectPath(projectDir, subPath)
	if err != nil {
		return "", err
	}
	defer root.Close()

	// Stat before Open rejects directories, devices, sockets and named pipes.
	// In particular, opening a FIFO for reading can block forever waiting for a
	// writer; regular-file-only previews avoid that class of UI hang.
	info, err := root.Stat(rel)
	if err != nil {
		return "", fmt.Errorf("stat project file: %w", err)
	}
	if !info.Mode().IsRegular() {
		return "", fmt.Errorf("project path is not a regular file")
	}
	f, err := root.Open(rel)
	if err != nil {
		return "", fmt.Errorf("open project file: %w", err)
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, projectFilePreviewMaxBytes+1))
	if err != nil {
		return "", fmt.Errorf("read project file: %w", err)
	}
	truncated := info.Size() > projectFilePreviewMaxBytes || len(data) > projectFilePreviewMaxBytes
	if len(data) > projectFilePreviewMaxBytes {
		data = data[:projectFilePreviewMaxBytes]
	}
	// A byte cap may split one UTF-8 rune. Remove at most its trailing bytes;
	// invalid data elsewhere is binary/non-text and should not be sent through
	// the JSON bridge or embedded into an LLM prompt.
	if !utf8.Valid(data) && truncated {
		for removed := 0; removed < utf8.UTFMax-1 && len(data) > 0 && !utf8.Valid(data); removed++ {
			data = data[:len(data)-1]
		}
	}
	if !utf8.Valid(data) || strings.IndexByte(string(data), 0) >= 0 {
		return "", fmt.Errorf("project file is not valid UTF-8 text")
	}
	text := string(data)
	if truncated {
		text += "\n\n[truncated at 100KB]"
	}
	return text, nil
}
