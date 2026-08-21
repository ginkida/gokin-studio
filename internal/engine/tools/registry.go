package tools

import (
	"fmt"
	"log"
	"sync"

	"google.golang.org/genai"
)

// Registry manages the collection of available tools.
type Registry struct {
	tools map[string]Tool
	mu    sync.RWMutex
}

// NewRegistry creates a new tool registry.
func NewRegistry() *Registry {
	return &Registry{
		tools: make(map[string]Tool),
	}
}

// Get retrieves a tool by name (read-optimized with RLock).
func (r *Registry) Get(name string) (Tool, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	tool, ok := r.tools[name]
	return tool, ok
}

// List returns all registered tools (read-optimized).
func (r *Registry) List() []Tool {
	r.mu.RLock()
	defer r.mu.RUnlock()

	tools := make([]Tool, 0, len(r.tools))
	for _, tool := range r.tools {
		tools = append(tools, tool)
	}
	return tools
}

// Names returns the names of all registered tools (read-optimized).
func (r *Registry) Names() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()

	names := make([]string, 0, len(r.tools))
	for name := range r.tools {
		names = append(names, name)
	}
	return names
}

// Declarations returns all tool declarations for Gemini (read-optimized).
func (r *Registry) Declarations() []*genai.FunctionDeclaration {
	r.mu.RLock()
	defer r.mu.RUnlock()

	declarations := make([]*genai.FunctionDeclaration, 0, len(r.tools))
	for _, tool := range r.tools {
		declarations = append(declarations, tool.Declaration())
	}
	return declarations
}

// Register adds a tool to the registry.
func (r *Registry) Register(tool Tool) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	name := tool.Name()
	if _, exists := r.tools[name]; exists {
		return fmt.Errorf("tool already registered: %s", name)
	}

	r.tools[name] = tool
	return nil
}

// Unregister removes a tool from the registry. Registry-backed helpers such
// as tools_list observe the change because they retain the same registry
// pointer. It returns whether a tool was present.
func (r *Registry) Unregister(name string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()

	if _, exists := r.tools[name]; !exists {
		return false
	}
	delete(r.tools, name)
	return true
}

// MustRegister adds a tool to the registry and logs a warning on error.
func (r *Registry) MustRegister(tool Tool) {
	if err := r.Register(tool); err != nil {
		log.Printf("WARNING: failed to register tool %q: %v", tool.Name(), err)
	}
}

// GeminiTools returns the tools in Gemini format.
func (r *Registry) GeminiTools() []*genai.Tool {
	return []*genai.Tool{
		{
			FunctionDeclarations: r.Declarations(),
		},
	}
}

// PlanModeControlToolNames are the interactive plan-mode control tools. They
// are a SUBSET of ToolSetPlanning (which also includes task/task_output/task_stop
// background-agent tools), so they are listed explicitly — gating the whole
// ToolSetPlanning would wrongly drop non-plan task tools. Exported so callers
// can combine them with other exclusion sets (e.g. plan-off + memory-off).
var PlanModeControlToolNames = map[string]bool{
	"enter_plan_mode":      true,
	"exit_plan_mode":       true,
	"update_plan_progress": true,
	"get_plan_status":      true,
}

// GeminiToolsExcluding returns the full tool envelope minus the named tools.
// Used to drop feature-gated tools the model must not call because the feature
// is off in config — offering a disabled tool makes the model call it and hit a
// confusing "unavailable" error.
func (r *Registry) GeminiToolsExcluding(exclude map[string]bool) []*genai.Tool {
	all := r.Declarations()
	kept := make([]*genai.FunctionDeclaration, 0, len(all))
	for _, d := range all {
		if d == nil || exclude[d.Name] {
			continue
		}
		kept = append(kept, d)
	}
	return []*genai.Tool{{FunctionDeclarations: kept}}
}

// GeminiToolsExcludingPlanMode drops the interactive plan-mode control tools
// (the model cannot enter_plan_mode and strand itself read-only with no
// interactive plan approval, e.g. in headless/eval runs).
func (r *Registry) GeminiToolsExcludingPlanMode() []*genai.Tool {
	return r.GeminiToolsExcluding(PlanModeControlToolNames)
}

// ToolSet defines a named group of tools.
type ToolSet string

