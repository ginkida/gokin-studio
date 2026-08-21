package studio

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"github.com/ginkida/gokin-studio/internal/engine/tools"
	wailsRuntime "github.com/wailsapp/wails/v2/pkg/runtime"
	"google.golang.org/genai"
)

const (
	maxCodeReviewFindings   = 20
	maxCodeReviewTitleRunes = 160
	maxCodeReviewBodyRunes  = 2000
)

var codeReviewHunkRE = regexp.MustCompile(`^@@ -(\d+)(?:,\d+)? \+(\d+)(?:,\d+)? @@`)

type CodeReviewFinding struct {
	ID       string `json:"id"`
	Path     string `json:"path"`
	Side     string `json:"side"`
	Line     int    `json:"line"`
	Severity string `json:"severity"`
	Title    string `json:"title"`
	Body     string `json:"body"`
}

type storedCodeReview struct {
	Fingerprint string
	Findings    []CodeReviewFinding
}

type submitCodeReviewTool struct {
	studio    *Studio
	projectID string
}

func (t *submitCodeReviewTool) Name() string { return "submit_code_review" }

func (t *submitCodeReviewTool) Description() string {
	return "Submit a bounded set of definite, high-signal findings for the current reviewed diff so they appear inline. " +
		"Call exactly once after review, including an empty findings array when no actionable issue exists. Do not report style, lint, speculation, or pre-existing issues."
}

func (t *submitCodeReviewTool) Declaration() *genai.FunctionDeclaration {
	return &genai.FunctionDeclaration{Name: t.Name(), Description: t.Description(), Parameters: &genai.Schema{
		Type:     genai.TypeObject,
		Required: []string{"session_id", "fingerprint", "findings"},
		Properties: map[string]*genai.Schema{
			"session_id":  {Type: genai.TypeString, Description: "Exact session ID supplied in the review request."},
			"fingerprint": {Type: genai.TypeString, Description: "Exact diff fingerprint supplied in the review request."},
			"findings": {Type: genai.TypeArray, Description: "Zero to 20 actionable findings.", Items: &genai.Schema{
				Type:     genai.TypeObject,
				Required: []string{"path", "side", "line", "severity", "title", "body"},
				Properties: map[string]*genai.Schema{
					"path":     {Type: genai.TypeString, Description: "Exact changed-file path."},
					"side":     {Type: genai.TypeString, Enum: []string{"old", "new"}},
					"line":     {Type: genai.TypeInteger, Description: "Exact visible diff line on the selected side."},
					"severity": {Type: genai.TypeString, Enum: []string{"critical", "high", "medium", "low"}},
					"title":    {Type: genai.TypeString, Description: "Concise defect title."},
					"body":     {Type: genai.TypeString, Description: "Why this is a defect, including the triggering scenario."},
				},
			}},
		},
	}}
}

func (t *submitCodeReviewTool) Validate(args map[string]any) error {
	if strings.TrimSpace(tools.GetStringDefault(args, "fingerprint", "")) == "" {
		return fmt.Errorf("fingerprint is required")
	}
	if strings.TrimSpace(tools.GetStringDefault(args, "session_id", "")) == "" {
		return fmt.Errorf("session_id is required")
	}
	if _, exists := args["findings"]; !exists {
		return fmt.Errorf("findings is required")
	}
	return nil
}

func (t *submitCodeReviewTool) Execute(_ context.Context, args map[string]any) (tools.ToolResult, error) {
	fingerprint := strings.TrimSpace(tools.GetStringDefault(args, "fingerprint", ""))
	sessionID := strings.TrimSpace(tools.GetStringDefault(args, "session_id", ""))
	raw, ok := args["findings"].([]any)
	if !ok {
		return tools.NewErrorResult("findings must be an array"), nil
	}
	if len(raw) > maxCodeReviewFindings {
		return tools.NewErrorResult(fmt.Sprintf("at most %d findings are allowed", maxCodeReviewFindings)), nil
	}
	findings := make([]CodeReviewFinding, 0, len(raw))
	for index, item := range raw {
		value, ok := item.(map[string]any)
		if !ok {
			return tools.NewErrorResult(fmt.Sprintf("finding %d must be an object", index+1)), nil
		}
		line, lineOK := tools.GetInt(value, "line")
		if !lineOK {
			return tools.NewErrorResult(fmt.Sprintf("finding %d line must be an integer", index+1)), nil
		}
		findings = append(findings, CodeReviewFinding{
			Path:     strings.TrimSpace(tools.GetStringDefault(value, "path", "")),
			Side:     strings.ToLower(strings.TrimSpace(tools.GetStringDefault(value, "side", ""))),
			Line:     line,
			Severity: strings.ToLower(strings.TrimSpace(tools.GetStringDefault(value, "severity", ""))),
			Title:    strings.TrimSpace(tools.GetStringDefault(value, "title", "")),
			Body:     strings.TrimSpace(tools.GetStringDefault(value, "body", "")),
		})
	}
	if err := t.studio.storeCodeReviewFindings(t.projectID, sessionID, fingerprint, findings); err != nil {
		return tools.NewErrorResult(err.Error()), nil
	}
	return tools.NewSuccessResult(fmt.Sprintf("Published %d inline code review finding(s) for diff %s.", len(findings), fingerprint)), nil
}

func codeReviewKey(projectID, sessionID string) string { return projectID + "\x00" + sessionID }

func codeReviewRequestPrompt(sessionID, fingerprint string) string {
	return fmt.Sprintf("Perform a focused code review of the current uncommitted diff. Use review_changes and read surrounding code where needed. Report only definite, actionable issues introduced by these changes: compile/runtime failures, security vulnerabilities, logic errors, regressions, or materially missing tests. Do not report style, lint, speculation, or pre-existing problems. Do not modify files.\n\nWhen finished, call submit_code_review exactly once with session_id=%q, fingerprint=%q, and zero to 20 findings. Every finding path/side/line must point to a visible line in that exact diff. Call it with an empty findings array if there are no actionable issues.", sessionID, fingerprint)
}

