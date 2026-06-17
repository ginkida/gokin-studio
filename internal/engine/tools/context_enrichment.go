package tools

import (
	"bufio"
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// FilePredictor predicts related files based on access patterns and import graphs.
type FilePredictor interface {
	PredictFiles(currentFile string, limit int) []PredictedFile
}

// PredictedFile represents a predicted related file.
type PredictedFile struct {
	Path       string
	Confidence float64
	Reason     string
}

// ContextEnricher appends relevant project context to tool results after a
// write/edit so the model can spot inconsistencies immediately — e.g. it
// shows exported function signatures when editing a test file, or go-version
// info when editing a GitHub Actions workflow.
//
// The zero value (nil) is safe to use (Enrich returns "").
// Replaces the previous stub in stubs.go.
type ContextEnricher struct {
	workDir   string
	predictor FilePredictor
}

// NewContextEnricher creates a new enricher for the given project root.
func NewContextEnricher(workDir string) *ContextEnricher {
	return &ContextEnricher{workDir: workDir}
}

// SetPredictor sets the file predictor for prediction-based enrichment.
func (e *ContextEnricher) SetPredictor(p FilePredictor) {
	if e != nil {
		e.predictor = p
	}
}

// Enrich returns a context hint for the given file path, or empty string if
// no hint is applicable. nil-receiver safe.
func (e *ContextEnricher) Enrich(filePath string) string {
	if e == nil {
		return ""
	}
	var result string
	switch {
	case isGitHubWorkflowFile(filePath):
		result = e.enrichWorkflow(filePath)
	case strings.HasSuffix(filePath, "_test.go"):
		result = e.enrichTestFile(filePath)
	}

	if prediction := e.enrichFromPredictions(filePath); prediction != "" {
		if result != "" {
			result += "\n" + prediction
		} else {
			result = prediction
		}
	}
	return result
}

func (e *ContextEnricher) enrichFromPredictions(filePath string) string {
	if e.predictor == nil {
		return ""
	}
	predictions := e.predictor.PredictFiles(filePath, 3)
	if len(predictions) == 0 {
		return ""
	}

	var hints []string
	for _, p := range predictions {
		if p.Confidence < 0.3 {
			continue
		}
		summary := readFileSummaryLines(p.Path, 3)
		if summary != "" {
			hints = append(hints, fmt.Sprintf("%s (%s): %s",
				filepath.Base(p.Path), p.Reason, summary))
		}
	}
	if len(hints) == 0 {
		return ""
	}
	return "[context:predicted] Related files: " + strings.Join(hints, "; ")
}

// readFileSummaryLines reads the first n non-empty, non-comment lines from a file.
func readFileSummaryLines(path string, n int) string {
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()

	var result []string
	scanner := bufio.NewScanner(f)
	for len(result) < n && scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line != "" && !strings.HasPrefix(line, "//") && !strings.HasPrefix(line, "#") {
			result = append(result, line)
		}
	}
	return strings.Join(result, " | ")
}

var goVersionInWorkflowEnricherRe = regexp.MustCompile(`go-version:\s*['"]?([0-9]+\.[0-9]+[0-9.]*)['"]?`)

func (e *ContextEnricher) enrichWorkflow(filePath string) string {
	var hints []string

	if ver := readGoModVersionFromDir(e.workDir); ver != "" {
		hints = append(hints, fmt.Sprintf("go.mod specifies go %s", ver))
	}

	baseName := filepath.Base(filePath)
	workflowDir := filepath.Join(e.workDir, ".github", "workflows")
	entries, err := os.ReadDir(workflowDir)
	if err != nil {
		return formatContextHints(hints)
	}

	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || name == baseName {
			continue
		}
		if !strings.HasSuffix(name, ".yml") && !strings.HasSuffix(name, ".yaml") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(workflowDir, name))
		if err != nil {
			continue
		}
		if m := goVersionInWorkflowEnricherRe.FindStringSubmatch(string(data)); m != nil {
			hints = append(hints, fmt.Sprintf("%s uses go-version: %s", name, m[1]))
		}
	}

	return formatContextHints(hints)
}

var exportedFuncRe = regexp.MustCompile(`^func\s+([A-Z]\w*)\s*\(([^)]*)\)`)

func (e *ContextEnricher) enrichTestFile(testPath string) string {
	srcPath := strings.TrimSuffix(testPath, "_test.go") + ".go"
	data, err := os.ReadFile(srcPath)
	if err != nil {
		return ""
	}

	var sigs []string
	scanner := bufio.NewScanner(bytes.NewReader(data))
	for scanner.Scan() {
		line := scanner.Text()
		if m := exportedFuncRe.FindStringSubmatch(line); m != nil {
			sigs = append(sigs, fmt.Sprintf("func %s(%s)", m[1], m[2]))
		}
	}
	if len(sigs) == 0 {
		return ""
	}

	result := "[context] Tested file signatures: " + strings.Join(sigs, ", ")
	if runes := []rune(result); len(runes) > 500 {
		result = string(runes[:497]) + "..."
	}
	return result
}

func formatContextHints(hints []string) string {
	if len(hints) == 0 {
		return ""
	}
	return "[context] " + strings.Join(hints, "; ")
}

// readGoModVersionFromDir extracts the Go version from go.mod in dir.
func readGoModVersionFromDir(dir string) string {
	data, err := os.ReadFile(filepath.Join(dir, "go.mod"))
	if err != nil {
		return ""
	}
	scanner := bufio.NewScanner(bytes.NewReader(data))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if after, ok := strings.CutPrefix(line, "go "); ok {
			return strings.TrimSpace(after)
		}
	}
	return ""
}

// isGitHubWorkflowFile reports whether the path is a GitHub Actions workflow file.
func isGitHubWorkflowFile(filePath string) bool {
	return strings.Contains(filePath, ".github/workflows/") &&
		(strings.HasSuffix(filePath, ".yml") || strings.HasSuffix(filePath, ".yaml"))
}
