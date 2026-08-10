package studio

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"os/exec"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	pullRequestTimeout             = 12 * time.Second
	pullRequestOutputMaxBytes      = 512 << 10
	pullRequestChecksMax           = 50
	pullRequestRelatedListLimit    = 100
	pullRequestRelatedDisplayLimit = 12
)

type PullRequestCheck struct {
	Name       string `json:"name"`
	Workflow   string `json:"workflow,omitempty"`
	Status     string `json:"status"`
	Conclusion string `json:"conclusion,omitempty"`
}

type RelatedPullRequest struct {
	Number     int    `json:"number"`
	Title      string `json:"title,omitempty"`
	URL        string `json:"url"`
	State      string `json:"state"`
	Draft      bool   `json:"draft,omitempty"`
	HeadBranch string `json:"headBranch,omitempty"`
	BaseBranch string `json:"baseBranch,omitempty"`
	Relation   string `json:"relation"`
	Depth      int    `json:"depth,omitempty"`
}

type PullRequestStatus struct {
	CLIAvailable        bool                 `json:"cliAvailable"`
	Repository          bool                 `json:"repository"`
	Remote              string               `json:"remote,omitempty"`
	HasPullRequest      bool                 `json:"hasPullRequest"`
	Number              int                  `json:"number,omitempty"`
	Title               string               `json:"title,omitempty"`
	URL                 string               `json:"url,omitempty"`
	State               string               `json:"state,omitempty"`
	Draft               bool                 `json:"draft,omitempty"`
	HeadBranch          string               `json:"headBranch,omitempty"`
	HeadOID             string               `json:"headOID,omitempty"`
	BaseBranch          string               `json:"baseBranch,omitempty"`
	Mergeable           string               `json:"mergeable,omitempty"`
	ReviewDecision      string               `json:"reviewDecision,omitempty"`
	AutoMergeEnabled    bool                 `json:"autoMergeEnabled,omitempty"`
	Overall             string               `json:"overall,omitempty"`
	Passed              int                  `json:"passed,omitempty"`
	Pending             int                  `json:"pending,omitempty"`
	Failed              int                  `json:"failed,omitempty"`
	Checks              []PullRequestCheck   `json:"checks,omitempty"`
	ChecksTruncated     bool                 `json:"checksTruncated,omitempty"`
	Fingerprint         string               `json:"fingerprint,omitempty"`
	NeedsAuthentication bool                 `json:"needsAuthentication,omitempty"`
	Message             string               `json:"message,omitempty"`
	CheckedAt           int64                `json:"checkedAt"`
	AutoArchiveEnabled  bool                 `json:"autoArchiveEnabled,omitempty"`
	AutoArchived        bool                 `json:"autoArchived,omitempty"`
	AutoArchiveBlocked  string               `json:"autoArchiveBlocked,omitempty"`
	RelatedPullRequests []RelatedPullRequest `json:"relatedPullRequests,omitempty"`
	RelatedTruncated    bool                 `json:"relatedTruncated,omitempty"`
	RelatedMessage      string               `json:"relatedMessage,omitempty"`
}

type ghPullRequest struct {
	Number           int             `json:"number"`
	Title            string          `json:"title"`
	State            string          `json:"state"`
	IsDraft          bool            `json:"isDraft"`
	HeadRefName      string          `json:"headRefName"`
	HeadRefOID       string          `json:"headRefOid"`
	BaseRefName      string          `json:"baseRefName"`
	Mergeable        string          `json:"mergeable"`
	ReviewDecision   string          `json:"reviewDecision"`
	AutoMergeRequest json.RawMessage `json:"autoMergeRequest"`
}

type ghStatusCheck struct {
	Name     string `json:"name"`
	Workflow string `json:"workflow"`
	State    string `json:"state"`
	Bucket   string `json:"bucket"`
}

type gitRemoteIdentity struct {
	Host       string
	Owner      string
	Repository string
}

// GetProjectPullRequestStatus returns a bounded snapshot for the pull request
// associated with the project's current branch. It never mutates GitHub and
// never trusts a browser URL supplied by the CLI.
func (s *Studio) GetProjectPullRequestStatus(projectID string) (*PullRequestStatus, error) {
	dir, err := s.projectDirectory(projectID)
	if err != nil {
		return nil, err
	}
	return s.pullRequestStatus(context.Background(), dir)
}

