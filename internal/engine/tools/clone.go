package tools

// cloneWorkDir uses the requested workspace when present and otherwise keeps
// the source tool's root. The empty-workDir form is used for ordinary
// per-agent cloning; an explicit root is used by isolated workspaces.
func cloneWorkDir(requested, current string) string {
	if requested != "" {
		return requested
	}
	return current
}

// CloneToolForWorkDir returns an independent built-in tool instance, retargeted
// to workDir when the tool is workspace-bound. Dependencies that are designed
// to be shared (persistent stores, handlers, task managers, HTTP transports)
// are retained, while mutable per-agent state such as shell sessions, todo
// lists, history callbacks and agent IDs is not shared.
//
// Unknown extension tools are returned unchanged because the Tool interface
// has no cloning contract. Built-ins must be added here when they acquire
// mutable state or a workspace root.
func CloneToolForWorkDir(tool Tool, workDir string) Tool {
	switch source := tool.(type) {
	case *ReadTool:
		clone := NewReadTool(cloneWorkDir(workDir, source.workDir))
		if workDir == "" {
			clone.predictor = source.predictor
		}
		return clone
	case *WriteTool:
		clone := NewWriteTool(cloneWorkDir(workDir, source.workDir))
		if workDir == "" {
			clone.undoManager = source.undoManager
		}
		clone.diffHandler = source.diffHandler
		clone.diffEnabled = source.diffEnabled
		return clone
	case *EditTool:
		clone := NewEditTool(cloneWorkDir(workDir, source.workDir))
		if workDir == "" {
			clone.undoManager = source.undoManager
		}
		clone.diffHandler = source.diffHandler
		clone.diffEnabled = source.diffEnabled
		return clone
	case *DocumentCreateTool:
		return NewDocumentCreateTool(cloneWorkDir(workDir, source.workDir))
	case *BashTool:
		target := cloneWorkDir(workDir, source.workDir)
		clone := NewBashTool(target)
		if workDir == "" {
			clone.taskManager = source.taskManager
		}
		clone.timeout = source.timeout
		clone.sandboxEnabled = source.sandboxEnabled
		clone.unrestrictedMode = source.unrestrictedMode
		if source.managedWorkspaceApplyBack {
			clone.EnableManagedWorkspaceApplyBackMode(target)
		} else if source.workspaceBoundaryEnabled {
			clone.SetWorkspaceBoundary(target)
		}
		return clone
	case *GlobTool:
		clone := NewGlobTool(cloneWorkDir(workDir, source.workDir))
		if workDir == "" {
			clone.cache = source.cache
			clone.predictor = source.predictor
		}
		return clone
	case *GrepTool:
		clone := NewGrepTool(cloneWorkDir(workDir, source.workDir))
		if workDir == "" {
			clone.cache = source.cache
			clone.predictor = source.predictor
		}
		return clone
	case *TodoTool:
		return NewTodoTool()
	case *ListDirTool:
		return NewListDirTool(cloneWorkDir(workDir, source.baseDir))
	case *DiffTool:
		return NewDiffTool()
	case *TreeTool:
		return NewTreeTool(cloneWorkDir(workDir, source.workDir))
	case *EnvTool:
		return NewEnvTool()
	case *AskUserTool:
		clone := NewAskUserTool()
		clone.handler = source.handler
		return clone
	case *ScheduledTaskTool:
		clone := NewScheduledTaskTool()
		clone.handler = source.handler
		return clone
	case *TaskOutputTool:
		clone := NewTaskOutputTool()
		clone.manager = source.manager
		clone.runner = source.runner
		return clone
	case *TaskStopTool:
		clone := NewTaskStopTool()
		clone.manager = source.manager
		clone.runner = source.runner
		return clone
	case *WebFetchTool:
		return &WebFetchTool{client: source.client, maxSize: source.maxSize}
	case *WebSearchTool:
		return &WebSearchTool{
			client: source.client, provider: source.provider, apiKey: source.apiKey,
			googleCX: source.googleCX, maxResults: source.maxResults,
		}
	case *TaskTool:
		clone := NewTaskTool()
		clone.runner = source.runner
		return clone
	case *CoordinateTool:
		clone := NewCoordinateTool()
		clone.coordinatorFactory = source.coordinatorFactory
		clone.executor = source.executor
		clone.callback = source.callback
		return clone
	case *KillShellTool:
		clone := NewKillShellTool()
		clone.manager = source.manager
		return clone
	case *MemoryTool:
		clone := NewMemoryTool()
		clone.store = source.store
		return clone
	case *MemorizeTool:
		return NewMemorizeTool(source.learning)
	case *PinContextTool:
		clone := NewPinContextTool(source.updater)
		clone.workDir = cloneWorkDir(workDir, source.workDir)
		return clone
	case *HistorySearchTool:
		return NewHistorySearchTool(source.historyGetter)
	case *EnterPlanModeTool:
		clone := NewEnterPlanModeTool()
		clone.manager = source.manager
		return clone
	case *UpdatePlanProgressTool:
		clone := NewUpdatePlanProgressTool()
		clone.manager = source.manager
		return clone
	case *GetPlanStatusTool:
		clone := NewGetPlanStatusTool()
		clone.manager = source.manager
		return clone
	case *ExitPlanModeTool:
		clone := NewExitPlanModeTool()
		clone.manager = source.manager
		return clone
	case *BatchTool:
		clone := NewBatchTool(cloneWorkDir(workDir, source.workDir))
		if workDir == "" {
			clone.undoManager = source.undoManager
		}
		clone.progressCallback = source.progressCallback
		clone.failureThreshold = source.failureThreshold
		return clone
	case *DelegateTool:
		clone := NewDelegateTool()
		clone.handler = source.handler
		return clone
	case *SessionAgentTool:
		clone := NewSessionAgentTool()
		clone.handler = source.handler
		return clone
	case *SearchSessionTranscriptsTool:
		clone := NewSearchSessionTranscriptsTool()
		clone.handler = source.handler
		return clone
	case *CopyTool:
		clone := NewCopyTool(cloneWorkDir(workDir, source.workDir))
		if workDir == "" {
			clone.undoManager = source.undoManager
		}
		return clone
	case *MoveTool:
		clone := NewMoveTool(cloneWorkDir(workDir, source.workDir))
		if workDir == "" {
			clone.undoManager = source.undoManager
		}
		return clone
	case *DeleteTool:
		clone := NewDeleteTool(cloneWorkDir(workDir, source.workDir))
		if workDir == "" {
			clone.undoManager = source.undoManager
		}
		return clone
	case *MkdirTool:
		clone := NewMkdirTool(cloneWorkDir(workDir, source.workDir))
		if workDir == "" {
			clone.undoManager = source.undoManager
		}
		return clone
	case *GitLogTool:
		return NewGitLogTool(cloneWorkDir(workDir, source.workDir))
	case *GitBlameTool:
		return NewGitBlameTool(cloneWorkDir(workDir, source.workDir))
	case *GitDiffTool:
		return NewGitDiffTool(cloneWorkDir(workDir, source.workDir))
	case *GitStatusTool:
		return NewGitStatusTool(cloneWorkDir(workDir, source.workDir))
	case *GitAddTool:
		return NewGitAddTool(cloneWorkDir(workDir, source.workDir))
	case *GitCommitTool:
		return NewGitCommitTool(cloneWorkDir(workDir, source.workDir))
	case *GitBranchTool:
		return NewGitBranchTool(cloneWorkDir(workDir, source.workDir))
	case *GitPRTool:
		return NewGitPRTool(cloneWorkDir(workDir, source.workDir))
	case *SharedMemoryTool:
		clone := NewSharedMemoryTool()
		clone.memory = source.memory
		return clone
	case *UpdateScratchpadTool:
		return NewUpdateScratchpadTool(source.updater)
	case *RunTestsTool:
		return NewRunTestsTool(cloneWorkDir(workDir, source.workDir))
	case *VerifyCodeTool:
		return NewVerifyCodeTool(cloneWorkDir(workDir, source.workDir))
	case *ReviewChangesTool:
		return NewReviewChangesTool(cloneWorkDir(workDir, source.workDir))
	case *CheckImpactTool:
		return NewCheckImpactTool(cloneWorkDir(workDir, source.workDir))
	case *GoToDefinitionTool:
		return NewGoToDefinitionTool(cloneWorkDir(workDir, source.workDir))
	case *FindReferencesTool:
		return NewFindReferencesTool(cloneWorkDir(workDir, source.workDir))
	case *ComputerScreenshotTool:
		clone := NewComputerScreenshotTool(cloneWorkDir(workDir, source.workDir), source.includeImage)
		clone.capture = source.capture
		return clone
	case *ComputerActionTool:
		return &ComputerActionTool{click: source.click, typ: source.typ, key: source.key}
	case *PluginResourceTool:
		allowed := make(map[string]bool, len(source.allowed))
		for name, enabled := range source.allowed {
			allowed[name] = enabled
		}
		return &PluginResourceTool{root: source.root, allowed: allowed}
	case *PluginAgentTool:
		return NewPluginAgentTool(source.specs, source.runner)
	default:
		return tool
	}
}

// CloneRegistryForWorkDir creates an independent registry whose built-in
// workspace tools are rooted at workDir. tools_list must be rebuilt against
// the clone; retaining the original instance would disclose and describe the
// source registry instead.
func CloneRegistryForWorkDir(reg ToolRegistry, workDir string) ToolRegistry {
	clone := NewRegistry()
	if reg == nil {
		return clone
	}

	hadToolsList := false
	for _, tool := range reg.List() {
		if tool == nil {
			continue
		}
		if tool.Name() == "tools_list" {
			hadToolsList = true
			continue
		}
		_ = clone.Register(CloneToolForWorkDir(tool, workDir))
	}
	if hadToolsList {
		clone.MustRegister(NewToolsListTool(clone))
	}
	return clone
}
