package agent

import (
	"context"
	"fmt"
	"math"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/ginkida/gokin-studio/internal/engine/logging"
)

const (
	agentMessageResponseTimeout   = 5 * time.Minute
	agentMessageResponseRetention = 10 * time.Minute
	maxPendingAgentMessages       = 256
	maxAgentMessageContentBytes   = 1 << 20
	maxAgentMessageModelBytes     = 1024
	maxAgentMessageTypeBytes      = 64
)

// Message represents a message exchanged between agents.
type Message struct {
	ID        string         `json:"id"`
	From      string         `json:"from"`    // Sender agent ID
	To        string         `json:"to"`      // Target role (explore, bash, etc.) or agent ID
	Type      string         `json:"type"`    // help_request, response, delegate, etc.
	Content   string         `json:"content"` // The message text
	Data      map[string]any `json:"data"`    // Additional structured data
	Timestamp time.Time      `json:"timestamp"`
}

// AgentMessenger enables communication between agents.
type AgentMessenger struct {
	ctx         context.Context
	runner      *Runner
	fromAgentID string

	// Message tracking
	inbox      map[string]chan Message         // agentID -> incoming messages
	pending    map[string]*pendingAgentMessage // messageID -> exact response owner
	msgCounter uint64
	// responseRetention bounds entries whose caller never invokes
	// ReceiveResponse. Once claimed, the receiver owns cleanup instead.
	responseRetention time.Duration

	mu sync.RWMutex
}

type pendingAgentMessage struct {
	response chan agentMessageResponse
	claimed  bool
	expiry   *time.Timer
	ctx      context.Context
	cancel   context.CancelFunc
}

type agentMessageResponse struct {
	content string
	err     error
}

// NewAgentMessenger creates a messenger for an agent.
func NewAgentMessenger(ctx context.Context, runner *Runner, fromAgentID string) *AgentMessenger {
	if ctx == nil {
		ctx = context.Background()
	}
	return &AgentMessenger{
		ctx:               ctx,
		runner:            runner,
		fromAgentID:       fromAgentID,
		inbox:             make(map[string]chan Message),
		pending:           make(map[string]*pendingAgentMessage),
		responseRetention: agentMessageResponseRetention,
	}
}

// SendMessage sends a message to another agent (by role or ID).
// Returns the message ID for tracking responses.
func (m *AgentMessenger) SendMessage(msgType string, toRole string, content string, data map[string]any) (string, error) {
	// Reject unsupported work before allocating a correlation entry. The old
	// order leaked one pending channel for every invalid type.
	switch msgType {
	case "help_request", "data_request", "delegate":
	default:
		return "", fmt.Errorf("unknown message type: %s", msgType)
	}
	if m.runner == nil {
		return "", fmt.Errorf("agent messaging is unavailable: runner is not configured")
	}
	select {
	case <-m.ctx.Done():
		return "", m.ctx.Err()
	default:
	}

	toRole, data, err := m.prepareMessage(msgType, toRole, content, data)
	if err != nil {
		return "", err
	}

	msgID, owner, err := m.registerResponse()
	if err != nil {
		return "", err
	}

	msg := Message{
		ID:        msgID,
		From:      m.fromAgentID,
		To:        toRole,
		Type:      msgType,
		Content:   content,
		Data:      data,
		Timestamp: time.Now(),
	}

	logging.Debug("sending inter-agent message",
		"msg_id", msgID,
		"from", m.fromAgentID,
		"to", toRole,
		"type", msgType)

	// Handle the message based on type
	switch msgType {
	case "help_request", "data_request":
		// Spawn a sub-agent to handle the request
		go m.runMessageHandler(msg, owner, m.handleHelpRequest)
	case "delegate":
		// Delegate task to sub-agent
		go m.runMessageHandler(msg, owner, m.handleDelegation)
	}

	return msgID, nil
}

func (m *AgentMessenger) registerResponse() (string, *pendingAgentMessage, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.pending) >= maxPendingAgentMessages {
		return "", nil, fmt.Errorf("too many pending agent messages (maximum %d)", maxPendingAgentMessages)
	}
	var msgID string
	for {
		m.msgCounter++
		msgID = fmt.Sprintf("msg_%s_%d", m.fromAgentID, m.msgCounter)
		if _, exists := m.pending[msgID]; !exists {
			break
		}
	}
	ownerCtx, cancel := context.WithCancel(m.ctx)
	owner := &pendingAgentMessage{
		response: make(chan agentMessageResponse, 1),
		ctx:      ownerCtx,
		cancel:   cancel,
	}
	m.pending[msgID] = owner
	retention := m.responseRetention
	if retention <= 0 {
		retention = agentMessageResponseRetention
	}
	owner.expiry = time.AfterFunc(retention, func() {
		m.expireUnclaimedResponse(msgID, owner)
	})
	return msgID, owner, nil
}

