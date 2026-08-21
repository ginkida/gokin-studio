package agent

import (
	"encoding/json"
	"sync"
	"time"
)

// PlanNodeStatus represents the execution status of a plan node.
type PlanNodeStatus string

const (
	PlanNodePending   PlanNodeStatus = "pending"
	PlanNodeExecuting PlanNodeStatus = "executing"
	PlanNodeSucceeded PlanNodeStatus = "succeeded"
	PlanNodeFailed    PlanNodeStatus = "failed"
	PlanNodePruned    PlanNodeStatus = "pruned"
)

// ActionType defines the type of action a plan node represents.
type ActionType string

const (
	ActionToolCall  ActionType = "tool_call"
	ActionDelegate  ActionType = "delegate"
	ActionDecompose ActionType = "decompose"
	ActionVerify    ActionType = "verify"
)

// PlannedAction represents an action to be executed as part of a plan.
type PlannedAction struct {
	Type          ActionType     `json:"type"`
	ToolName      string         `json:"tool_name,omitempty"`
	ToolArgs      map[string]any `json:"tool_args,omitempty"`
	AgentType     AgentType      `json:"agent_type,omitempty"`
	Prompt        string         `json:"prompt,omitempty"`
	Prerequisites []string       `json:"prerequisites,omitempty"` // IDs of nodes that must complete first
	NodeID        string         `json:"node_id,omitempty"`       // Back-reference to the node
}

// PlanNode represents a single node in the plan tree.
type PlanNode struct {
	ID       string      `json:"id"`
	ParentID string      `json:"parent_id,omitempty"`
	Children []*PlanNode `json:"children,omitempty"`

	// Action to execute at this node
	Action *PlannedAction `json:"action"`

	// Scoring metrics (0.0-1.0)
	Score        float64 `json:"score"`         // Composite score
	SuccessProb  float64 `json:"success_prob"`  // Probability of success (from StrategyOptimizer)
	CostEstimate float64 `json:"cost_estimate"` // Estimated cost (tokens, time, etc.)
	GoalProgress float64 `json:"goal_progress"` // Progress toward the goal (0.0-1.0)

	// Execution state
	Status PlanNodeStatus `json:"status"`
	Result *AgentResult   `json:"result,omitempty"`
	Depth  int            `json:"depth"`

	// MCTS statistics
	VisitCount  int     `json:"visit_count"`
	TotalReward float64 `json:"total_reward"`

	// Metadata
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
	Error     string    `json:"error,omitempty"`
}

// IsTerminal returns true if the node is in a terminal state.
func (n *PlanNode) IsTerminal() bool {
	return n.Status == PlanNodeSucceeded || n.Status == PlanNodeFailed || n.Status == PlanNodePruned
}

// IsLeaf returns true if the node has no children.
func (n *PlanNode) IsLeaf() bool {
	return len(n.Children) == 0
}

// AverageReward returns the average reward for MCTS.
func (n *PlanNode) AverageReward() float64 {
	if n.VisitCount == 0 {
		return 0
	}
	return n.TotalReward / float64(n.VisitCount)
}

// PlanTree represents the complete plan tree structure.
type PlanTree struct {
	ID          string      `json:"id"`
	Root        *PlanNode   `json:"root"`
	CurrentNode *PlanNode   `json:"-"` // Not serialized
	BestPath    []*PlanNode `json:"-"` // Not serialized
	Goal        *PlanGoal   `json:"goal"`

	// Tree statistics
	TotalNodes    int `json:"total_nodes"`
	MaxDepth      int `json:"max_depth"`
	CurrentDepth  int `json:"current_depth"`
	ExpandedNodes int `json:"expanded_nodes"`

	// Execution tracking
	ReplanCount int       `json:"replan_count"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`

	// Node index for quick lookup
	nodeIndex map[string]*PlanNode `json:"-"`

	mu sync.RWMutex // Protects the tree structure and nodes
}

// PlanGoal describes the objective of the plan.
type PlanGoal struct {
	Description     string        `json:"description"`
	SuccessCriteria []string      `json:"success_criteria,omitempty"`
	MaxDepth        int           `json:"max_depth"`
	MaxNodes        int           `json:"max_nodes"`
	Timeout         time.Duration `json:"timeout"`
	MinSuccessProb  float64       `json:"min_success_prob,omitempty"`
}