func (s *Studio) GetSessionPullRequestStatus(projectID, sessionID string) (*PullRequestStatus, error) {
	p, session, err := s.projectSession(projectID, sessionID)
	if err != nil {
		return nil, err
	}
	dir, err := sessionWorkingDirectory(p, session)
	if err != nil {
		return nil, err
	}
	status, err := s.pullRequestStatus(context.Background(), dir)
	if err != nil {
		return nil, err
	}
	status.AutoArchiveEnabled = s.pullRequestAutoArchiveEnabled()
	if status.AutoArchiveEnabled && (status.State == "MERGED" || status.State == "CLOSED") {
		archived, blocked := s.autoArchiveSessionForPullRequest(projectID, sessionID, dir, status)
		status.AutoArchived = archived
		status.AutoArchiveBlocked = blocked
	}
	return status, nil
}

// SetProjectPullRequestAutoMerge toggles GitHub's server-side squash
// auto-merge for the current branch. The caller must present the exact PR
// number it just reviewed; we re-read the branch association before mutating.
func (s *Studio) SetProjectPullRequestAutoMerge(projectID string, number int, expectedHeadOID string, enabled bool) (*PullRequestStatus, error) {
	dir, err := s.projectDirectory(projectID)
	if err != nil {
		return nil, err
	}
	return s.setPullRequestAutoMergeAt(dir, number, expectedHeadOID, enabled)
}

func (s *Studio) SetSessionPullRequestAutoMerge(projectID, sessionID string, number int, expectedHeadOID string, enabled bool) (*PullRequestStatus, error) {
	p, session, err := s.projectSession(projectID, sessionID)
	if err != nil {
		return nil, err
	}
	dir, err := sessionWorkingDirectory(p, session)
	if err != nil {
		return nil, err
	}
	return s.setPullRequestAutoMergeAt(dir, number, expectedHeadOID, enabled)
}

func (s *Studio) setPullRequestAutoMergeAt(dir string, number int, expectedHeadOID string, enabled bool) (*PullRequestStatus, error) {
	if number <= 0 || number > 1_000_000_000 {
		return nil, errors.New("invalid pull request number")
	}
	s.pullRequestMu.Lock()
	defer s.pullRequestMu.Unlock()

	current, err := s.pullRequestStatusCore(context.Background(), dir, false)
	if err != nil {
		return nil, err
	}
	if !current.HasPullRequest || current.Number != number {
		return nil, errors.New("the current branch is no longer associated with that pull request")
	}
	if enabled && current.State != "OPEN" {
		return nil, errors.New("auto-merge can only be enabled for an open pull request")
	}
	if enabled {
		expectedHeadOID = canonicalGitOID(expectedHeadOID)
		if expectedHeadOID == "" || current.HeadOID == "" || expectedHeadOID != current.HeadOID {
			return nil, errors.New("the pull request head changed; refresh its status before enabling auto-merge")
		}
	}
	args := []string{"pr", "merge", strconv.Itoa(number)}
	if enabled {
		args = append(args, "--squash", "--auto", "--match-head-commit", current.HeadOID)
	} else {
		args = append(args, "--disable-auto")
	}
	ctx, cancel := context.WithTimeout(context.Background(), pullRequestTimeout)
	defer cancel()
	if output, err := s.runGH(ctx, dir, args...); err != nil {
		return nil, fmt.Errorf("could not %s auto-merge: %s", map[bool]string{true: "enable", false: "disable"}[enabled], friendlyGHError(output, err))
	}
	return s.pullRequestStatus(context.Background(), dir)
}

func (s *Studio) projectDirectory(projectID string) (string, error) {
	s.mu.RLock()
	p, ok := s.projects[projectID]
	s.mu.RUnlock()
	if !ok {
		return "", fmt.Errorf("project not found: %s", projectID)
	}
	p.mu.RLock()
	dir := p.Directory
	p.mu.RUnlock()
	return dir, nil
}

func (s *Studio) pullRequestStatus(parent context.Context, dir string) (*PullRequestStatus, error) {
	return s.pullRequestStatusCore(parent, dir, true)
}