// ReceiveResponse waits for a response to a specific message.
func (m *AgentMessenger) ReceiveResponse(ctx context.Context, messageID string) (string, error) {
	if ctx == nil {
		ctx = m.ctx
		if ctx == nil {
			ctx = context.Background()
		}
	}
	owner, err := m.claimResponse(messageID)
	if err != nil {
		return "", err
	}
	defer m.cleanupResponse(messageID, owner)

	timer := time.NewTimer(agentMessageResponseTimeout)
	defer timer.Stop()

	select {
	case response := <-owner.response:
		return response.content, response.err
	case <-ctx.Done():
		return "", ctx.Err()
	case <-timer.C:
		return "", fmt.Errorf("timeout waiting for response to message %s", messageID)
	}
}

func (m *AgentMessenger) claimResponse(messageID string) (*pendingAgentMessage, error) {
	m.mu.Lock()
	owner, ok := m.pending[messageID]
	if !ok {
		m.mu.Unlock()
		return nil, fmt.Errorf("no pending message with ID: %s", messageID)
	}
	if owner.claimed {
		m.mu.Unlock()
		return nil, fmt.Errorf("response for message %s is already being received", messageID)
	}
	owner.claimed = true
	expiry := owner.expiry
	owner.expiry = nil
	m.mu.Unlock()
	if expiry != nil {
		expiry.Stop()
	}
	return owner, nil
}

func (m *AgentMessenger) cleanupResponse(messageID string, owner *pendingAgentMessage) {
	if owner == nil {
		return
	}
	m.mu.Lock()
	if m.pending[messageID] == owner {
		delete(m.pending, messageID)
	}
	expiry := owner.expiry
	owner.expiry = nil
	m.mu.Unlock()
	if expiry != nil {
		expiry.Stop()
	}
	if owner.cancel != nil {
		owner.cancel()
	}
}

func (m *AgentMessenger) expireUnclaimedResponse(messageID string, owner *pendingAgentMessage) {
	m.mu.Lock()
	expired := false
	if m.pending[messageID] == owner && !owner.claimed {
		delete(m.pending, messageID)
		expired = true
	}
	owner.expiry = nil
	m.mu.Unlock()
	if expired && owner.cancel != nil {
		owner.cancel()
	}
}

// deliverResponse atomically verifies that the handler still owns messageID
// before publishing. A timed-out old handler must not answer a future entry if
// the correlation ID is ever reused after counter wraparound.
func (m *AgentMessenger) deliverResponse(messageID string, owner *pendingAgentMessage, response string) bool {
	return m.deliverMessageResult(messageID, owner, agentMessageResponse{content: response})
}

func (m *AgentMessenger) deliverError(messageID string, owner *pendingAgentMessage, err error) bool {
	if err == nil {
		return false
	}
	return m.deliverMessageResult(messageID, owner, agentMessageResponse{err: err})
}

func (m *AgentMessenger) deliverMessageResult(
	messageID string,
	owner *pendingAgentMessage,
	response agentMessageResponse,
) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.pending[messageID] != owner {
		return false
	}
	select {
	case owner.response <- response:
		return true
	default:
		return false
	}
}

func (m *AgentMessenger) runMessageHandler(
	msg Message,
	owner *pendingAgentMessage,
	handler func(Message, *pendingAgentMessage),
) {
	defer func() {
		if recovered := recover(); recovered != nil {
			logging.Error("agent message handler panicked",
				"msg_id", msg.ID,
				"type", msg.Type,
				"panic", recovered)
			m.deliverError(msg.ID, owner, fmt.Errorf("agent message handler panicked"))
		}
	}()
	handler(msg, owner)
}

func (m *AgentMessenger) prepareMessage(
	msgType string,
	toRole string,
	content string,
	data map[string]any,
) (string, map[string]any, error) {
	if err := validateAgentMessageContent(content); err != nil {
		return "", nil, err
	}

	maxTurns := 15
	model := ""
	canonicalData := map[string]any(nil)
	if msgType == "delegate" {
		maxTurns = 30
		var err error
		maxTurns, err = agentMessageIntOption(data, "max_turns", maxTurns)
		if err != nil {
			return "", nil, err
		}
		if maxTurns < 0 || maxTurns > MaxTurnLimit {
			return "", nil, fmt.Errorf("delegation option max_turns must be between 0 and %d", MaxTurnLimit)
		}

		delegationDepth, err := agentMessageIntOption(data, "delegation_depth", 0)
		if err != nil {
			return "", nil, err
		}
		if delegationDepth < 0 || delegationDepth > MaxDelegationDepth {
			return "", nil, fmt.Errorf("delegation option delegation_depth must be between 0 and %d", MaxDelegationDepth)
		}

		if rawModel, exists := data["model"]; exists {
			var ok bool
			model, ok = rawModel.(string)
			if !ok {
				return "", nil, fmt.Errorf("delegation option model must be text")
			}
			if !utf8.ValidString(model) || strings.ContainsRune(model, 0) || len(model) > maxAgentMessageModelBytes {
				return "", nil, fmt.Errorf("delegation option model must be valid UTF-8 text up to %d bytes", maxAgentMessageModelBytes)
			}
		}

		canonicalData = map[string]any{
			"max_turns":        maxTurns,
			"model":            strings.TrimSpace(model),
			"delegation_depth": delegationDepth,
		}
	}

	deps := m.runner.snapshotAgentDeps()
	canonicalRole, canonicalModel, err := normalizeAgentSpawnRequest(deps, toRole, content, maxTurns, model)
	if err != nil {
		return "", nil, fmt.Errorf("invalid agent message: %w", err)
	}
	if canonicalData != nil {
		canonicalData["model"] = canonicalModel
	}
	return canonicalRole, canonicalData, nil
}