type planTreeJSON struct {
	ID            string    `json:"id"`
	Root          *PlanNode `json:"root"`
	CurrentNodeID string    `json:"current_node_id,omitempty"`
	BestPathIDs   []string  `json:"best_path_ids,omitempty"`
	Goal          *PlanGoal `json:"goal"`
	TotalNodes    int       `json:"total_nodes"`
	MaxDepth      int       `json:"max_depth"`
	CurrentDepth  int       `json:"current_depth"`
	ExpandedNodes int       `json:"expanded_nodes"`
	ReplanCount   int       `json:"replan_count"`
	CreatedAt     time.Time `json:"created_at"`
	UpdatedAt     time.Time `json:"updated_at"`
}

// MarshalJSON snapshots the tree under its own lock and includes the pointer-
// derived navigation state that the default JSON representation historically
// dropped. The added fields are optional, so existing state files remain valid.
func (t *PlanTree) MarshalJSON() ([]byte, error) {
	if t == nil {
		return []byte("null"), nil
	}
	t.mu.RLock()
	defer t.mu.RUnlock()

	payload := planTreeJSON{
		ID:            t.ID,
		Root:          t.Root,
		Goal:          t.Goal,
		TotalNodes:    t.TotalNodes,
		MaxDepth:      t.MaxDepth,
		CurrentDepth:  t.CurrentDepth,
		ExpandedNodes: t.ExpandedNodes,
		ReplanCount:   t.ReplanCount,
		CreatedAt:     t.CreatedAt,
		UpdatedAt:     t.UpdatedAt,
	}
	if t.CurrentNode != nil {
		payload.CurrentNodeID = t.CurrentNode.ID
	}
	for _, node := range t.BestPath {
		if node != nil {
			payload.BestPathIDs = append(payload.BestPathIDs, node.ID)
		}
	}
	return json.Marshal(payload)
}

// UnmarshalJSON restores the node index and navigation pointers omitted by
// older state files. When old data has no current-node field, the root is the
// safest continuation point.
func (t *PlanTree) UnmarshalJSON(data []byte) error {
	var payload planTreeJSON
	if err := json.Unmarshal(data, &payload); err != nil {
		return err
	}

	t.mu.Lock()
	defer t.mu.Unlock()
	t.ID = payload.ID
	t.Root = payload.Root
	t.Goal = payload.Goal
	t.TotalNodes = payload.TotalNodes
	t.MaxDepth = payload.MaxDepth
	t.CurrentDepth = payload.CurrentDepth
	t.ExpandedNodes = payload.ExpandedNodes
	t.ReplanCount = payload.ReplanCount
	t.CreatedAt = payload.CreatedAt
	t.UpdatedAt = payload.UpdatedAt
	t.nodeIndex = make(map[string]*PlanNode)
	t.rebuildIndex()
	t.CurrentNode = t.nodeIndex[payload.CurrentNodeID]
	if t.CurrentNode == nil {
		t.CurrentNode = t.Root
	}
	t.BestPath = nil
	for _, nodeID := range payload.BestPathIDs {
		if node := t.nodeIndex[nodeID]; node != nil {
			t.BestPath = append(t.BestPath, node)
		}
	}
	return nil
}

func clonePlanTreeSnapshot(tree *PlanTree) *PlanTree {
	if tree == nil {
		return nil
	}
	tree.mu.RLock()
	defer tree.mu.RUnlock()

	clonedNodes := make(map[*PlanNode]*PlanNode)
	var cloneNode func(*PlanNode) *PlanNode
	cloneNode = func(node *PlanNode) *PlanNode {
		if node == nil {
			return nil
		}
		if existing := clonedNodes[node]; existing != nil {
			return existing
		}
		cloned := *node
		cloned.Action = nil
		cloned.Result = nil
		cloned.Children = nil
		clonedNodes[node] = &cloned
		if node.Action != nil {
			action := *node.Action
			action.ToolArgs = cloneStringAnyMap(node.Action.ToolArgs)
			action.Prerequisites = append([]string(nil), node.Action.Prerequisites...)
			cloned.Action = &action
		}
		cloned.Result = cloneAgentResult(node.Result)
		for _, child := range node.Children {
			cloned.Children = append(cloned.Children, cloneNode(child))
		}
		return &cloned
	}

	snapshot := &PlanTree{
		ID:            tree.ID,
		Root:          cloneNode(tree.Root),
		TotalNodes:    tree.TotalNodes,
		MaxDepth:      tree.MaxDepth,
		CurrentDepth:  tree.CurrentDepth,
		ExpandedNodes: tree.ExpandedNodes,
		ReplanCount:   tree.ReplanCount,
		CreatedAt:     tree.CreatedAt,
		UpdatedAt:     tree.UpdatedAt,
		nodeIndex:     make(map[string]*PlanNode),
	}
	if tree.Goal != nil {
		goal := *tree.Goal
		goal.SuccessCriteria = append([]string(nil), tree.Goal.SuccessCriteria...)
		snapshot.Goal = &goal
	}
	for _, cloned := range clonedNodes {
		snapshot.nodeIndex[cloned.ID] = cloned
	}
	snapshot.CurrentNode = clonedNodes[tree.CurrentNode]
	if snapshot.CurrentNode == nil {
		snapshot.CurrentNode = snapshot.Root
	}
	for _, node := range tree.BestPath {
		if cloned := clonedNodes[node]; cloned != nil {
			snapshot.BestPath = append(snapshot.BestPath, cloned)
		}
	}
	return snapshot
}