func (s *Studio) pullRequestStatusCore(parent context.Context, dir string, includeRelated bool) (*PullRequestStatus, error) {
	status := &PullRequestStatus{CheckedAt: time.Now().UnixMilli()}
	if !runGitBool(dir, "rev-parse", "--is-inside-work-tree") {
		status.Message = "This project is not a Git repository."
		return status, nil
	}
	status.Repository = true
	remote, ok := parseGitRemote(runGit(dir, "remote", "get-url", "origin"))
	if !ok {
		status.Message = "Add a valid HTTPS or SSH origin remote to monitor pull requests."
		return status, nil
	}
	status.Remote = remote.Owner + "/" + remote.Repository
	if s.testGHCommand == nil {
		if _, err := exec.LookPath("gh"); err != nil {
			status.Message = "Install and authenticate GitHub CLI (gh) to monitor pull requests."
			return status, nil
		}
	}
	status.CLIAvailable = true

	ctx, cancel := context.WithTimeout(parent, pullRequestTimeout)
	defer cancel()
	output, err := s.runGH(ctx, dir,
		"pr", "view", "--json",
		"number,title,state,isDraft,headRefName,headRefOid,baseRefName,mergeable,reviewDecision,autoMergeRequest",
	)
	if err != nil {
		lower := strings.ToLower(string(output))
		switch {
		case strings.Contains(lower, "no pull requests found"),
			strings.Contains(lower, "could not resolve to a pull request"),
			strings.Contains(lower, "no pull request found"):
			status.Message = "The current branch has no pull request."
			return status, nil
		case strings.Contains(lower, "authentication"), strings.Contains(lower, "not logged"), strings.Contains(lower, "gh auth login"):
			status.NeedsAuthentication = true
			status.Message = "Authenticate GitHub CLI with `gh auth login` to monitor pull requests."
			return status, nil
		default:
			return nil, fmt.Errorf("could not read pull request status: %s", friendlyGHError(output, err))
		}
	}
	if len(output) == 0 {
		return nil, errors.New("GitHub CLI returned an empty pull request status")
	}
	var raw ghPullRequest
	if err := json.Unmarshal(output, &raw); err != nil {
		return nil, errors.New("GitHub CLI returned an invalid pull request status")
	}
	if raw.Number <= 0 || raw.Number > 1_000_000_000 {
		return nil, errors.New("GitHub CLI returned an invalid pull request number")
	}

	status.HasPullRequest = true
	status.Number = raw.Number
	status.Title = boundedPRText(raw.Title, 240)
	status.URL = canonicalPullRequestURL(remote, raw.Number)
	status.State = allowedUpper(raw.State, "OPEN", "CLOSED", "MERGED")
	status.Draft = raw.IsDraft
	status.HeadBranch = boundedPRText(raw.HeadRefName, 200)
	status.HeadOID = canonicalGitOID(raw.HeadRefOID)
	status.BaseBranch = boundedPRText(raw.BaseRefName, 200)
	status.Mergeable = allowedUpper(raw.Mergeable, "MERGEABLE", "CONFLICTING", "UNKNOWN")
	status.ReviewDecision = allowedUpper(raw.ReviewDecision, "APPROVED", "CHANGES_REQUESTED", "REVIEW_REQUIRED")
	status.AutoMergeEnabled = raw.AutoMergeRequest != nil && string(raw.AutoMergeRequest) != "null"

	checksCtx, checksCancel := context.WithTimeout(parent, pullRequestTimeout)
	defer checksCancel()
	checksOutput, checksErr := s.runGH(checksCtx, dir,
		"pr", "checks", strconv.Itoa(raw.Number), "--json", "name,state,bucket,workflow",
	)
	var checks []ghStatusCheck
	// gh exits 8 while checks are pending and may exit non-zero for failures,
	// but still returns the authoritative JSON array. Parse valid output first.
	if len(checksOutput) > 0 && json.Unmarshal(checksOutput, &checks) == nil {
		// Valid JSON wins over the process exit code.
	} else if checksErr != nil && reportsNoPullRequestChecks(checksOutput) {
		// A pull request may legitimately have no check runs yet. Some gh
		// versions report that state as human-readable stderr with a non-zero
		// exit code instead of returning an empty JSON array.
		checks = []ghStatusCheck{}
	} else if checksErr != nil {
		return nil, fmt.Errorf("could not read pull request checks: %s", friendlyGHError(checksOutput, checksErr))
	} else {
		return nil, errors.New("GitHub CLI returned an invalid pull request checks status")
	}
	if len(checks) > pullRequestChecksMax {
		checks = checks[:pullRequestChecksMax]
		status.ChecksTruncated = true
	}
	for _, rawCheck := range checks {
		name := boundedPRText(firstNonBlank(rawCheck.Name, "Unnamed check"), 160)
		workflow := boundedPRText(rawCheck.Workflow, 160)
		checkStatus, conclusion := normalizePullRequestCheck(rawCheck)
		status.Checks = append(status.Checks, PullRequestCheck{
			Name:       name,
			Workflow:   workflow,
			Status:     checkStatus,
			Conclusion: conclusion,
		})
		switch checkStatus {
		case "passed":
			status.Passed++
		case "failed":
			status.Failed++
		default:
			status.Pending++
		}
	}
	switch {
	case status.Failed > 0:
		status.Overall = "failing"
	case status.Pending > 0:
		status.Overall = "pending"
	case len(status.Checks) > 0:
		status.Overall = "passing"
	default:
		status.Overall = "none"
	}
	status.Fingerprint = pullRequestFingerprint(status)
	if includeRelated {
		related, truncated, message := s.discoverRelatedPullRequests(parent, dir, remote, raw)
		status.RelatedPullRequests = related
		status.RelatedTruncated = truncated
		status.RelatedMessage = message
	}
	return status, nil
}

