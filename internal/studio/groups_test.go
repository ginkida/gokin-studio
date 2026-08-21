package studio

import (
	"context"
	"strings"
	"testing"

	"github.com/ginkida/gokin-studio/internal/engine/tools"
)

func TestSaveProjectGroupSanitizesAndCaps(t *testing.T) {
	s, from, to, _ := delegationTestStudio(t)
	group, err := s.SaveProjectGroup(ProjectGroupConfig{
		Name:          "  Shop\nstack  ",
		Description:   "how to coordinate",
		SharedContext: strings.Repeat("ф", 4000), // multibyte: truncation stays rune-safe
		Members: []GroupMemberConfig{
			{ProjectID: from.ID, UseFor: "web\tapp"},
			{ProjectID: to.ID, UseFor: "deploys"},
			{ProjectID: to.ID, UseFor: "duplicate"},
			{ProjectID: "", UseFor: "empty"},
		},
	})
	if err != nil {
		t.Fatalf("SaveProjectGroup: %v", err)
	}
	if group.Name != "Shop stack" {
		t.Fatalf("name = %q; structure characters must be collapsed", group.Name)
	}
	if len(group.SharedContext) > maxGroupSharedContext {
		t.Fatalf("shared context = %d bytes, cap is %d", len(group.SharedContext), maxGroupSharedContext)
	}
	if !strings.HasSuffix(group.SharedContext, "ф") {
		t.Fatal("shared context truncation split a multibyte rune")
	}
	if len(group.Members) != 2 {
		t.Fatalf("members = %+v; duplicates and blanks must be dropped", group.Members)
	}
	if group.Members[0].UseFor != "web app" {
		t.Fatalf("use_for = %q", group.Members[0].UseFor)
	}
}