const (
	// ToolSetCore contains essential tools always available.
	ToolSetCore ToolSet = "core"
	// ToolSetGit contains git-related tools.
	ToolSetGit ToolSet = "git"
	// ToolSetPlanning contains plan mode tools.
	ToolSetPlanning ToolSet = "planning"
	// ToolSetAgent contains agent/coordination tools.
	ToolSetAgent ToolSet = "agent"
	// ToolSetWeb contains web fetch/search tools.
	ToolSetWeb ToolSet = "web"
	// ToolSetAdvanced contains advanced code analysis tools.
	ToolSetAdvanced ToolSet = "advanced"
	// ToolSetMemory contains memory and context tools.
	ToolSetMemory ToolSet = "memory"
	// ToolSetFileOps contains file management tools beyond core read/write/edit.
	ToolSetFileOps ToolSet = "fileops"
	// ToolSetSemantic contains semantic search tools (requires embeddings).
	ToolSetSemantic ToolSet = "semantic"
	// ToolSetOllamaCore is a minimal set for Ollama models.
	ToolSetOllamaCore ToolSet = "ollama_core"
)

// toolSetDefinitions maps tool sets to their member tool names.
var toolSetDefinitions = map[ToolSet][]string{
	ToolSetCore: {
		"read", "write", "edit", "bash", "glob", "grep",
		"ask_user", "list_dir", "tree", "diff", "todo",
		"tools_list", "document_create", "scheduled_task",
	},
	ToolSetGit: {
		"git_status", "git_diff", "git_add", "git_commit",
		"git_log", "git_blame", "git_branch", "git_pr",
	},
	ToolSetPlanning: {
		"enter_plan_mode", "update_plan_progress", "get_plan_status",
		"exit_plan_mode",
		"task", "task_output", "task_stop",
	},
	ToolSetAgent: {
		"delegate", "coordinate", "shared_memory", "session_agent", "search_session_transcripts",
	},
	ToolSetWeb: {
		"web_fetch", "web_search",
	},
	ToolSetAdvanced: {
		"batch", "check_impact",
		"verify_code", "run_tests", "review_changes",
		"go_to_definition", "find_references",
	},
	ToolSetSemantic: {
		"go_to_definition", "find_references",
	},
	ToolSetMemory: {
		"memory", "memorize", "pin_context", "history_search",
	},
	ToolSetFileOps: {
		"copy", "move", "delete", "mkdir",
		"env", "kill_shell",
	},
	ToolSetOllamaCore: {
		"read", "write", "edit", "bash", "glob", "grep",
		"ask_user", "list_dir", "todo",
	},
}

// FilteredDeclarations returns declarations for only the specified tool sets.
func (r *Registry) FilteredDeclarations(sets ...ToolSet) []*genai.FunctionDeclaration {
	allowed := r.toolNamesFromSets(sets...)

	r.mu.RLock()
	defer r.mu.RUnlock()

	decls := make([]*genai.FunctionDeclaration, 0, len(allowed))
	for name, tool := range r.tools {
		if allowed[name] {
			decls = append(decls, tool.Declaration())
		}
	}
	return decls
}

// FilteredGeminiTools returns tools in Gemini format for the specified tool sets.
func (r *Registry) FilteredGeminiTools(sets ...ToolSet) []*genai.Tool {
	return []*genai.Tool{
		{
			FunctionDeclarations: r.FilteredDeclarations(sets...),
		},
	}
}

// toolNamesFromSets returns a set of tool names from the given tool sets.
func (r *Registry) toolNamesFromSets(sets ...ToolSet) map[string]bool {
	names := make(map[string]bool)
	for _, set := range sets {
		if tools, ok := toolSetDefinitions[set]; ok {
			for _, name := range tools {
				names[name] = true
			}
		}
	}
	return names
}