func (s *Studio) discoverRelatedPullRequests(parent context.Context, dir string, remote gitRemoteIdentity, current ghPullRequest) ([]RelatedPullRequest, bool, string) {
	ctx, cancel := context.WithTimeout(parent, pullRequestTimeout)
	defer cancel()
	output, err := s.runGH(ctx, dir,
		"pr", "list", "--state", "all", "--limit", strconv.Itoa(pullRequestRelatedListLimit), "--json",
		"number,title,state,isDraft,headRefName,headRefOid,baseRefName",
	)
	if err != nil {
		message := boundedPRText(friendlyGHError(output, err), 240)
		if message == "" {
			message = "GitHub CLI could not list related pull requests."
		}
		return nil, false, message
	}
	var raw []ghPullRequest
	if len(output) == 0 || json.Unmarshal(output, &raw) != nil {
		return nil, false, "GitHub CLI returned invalid related pull request data."
	}
	listTruncated := len(raw) >= pullRequestRelatedListLimit
	related := buildRelatedPullRequests(remote, current, raw)
	if len(related) > pullRequestRelatedDisplayLimit {
		related = related[:pullRequestRelatedDisplayLimit]
		listTruncated = true
	}
	return related, listTruncated, ""
}

func buildRelatedPullRequests(remote gitRemoteIdentity, current ghPullRequest, raw []ghPullRequest) []RelatedPullRequest {
	candidates := make([]RelatedPullRequest, 0, len(raw))
	seenNumbers := make(map[int]bool, len(raw)+1)
	seenNumbers[current.Number] = true
	for _, item := range raw {
		if item.Number <= 0 || item.Number > 1_000_000_000 || seenNumbers[item.Number] {
			continue
		}
		state := allowedUpper(item.State, "OPEN", "CLOSED", "MERGED")
		head := boundedPRText(item.HeadRefName, 200)
		base := boundedPRText(item.BaseRefName, 200)
		if state == "UNKNOWN" || head == "" || base == "" {
			continue
		}
		seenNumbers[item.Number] = true
		candidates = append(candidates, RelatedPullRequest{
			Number: item.Number, Title: boundedPRText(item.Title, 240),
			URL: canonicalPullRequestURL(remote, item.Number), State: state, Draft: item.IsDraft,
			HeadBranch: head, BaseBranch: base,
		})
	}
	sort.SliceStable(candidates, func(i, j int) bool {
		if candidates[i].State != candidates[j].State {
			return candidates[i].State == "OPEN"
		}
		return candidates[i].Number > candidates[j].Number
	})

	currentHead := boundedPRText(current.HeadRefName, 200)
	currentBase := boundedPRText(current.BaseRefName, 200)
	if currentHead == "" || currentBase == "" {
		return nil
	}
	used := map[int]bool{current.Number: true}
	result := make([]RelatedPullRequest, 0)

	// Follow the nearest PR whose head is this PR's base, then continue up the
	// branch graph. A visited set makes malformed cyclic branch data harmless.
	for branch, depth := currentBase, 1; branch != ""; depth++ {
		index := relatedPRIndex(candidates, used, func(item RelatedPullRequest) bool {
			return item.HeadBranch == branch
		})
		if index < 0 {
			break
		}
		item := candidates[index]
		item.Relation, item.Depth = "parent", depth
		result = append(result, item)
		used[item.Number] = true
		branch = item.BaseBranch
	}

	// Breadth-first traversal finds direct children before deeper descendants.
	type branchDepth struct {
		branch string
		depth  int
	}
	queue := []branchDepth{{branch: currentHead, depth: 1}}
	for len(queue) > 0 {
		next := queue[0]
		queue = queue[1:]
		for _, item := range candidates {
			if used[item.Number] || item.BaseBranch != next.branch {
				continue
			}
			item.Relation, item.Depth = "child", next.depth
			result = append(result, item)
			used[item.Number] = true
			queue = append(queue, branchDepth{branch: item.HeadBranch, depth: next.depth + 1})
		}
	}

	// Siblings share the current PR's base. Closed historical siblings are
	// intentionally omitted; they would overwhelm active parallel work.
	for _, item := range candidates {
		if used[item.Number] || item.State != "OPEN" || item.BaseBranch != currentBase {
			continue
		}
		item.Relation = "sibling"
		result = append(result, item)
		used[item.Number] = true
	}
	return result
}

