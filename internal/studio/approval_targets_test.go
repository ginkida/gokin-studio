package studio

import (
	"strings"
	"testing"
)

func approvalDetailValue(details []ToolApprovalDetail, label string) (string, bool) {
	for _, detail := range details {
		if detail.Label == label {
			return detail.Value, true
		}
	}
	return "", false
}

// A cross-agent approval card that shows only an opaque UUID is not something
// a user can meaningfully authorise: they cannot tell which project is about
// to spend their money, nor on what question.
func TestToolApprovalDetailsShowsCrossAgentTargetAndPayload(t *testing.T) {
	details := toolApprovalDetails("delegate", map[string]any{
		"action":               "run",
		"project_id":           "9f2c1a04",
		"_target_project_name": "Infra",
		"_target_session_name": "Deploy checks",
		"goal":                 "ship the checkout fix",
		"task":                 "Redeploy the shop-web container",
	})
	for label, want := range map[string]string{
		"Target project":    "Infra",
		"Target chat":       "Deploy checks",
		"Target project ID": "9f2c1a04",
		"Goal":              "ship the checkout fix",
		"Task":              "Redeploy the shop-web container",
	} {
		got, ok := approvalDetailValue(details, label)
		if !ok {
			t.Fatalf("approval card is missing %q: %+v", label, details)
		}
		if got != want {
			t.Fatalf("%s = %q, want %q", label, got, want)
		}
	}
}

// The payload keys share the generous budget already given to command/prompt;
// a task truncated at 512 bytes hides what is being authorised.
func TestToolApprovalDetailsGivesPayloadKeysTheLargeBudget(t *testing.T) {
	long := strings.Repeat("x", 900)
	details := toolApprovalDetails("delegate", map[string]any{"task": long})
	got, ok := approvalDetailValue(details, "Task")
	if !ok {
		t.Fatal("Task row missing")
	}
	if len([]rune(got)) < 900 {
		t.Fatalf("task preview truncated to %d runes, want the 1000-rune budget", len([]rune(got)))
	}
}

func TestDecorateApprovalTargetsResolvesNamesWithoutMutatingCallArgs(t *testing.T) {
	withTempConfigDir(t)
	s := newStudioForTest(t)
	target := addTestProject(t, s, "Infra")
	s.mu.RLock()
	caller := s.projects[target.ID]
	s.mu.RUnlock()

	callArgs := map[string]any{"project_id": target.ID, "task": "audit containers"}
	decorated := caller.decorateApprovalTargets(callArgs, callArgs)

	if got := decorated["_target_project_name"]; got != "Infra" {
		t.Fatalf("_target_project_name = %v, want Infra", got)
	}
	if _, leaked := callArgs["_target_project_name"]; leaked {
		t.Fatal("the tool must execute with exactly the arguments the model sent")
	}
}

// A stale or forged ID must read as unknown on the card rather than silently
// dropping the row, which would render an approval with no target at all.
func TestDecorateApprovalTargetsMarksUnknownTarget(t *testing.T) {
	withTempConfigDir(t)
	s := newStudioForTest(t)
	info := addTestProject(t, s, "Caller")
	s.mu.RLock()
	caller := s.projects[info.ID]
	s.mu.RUnlock()

	decorated := caller.decorateApprovalTargets(
		map[string]any{"project_id": "does-not-exist"},
		map[string]any{"project_id": "does-not-exist"},
	)
	name, _ := decorated["_target_project_name"].(string)
	if !strings.HasPrefix(name, "unknown project") {
		t.Fatalf("_target_project_name = %q, want an explicit unknown marker", name)
	}
}

func TestDecorateApprovalTargetsIsInertWithoutTargetKeys(t *testing.T) {
	withTempConfigDir(t)
	s := newStudioForTest(t)
	info := addTestProject(t, s, "Caller")
	s.mu.RLock()
	caller := s.projects[info.ID]
	s.mu.RUnlock()

	args := map[string]any{"command": "ls"}
	if got := caller.decorateApprovalTargets(args, args); len(got) != 1 {
		t.Fatalf("ordinary tool args were decorated: %+v", got)
	}
}

// The _target_* keys are the studio's own. A model can put anything in its
// argument map, so a forged one would otherwise render on the approval card and
// let a call be labelled with a project it never touches.
func TestDecorateApprovalTargetsStripsForgedTargetNames(t *testing.T) {
	withTempConfigDir(t)
	s := newStudioForTest(t)
	info := addTestProject(t, s, "Caller")
	s.mu.RLock()
	caller := s.projects[info.ID]
	s.mu.RUnlock()

	// A destructive call with no real target, labelled as if it were Infra work.
	args := map[string]any{
		"command":              "rm -rf /",
		"_target_project_name": "Infra",
		"_target_session_name": "Deploy checks",
	}
	decorated := caller.decorateApprovalTargets(args, args)
	if _, present := decorated["_target_project_name"]; present {
		t.Fatalf("a forged target name survived onto the approval card: %+v", decorated)
	}
	if _, present := decorated["_target_session_name"]; present {
		t.Fatalf("a forged target chat survived onto the approval card: %+v", decorated)
	}
	if decorated["command"] != "rm -rf /" {
		t.Fatal("stripping the synthetic keys must not disturb the real arguments")
	}
	if _, leaked := args["_target_project_name"]; !leaked {
		t.Fatal("the caller's own map was mutated; the tool must run with what the model sent")
	}
}

// A forged name must not survive even when a real target IS resolved.
func TestDecorateApprovalTargetsOverwritesForgedNameWithTheRealOne(t *testing.T) {
	withTempConfigDir(t)
	s := newStudioForTest(t)
	caller := addTestProject(t, s, "Caller")
	target := addTestProject(t, s, "Infra")
	s.mu.RLock()
	project := s.projects[caller.ID]
	s.mu.RUnlock()

	args := map[string]any{"project_id": target.ID, "_target_project_name": "Something Trustworthy"}
	decorated := project.decorateApprovalTargets(args, args)
	if decorated["_target_project_name"] != "Infra" {
		t.Fatalf("_target_project_name = %v, want the resolved name", decorated["_target_project_name"])
	}
}