// DefaultRegistry creates a registry with all default tools.
func DefaultRegistry(workDir string) *Registry {
	r := NewRegistry()

	// Register all tools
	r.MustRegister(NewReadTool(workDir))
	r.MustRegister(NewWriteTool(workDir))
	r.MustRegister(NewEditTool(workDir))
	r.MustRegister(NewDocumentCreateTool(workDir))
	r.MustRegister(NewBashTool(workDir))
	r.MustRegister(NewGlobTool(workDir))
	r.MustRegister(NewGrepTool(workDir))
	r.MustRegister(NewTodoTool())
	r.MustRegister(NewListDirTool(workDir))
	r.MustRegister(NewDiffTool())
	r.MustRegister(NewTreeTool(workDir))
	r.MustRegister(NewEnvTool())
	r.MustRegister(NewAskUserTool())
	r.MustRegister(NewScheduledTaskTool())
	r.MustRegister(NewTaskOutputTool())
	r.MustRegister(NewTaskStopTool())
	r.MustRegister(NewWebFetchTool())
	r.MustRegister(NewWebSearchTool())
	r.MustRegister(NewTaskTool())
	r.MustRegister(NewKillShellTool())
	r.MustRegister(NewMemoryTool())
	r.MustRegister(NewMemorizeTool(nil))
	r.MustRegister(NewPinContextTool(nil))
	r.MustRegister(NewHistorySearchTool(nil))
	r.MustRegister(NewEnterPlanModeTool())
	r.MustRegister(NewUpdatePlanProgressTool())
	r.MustRegister(NewGetPlanStatusTool())
	r.MustRegister(NewExitPlanModeTool())
	r.MustRegister(NewBatchTool(workDir))
	r.MustRegister(NewToolsListTool(r))
	r.MustRegister(NewDelegateTool())
	r.MustRegister(NewSessionAgentTool())
	r.MustRegister(NewSearchSessionTranscriptsTool())

	// File operation tools
	r.MustRegister(NewCopyTool(workDir))
	r.MustRegister(NewMoveTool(workDir))
	r.MustRegister(NewDeleteTool(workDir))
	r.MustRegister(NewMkdirTool(workDir))

	// Git tools
	r.MustRegister(NewGitLogTool(workDir))
	r.MustRegister(NewGitBlameTool(workDir))
	r.MustRegister(NewGitDiffTool(workDir))
	r.MustRegister(NewGitStatusTool(workDir))
	r.MustRegister(NewGitAddTool(workDir))
	r.MustRegister(NewGitCommitTool(workDir))
	r.MustRegister(NewGitBranchTool(workDir))
	r.MustRegister(NewGitPRTool(workDir))

	// Coordination tool
	r.MustRegister(NewCoordinateTool())

	// Shared memory tool (Phase 2)
	r.MustRegister(NewSharedMemoryTool())

	// Agent Scratchpad tool (Phase 7)

	// Testing, verification, and self-review tools
	r.MustRegister(NewRunTestsTool(workDir))
	r.MustRegister(NewVerifyCodeTool(workDir))
	r.MustRegister(NewReviewChangesTool(workDir))
	r.MustRegister(NewCheckImpactTool(workDir))

	// Semantic code navigation tools (gopls-backed with AST fallback)
	r.MustRegister(NewGoToDefinitionTool(workDir))
	r.MustRegister(NewFindReferencesTool(workDir))

	return r
}

// ========== LazyRegistry - Lazy-Loading Tool Registry ==========

// ToolLister interface for listing tools without full registry access.
// Used by ToolsListTool to avoid cyclic dependency.
type ToolLister interface {
	Names() []string
	Declarations() []*genai.FunctionDeclaration
}

// LazyRegistry manages tools with lazy loading.
// Tools are only instantiated when first accessed.
type LazyRegistry struct {
	entries      map[string]*ToolEntry
	declarations map[string]*genai.FunctionDeclaration
	mu           sync.RWMutex
}

// NewLazyRegistry creates a new lazy registry.
func NewLazyRegistry() *LazyRegistry {
	return &LazyRegistry{
		entries:      make(map[string]*ToolEntry),
		declarations: make(map[string]*genai.FunctionDeclaration),
	}
}

// RegisterFactory registers a tool factory with its declaration.
// The tool will not be instantiated until Get() is called.
func (r *LazyRegistry) RegisterFactory(name string, factory ToolFactory, decl *genai.FunctionDeclaration) {
	r.mu.Lock()
	defer r.mu.Unlock()

	r.entries[name] = NewToolEntry(factory)
	if decl != nil {
		r.declarations[name] = decl
	}
}

// Get retrieves a tool by name, instantiating it if necessary.
func (r *LazyRegistry) Get(name string) (Tool, bool) {
	r.mu.RLock()
	entry, ok := r.entries[name]
	r.mu.RUnlock()

	if !ok {
		return nil, false
	}

	return entry.Get(), true
}

// Configure adds a configuration function for a tool.
// The config will be applied when the tool is instantiated.
func (r *LazyRegistry) Configure(name string, cfg func(Tool)) {
	r.mu.RLock()
	entry, ok := r.entries[name]
	r.mu.RUnlock()

	if ok {
		entry.Configure(cfg)
	}
}

