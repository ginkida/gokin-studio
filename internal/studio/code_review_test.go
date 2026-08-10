package studio

import (
	"context"
	"path/filepath"
	"testing"
)

func TestCodeReviewFindingsAreFingerprintAndLineBound(t *testing.T) {
	s := newStudioForTest(t)
	s.testCodeReviewEmitter = func(map[string]any) {}
	repo := prepareSessionWorktreeRepo(t)
	info, err := s.AddProject("inline-review", repo)
	if err != nil {
		t.Fatal(err)
	}
	session := s.projects[info.ID].sessions["default"].Info()
	if err := writeFile(filepath.Join(session.WorktreePath, "finding.txt"), "first\nsecond\n"); err != nil {
		t.Fatal(err)
	}
	review, err := s.GetSessionGitReview(info.ID, "default")
	if err != nil {
		t.Fatal(err)
	}
	if review.Fingerprint == "" {
		t.Fatal("review fingerprint is empty")
	}
	finding := CodeReviewFinding{
		Path: "finding.txt", Side: "new", Line: 1, Severity: "high",
		Title: "First line breaks startup", Body: "The new first line is interpreted as a command and fails on every launch.",
	}
	if err := s.storeCodeReviewFindings(info.ID, "default", review.Fingerprint, []CodeReviewFinding{finding}); err != nil {
		t.Fatal(err)
	}
	attached, err := s.GetSessionGitReview(info.ID, "default")
	if err != nil {
		t.Fatal(err)
	}
	if !attached.ReviewCompleted || len(attached.Findings) != 1 || attached.Findings[0].ID == "" {
		t.Fatalf("finding was not attached: %#v", attached)
	}
	if err := s.storeCodeReviewFindings(info.ID, "default", "stale-fingerprint", []CodeReviewFinding{finding}); err == nil {
		t.Fatal("stale review was accepted")
	}
	finding.Line = 999
	if err := s.storeCodeReviewFindings(info.ID, "default", review.Fingerprint, []CodeReviewFinding{finding}); err == nil {
		t.Fatal("off-diff line was accepted")
	}
	if err := writeFile(filepath.Join(session.WorktreePath, "finding.txt"), "changed again\n"); err != nil {
		t.Fatal(err)
	}
	changed, err := s.GetSessionGitReview(info.ID, "default")
	if err != nil {
		t.Fatal(err)
	}
	if changed.ReviewCompleted || len(changed.Findings) != 0 {
		t.Fatal("findings survived a diff fingerprint change")
	}
}

func TestSubmitCodeReviewToolPublishesEmptyReview(t *testing.T) {
	s := newStudioForTest(t)
	s.testCodeReviewEmitter = func(map[string]any) {}
	repo := prepareSessionWorktreeRepo(t)
	info, err := s.AddProject("empty-inline-review", repo)
	if err != nil {
		t.Fatal(err)
	}
	session := s.projects[info.ID].sessions["default"].Info()
	if err := writeFile(filepath.Join(session.WorktreePath, "empty-review.txt"), "new\n"); err != nil {
		t.Fatal(err)
	}
	review, err := s.GetSessionGitReview(info.ID, "default")
	if err != nil {
		t.Fatal(err)
	}
	tool := &submitCodeReviewTool{studio: s, projectID: info.ID}
	result, err := tool.Execute(context.Background(), map[string]any{
		"session_id": "default", "fingerprint": review.Fingerprint, "findings": []any{},
	})
	if err != nil || !result.Success {
		t.Fatalf("submit empty review: result=%#v err=%v", result, err)
	}
	attached, err := s.GetSessionGitReview(info.ID, "default")
	if err != nil {
		t.Fatal(err)
	}
	if !attached.ReviewCompleted || len(attached.Findings) != 0 {
		t.Fatalf("empty completed review not represented: %#v", attached)
	}
}
