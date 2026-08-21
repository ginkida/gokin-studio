package studio

import "strings"

// Approval cards for cross-agent tools receive opaque IDs (`project_id`,
// `session_id`). "Send this task to 9f2c1a04" is not something a user can
// meaningfully authorise, so the call site resolves those IDs to the names
// shown in the sidebar and the session tab bar before the card is rendered.
//
// This lives outside toolApprovalDetails on purpose: that function is pure and
// unit-tested against a plain args map, while name resolution needs the studio
// registry and its locks.

// decorateApprovalTargets returns the args to render in the approval card,
// with `_target_project_name` / `_target_session_name` filled in when the call
// addresses another project or chat. It never mutates the caller's original
// argument map — a tool executes with exactly what the model sent.
//
// original is the untouched call arguments; args may already be a clone made
// by an earlier decorator (workspace isolation), in which case it is reused.
func (p *Project) decorateApprovalTargets(args, original map[string]any) map[string]any {
	if p == nil || p.studio == nil || args == nil {
		return args
	}
	projectID := strings.TrimSpace(stringArg(args, "project_id"))
	sessionID := strings.TrimSpace(stringArg(args, "session_id"))

	// The _target_* keys are OURS. A model can put anything in its argument
	// map, so a forged "_target_project_name" would otherwise render on the
	// approval card and let a call be labelled with a project it does not
	// touch — a `bash rm -rf` presented as work in "Infra". Strip them
	// unconditionally, then write only what we resolved ourselves.
	forged := false
	for _, key := range approvalTargetKeys {
		if _, present := args[key]; present {
			forged = true
			break
		}
	}
	if projectID == "" && sessionID == "" && !forged {
		return args
	}

	projectName, sessionName := p.studio.resolveApprovalTargetNames(projectID, sessionID)
	if projectName == "" && sessionName == "" && !forged {
		return args
	}
	// Clone before touching anything: the tool must execute with exactly the
	// arguments the model sent.
	decorated := args
	if sameMap(args, original) {
		decorated = cloneHookInput(args)
	}
	for _, key := range approvalTargetKeys {
		delete(decorated, key)
	}
	if projectName != "" {
		decorated["_target_project_name"] = projectName
	}
	if sessionName != "" {
		decorated["_target_session_name"] = sessionName
	}
	return decorated
}

// approvalTargetKeys are the synthetic keys the studio owns on an approval card.
var approvalTargetKeys = []string{"_target_project_name", "_target_session_name"}

func sameMap(a, b map[string]any) bool {
	if len(a) != len(b) {
		return false
	}
	for key := range a {
		if _, ok := b[key]; !ok {
			return false
		}
	}
	return true
}

// resolveApprovalTargetNames maps a project ID and an optional session ID to
// their display names. A missing target resolves to an explicit marker rather
// than an empty string, so the card says the target is unknown instead of
// quietly dropping the row.
//
// Lock order is the documented Studio.mu -> Project.mu -> session.mu, and both
// inner locks are released before returning.
func (s *Studio) resolveApprovalTargetNames(projectID, sessionID string) (string, string) {
	if projectID == "" && sessionID == "" {
		return "", ""
	}
	s.mu.RLock()
	project := s.projects[projectID]
	s.mu.RUnlock()

	projectName := ""
	if projectID != "" {
		if project == nil {
			return "unknown project (" + previewApprovalText(projectID, 64) + ")", ""
		}
		project.mu.RLock()
		projectName = project.Name
		project.mu.RUnlock()
	}

	sessionName := ""
	if sessionID != "" && project != nil {
		if session := project.GetSession(sessionID); session != nil && session.ID == sessionID {
			session.mu.RLock()
			sessionName = session.Name
			session.mu.RUnlock()
		} else {
			// GetSession falls back to the default chat for several legacy
			// call sites. That convenience must never make an approval card
			// claim a target that does not exist.
			sessionName = "unknown chat (" + previewApprovalText(sessionID, 64) + ")"
		}
	}
	return projectName, sessionName
}