// ConfigureTyped adds a typed configuration function for a specific tool type.
func ConfigureTyped[T Tool](r *LazyRegistry, name string, cfg func(T)) {
	r.Configure(name, func(t Tool) {
		if typed, ok := t.(T); ok {
			cfg(typed)
		}
	})
}

// Declarations returns all tool declarations without instantiating tools.
func (r *LazyRegistry) Declarations() []*genai.FunctionDeclaration {
	r.mu.RLock()
	defer r.mu.RUnlock()

	decls := make([]*genai.FunctionDeclaration, 0, len(r.declarations))
	for _, decl := range r.declarations {
		decls = append(decls, decl)
	}
	return decls
}

// Names returns the names of all registered tools.
func (r *LazyRegistry) Names() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()

	names := make([]string, 0, len(r.entries))
	for name := range r.entries {
		names = append(names, name)
	}
	return names
}

// List returns all tools, instantiating them if necessary.
func (r *LazyRegistry) List() []Tool {
	r.mu.RLock()
	entries := make([]*ToolEntry, 0, len(r.entries))
	for _, entry := range r.entries {
		entries = append(entries, entry)
	}
	r.mu.RUnlock()

	tools := make([]Tool, len(entries))
	for i, entry := range entries {
		tools[i] = entry.Get()
	}
	return tools
}

// GeminiTools returns tool declarations in Gemini format without instantiation.
func (r *LazyRegistry) GeminiTools() []*genai.Tool {
	return []*genai.Tool{
		{
			FunctionDeclarations: r.Declarations(),
		},
	}
}

// Register adds an already-instantiated tool to the registry.
// This is for backward compatibility and dynamic tools.
func (r *LazyRegistry) Register(tool Tool) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	name := tool.Name()
	if _, exists := r.entries[name]; exists {
		return fmt.Errorf("tool already registered: %s", name)
	}

	// Create a factory that returns the existing instance
	r.entries[name] = &ToolEntry{
		factory:  func() Tool { return tool },
		instance: tool,
	}
	r.declarations[name] = tool.Declaration()
	return nil
}

// MustRegister adds a tool and logs a warning on error.
func (r *LazyRegistry) MustRegister(tool Tool) {
	if err := r.Register(tool); err != nil {
		log.Printf("WARNING: failed to register tool %q: %v", tool.Name(), err)
	}
}

// IsInstantiated returns true if a tool has been instantiated.
func (r *LazyRegistry) IsInstantiated(name string) bool {
	r.mu.RLock()
	entry, ok := r.entries[name]
	r.mu.RUnlock()

	if !ok {
		return false
	}
	return entry.IsInstantiated()
}

// InstantiatedCount returns the number of instantiated tools.
func (r *LazyRegistry) InstantiatedCount() int {
	r.mu.RLock()
	defer r.mu.RUnlock()

	count := 0
	for _, entry := range r.entries {
		if entry.IsInstantiated() {
			count++
		}
	}
	return count
}

// TotalCount returns the total number of registered tools.
func (r *LazyRegistry) TotalCount() int {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return len(r.entries)
}