// NewPlanTree creates a new plan tree with the given goal.
func NewPlanTree(goal *PlanGoal) *PlanTree {
	id := generateAgentID() // Reuse ID generation
	now := time.Now()

	root := &PlanNode{
		ID:        id + "-root",
		Status:    PlanNodePending,
		Depth:     0,
		CreatedAt: now,
		UpdatedAt: now,
		Action: &PlannedAction{
			Type:   ActionDecompose,
			Prompt: goal.Description,
		},
	}

	return &PlanTree{
		ID:          id,
		Root:        root,
		CurrentNode: root,
		Goal:        goal,
		TotalNodes:  1,
		MaxDepth:    0,
		CreatedAt:   now,
		UpdatedAt:   now,
		nodeIndex:   map[string]*PlanNode{root.ID: root},
	}
}

// GetNode retrieves a node by ID.
// Caller must hold tree.mu (read or write lock).
func (t *PlanTree) GetNode(id string) (*PlanNode, bool) {
	if t.nodeIndex == nil {
		// nodeIndex can be nil after JSON deserialization (json:"-").
		// Caller must hold a write lock if this path is possible.
		t.rebuildIndex()
	}
	node, ok := t.nodeIndex[id]
	return node, ok
}

// EnsureIndex rebuilds the nodeIndex if nil (e.g. after deserialization).
// Must be called under tree.mu write lock or before sharing the tree.
func (t *PlanTree) EnsureIndex() {
	if t.nodeIndex == nil {
		t.rebuildIndex()
	}
}

// AddNode adds a new child node to the parent.
func (t *PlanTree) AddNode(parentID string, action *PlannedAction) *PlanNode {
	parent, ok := t.GetNode(parentID)
	if !ok {
		return nil
	}

	now := time.Now()
	nodeID := generateAgentID()

	node := &PlanNode{
		ID:        nodeID,
		ParentID:  parentID,
		Action:    action,
		Status:    PlanNodePending,
		Depth:     parent.Depth + 1,
		CreatedAt: now,
		UpdatedAt: now,
	}

	if action != nil {
		action.NodeID = nodeID
	}

	parent.Children = append(parent.Children, node)

	if t.nodeIndex == nil {
		t.nodeIndex = make(map[string]*PlanNode)
	}
	t.nodeIndex[nodeID] = node

	t.TotalNodes++
	if node.Depth > t.MaxDepth {
		t.MaxDepth = node.Depth
	}

	t.UpdatedAt = now

	return node
}

// GetParent returns the parent node of the given node.
func (t *PlanTree) GetParent(nodeID string) (*PlanNode, bool) {
	node, ok := t.GetNode(nodeID)
	if !ok || node.ParentID == "" {
		return nil, false
	}
	return t.GetNode(node.ParentID)
}

// PruneSubtree marks a node and all its descendants as pruned.
func (t *PlanTree) PruneSubtree(nodeID string) {
	node, ok := t.GetNode(nodeID)
	if !ok {
		return
	}

	var prune func(n *PlanNode)
	prune = func(n *PlanNode) {
		n.Status = PlanNodePruned
		n.UpdatedAt = time.Now()
		for _, child := range n.Children {
			prune(child)
		}
	}

	prune(node)
	t.UpdatedAt = time.Now()
}

// GetPendingNodes returns all nodes in pending status.
func (t *PlanTree) GetPendingNodes() []*PlanNode {
	var pending []*PlanNode

	var collect func(n *PlanNode)
	collect = func(n *PlanNode) {
		if n.Status == PlanNodePending {
			pending = append(pending, n)
		}
		for _, child := range n.Children {
			collect(child)
		}
	}

	if t.Root != nil {
		collect(t.Root)
	}

	return pending
}