// Deleting a project must not make the config unparseable or hide the dangling
// membership from the user.
func TestGroupMemberOfDeletedProjectIsFlaggedNotFatal(t *testing.T) {
	s, from, to, _ := delegationTestStudio(t)
	if _, err := s.SaveProjectGroup(ProjectGroupConfig{
		Name:    "Shop",
		Members: []GroupMemberConfig{{ProjectID: from.ID}, {ProjectID: to.ID}},
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.RemoveProject(to.ID); err != nil {
		t.Fatalf("RemoveProject: %v", err)
	}
	groups := s.ListProjectGroups()
	if len(groups) != 1 || len(groups[0].Members) != 2 {
		t.Fatalf("groups = %+v", groups)
	}
	unknown := 0
	for _, member := range groups[0].Members {
		if member.Unknown {
			unknown++
		}
	}
	if unknown != 1 {
		t.Fatalf("expected exactly one dangling member, got %d: %+v", unknown, groups[0].Members)
	}
}

// The default policy must reproduce the reachability that existed before
// policies, so upgrading changes nothing for anyone.
func TestDelegationPolicyDefaultPreservesReachability(t *testing.T) {
	s, from, to, _ := delegationTestStudio(t)
	s.mu.RLock()
	target := s.projects[to.ID]
	policyGroup, err := s.delegationPolicyAllowsLocked(from.ID, target)
	s.mu.RUnlock()
	if err != nil {
		t.Fatalf("default policy refused a delegation: %v", err)
	}
	if policyGroup.ID != "" {
		t.Fatalf("no group should apply without membership: %+v", policyGroup)
	}
}

// "off" is checked before any session exists, so a refusal leaves no child
// chat and no worktree behind.
func TestDelegationPolicyOffRefusesBeforeSessionCreated(t *testing.T) {
	s, from, to, _ := delegationTestStudio(t)
	if err := s.SetProjectDelegationPolicy(to.ID, DelegationPolicyOff); err != nil {
		t.Fatalf("SetProjectDelegationPolicy: %v", err)
	}
	_, err := s.StartDelegation(from.ID, "default", to.ID, "run", "", "do work")
	if DelegationErrorType(err) != DelegationErrorPolicy {
		t.Fatalf("error_type = %q, want policy (%v)", DelegationErrorType(err), err)
	}
	assertNoDelegationSideEffects(t, s, to)
}

func TestDelegationPolicyGroupRequiresSharedMembership(t *testing.T) {
	s, from, to, _ := delegationTestStudio(t)
	if err := s.SetProjectDelegationPolicy(to.ID, DelegationPolicyGroup); err != nil {
		t.Fatal(err)
	}
	if _, err := s.StartDelegation(from.ID, "default", to.ID, "run", "", "do work"); DelegationErrorType(err) != DelegationErrorPolicy {
		t.Fatalf("ungrouped caller was allowed: %v", err)
	}
	assertNoDelegationSideEffects(t, s, to)

	if _, err := s.SaveProjectGroup(ProjectGroupConfig{
		Name:          "Shop",
		SharedContext: "stack=shop-prod",
		Members:       []GroupMemberConfig{{ProjectID: from.ID}, {ProjectID: to.ID}},
	}); err != nil {
		t.Fatal(err)
	}
	s.mu.RLock()
	target := s.projects[to.ID]
	group, err := s.delegationPolicyAllowsLocked(from.ID, target)
	s.mu.RUnlock()
	if err != nil {
		t.Fatalf("shared group still refused: %v", err)
	}
	if group.SharedContext != "stack=shop-prod" {
		t.Fatalf("group facts not resolved: %+v", group)
	}
}

// Only SharedContext crosses into a member's prompt, and it arrives under the
// same untrusted-context footer as everything else. Description and UseFor are
// orchestrator-facing and must never be injected.
func TestOnlySharedContextReachesTheTargetPrompt(t *testing.T) {
	envelope := crossAgentEnvelope("Caller", "Chat", "p1", "s1", "goal",
		"stack=shop-prod", "Shop", "Redeploy")
	if !strings.Contains(envelope, "stack=shop-prod") {
		t.Fatal("shared context did not reach the envelope")
	}
	if !strings.Contains(envelope, crossAgentInjectionFooter) {
		t.Fatal("shared context is not framed as untrusted attributed context")
	}
	// An orchestrator-facing description must not appear anywhere.
	if strings.Contains(envelope, "how to coordinate") {
		t.Fatal("group description leaked into a member's prompt")
	}
}

func TestDelegateListReportsPolicyRefusalAsUnreachable(t *testing.T) {
	s, from, to, _ := delegationTestStudio(t)
	if err := s.SetProjectDelegationPolicy(to.ID, DelegationPolicyOff); err != nil {
		t.Fatal(err)
	}
	data, _ := s.delegateList(from.ID, nil).Data.(map[string]any)
	targets, _ := data["targets"].([]DelegationTargetInfo)
	if len(targets) != 1 {
		t.Fatalf("targets = %+v", targets)
	}
	if targets[0].Reachable {
		t.Fatal("a project with delegation off is still advertised as reachable")
	}
	if !strings.Contains(targets[0].Reason, "does not accept") {
		t.Fatalf("reason = %q", targets[0].Reason)
	}
}

func TestDeleteProjectGroupLeavesMembersIntact(t *testing.T) {
	s, from, to, _ := delegationTestStudio(t)
	group, err := s.SaveProjectGroup(ProjectGroupConfig{
		Name:    "Temp",
		Members: []GroupMemberConfig{{ProjectID: from.ID}, {ProjectID: to.ID}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteProjectGroup(group.ID); err != nil {
		t.Fatalf("DeleteProjectGroup: %v", err)
	}
	if len(s.ListProjectGroups()) != 0 {
		t.Fatal("group survived deletion")
	}
	s.mu.RLock()
	_, fromAlive := s.projects[from.ID]
	_, toAlive := s.projects[to.ID]
	s.mu.RUnlock()
	if !fromAlive || !toAlive {
		t.Fatal("deleting a group must not delete its member projects")
	}
	if err := s.DeleteProjectGroup(group.ID); err == nil {
		t.Fatal("deleting an unknown group should report not found")
	}
}

// A group auto-injects text into a member's prompt and widens the set of
// projects a caller can reach, so letting a model edit one would be
// self-authorisation. Group mutation stays a user-only Wails binding.
func TestNoToolActionCanMutateGroupsOrPolicy(t *testing.T) {
	declaration := tools.NewDelegateTool().Declaration()
	actionSchema := declaration.Parameters.Properties["action"]
	if actionSchema == nil || len(actionSchema.Enum) == 0 {
		t.Fatal("delegate has no bounded action enum")
	}
	allowed := map[string]bool{"list": true, "ask": true, "run": true, "batch": true, "status": true, "fetch": true, "cancel": true}
	for _, action := range actionSchema.Enum {
		if !allowed[action] {
			t.Fatalf("delegate exposes an unreviewed action %q", action)
		}
	}
	// No group or policy vocabulary anywhere in the tool's parameter surface.
	for name := range declaration.Parameters.Properties {
		lowered := strings.ToLower(name)
		if strings.Contains(lowered, "group") || strings.Contains(lowered, "policy") {
			t.Fatalf("delegate accepts %q; groups and policy must be user-only", name)
		}
	}

	// The handler itself refuses anything outside the enum.
	s, from, _, _ := delegationTestStudio(t)
	handler := s.makeDelegateHandler()
	ctx := withAskUserRouting(context.Background(), from.ID, "default")
	for _, action := range []string{"save_group", "join_group", "set_policy"} {
		result, err := handler(ctx, action, map[string]any{"name": "x"})
		if err != nil {
			t.Fatalf("handler error for %q: %v", action, err)
		}
		if result.Success {
			t.Fatalf("handler accepted group-mutating action %q", action)
		}
	}
}