// DefaultLazyRegistry creates a lazy registry with all default tools.
// Tools are registered with factories but not instantiated.
// Declarations are resolved lazily from each tool's Declaration() method
// when the tool is first accessed via Get().
func DefaultLazyRegistry(workDir string) *LazyRegistry {
	r := NewLazyRegistry()

	// Core file tools
	r.RegisterFactory("read", func() Tool { return NewReadTool(workDir) }, nil)
	r.RegisterFactory("write", func() Tool { return NewWriteTool(workDir) }, nil)
	r.RegisterFactory("edit", func() Tool { return NewEditTool(workDir) }, nil)
	r.RegisterFactory("document_create", func() Tool { return NewDocumentCreateTool(workDir) }, nil)

	// Search tools
	r.RegisterFactory("glob", func() Tool { return NewGlobTool(workDir) }, nil)
	r.RegisterFactory("grep", func() Tool { return NewGrepTool(workDir) }, nil)

	// Shell and execution
	r.RegisterFactory("bash", func() Tool { return NewBashTool(workDir) }, nil)
	r.RegisterFactory("run_tests", func() Tool { return NewRunTestsTool(workDir) }, nil)
	r.RegisterFactory("task", func() Tool { return NewTaskTool() }, nil)
	r.RegisterFactory("task_output", func() Tool { return NewTaskOutputTool() }, nil)
	r.RegisterFactory("task_stop", func() Tool { return NewTaskStopTool() }, nil)
	r.RegisterFactory("coordinate", func() Tool { return NewCoordinateTool() }, nil)
	r.RegisterFactory("kill_shell", func() Tool { return NewKillShellTool() }, nil)

	// Directory tools
	r.RegisterFactory("list_dir", func() Tool { return NewListDirTool(workDir) }, nil)
	r.RegisterFactory("tree", func() Tool { return NewTreeTool(workDir) }, nil)

	// File operations
	r.RegisterFactory("copy", func() Tool { return NewCopyTool(workDir) }, nil)
	r.RegisterFactory("move", func() Tool { return NewMoveTool(workDir) }, nil)
	r.RegisterFactory("delete", func() Tool { return NewDeleteTool(workDir) }, nil)
	r.RegisterFactory("mkdir", func() Tool { return NewMkdirTool(workDir) }, nil)

	// Utility tools
	r.RegisterFactory("diff", func() Tool { return NewDiffTool() }, nil)
	r.RegisterFactory("env", func() Tool { return NewEnvTool() }, nil)
	r.RegisterFactory("todo", func() Tool { return NewTodoTool() }, nil)
	// User interaction
	r.RegisterFactory("ask_user", func() Tool { return NewAskUserTool() }, nil)
	r.RegisterFactory("scheduled_task", func() Tool { return NewScheduledTaskTool() }, nil)
	r.RegisterFactory("delegate", func() Tool { return NewDelegateTool() }, nil)
	r.RegisterFactory("session_agent", func() Tool { return NewSessionAgentTool() }, nil)
	r.RegisterFactory("search_session_transcripts", func() Tool { return NewSearchSessionTranscriptsTool() }, nil)

	// Web tools
	r.RegisterFactory("web_fetch", func() Tool { return NewWebFetchTool() }, nil)
	r.RegisterFactory("web_search", func() Tool { return NewWebSearchTool() }, nil)

	// Memory tools
	r.RegisterFactory("memory", func() Tool { return NewMemoryTool() }, nil)
	r.RegisterFactory("shared_memory", func() Tool { return NewSharedMemoryTool() }, nil)

	// Plan mode tools
	r.RegisterFactory("enter_plan_mode", func() Tool { return NewEnterPlanModeTool() }, nil)
	r.RegisterFactory("update_plan_progress", func() Tool { return NewUpdatePlanProgressTool() }, nil)
	r.RegisterFactory("get_plan_status", func() Tool { return NewGetPlanStatusTool() }, nil)
	r.RegisterFactory("exit_plan_mode", func() Tool { return NewExitPlanModeTool() }, nil)

	// Code analysis tools
	r.RegisterFactory("batch", func() Tool { return NewBatchTool(workDir) }, nil)

	// Git tools
	r.RegisterFactory("git_log", func() Tool { return NewGitLogTool(workDir) }, nil)
	r.RegisterFactory("git_blame", func() Tool { return NewGitBlameTool(workDir) }, nil)
	r.RegisterFactory("git_diff", func() Tool { return NewGitDiffTool(workDir) }, nil)
	r.RegisterFactory("git_status", func() Tool { return NewGitStatusTool(workDir) }, nil)
	r.RegisterFactory("git_add", func() Tool { return NewGitAddTool(workDir) }, nil)
	r.RegisterFactory("git_commit", func() Tool { return NewGitCommitTool(workDir) }, nil)
	r.RegisterFactory("git_branch", func() Tool { return NewGitBranchTool(workDir) }, nil)
	r.RegisterFactory("git_pr", func() Tool { return NewGitPRTool(workDir) }, nil)

	// Other tools
	r.RegisterFactory("memorize", func() Tool { return NewMemorizeTool(nil) }, nil)

	// Custom improvements
	r.RegisterFactory("pin_context", func() Tool { return NewPinContextTool(nil) }, nil)
	r.RegisterFactory("history_search", func() Tool { return NewHistorySearchTool(nil) }, nil)

	// Code verification / self-review tools
	r.RegisterFactory("verify_code", func() Tool { return NewVerifyCodeTool(workDir) }, nil)
	r.RegisterFactory("review_changes", func() Tool { return NewReviewChangesTool(workDir) }, nil)
	r.RegisterFactory("check_impact", func() Tool { return NewCheckImpactTool(workDir) }, nil)

	// Semantic code navigation tools
	r.RegisterFactory("go_to_definition", func() Tool { return NewGoToDefinitionTool(workDir) }, nil)
	r.RegisterFactory("find_references", func() Tool { return NewFindReferencesTool(workDir) }, nil)

	return r
}
