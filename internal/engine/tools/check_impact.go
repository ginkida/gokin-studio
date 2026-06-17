package tools

import (
	"context"
	"fmt"
	"regexp"
	"sort"
	"strings"

	"google.golang.org/genai"
)

// CheckImpactTool analyzes the blast radius of changing a symbol.
//
// Ported from gokin internal/tools/check_impact.go.
// Uses the in-package GrepTool engine so it's dependency-free and
// gitignore-aware — shelling out to system grep was flaky on hosts
// without grep in PATH and on directories with permission errors.
type CheckImpactTool struct {
	workDir string
	engine  *GrepTool
}

// NewCheckImpactTool creates a new CheckImpactTool.
func NewCheckImpactTool(workDir string) *CheckImpactTool {
	return &CheckImpactTool{
		workDir: workDir,
		engine:  NewGrepTool(workDir),
	}
}

func (t *CheckImpactTool) Name() string { return "check_impact" }

func (t *CheckImpactTool) Description() string {
	return `Blast Radius Analysis tool. Finds all usages and potential impacts of changing a symbol (function, variable, etc.).
Categorizes findings into Definitions, Imports, and Usages to help assess the risk of modification.`
}

func (t *CheckImpactTool) Declaration() *genai.FunctionDeclaration {
	return &genai.FunctionDeclaration{
		Name:        t.Name(),
		Description: t.Description(),
		Parameters: &genai.Schema{
			Type: genai.TypeObject,
			Properties: map[string]*genai.Schema{
				"symbol": {
					Type:        genai.TypeString,
					Description: "The symbol name to analyze (e.g., 'Agent', 'Run', 'executeTool')",
				},
			},
			Required: []string{"symbol"},
		},
	}
}

func (t *CheckImpactTool) Validate(args map[string]any) error {
	symbol, ok := GetString(args, "symbol")
	if !ok || symbol == "" {
		return NewValidationError("symbol", "is required")
	}
	return nil
}

func (t *CheckImpactTool) Execute(ctx context.Context, args map[string]any) (ToolResult, error) {
	symbol, _ := GetString(args, "symbol")

	files, err := t.engine.getFiles(t.workDir, "")
	if err != nil {
		return NewErrorResult(fmt.Sprintf("check_impact: search failed for %q: %v", symbol, err)), nil
	}
	re, err := regexp.Compile(regexp.QuoteMeta(symbol))
	if err != nil {
		return NewErrorResult(fmt.Sprintf("check_impact: bad symbol %q: %v", symbol, err)), nil
	}
	found := t.engine.searchParallel(ctx, files, re, 0, nil)
	if ctx.Err() != nil {
		return NewErrorResult(fmt.Sprintf("check_impact: search cancelled for %q: %v", symbol, ctx.Err())), nil
	}
	// Deterministic order (searchParallel collects from goroutines).
	sort.Slice(found, func(i, j int) bool { return found[i].path < found[j].path })

	var lines []string
	for _, fm := range found {
		for _, m := range fm.matches {
			lines = append(lines, fmt.Sprintf("%s:%d:%s", fm.path, m.lineNum, m.line))
		}
	}

	var report strings.Builder
	fmt.Fprintf(&report, "# Impact Report for symbol: %s\n\n", symbol)

	categories := map[string][]string{
		"Definitions": {},
		"Imports":     {},
		"Usages":      {},
	}

	for _, line := range lines {
		if line == "" {
			continue
		}
		lowerLine := strings.ToLower(line)
		if strings.Contains(lowerLine, "func ") || strings.Contains(lowerLine, "type ") || strings.Contains(lowerLine, "var ") {
			categories["Definitions"] = append(categories["Definitions"], line)
		} else if strings.Contains(lowerLine, "import ") || strings.Contains(lowerLine, "require(") {
			categories["Imports"] = append(categories["Imports"], line)
		} else {
			categories["Usages"] = append(categories["Usages"], line)
		}
	}

	// Fixed iteration order so the report is deterministic.
	for _, cat := range []string{"Definitions", "Imports", "Usages"} {
		matches := categories[cat]
		if len(matches) > 0 {
			fmt.Fprintf(&report, "## %s (%d)\n", cat, len(matches))
			limit := min(10, len(matches))
			for i := range limit {
				cleanLine := strings.TrimPrefix(matches[i], t.workDir)
				fmt.Fprintf(&report, "- %s\n", cleanLine)
			}
			if len(matches) > limit {
				fmt.Fprintf(&report, "- ... and %d more\n", len(matches)-limit)
			}
			report.WriteString("\n")
		}
	}

	if report.Len() < 100 {
		return NewSuccessResult(fmt.Sprintf("No significant impact found for symbol: %s. It might be private or unused.", symbol)), nil
	}

	return NewSuccessResult(report.String()), nil
}
