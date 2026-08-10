package studio

import (
	"context"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/ginkida/gokin-studio/internal/engine/tools"
	"google.golang.org/genai"
)

func TestScheduledTaskToolCRUDDefaultsAndProjectIsolation(t *testing.T) {
	withTempConfigDir(t)
	s := newStudioForTest(t)
	first := addTestProject(t, s, "First scheduled project")
	second := addTestProject(t, s, "Second scheduled project")
	session, err := s.CreateChatSession(first.ID)
	if err != nil {
		t.Fatal(err)
	}
	handler := s.makeScheduledTaskHandler(first.ID)

	if _, err := handler(context.Background(), "list", map[string]any{"action": "list"}); err == nil ||
		!strings.Contains(err.Error(), "routing") {
		t.Fatalf("unrouted call error = %v", err)
	}
	wrongRoute := withAskUserRouting(context.Background(), second.ID, "default")
	if _, err := handler(wrongRoute, "list", map[string]any{"action": "list"}); err == nil ||
		!strings.Contains(err.Error(), "routing") {
		t.Fatalf("cross-project routing error = %v", err)
	}

	ctx := withAskUserRouting(context.Background(), first.ID, session.ID)
	createArgs := map[string]any{
		"action": "create", "name": "Kimi weekday review",
		"prompt": "Review the repository every weekday.", "schedule": "weekdays",
		"time_of_day": "09:45", "provider": "kimi", "model": "k3",
	}
	createdResult, err := handler(ctx, "create", createArgs)
	if err != nil || !createdResult.Success {
		t.Fatalf("create = %#v, %v", createdResult, err)
	}
	tasks, err := s.ListScheduledTasks(first.ID)
	if err != nil || len(tasks) != 1 {
		t.Fatalf("tasks = %#v, %v", tasks, err)
	}
	created := tasks[0]
	if created.SessionID != session.ID || created.Provider != "kimi" || created.Model != "k3" ||
		created.ApprovalMode != "manual" || !created.Enabled || created.Schedule != "weekdays" ||
		created.TimeOfDay != "09:45" {
		t.Fatalf("created defaults/routing = %#v", created)
	}

	listResult, err := handler(ctx, "list", map[string]any{"action": "list"})
	if err != nil || !listResult.Success || !strings.Contains(listResult.Content, created.ID) {
		t.Fatalf("list = %#v, %v", listResult, err)
	}
	views, ok := listResult.Data.([]map[string]any)
	if !ok || len(views) != 1 || views[0]["provider"] != "kimi" || views[0]["model"] != "k3" {
		t.Fatalf("list data = %#v", listResult.Data)
	}

	updateArgs := map[string]any{
		"action": "update", "task_id": created.ID, "name": "Kimi interval review",
		"schedule": "interval", "interval_minutes": 45, "approval_mode": "auto",
	}
	updatedResult, err := handler(ctx, "update", updateArgs)
	if err != nil || !updatedResult.Success {
		t.Fatalf("update = %#v, %v", updatedResult, err)
	}
	tasks, _ = s.ListScheduledTasks(first.ID)
	if tasks[0].Schedule != "interval" || tasks[0].IntervalMinutes != 45 ||
		tasks[0].TimeOfDay != "" || tasks[0].ApprovalMode != "auto" {
		t.Fatalf("updated task = %#v", tasks[0])
	}

	for _, tc := range []struct {
		action  string
		enabled bool
	}{
		{"pause", false},
		{"resume", true},
	} {
		result, callErr := handler(ctx, tc.action, map[string]any{"action": tc.action, "task_id": created.ID})
		if callErr != nil || !result.Success {
			t.Fatalf("%s = %#v, %v", tc.action, result, callErr)
		}
		tasks, _ = s.ListScheduledTasks(first.ID)
		if tasks[0].Enabled != tc.enabled {
			t.Fatalf("%s enabled = %v, want %v", tc.action, tasks[0].Enabled, tc.enabled)
		}
	}

	foreign, err := s.SaveScheduledTask(ScheduledTask{
		ProjectID: second.ID, SessionID: "default", Prompt: "Foreign routine",
		Schedule: "manual", Enabled: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := handler(ctx, "update", map[string]any{
		"action": "update", "task_id": foreign.ID, "name": "stolen",
	}); err == nil || !strings.Contains(err.Error(), "not found") {
		t.Fatalf("cross-project update error = %v", err)
	}

	deleteResult, err := handler(ctx, "delete", map[string]any{"action": "delete", "task_id": created.ID})
	if err != nil || !deleteResult.Success {
		t.Fatalf("delete = %#v, %v", deleteResult, err)
	}
	tasks, err = s.ListScheduledTasks(first.ID)
	if err != nil || len(tasks) != 0 {
		t.Fatalf("after delete = %#v, %v", tasks, err)
	}
	foreignTasks, err := s.ListScheduledTasks(second.ID)
	if err != nil || len(foreignTasks) != 1 || foreignTasks[0].ID != foreign.ID {
		t.Fatalf("foreign task changed = %#v, %v", foreignTasks, err)
	}
}

func TestScheduledTaskToolIsWiredIntoProjectRegistry(t *testing.T) {
	withTempConfigDir(t)
	s := newStudioForTest(t)
	info := addTestProject(t, s, "Wired scheduled project")
	s.mu.RLock()
	project := s.projects[info.ID]
	s.mu.RUnlock()

	registry := tools.DefaultRegistry(project.Directory)
	project.initMemoryAndPlan(registry)
	raw, ok := registry.Get("scheduled_task")
	if !ok {
		t.Fatal("scheduled_task missing from default registry")
	}
	tool, ok := raw.(*tools.ScheduledTaskTool)
	if !ok {
		t.Fatalf("scheduled_task type = %T", raw)
	}
	ctx := withAskUserRouting(context.Background(), info.ID, "default")
	result, err := tool.Execute(ctx, map[string]any{"action": "list"})
	if err != nil || !result.Success || !strings.Contains(result.Content, "No scheduled routines") {
		t.Fatalf("wired list = %#v, %v", result, err)
	}
}

func TestScheduledTaskToolRunNowStartsInspectableChildChat(t *testing.T) {
	withTempConfigDir(t)
	s := newStudioForTest(t)
	info := addTestProject(t, s, "Run-now scheduled project")
	s.mu.RLock()
	project := s.projects[info.ID]
	s.mu.RUnlock()
	recorder := &recorder{}
	project.testEmitter = recorder.emit
	project.client = &mockClient{responses: []mockResp{{text: "Scheduled result"}}}
	project.registry = tools.DefaultRegistry(project.Directory)
	project.initMemoryAndPlan(project.registry)

	task, err := s.SaveScheduledTask(ScheduledTask{
		ProjectID: info.ID, SessionID: "default", Name: "Manual review",
		Prompt: "Review now.", Schedule: "manual", Enabled: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	handler := s.makeScheduledTaskHandler(info.ID)
	ctx := withAskUserRouting(context.Background(), info.ID, "default")
	result, err := handler(ctx, "run_now", map[string]any{"action": "run_now", "task_id": task.ID})
	if err != nil || !result.Success {
		t.Fatalf("run_now = %#v, %v", result, err)
	}
	run, ok := result.Data.(ScheduledTaskRun)
	if !ok || run.SessionID == "" || run.SessionID == "default" {
		t.Fatalf("run data = %#v", result.Data)
	}
	child := project.GetSession(run.SessionID)
	if child == nil || child.ParentID != "default" {
		t.Fatalf("child session = %#v", child)
	}

	s.wg.Wait()
	runs, err := s.ListScheduledTaskRuns(info.ID, task.ID)
	if err != nil || len(runs) != 1 || runs[0].SessionID != run.SessionID ||
		runs[0].Status == "running" || runs[0].CompletedAt == 0 {
		t.Fatalf("terminal runs = %#v, %v", runs, err)
	}
	if len(recorder.find(EventSessionsChanged)) == 0 {
		t.Fatal("run_now did not announce the child chat")
	}
}

func TestScheduledTaskApprovalDetailsShowExactFutureWork(t *testing.T) {
	details := toolApprovalDetails("scheduled_task", map[string]any{
		"action": "create", "name": "Morning review", "prompt": "Review open work",
		"schedule": "weekly", "time_of_day": "09:30", "weekday": float64(1),
		"provider": "glm", "model": "glm-5.2", "approval_mode": "manual", "enabled": true,
	})
	seen := make(map[string]string, len(details))
	for _, detail := range details {
		seen[detail.Label] = detail.Value
	}
	for label, want := range map[string]string{
		"Tool": "scheduled_task", "Action": "create", "Name": "Morning review",
		"Prompt": "Review open work", "Schedule": "weekly", "Local time": "09:30",
		"Weekday (0=Sun)": "1", "Provider": "glm", "Model": "glm-5.2",
		"Approval mode": "manual", "Enabled": "true",
	} {
		if seen[label] != want {
			t.Errorf("%s = %q, want %q; details=%#v", label, seen[label], want, details)
		}
	}
}

func TestScheduledTaskMutationIsHardGatedInSkipMode(t *testing.T) {
	scheduled := &countedNamedTool{name: "scheduled_task"}
	registry := tools.NewRegistry()
	registry.MustRegister(scheduled)
	model := &mockClient{responses: []mockResp{
		{funcCalls: []*genai.FunctionCall{
			{Name: "scheduled_task", Args: map[string]any{"action": "list"}},
			{Name: "scheduled_task", Args: map[string]any{
				"action": "create", "prompt": "Future work", "schedule": "manual",
			}},
		}},
		{text: "done"},
	}}
	project, _ := newTestProject(t, model, registry)
	project.PermissionMode = "skip"
	var approvals atomic.Int32
	project.testToolApproval = func(_ context.Context, toolName string) (bool, error) {
		approvals.Add(1)
		if toolName != "scheduled_task" {
			t.Fatalf("approval requested for %q", toolName)
		}
		return false, nil
	}

	runAgent(project, "list routines, then create one")

	if approvals.Load() != 1 {
		t.Fatalf("approvals = %d, want one exact mutation approval", approvals.Load())
	}
	if scheduled.calls.Load() != 1 {
		t.Fatalf("tool executions = %d, want only the read-only list", scheduled.calls.Load())
	}
	if len(model.lastFuncRespResults) != 2 {
		t.Fatalf("provider results = %d, want list plus denied create", len(model.lastFuncRespResults))
	}
	if got, _ := model.lastFuncRespResults[1].Response["error"].(string); !strings.Contains(got, "denied by the user") {
		t.Fatalf("mutation result = %#v", model.lastFuncRespResults[1].Response)
	}
}