func validateAgentMessageContent(content string) error {
	if strings.TrimSpace(content) == "" {
		return fmt.Errorf("agent message content must not be blank")
	}
	if !utf8.ValidString(content) || strings.ContainsRune(content, 0) || len(content) > maxAgentMessageContentBytes {
		return fmt.Errorf("agent message content must be valid UTF-8 text up to %d bytes", maxAgentMessageContentBytes)
	}
	return nil
}

func agentMessageIntOption(data map[string]any, key string, fallback int) (int, error) {
	raw, exists := data[key]
	if !exists {
		return fallback, nil
	}
	switch number := raw.(type) {
	case int:
		return number, nil
	case int64:
		value := int(number)
		if int64(value) != number {
			return 0, fmt.Errorf("delegation option %s is outside the supported integer range", key)
		}
		return value, nil
	case float64:
		if math.IsNaN(number) || math.IsInf(number, 0) || math.Trunc(number) != number {
			return 0, fmt.Errorf("delegation option %s must be an integer", key)
		}
		value := int(number)
		if float64(value) != number {
			return 0, fmt.Errorf("delegation option %s is outside the supported integer range", key)
		}
		return value, nil
	default:
		return 0, fmt.Errorf("delegation option %s must be an integer", key)
	}
}

// handleHelpRequest spawns a sub-agent to answer a help request.
func (m *AgentMessenger) handleHelpRequest(msg Message, owner *pendingAgentMessage) {
	ctx, cancel := context.WithTimeout(m.messageHandlerContext(owner), 3*time.Minute)
	defer cancel()

	// Map role to agent type
	agentType := msg.To

	// Build prompt for the helper agent
	prompt := fmt.Sprintf(
		"Another agent (ID: %s) is asking for help:\n\n%s\n\n"+
			"Please provide a helpful response to assist them.",
		msg.From, msg.Content)

	logging.Info("spawning helper agent",
		"agent_type", agentType,
		"for_message", msg.ID,
		"requester", msg.From)

	// Spawn the helper agent
	agentID, err := m.runner.Spawn(ctx, agentType, prompt, 15, "")

	if err != nil {
		if !m.deliverError(msg.ID, owner, fmt.Errorf("%s agent failed: %w", agentType, err)) {
			logging.Debug("response owner is gone or already completed", "msg_id", msg.ID)
		}
		return
	}

	// Get the result using exact agent ID (not by type which may return wrong result)
	result, ok := m.runner.GetResult(agentID)
	if !ok || result.Output == "" {
		if !m.deliverError(msg.ID, owner, fmt.Errorf("%s agent returned no response", agentType)) {
			logging.Debug("response owner is gone or already completed", "msg_id", msg.ID)
		}
		return
	}
	if !m.deliverResponse(msg.ID, owner, result.Output) {
		logging.Debug("response owner is gone or already completed", "msg_id", msg.ID)
	}
}

