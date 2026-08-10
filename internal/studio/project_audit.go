package studio

// auditProjectProvider logs the provider/model change for a project. Both
// fields are project-visible identifiers (not secrets) so values are
// logged in full. Called by SetProjectProvider after the new state is
// applied — old vs new values diffed inline.
func (s *Studio) auditProjectProvider(name, oldProv, oldModel, newProv, newModel string) {
	if oldProv == newProv && oldModel == newModel {
		return
	}
	if oldProv != newProv && oldModel != newModel {
		s.logf("info", "project", "%s: provider %q/%q → %q/%q", name, oldProv, oldModel, newProv, newModel)
		return
	}
	if oldProv != newProv {
		s.logf("info", "project", "%s: provider %q → %q", name, oldProv, newProv)
	}
	if oldModel != newModel {
		s.logf("info", "project", "%s: model %q → %q", name, oldModel, newModel)
	}
}

// auditProjectThinking logs thinking mode/budget changes. Both values
// are configuration metadata (not secrets).
func (s *Studio) auditProjectThinking(name, oldMode string, oldBudget int32, newMode string, newBudget int32) {
	if oldMode != newMode {
		s.logf("info", "project", "%s: thinking mode %q → %q", name, oldMode, newMode)
	}
	if oldBudget != newBudget {
		s.logf("info", "project", "%s: thinking budget %d → %d", name, oldBudget, newBudget)
	}
}

// auditProjectBudget logs per-project monthly cap changes.
func (s *Studio) auditProjectBudget(name string, oldBudget, newBudget float64) {
	if oldBudget == newBudget {
		return
	}
	s.logf("info", "project", "%s: budget $%.2f → $%.2f", name, oldBudget, newBudget)
}

// auditProjectModelParams logs temperature + max-tokens changes.
func (s *Studio) auditProjectModelParams(name string, oldTemp, newTemp float32, oldMax, newMax int) {
	if oldTemp != newTemp {
		s.logf("info", "project", "%s: temperature %.2f → %.2f", name, oldTemp, newTemp)
	}
	if oldMax != newMax {
		s.logf("info", "project", "%s: max tokens %d → %d", name, oldMax, newMax)
	}
}

// auditProjectSystemPrompt logs WHETHER the system prompt changed, never
// the content itself. System prompts can carry domain-sensitive instructions
// (internal API documentation, company conventions, even partial credentials
// pasted by careless users) so logging the value would be a privacy risk for
// the persistent event log + backup archives.
func (s *Studio) auditProjectSystemPrompt(name, oldPrompt, newPrompt string) {
	if oldPrompt == newPrompt {
		return
	}
	oldHas := oldPrompt != ""
	newHas := newPrompt != ""
	switch {
	case !oldHas && newHas:
		s.logf("info", "project", "%s: system prompt set (%d chars, content not logged)", name, len(newPrompt))
	case oldHas && !newHas:
		s.logf("info", "project", "%s: system prompt cleared", name)
	default:
		s.logf("info", "project", "%s: system prompt updated (%d → %d chars, content not logged)", name, len(oldPrompt), len(newPrompt))
	}
}

// auditProjectPinned logs sidebar-pin toggles.
func (s *Studio) auditProjectPinned(name string, oldPinned, newPinned bool) {
	if oldPinned == newPinned {
		return
	}
	state := "pinned to top"
	if !newPinned {
		state = "unpinned"
	}
	s.logf("info", "project", "%s: %s", name, state)
}

// auditProjectAdded logs new project creation. Directory is included
// because it's the user-visible context of "which project is this".
func (s *Studio) auditProjectAdded(name, directory string) {
	s.logf("info", "project", "added project %q at %s", name, directory)
}

// auditProjectDirectory logs an explicit user-driven workspace relink. Paths
// are already user-visible project metadata and are never provider secrets.
func (s *Studio) auditProjectDirectory(name, oldDirectory, newDirectory string) {
	if oldDirectory == newDirectory {
		return
	}
	s.logf("info", "project", "%s: folder %s → %s", name, oldDirectory, newDirectory)
}

// auditProjectRemoved logs deletion.
func (s *Studio) auditProjectRemoved(name string) {
	s.logf("info", "project", "removed project %q", name)
}

// auditProjectRenamed logs a name change.
func (s *Studio) auditProjectRenamed(oldName, newName string) {
	if oldName == newName {
		return
	}
	s.logf("info", "project", "renamed %q → %q", oldName, newName)
}
