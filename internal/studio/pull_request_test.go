package studio

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
)

func TestParseGitRemote(t *testing.T) {
	tests := []struct {
		input string
		want  gitRemoteIdentity
		ok    bool
	}{
		{"git@github.com:acme/widget.git", gitRemoteIdentity{Host: "github.com", Owner: "acme", Repository: "widget"}, true},
		{"ssh://git@github.example.com/acme/widget.git", gitRemoteIdentity{Host: "github.example.com", Owner: "acme", Repository: "widget"}, true},
		{"https://github.com/acme/widget", gitRemoteIdentity{Host: "github.com", Owner: "acme", Repository: "widget"}, true},
		{"https://user@github.com/acme/widget.git", gitRemoteIdentity{Host: "github.com", Owner: "acme", Repository: "widget"}, true},
		{"git://github.com/acme/widget.git", gitRemoteIdentity{}, false},
		{"http://github.com/acme/widget.git", gitRemoteIdentity{}, false},
		{"https://127.0.0.1/acme/widget.git", gitRemoteIdentity{}, false},
		{"https://localhost/acme/widget.git", gitRemoteIdentity{}, false},
		{"https://github/acme/widget.git", gitRemoteIdentity{}, false},
		{"https://.github.com/acme/widget.git", gitRemoteIdentity{}, false},
		{"https://github-.com/acme/widget.git", gitRemoteIdentity{}, false},
		{"https://github.com:8443/acme/widget.git", gitRemoteIdentity{}, false},
		{"https://github.com/acme/widget/extra.git", gitRemoteIdentity{}, false},
		{"file:///tmp/widget.git", gitRemoteIdentity{}, false},
		{"not-a-remote", gitRemoteIdentity{}, false},
	}
	for _, test := range tests {
		t.Run(test.input, func(t *testing.T) {
			got, ok := parseGitRemote(test.input)
			if ok != test.ok || !reflect.DeepEqual(got, test.want) {
				t.Fatalf("parseGitRemote(%q) = %#v, %v; want %#v, %v", test.input, got, ok, test.want, test.ok)
			}
		})
	}
}

func TestGetProjectPullRequestStatusAllowsNoChecksYet(t *testing.T) {
	s, projectID, _ := newPullRequestTestStudio(t)
	s.testGHCommand = func(_ context.Context, _ string, args ...string) ([]byte, error) {
		if len(args) >= 2 && args[0] == "pr" && args[1] == "checks" {
			return []byte("no checks reported on the 'feature/ci' branch"), errors.New("exit status 1")
		}
		if len(args) >= 2 && args[0] == "pr" && args[1] == "list" {
			return []byte(`[]`), nil
		}
		return []byte(`{"number":42,"title":"No checks yet","state":"OPEN","headRefOid":"abcdef1"}`), nil
	}

	status, err := s.GetProjectPullRequestStatus(projectID)
	if err != nil {
		t.Fatalf("GetProjectPullRequestStatus: %v", err)
	}
	if !status.HasPullRequest || len(status.Checks) != 0 || status.Overall != "none" || status.Fingerprint == "" {
		t.Fatalf("status = %+v", status)
	}
}

