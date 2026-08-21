package agent

import (
	"encoding/json"
	"fmt"
	"time"
)

// AgentCheckpoint represents a complete agent state for resumption.
type AgentCheckpoint struct {
	// Core state
	AgentState *AgentState `json:"agent_state"`

	// Extended state
	SharedMemorySnapshot map[string]*SharedEntry `json:"shared_memory,omitempty"`
	PlanTreeSnapshot     *SerializedPlanTree     `json:"plan_tree,omitempty"`
	ReflectorState       *ReflectorSnapshot      `json:"reflector,omitempty"`
	ScratchpadContent    string                  `json:"scratchpad,omitempty"`

	// Metadata
	Timestamp     time.Time `json:"timestamp"`
	CheckpointID  string    `json:"checkpoint_id"`
	TriggerReason string    `json:"trigger_reason"` // "auto", "manual", "error"
	TurnNumber    int       `json:"turn_number"`
}

// SerializedPlanTree represents a serializable plan tree.
type SerializedPlanTree struct {
	RootID      string                      `json:"root_id"`
	Nodes       map[string]*SerializedPNode `json:"nodes"`
	CurrentPath []string                    `json:"current_path"`
	TotalNodes  int                         `json:"total_nodes"`
	Goal        string                      `json:"goal,omitempty"`
}

// SerializedPNode represents a serializable plan node.
type SerializedPNode struct {
	ID         string            `json:"id"`
	Action     *SerializedAction `json:"action"`
	Status     string            `json:"status"`
	Children   []string          `json:"children"`
	Result     string            `json:"result,omitempty"`
	Error      string            `json:"error,omitempty"`
	Confidence float64           `json:"confidence"`
}

// SerializedAction represents a serializable planned action.
type SerializedAction struct {
	Type      string         `json:"type"`
	AgentType string         `json:"agent_type"`
	Prompt    string         `json:"prompt"`
	ToolName  string         `json:"tool_name,omitempty"`
	ToolArgs  map[string]any `json:"tool_args,omitempty"`
}

// ReflectorSnapshot captures reflection history for persistence.
type ReflectorSnapshot struct {
	RecentErrors []string `json:"recent_errors"`
	LearnedFixes []string `json:"learned_fixes"`
}

// SaveCheckpoint creates and persists a checkpoint.
func (a *Agent) SaveCheckpoint(reason string) (*AgentCheckpoint, error) {
	state := a.GetState()
	a.stateMu.RLock()
	scratchpad := a.Scratchpad
	a.stateMu.RUnlock()
	cp := &AgentCheckpoint{
		AgentState:        state,
		Timestamp:         time.Now(),
		CheckpointID:      fmt.Sprintf("%s-%d", a.ID, time.Now().UnixNano()),
		TriggerReason:     reason,
		TurnNumber:        a.GetTurnCount(),
		ScratchpadContent: scratchpad,
	}

	// Capture shared memory if available
	if a.sharedMemory != nil {
		cp.SharedMemorySnapshot = a.exportSharedMemory()
	}

	// Keep the compact legacy snapshot for backward compatibility. AgentState
	// already owns a full deep copy and is the authoritative restore source.
	if state.ActivePlan != nil {
		cp.PlanTreeSnapshot = serializePlanTree(state.ActivePlan)
	}

	// Capture reflector state if available
	if a.reflector != nil {
		cp.ReflectorState = a.reflector.Snapshot()
	}

	// Persist to store if available
	if a.store != nil {
		if err := a.store.SaveCheckpoint(cp); err != nil {
			return cp, fmt.Errorf("failed to persist checkpoint: %w", err)
		}
	}

	return cp, nil
}

// RestoreFromCheckpoint restores agent state from checkpoint.
func (a *Agent) RestoreFromCheckpoint(cp *AgentCheckpoint) error {
	if cp == nil || cp.AgentState == nil {
		return fmt.Errorf("cannot restore nil checkpoint state")
	}
	// Restore core history and state
	if err := a.RestoreHistory(cp.AgentState); err != nil {
		return fmt.Errorf("failed to restore history: %w", err)
	}

	// Restore shared memory
	if cp.SharedMemorySnapshot != nil && a.sharedMemory != nil {
		a.importSharedMemory(cp.SharedMemorySnapshot)
	}

	// AgentState contains the lossless plan representation used by current
	// versions. Fall back to the compact snapshot for older checkpoints.
	a.stateMu.RLock()
	hasActivePlan := a.activePlan != nil
	a.stateMu.RUnlock()
	if !hasActivePlan && cp.PlanTreeSnapshot != nil {
		restoredPlan := deserializePlanTree(cp.PlanTreeSnapshot)
		a.stateMu.Lock()
		a.activePlan = restoredPlan
		a.planningMode = restoredPlan != nil
		a.stateMu.Unlock()
	}

	// Restore reflector state
	if cp.ReflectorState != nil && a.reflector != nil {
		a.reflector.Restore(cp.ReflectorState)
	}

	// Restore scratchpad
	a.stateMu.Lock()
	a.Scratchpad = cp.ScratchpadContent
	a.stateMu.Unlock()

	return nil
}

// exportSharedMemory exports shared memory entries for checkpointing.
func (a *Agent) exportSharedMemory() map[string]*SharedEntry {
	entries := a.sharedMemory.ReadAll()
	result := make(map[string]*SharedEntry, len(entries))
	for _, entry := range entries {
		result[entry.Key] = entry
	}
	return result
}

// importSharedMemory imports shared memory entries from checkpoint.
func (a *Agent) importSharedMemory(snapshot map[string]*SharedEntry) {
	a.sharedMemory.restoreEntries(snapshot)
}