func relatedPRIndex(candidates []RelatedPullRequest, used map[int]bool, matches func(RelatedPullRequest) bool) int {
	for index, item := range candidates {
		if !used[item.Number] && matches(item) {
			return index
		}
	}
	return -1
}

func (s *Studio) runGH(ctx context.Context, dir string, args ...string) ([]byte, error) {
	if s.testGHCommand != nil {
		output, err := s.testGHCommand(ctx, dir, args...)
		if len(output) > pullRequestOutputMaxBytes {
			return output[:pullRequestOutputMaxBytes], errors.New("GitHub CLI output exceeded the safety limit")
		}
		return output, err
	}
	cmd := exec.CommandContext(ctx, "gh", args...)
	cmd.Dir = dir
	cmd.WaitDelay = gitWaitDelay
	cmd.Env = append(os.Environ(), "GH_PROMPT_DISABLED=1", "PAGER=cat", "NO_COLOR=1")
	output := &cappedCommandOutput{limit: pullRequestOutputMaxBytes}
	cmd.Stdout = output
	cmd.Stderr = output
	err := cmd.Run()
	if ctx.Err() != nil {
		return output.data, ctx.Err()
	}
	if output.truncated {
		return output.data, errors.New("GitHub CLI output exceeded the safety limit")
	}
	return output.data, err
}

func parseGitRemote(raw string) (gitRemoteIdentity, bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" || len(raw) > 2048 || !utf8.ValidString(raw) {
		return gitRemoteIdentity{}, false
	}
	var host, path string
	if strings.Contains(raw, "://") {
		u, err := url.Parse(raw)
		if err != nil || u.Hostname() == "" || u.Port() != "" {
			return gitRemoteIdentity{}, false
		}
		switch strings.ToLower(u.Scheme) {
		case "https", "ssh":
		default:
			return gitRemoteIdentity{}, false
		}
		host, path = u.Hostname(), u.Path
	} else {
		colon := strings.IndexByte(raw, ':')
		if colon <= 0 || strings.Contains(raw[:colon], "/") {
			return gitRemoteIdentity{}, false
		}
		hostPart := raw[:colon]
		if at := strings.LastIndexByte(hostPart, '@'); at >= 0 {
			hostPart = hostPart[at+1:]
		}
		host, path = hostPart, raw[colon+1:]
	}
	host = strings.ToLower(strings.TrimSpace(host))
	if !safeGitHost(host) {
		return gitRemoteIdentity{}, false
	}
	path = strings.TrimSuffix(strings.Trim(strings.TrimSpace(path), "/"), ".git")
	parts := strings.Split(path, "/")
	if len(parts) != 2 || !safeGitRepoPart(parts[0]) || !safeGitRepoPart(parts[1]) {
		return gitRemoteIdentity{}, false
	}
	return gitRemoteIdentity{Host: host, Owner: parts[0], Repository: parts[1]}, true
}