// handleDelegation spawns a sub-agent to handle a delegated task.
func (m *AgentMessenger) handleDelegation(msg Message, owner *pendingAgentMessage) {
	ctx, cancel := context.WithTimeout(m.messageHandlerContext(owner), 5*time.Minute)
	defer cancel()

	agentType := msg.To

	// SendMessage canonicalizes delegation options before launching this
	// goroutine, so caller mutations cannot race with execution.
	maxTurns := msg.Data["max_turns"].(int)
	model := msg.Data["model"].(string)
	delegationDepth := msg.Data["delegation_depth"].(int)

	// Increment delegation depth for the spawned agent
	delegationDepth++

	// Check if we've exceeded the maximum delegation depth
	if delegationDepth > MaxDelegationDepth {
		logging.Warn("delegation depth exceeded",
			"depth", delegationDepth,
			"max", MaxDelegationDepth,
			"from", msg.From)

		err := fmt.Errorf("delegation failed: maximum depth (%d) exceeded", MaxDelegationDepth)
		if !m.deliverError(msg.ID, owner, err) {
			logging.Debug("response owner is gone or already completed", "msg_id", msg.ID)
		}
		return
	}

	logging.Info("delegating to sub-agent",
		"agent_type", agentType,
		"from", msg.From,
		"msg_id", msg.ID)

	// Spawn the delegate agent with delegation depth propagated via context
	spawnCtx := WithDelegationDepth(ctx, delegationDepth)
	agentID, err := m.runner.Spawn(spawnCtx, agentType, msg.Content, maxTurns, model)

	if err != nil {
		if !m.deliverError(msg.ID, owner, fmt.Errorf("delegation to %s failed: %w", agentType, err)) {
			logging.Debug("response owner is gone or already completed", "msg_id", msg.ID)
		}
		return
	}

	// Get the result
	result, ok := m.runner.GetResult(agentID)
	if ok && result.Output != "" {
		if !m.deliverResponse(msg.ID, owner, result.Output) {
			logging.Debug("response owner is gone or already completed", "msg_id", msg.ID)
		}
		return
	}
	if ok && result.Error != "" {
		if !m.deliverError(msg.ID, owner, fmt.Errorf("delegated agent failed: %s", result.Error)) {
			logging.Debug("response owner is gone or already completed", "msg_id", msg.ID)
		}
		return
	}
	if !m.deliverResponse(msg.ID, owner, "Delegated task completed (no output)") {
		logging.Debug("response owner is gone or already completed", "msg_id", msg.ID)
	}
}

func (m *AgentMessenger) messageHandlerContext(owner *pendingAgentMessage) context.Context {
	if owner != nil && owner.ctx != nil {
		return owner.ctx
	}
	if m.ctx != nil {
		return m.ctx
	}
	return context.Background()
}

// Broadcast sends a message to all agents of a given type.
func (m *AgentMessenger) Broadcast(msgType string, targetType string, content string) error {
	if m.runner == nil {
		return fmt.Errorf("agent messaging is unavailable: runner is not configured")
	}
	if strings.TrimSpace(msgType) == "" || !utf8.ValidString(msgType) || strings.ContainsRune(msgType, 0) || len(msgType) > maxAgentMessageTypeBytes {
		return fmt.Errorf("broadcast message type must be valid non-blank UTF-8 text up to %d bytes", maxAgentMessageTypeBytes)
	}
	if err := validateAgentMessageContent(content); err != nil {
		return err
	}
	select {
	case <-m.ctx.Done():
		return m.ctx.Err()
	default:
	}

	deps := m.runner.snapshotAgentDeps()
	canonicalType, _, err := normalizeAgentSpawnRequest(deps, targetType, content, 0, "")
	if err != nil {
		return fmt.Errorf("invalid broadcast target: %w", err)
	}

	// Snapshot agent pointers before consulting their independently locked
	// lifecycle state. This avoids holding Runner.mu across Agent state locks.
	m.runner.mu.RLock()
	agents := make([]*Agent, 0, len(m.runner.agents))
	for _, agent := range m.runner.agents {
		agents = append(agents, agent)
	}
	m.runner.mu.RUnlock()
	var targetIDs []string
	for _, agent := range agents {
		if agent != nil && string(agent.Type) == canonicalType && agent.GetStatus() == AgentStatusRunning {
			targetIDs = append(targetIDs, agent.ID)
		}
	}

	count := 0
	m.mu.Lock()
	for _, agentID := range targetIDs {
		if inbox, ok := m.inbox[agentID]; ok {
			m.msgCounter++
			msg := Message{
				ID:        fmt.Sprintf("broadcast_%s_%d", m.fromAgentID, m.msgCounter),
				From:      m.fromAgentID,
				To:        agentID,
				Type:      msgType,
				Content:   content,
				Timestamp: time.Now(),
			}
			select {
			case inbox <- msg:
				count++
			default:
				// Inbox full, skip
			}
		}
	}
	m.mu.Unlock()

	logging.Debug("broadcast sent", "type", targetType, "recipients", count)
	return nil
}

// RegisterInbox creates an inbox for an agent to receive messages.
func (m *AgentMessenger) RegisterInbox(agentID string) <-chan Message {
	m.mu.Lock()
	defer m.mu.Unlock()

	if inbox, exists := m.inbox[agentID]; exists {
		return inbox
	}
	inbox := make(chan Message, 10)
	m.inbox[agentID] = inbox
	return inbox
}

// UnregisterInbox removes an agent's inbox.
func (m *AgentMessenger) UnregisterInbox(agentID string) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if inbox, ok := m.inbox[agentID]; ok {
		close(inbox)
		delete(m.inbox, agentID)
	}
}