func TestGetProjectPullRequestStatusNormalizesChecksAndURL(t *testing.T) {
	s, projectID, dir := newPullRequestTestStudio(t)
	var calls [][]string
	s.testGHCommand = func(_ context.Context, gotDir string, args ...string) ([]byte, error) {
		if gotDir != dir {
			t.Fatalf("gh dir = %q, want %q", gotDir, dir)
		}
		calls = append(calls, append([]string(nil), args...))
		if len(args) >= 2 && args[0] == "pr" && args[1] == "checks" {
			return []byte(`[
				{"name":"unit", "workflow":"CI", "state":"SUCCESS", "bucket":"pass"},
				{"name":"lint", "workflow":"CI", "state":"FAILURE", "bucket":"fail", "link":"https://evil.example/check"},
				{"name":"deploy", "workflow":"Release", "state":"IN_PROGRESS", "bucket":"pending"}
			]`), errors.New("exit status 1")
		}
		if len(args) >= 2 && args[0] == "pr" && args[1] == "list" {
			return []byte(`[]`), nil
		}
		return []byte(`{
			"number":42,
			"title":"  Improve\nCI visibility  ",
			"url":"https://evil.example/phish",
			"state":"OPEN",
			"isDraft":false,
			"headRefName":"feature/ci",
			"headRefOid":"0123456789abcdef0123456789abcdef01234567",
			"baseRefName":"main",
			"mergeable":"MERGEABLE",
			"reviewDecision":"APPROVED",
			"autoMergeRequest":{"enabledAt":"2026-08-04T00:00:00Z"}
		}`), nil
	}

	status, err := s.GetProjectPullRequestStatus(projectID)
	if err != nil {
		t.Fatalf("GetProjectPullRequestStatus: %v", err)
	}
	if !status.CLIAvailable || !status.Repository || !status.HasPullRequest {
		t.Fatalf("status availability = %+v", status)
	}
	if status.URL != "https://github.com/acme/widget/pull/42" {
		t.Fatalf("canonical URL = %q", status.URL)
	}
	if status.Title != "Improve CI visibility" || status.Remote != "acme/widget" {
		t.Fatalf("sanitized metadata = title %q remote %q", status.Title, status.Remote)
	}
	if status.Passed != 1 || status.Failed != 1 || status.Pending != 1 || status.Overall != "failing" {
		t.Fatalf("check summary = %+v", status)
	}
	if !status.AutoMergeEnabled || status.Fingerprint == "" || len(status.Checks) != 3 {
		t.Fatalf("status detail = %+v", status)
	}
	if got, want := calls, [][]string{
		{"pr", "view", "--json", "number,title,state,isDraft,headRefName,headRefOid,baseRefName,mergeable,reviewDecision,autoMergeRequest"},
		{"pr", "checks", "42", "--json", "name,state,bucket,workflow"},
		{"pr", "list", "--state", "all", "--limit", "100", "--json", "number,title,state,isDraft,headRefName,headRefOid,baseRefName"},
	}; !reflect.DeepEqual(got, want) {
		t.Fatalf("gh calls = %#v, want %#v", got, want)
	}
}

func TestGetProjectPullRequestStatusDiscoversStackAndOpenSiblings(t *testing.T) {
	s, projectID, _ := newPullRequestTestStudio(t)
	s.testGHCommand = func(_ context.Context, _ string, args ...string) ([]byte, error) {
		if len(args) >= 2 && args[1] == "checks" {
			return []byte(`[]`), nil
		}
		if len(args) >= 2 && args[1] == "list" {
			return []byte(`[
				{"number":42,"title":"Current","state":"OPEN","headRefName":"feature/checkout","baseRefName":"feature/base"},
				{"number":30,"title":"Base layer","state":"OPEN","headRefName":"feature/base","baseRefName":"main"},
				{"number":20,"title":"Foundation","state":"MERGED","headRefName":"main","baseRefName":"release"},
				{"number":50,"title":"UI layer","state":"OPEN","isDraft":true,"headRefName":"feature/ui","baseRefName":"feature/checkout"},
				{"number":51,"title":"UI tests","state":"OPEN","headRefName":"feature/ui-tests","baseRefName":"feature/ui"},
				{"number":40,"title":"Parallel API","state":"OPEN","headRefName":"feature/api","baseRefName":"feature/base"},
				{"number":39,"title":"Old sibling","state":"CLOSED","headRefName":"feature/old","baseRefName":"feature/base"},
				{"number":0,"title":"Invalid","state":"OPEN","headRefName":"bad","baseRefName":"feature/base"},
				{"number":60,"title":"Invalid state","state":"ALIEN","headRefName":"bad-state","baseRefName":"feature/base"}
			]`), nil
		}
		return []byte(`{
			"number":42,"title":"Checkout","state":"OPEN","headRefName":"feature/checkout",
			"headRefOid":"abcdef1","baseRefName":"feature/base"
		}`), nil
	}

	status, err := s.GetProjectPullRequestStatus(projectID)
	if err != nil {
		t.Fatal(err)
	}
	want := []struct {
		number   int
		relation string
		depth    int
	}{
		{30, "parent", 1}, {20, "parent", 2},
		{50, "child", 1}, {51, "child", 2},
		{40, "sibling", 0},
	}
	if len(status.RelatedPullRequests) != len(want) || status.RelatedTruncated || status.RelatedMessage != "" {
		t.Fatalf("related status = %+v", status)
	}
	for index, expected := range want {
		got := status.RelatedPullRequests[index]
		if got.Number != expected.number || got.Relation != expected.relation || got.Depth != expected.depth {
			t.Fatalf("related[%d] = %+v, want %+v", index, got, expected)
		}
		if got.URL != fmt.Sprintf("https://github.com/acme/widget/pull/%d", got.Number) {
			t.Fatalf("related canonical URL = %q", got.URL)
		}
	}
	if !status.RelatedPullRequests[2].Draft {
		t.Fatal("related draft state was lost")
	}
}