func reportsNoPullRequestChecks(output []byte) bool {
	message := strings.ToLower(string(output))
	return strings.Contains(message, "no checks reported") ||
		strings.Contains(message, "no checks found")
}

func safeGitHost(host string) bool {
	if host == "" || len(host) > 253 || !strings.Contains(host, ".") || host == "localhost" || strings.HasSuffix(host, ".local") || net.ParseIP(host) != nil || strings.Contains(host, "..") {
		return false
	}
	for _, label := range strings.Split(host, ".") {
		if label == "" || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return false
		}
		for _, r := range label {
			if (r < 'a' || r > 'z') && (r < '0' || r > '9') && r != '-' {
				return false
			}
		}
	}
	return true
}

func safeGitRepoPart(value string) bool {
	if value == "" || len(value) > 100 || value == "." || value == ".." {
		return false
	}
	for _, r := range value {
		if (r < 'a' || r > 'z') && (r < 'A' || r > 'Z') && (r < '0' || r > '9') && r != '.' && r != '_' && r != '-' {
			return false
		}
	}
	return true
}

func canonicalPullRequestURL(remote gitRemoteIdentity, number int) string {
	return "https://" + remote.Host + "/" + url.PathEscape(remote.Owner) + "/" + url.PathEscape(remote.Repository) + "/pull/" + strconv.Itoa(number)
}

func normalizePullRequestCheck(check ghStatusCheck) (string, string) {
	bucket := strings.ToLower(strings.TrimSpace(check.Bucket))
	state := strings.ToUpper(strings.TrimSpace(check.State))
	switch bucket {
	case "pass", "skipping":
		return "passed", firstNonBlank(state, strings.ToUpper(bucket))
	case "fail", "cancel":
		return "failed", firstNonBlank(state, strings.ToUpper(bucket))
	case "pending":
		return "pending", firstNonBlank(state, "PENDING")
	}
	switch state {
	case "SUCCESS", "NEUTRAL", "SKIPPED":
		return "passed", state
	case "FAILURE", "ERROR", "TIMED_OUT", "CANCELLED", "ACTION_REQUIRED":
		return "failed", state
	default:
		return "pending", firstNonBlank(state, "PENDING")
	}
}

func pullRequestFingerprint(status *PullRequestStatus) string {
	parts := make([]string, 0, len(status.Checks)+1)
	parts = append(parts, status.HeadOID)
	for _, check := range status.Checks {
		parts = append(parts, check.Workflow+"\x00"+check.Name+"\x00"+check.Status+"\x00"+check.Conclusion)
	}
	sort.Strings(parts[1:])
	digest := sha256.Sum256([]byte(strings.Join(parts, "\x01")))
	return hex.EncodeToString(digest[:8])
}

func canonicalGitOID(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	if len(value) < 7 || len(value) > 64 {
		return ""
	}
	for _, r := range value {
		if (r < '0' || r > '9') && (r < 'a' || r > 'f') {
			return ""
		}
	}
	return value
}

func boundedPRText(value string, maxRunes int) string {
	value = strings.Join(strings.Fields(strings.TrimSpace(value)), " ")
	if !utf8.ValidString(value) {
		return ""
	}
	runes := []rune(value)
	if len(runes) > maxRunes {
		value = string(runes[:maxRunes])
	}
	return value
}

func allowedUpper(value string, allowed ...string) string {
	value = strings.ToUpper(strings.TrimSpace(value))
	for _, candidate := range allowed {
		if value == candidate {
			return value
		}
	}
	return "UNKNOWN"
}

func firstNonBlank(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

func friendlyGHError(output []byte, err error) string {
	text := strings.ToLower(string(output))
	switch {
	case strings.Contains(text, "authentication"), strings.Contains(text, "not logged"), strings.Contains(text, "gh auth login"):
		return "GitHub CLI is not authenticated"
	case strings.Contains(text, "network"), strings.Contains(text, "connection"), strings.Contains(text, "timed out"):
		return "GitHub could not be reached"
	case strings.Contains(text, "auto-merge is not allowed"), strings.Contains(text, "auto merge is not allowed"):
		return "the repository does not allow auto-merge"
	case err != nil && errors.Is(err, context.DeadlineExceeded):
		return "the GitHub request timed out"
	default:
		return "GitHub CLI returned an error"
	}
}