// serializePlanTree converts a PlanTree to serializable form.
func serializePlanTree(tree *PlanTree) *SerializedPlanTree {
	if tree == nil {
		return nil
	}
	tree.mu.RLock()
	defer tree.mu.RUnlock()
	if tree.Root == nil {
		return nil
	}

	st := &SerializedPlanTree{
		RootID:     tree.Root.ID,
		Nodes:      make(map[string]*SerializedPNode),
		TotalNodes: tree.TotalNodes,
	}
	if tree.Goal != nil {
		st.Goal = tree.Goal.Description
	}

	// Serialize current path
	for _, node := range tree.BestPath {
		if node != nil {
			st.CurrentPath = append(st.CurrentPath, node.ID)
		}
	}

	// Serialize all nodes
	serializeNode(tree.Root, st.Nodes)

	return st
}

// serializeNode recursively serializes a plan node and its children.
func serializeNode(node *PlanNode, nodes map[string]*SerializedPNode) {
	if node == nil {
		return
	}

	sn := &SerializedPNode{
		ID:         node.ID,
		Status:     string(node.Status),
		Confidence: node.Score, // Use Score field as Confidence
	}

	if node.Action != nil {
		sn.Action = &SerializedAction{
			Type:      string(node.Action.Type),
			AgentType: string(node.Action.AgentType),
			Prompt:    node.Action.Prompt,
			ToolName:  node.Action.ToolName,
			ToolArgs:  cloneStringAnyMap(node.Action.ToolArgs),
		}
	}

	if node.Result != nil {
		sn.Result = node.Result.Output
		sn.Error = node.Result.Error
	}

	for _, child := range node.Children {
		sn.Children = append(sn.Children, child.ID)
		serializeNode(child, nodes)
	}

	nodes[node.ID] = sn
}

// deserializePlanTree reconstructs a PlanTree from serialized form.
func deserializePlanTree(st *SerializedPlanTree) *PlanTree {
	if st == nil || st.RootID == "" {
		return nil
	}

	tree := &PlanTree{
		TotalNodes: st.TotalNodes,
		nodeIndex:  make(map[string]*PlanNode),
	}
	if st.Goal != "" {
		tree.Goal = &PlanGoal{Description: st.Goal}
	}

	// Reconstruct nodes
	for id, sn := range st.Nodes {
		if sn == nil {
			continue
		}
		node := &PlanNode{
			ID:     id,
			Status: PlanNodeStatus(sn.Status),
			Score:  sn.Confidence,
		}

		if sn.Action != nil {
			node.Action = &PlannedAction{
				Type:      ActionType(sn.Action.Type),
				AgentType: AgentType(sn.Action.AgentType),
				Prompt:    sn.Action.Prompt,
				ToolName:  sn.Action.ToolName,
				ToolArgs:  cloneStringAnyMap(sn.Action.ToolArgs),
				NodeID:    id,
			}
		}

		if sn.Result != "" || sn.Error != "" {
			status := AgentStatusCompleted
			if sn.Error != "" {
				status = AgentStatusFailed
			}
			node.Result = &AgentResult{
				Status:    status,
				Output:    sn.Result,
				Error:     sn.Error,
				Completed: true,
			}
		}

		tree.nodeIndex[id] = node
	}

	// Reconstruct parent-child relationships
	for id, sn := range st.Nodes {
		node := tree.nodeIndex[id]
		if sn == nil || node == nil {
			continue
		}
		for _, childID := range sn.Children {
			if child, ok := tree.nodeIndex[childID]; ok {
				node.Children = append(node.Children, child)
				child.ParentID = id
			}
		}
	}

	// Set root
	tree.Root = tree.nodeIndex[st.RootID]
	if tree.Root == nil {
		return nil
	}

	// Reconstruct best path
	for _, nodeID := range st.CurrentPath {
		if node, ok := tree.nodeIndex[nodeID]; ok {
			tree.BestPath = append(tree.BestPath, node)
		}
	}
	if len(tree.BestPath) > 0 {
		tree.CurrentNode = tree.BestPath[len(tree.BestPath)-1]
	} else {
		tree.CurrentNode = tree.Root
	}

	return tree
}

// Snapshot returns a snapshot of the reflector's state.
// Note: The reflector uses immutable patterns, so we only save the error store state reference.
func (r *Reflector) Snapshot() *ReflectorSnapshot {
	if r == nil {
		return nil
	}

	// Reflector patterns are static, no state to snapshot beyond the error store
	// which is persisted separately
	return &ReflectorSnapshot{
		RecentErrors: make([]string, 0),
		LearnedFixes: make([]string, 0),
	}
}

// Restore restores the reflector's state from a snapshot.
// Note: Currently a no-op as reflector patterns are static and error store
// is persisted separately.
func (r *Reflector) Restore(snapshot *ReflectorSnapshot) {
	// No-op: patterns are static, error store has its own persistence
}

// MarshalJSON implements json.Marshaler for AgentCheckpoint.
func (cp *AgentCheckpoint) MarshalJSON() ([]byte, error) {
	type Alias AgentCheckpoint
	return json.Marshal(&struct {
		*Alias
	}{
		Alias: (*Alias)(cp),
	})
}

// UnmarshalJSON implements json.Unmarshaler for AgentCheckpoint.
func (cp *AgentCheckpoint) UnmarshalJSON(data []byte) error {
	type Alias AgentCheckpoint
	aux := &struct {
		*Alias
	}{
		Alias: (*Alias)(cp),
	}
	return json.Unmarshal(data, aux)
}