func TestGetProjectPullRequestStatusBoundsAndDegradesRelatedDiscovery(t *testing.T) {
	t.Run("bounds", func(t *testing.T) {
		s, projectID, _ := newPullRequestTestStudio(t)
		list := make([]map[string]any, 0, 20)
		for index := 0; index < 20; index++ {
			list = append(list, map[string]any{
				"number": index + 100, "title": strings.Repeat("x", 300), "state": "OPEN",
				"headRefName": fmt.Sprintf("feature/sibling-%d", index), "baseRefName": "main",
			})
		}
		encoded, _ := json.Marshal(list)
		s.testGHCommand = func(_ context.Context, _ string, args ...string) ([]byte, error) {
			if len(args) >= 2 && args[1] == "checks" {
				return []byte(`[]`), nil
			}
			if len(args) >= 2 && args[1] == "list" {
				return encoded, nil
			}
			return []byte(`{"number":42,"title":"Current","state":"OPEN","headRefName":"feature/current","headRefOid":"abcdef1","baseRefName":"main"}`), nil
		}
		status, err := s.GetProjectPullRequestStatus(projectID)
		if err != nil {
			t.Fatal(err)
		}
		if len(status.RelatedPullRequests) != pullRequestRelatedDisplayLimit || !status.RelatedTruncated {
			t.Fatalf("bounded related = %d, truncated=%v", len(status.RelatedPullRequests), status.RelatedTruncated)
		}
		for _, related := range status.RelatedPullRequests {
			if len([]rune(related.Title)) > 240 {
				t.Fatalf("unbounded related title: %d runes", len([]rune(related.Title)))
			}
		}
	})

	t.Run("supplementary failure", func(t *testing.T) {
		s, projectID, _ := newPullRequestTestStudio(t)
		s.testGHCommand = func(_ context.Context, _ string, args ...string) ([]byte, error) {
			if len(args) >= 2 && args[1] == "checks" {
				return []byte(`[]`), nil
			}
			if len(args) >= 2 && args[1] == "list" {
				return []byte("network unavailable"), errors.New("exit status 1")
			}
			return []byte(`{"number":42,"title":"Current","state":"OPEN","headRefName":"feature/current","headRefOid":"abcdef1","baseRefName":"main"}`), nil
		}
		status, err := s.GetProjectPullRequestStatus(projectID)
		if err != nil || !status.HasPullRequest || status.RelatedMessage != "GitHub could not be reached" {
			t.Fatalf("supplementary failure hid core status: %+v, %v", status, err)
		}
	})
}

func TestPullRequestFingerprintIsStableAcrossCheckOrder(t *testing.T) {
	first := &PullRequestStatus{HeadOID: "abc1234", Checks: []PullRequestCheck{
		{Name: "unit", Workflow: "CI", Status: "passed", Conclusion: "SUCCESS"},
		{Name: "lint", Workflow: "CI", Status: "failed", Conclusion: "FAILURE"},
	}}
	second := &PullRequestStatus{HeadOID: first.HeadOID, Checks: []PullRequestCheck{first.Checks[1], first.Checks[0]}}
	if a, b := pullRequestFingerprint(first), pullRequestFingerprint(second); a == "" || a != b {
		t.Fatalf("fingerprints = %q and %q", a, b)
	}
}

func TestGetProjectPullRequestStatusHandlesNoPRAndAuthentication(t *testing.T) {
	for _, test := range []struct {
		name      string
		output    string
		needsAuth bool
	}{
		{"no PR", "no pull requests found for branch feature", false},
		{"auth", "not logged into any GitHub hosts; run gh auth login", true},
	} {
		t.Run(test.name, func(t *testing.T) {
			s, projectID, _ := newPullRequestTestStudio(t)
			s.testGHCommand = func(context.Context, string, ...string) ([]byte, error) {
				return []byte(test.output), errors.New("exit status 1")
			}
			status, err := s.GetProjectPullRequestStatus(projectID)
			if err != nil {
				t.Fatalf("GetProjectPullRequestStatus: %v", err)
			}
			if status.HasPullRequest || status.NeedsAuthentication != test.needsAuth || status.Message == "" {
				t.Fatalf("status = %+v", status)
			}
		})
	}
}