// GetReadyNodes returns nodes that are pending and have all prerequisites met.
func (t *PlanTree) GetReadyNodes() []*PlanNode {
	var ready []*PlanNode

	pending := t.GetPendingNodes()
	for _, node := range pending {
		if t.arePrerequisitesMet(node) {
			ready = append(ready, node)
		}
	}

	return ready
}

// arePrerequisitesMet checks if all prerequisites for a node have succeeded.
func (t *PlanTree) arePrerequisitesMet(node *PlanNode) bool {
	if node.Action == nil || len(node.Action.Prerequisites) == 0 {
		// If no explicit prerequisites, check parent status
		if node.ParentID == "" {
			return true // Root node
		}
		parent, ok := t.GetNode(node.ParentID)
		if !ok {
			return false
		}
		// Parent must have succeeded for children to proceed
		return parent.Status == PlanNodeSucceeded
	}

	for _, prereqID := range node.Action.Prerequisites {
		prereq, ok := t.GetNode(prereqID)
		if !ok || prereq.Status != PlanNodeSucceeded {
			return false
		}
	}
	return true
}

// BlockedNode describes a pending node that cannot proceed and why.
type BlockedNode struct {
	Node   *PlanNode
	Reason string // human-readable explanation
}

// GetBlockedNodes returns pending nodes that are not ready (blocked by failed/incomplete dependencies).
func (t *PlanTree) GetBlockedNodes() []BlockedNode {
	pending := t.GetPendingNodes()
	var blocked []BlockedNode

	for _, node := range pending {
		if t.arePrerequisitesMet(node) {
			continue // this node is ready, not blocked
		}
		reason := t.blockedReason(node)
		blocked = append(blocked, BlockedNode{Node: node, Reason: reason})
	}

	return blocked
}

// blockedReason returns a human-readable explanation of why a node is blocked.
func (t *PlanTree) blockedReason(node *PlanNode) string {
	if node.Action != nil && len(node.Action.Prerequisites) > 0 {
		for _, prereqID := range node.Action.Prerequisites {
			prereq, ok := t.GetNode(prereqID)
			if !ok {
				return "prerequisite " + prereqID + " not found"
			}
			if prereq.Status == PlanNodeFailed {
				label := prereqID
				if prereq.Action != nil && prereq.Action.Prompt != "" {
					label = prereq.Action.Prompt
				}
				return "dependency failed: " + label
			}
			if prereq.Status != PlanNodeSucceeded {
				return "waiting on " + prereqID + " (status: " + string(prereq.Status) + ")"
			}
		}
	}

	if node.ParentID != "" {
		parent, ok := t.GetNode(node.ParentID)
		if !ok {
			return "parent " + node.ParentID + " not found"
		}
		if parent.Status == PlanNodeFailed {
			label := node.ParentID
			if parent.Action != nil && parent.Action.Prompt != "" {
				label = parent.Action.Prompt
			}
			return "parent failed: " + label
		}
		return "parent " + node.ParentID + " not ready (status: " + string(parent.Status) + ")"
	}

	return "unknown blocker"
}

// rebuildIndex rebuilds the node index from the tree structure.
func (t *PlanTree) rebuildIndex() {
	t.nodeIndex = make(map[string]*PlanNode)

	var index func(n *PlanNode)
	index = func(n *PlanNode) {
		t.nodeIndex[n.ID] = n
		for _, child := range n.Children {
			index(child)
		}
	}

	if t.Root != nil {
		index(t.Root)
	}
}

// ReconstructPath builds the path from root to the given node.
func (t *PlanTree) ReconstructPath(nodeID string) []*PlanNode {
	var path []*PlanNode

	current, ok := t.GetNode(nodeID)
	for ok && current != nil {
		path = append([]*PlanNode{current}, path...)
		if current.ParentID == "" {
			break
		}
		current, ok = t.GetNode(current.ParentID)
	}

	return path
}

// GetSucceededPath returns the path of successfully executed nodes.
func (t *PlanTree) GetSucceededPath() []*PlanNode {
	var path []*PlanNode

	var collect func(n *PlanNode)
	collect = func(n *PlanNode) {
		if n.Status == PlanNodeSucceeded {
			path = append(path, n)
			// Continue down the first succeeded child
			for _, child := range n.Children {
				if child.Status == PlanNodeSucceeded {
					collect(child)
					break
				}
			}
		}
	}

	if t.Root != nil {
		collect(t.Root)
	}

	return path
}