// StartCodeReview starts one explicitly user-requested turn under the runtime's
// Plan allowlist, regardless of the session's ordinary permission mode.
func (s *Studio) StartCodeReview(projectID, sessionID, fingerprint string) error {
	review, err := s.GetSessionGitReview(projectID, sessionID)
	if err != nil {
		return err
	}
	if review.Fingerprint == "" || review.Fingerprint != strings.TrimSpace(fingerprint) || len(review.Files) == 0 {
		return fmt.Errorf("the diff changed or is empty; refresh Review changes and try again")
	}
	return s.startMessageWithQueueEventPermission(projectID, codeReviewRequestPrompt(sessionID, review.Fingerprint), nil, sessionID, nil, "plan", nil)
}

func (s *Studio) registerCodeReviewTool(reg *tools.Registry, projectID string) {
	if s != nil && reg != nil {
		reg.MustRegister(&submitCodeReviewTool{studio: s, projectID: projectID})
	}
}

func (s *Studio) storeCodeReviewFindings(projectID, sessionID, fingerprint string, findings []CodeReviewFinding) error {
	review, err := s.GetSessionGitReview(projectID, sessionID)
	if err != nil {
		return err
	}
	if review.Fingerprint == "" || fingerprint != review.Fingerprint {
		return fmt.Errorf("the reviewed diff changed; reopen Review changes and start a fresh review")
	}
	files := make(map[string]GitReviewFile, len(review.Files))
	for _, file := range review.Files {
		files[file.Path] = file
	}
	validated := make([]CodeReviewFinding, 0, len(findings))
	seenFindingIDs := make(map[string]bool, len(findings))
	for index, finding := range findings {
		file, exists := files[finding.Path]
		if !exists || file.Binary || file.Patch == "" {
			return fmt.Errorf("finding %d references an unavailable changed file", index+1)
		}
		if finding.Side != "old" && finding.Side != "new" {
			return fmt.Errorf("finding %d side must be old or new", index+1)
		}
		if finding.Line <= 0 || !reviewPatchContainsLine(file.Patch, finding.Side, finding.Line) {
			return fmt.Errorf("finding %d references a line not visible in the current diff", index+1)
		}
		switch finding.Severity {
		case "critical", "high", "medium", "low":
		default:
			return fmt.Errorf("finding %d has an invalid severity", index+1)
		}
		if finding.Title == "" || finding.Body == "" || len([]rune(finding.Title)) > maxCodeReviewTitleRunes || len([]rune(finding.Body)) > maxCodeReviewBodyRunes {
			return fmt.Errorf("finding %d has an empty or oversized title/body", index+1)
		}
		sum := sha256.Sum256([]byte(fmt.Sprintf("%s\x00%s\x00%s\x00%d\x00%s\x00%s", fingerprint, finding.Path, finding.Side, finding.Line, finding.Title, finding.Body)))
		finding.ID = hex.EncodeToString(sum[:8])
		if seenFindingIDs[finding.ID] {
			return fmt.Errorf("finding %d duplicates an earlier finding", index+1)
		}
		seenFindingIDs[finding.ID] = true
		validated = append(validated, finding)
	}
	s.codeReviewMu.Lock()
	if s.codeReviewFindings == nil {
		s.codeReviewFindings = make(map[string]storedCodeReview)
	}
	s.codeReviewFindings[codeReviewKey(projectID, sessionID)] = storedCodeReview{Fingerprint: fingerprint, Findings: validated}
	s.codeReviewMu.Unlock()
	event := map[string]any{
		"projectID": projectID, "sessionID": sessionID, "fingerprint": fingerprint, "count": len(validated),
	}
	if s.testCodeReviewEmitter != nil {
		s.testCodeReviewEmitter(event)
	} else if s.ctx != nil {
		wailsRuntime.EventsEmit(s.ctx, EventCodeReviewReady, event)
	}
	return nil
}

func (s *Studio) attachCodeReviewFindings(projectID, sessionID string, review *ProjectGitReview) {
	if s == nil || review == nil || review.Fingerprint == "" {
		return
	}
	s.codeReviewMu.Lock()
	defer s.codeReviewMu.Unlock()
	key := codeReviewKey(projectID, sessionID)
	stored, exists := s.codeReviewFindings[key]
	if !exists {
		return
	}
	if stored.Fingerprint != review.Fingerprint {
		delete(s.codeReviewFindings, key)
		return
	}
	review.ReviewCompleted = true
	review.Findings = append([]CodeReviewFinding(nil), stored.Findings...)
}

func reviewPatchContainsLine(patch, side string, target int) bool {
	oldLine, newLine := 0, 0
	for _, line := range strings.Split(patch, "\n") {
		if match := codeReviewHunkRE.FindStringSubmatch(line); match != nil {
			oldLine, _ = strconv.Atoi(match[1])
			newLine, _ = strconv.Atoi(match[2])
			continue
		}
		if strings.HasPrefix(line, "+++") || strings.HasPrefix(line, "---") || strings.HasPrefix(line, "@@") {
			continue
		}
		switch {
		case strings.HasPrefix(line, "+"):
			if side == "new" && newLine == target {
				return true
			}
			newLine++
		case strings.HasPrefix(line, "-"):
			if side == "old" && oldLine == target {
				return true
			}
			oldLine++
		case strings.HasPrefix(line, " "):
			if (side == "old" && oldLine == target) || (side == "new" && newLine == target) {
				return true
			}
			oldLine++
			newLine++
		}
	}
	return false
}