func TestGetProjectPullRequestStatusBoundsCLIOutputAndChecks(t *testing.T) {
	s, projectID, _ := newPullRequestTestStudio(t)
	s.testGHCommand = func(context.Context, string, ...string) ([]byte, error) {
		return make([]byte, pullRequestOutputMaxBytes+1), nil
	}
	if _, err := s.GetProjectPullRequestStatus(projectID); err == nil || !strings.Contains(err.Error(), "GitHub CLI returned an error") {
		t.Fatalf("oversized output error = %v", err)
	}

	checks := make([]map[string]string, pullRequestChecksMax+7)
	for i := range checks {
		checks[i] = map[string]string{"name": fmt.Sprintf("check-%d", i), "state": "SUCCESS", "bucket": "pass"}
	}
	payload := map[string]any{
		"number": 3, "title": "bounded", "state": "OPEN", "headRefOid": "abcdef1",
	}
	encoded, _ := json.Marshal(payload)
	checksEncoded, _ := json.Marshal(checks)
	s.testGHCommand = func(_ context.Context, _ string, args ...string) ([]byte, error) {
		if len(args) >= 2 && args[1] == "checks" {
			return checksEncoded, nil
		}
		if len(args) >= 2 && args[1] == "list" {
			return []byte(`[]`), nil
		}
		return encoded, nil
	}
	status, err := s.GetProjectPullRequestStatus(projectID)
	if err != nil {
		t.Fatal(err)
	}
	if len(status.Checks) != pullRequestChecksMax || !status.ChecksTruncated || status.Passed != pullRequestChecksMax {
		t.Fatalf("bounded checks = len %d truncated %v passed %d", len(status.Checks), status.ChecksTruncated, status.Passed)
	}
}

func TestSetProjectPullRequestAutoMergeRevalidatesAndUsesFixedArguments(t *testing.T) {
	s, projectID, _ := newPullRequestTestStudio(t)
	autoMerge := false
	var calls [][]string
	s.testGHCommand = func(_ context.Context, _ string, args ...string) ([]byte, error) {
		calls = append(calls, append([]string(nil), args...))
		if len(args) >= 2 && args[0] == "pr" && args[1] == "checks" {
			return []byte(`[]`), nil
		}
		if len(args) >= 2 && args[0] == "pr" && args[1] == "merge" {
			want := []string{"pr", "merge", "42", "--squash", "--auto", "--match-head-commit", "abcdef1"}
			if !reflect.DeepEqual(args, want) {
				t.Fatalf("merge args = %#v, want %#v", args, want)
			}
			autoMerge = true
			return []byte("enabled"), nil
		}
		if len(args) >= 2 && args[0] == "pr" && args[1] == "list" {
			return []byte(`[]`), nil
		}
		request := "null"
		if autoMerge {
			request = `{"enabledAt":"now"}`
		}
		return []byte(fmt.Sprintf(`{"number":42,"title":"PR","state":"OPEN","headRefOid":"abcdef1","autoMergeRequest":%s}`, request)), nil
	}

	status, err := s.SetProjectPullRequestAutoMerge(projectID, 42, "abcdef1", true)
	if err != nil {
		t.Fatalf("SetProjectPullRequestAutoMerge: %v", err)
	}
	if !status.AutoMergeEnabled || len(calls) != 6 || status.RelatedMessage != "" {
		t.Fatalf("status/calls = %+v / %#v", status, calls)
	}

	before := len(calls)
	if _, err := s.SetProjectPullRequestAutoMerge(projectID, 42, "deadbeef", true); err == nil || !strings.Contains(err.Error(), "head changed") {
		t.Fatalf("stale head error = %v", err)
	}
	if len(calls) != before+2 {
		t.Fatalf("stale head should only re-read status; calls = %#v", calls[before:])
	}

	before = len(calls)
	if _, err := s.SetProjectPullRequestAutoMerge(projectID, 41, "abcdef1", true); err == nil || !strings.Contains(err.Error(), "no longer associated") {
		t.Fatalf("mismatched PR error = %v", err)
	}
	if len(calls) != before+2 {
		t.Fatalf("mismatch should only re-read status; calls = %#v", calls[before:])
	}
}

func newPullRequestTestStudio(t *testing.T) (*Studio, string, string) {
	t.Helper()
	withTempHistoryDir(t)
	s := newStudioForTest(t)
	dir := t.TempDir()
	initGitRepo(t, dir)
	gitMust(t, dir, "remote", "add", "origin", "git@github.com:acme/widget.git")
	info, err := s.AddProject("PR", dir)
	if err != nil {
		t.Fatalf("AddProject: %v", err)
	}
	return s, info.ID, dir
}
