package agent

import (
	"context"
	"math"
	"strings"
	"testing"
	"time"

	"github.com/ginkida/gokin-studio/internal/engine/tools"
)

func TestAgentMessengerRejectsUnknownTypeBeforeRegistration(t *testing.T) {
	m := NewAgentMessenger(context.Background(), nil, "source")
	if _, err := m.SendMessage("unknown", "target", "content", nil); err == nil {
		t.Fatal("unknown message type returned nil error")
	}
	m.mu.RLock()
	pending, counter := len(m.pending), m.msgCounter
	m.mu.RUnlock()
	if pending != 0 || counter != 0 {
		t.Fatalf("rejected message mutated registry: pending=%d counter=%d", pending, counter)
	}
}

func TestAgentMessengerRegistrationSkipsOccupiedCorrelationID(t *testing.T) {
	m := NewAgentMessenger(context.Background(), nil, "source")
	original := &pendingAgentMessage{response: make(chan agentMessageResponse, 1)}
	m.pending["msg_source_1"] = original

	messageID, owner, err := m.registerResponse()
	if err != nil {
		t.Fatalf("registerResponse returned error: %v", err)
	}
	defer m.cleanupResponse(messageID, owner)
	if messageID != "msg_source_2" {
		t.Fatalf("registered ID = %q, want msg_source_2", messageID)
	}
	if owner == nil || owner == original {
		t.Fatal("registration did not create a distinct response owner")
	}
	m.mu.RLock()
	stillOriginal := m.pending["msg_source_1"]
	registered := m.pending[messageID]
	m.mu.RUnlock()
	if stillOriginal != original || registered != owner {
		t.Fatal("collision handling overwrote an existing correlation owner")
	}
}

func TestAgentMessengerRejectsUnavailableRunnerBeforeRegistration(t *testing.T) {
	m := NewAgentMessenger(context.Background(), nil, "source")
	if _, err := m.SendMessage("delegate", "general", "content", nil); err == nil || !strings.Contains(err.Error(), "runner") {
		t.Fatalf("SendMessage error = %v, want unavailable runner", err)
	}
	if len(m.pending) != 0 || m.msgCounter != 0 {
		t.Fatalf("rejected message mutated registry: pending=%d counter=%d", len(m.pending), m.msgCounter)
	}
}

func TestAgentMessengerRejectsCancelledContextBeforeRegistration(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	m := NewAgentMessenger(ctx, NewRunner(context.Background(), nil, nil, ""), "source")
	if _, err := m.SendMessage("delegate", "general", "content", nil); err != context.Canceled {
		t.Fatalf("SendMessage error = %v, want context.Canceled", err)
	}
	if len(m.pending) != 0 || m.msgCounter != 0 {
		t.Fatalf("rejected message mutated registry: pending=%d counter=%d", len(m.pending), m.msgCounter)
	}
}

func TestAgentMessengerPrepareMessageCanonicalizesDelegationOptions(t *testing.T) {
	m := NewAgentMessenger(context.Background(), NewRunner(context.Background(), nil, nil, ""), "source")
	input := map[string]any{
		"max_turns":        float64(12),
		"model":            "  model-id  ",
		"delegation_depth": float64(2),
		"reason":           "mutable metadata",
	}

	role, data, err := m.prepareMessage("delegate", " BASH ", "do the work", input)
	if err != nil {
		t.Fatalf("prepareMessage returned error: %v", err)
	}
	input["max_turns"] = float64(99)
	input["model"] = "changed"
	input["delegation_depth"] = float64(4)
	if role != "bash" || data["max_turns"] != 12 || data["model"] != "model-id" || data["delegation_depth"] != 2 {
		t.Fatalf("canonical message = (%q, %#v)", role, data)
	}
	if _, copied := data["reason"]; copied {
		t.Fatal("unused caller metadata was retained by the asynchronous message")
	}
}

func TestAgentMessengerPrepareMessageRejectsMalformedInput(t *testing.T) {
	m := NewAgentMessenger(context.Background(), NewRunner(context.Background(), nil, nil, ""), "source")
	tests := []struct {
		name    string
		role    string
		content string
		data    map[string]any
		want    string
	}{
		{name: "blank content", role: "general", content: " \n ", want: "must not be blank"},
		{name: "invalid utf8", role: "general", content: string([]byte{0xff}), want: "valid UTF-8"},
		{name: "nul content", role: "general", content: "bad\x00content", want: "valid UTF-8"},
		{name: "oversized content", role: "general", content: strings.Repeat("x", maxAgentMessageContentBytes+1), want: "valid UTF-8"},
		{name: "unknown role", role: "not-a-role", content: "work", want: "unknown agent type"},
		{name: "fractional turns", role: "general", content: "work", data: map[string]any{"max_turns": 1.5}, want: "must be an integer"},
		{name: "nan turns", role: "general", content: "work", data: map[string]any{"max_turns": math.NaN()}, want: "must be an integer"},
		{name: "infinite depth", role: "general", content: "work", data: map[string]any{"delegation_depth": math.Inf(1)}, want: "must be an integer"},
		{name: "negative turns", role: "general", content: "work", data: map[string]any{"max_turns": -1}, want: "between 0"},
		{name: "excess depth", role: "general", content: "work", data: map[string]any{"delegation_depth": MaxDelegationDepth + 1}, want: "delegation_depth"},
		{name: "non-text model", role: "general", content: "work", data: map[string]any{"model": 7}, want: "model must be text"},
		{name: "nul model", role: "general", content: "work", data: map[string]any{"model": "bad\x00model"}, want: "valid UTF-8"},
		{name: "oversized model", role: "general", content: "work", data: map[string]any{"model": strings.Repeat("m", maxAgentMessageModelBytes+1)}, want: "valid UTF-8"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, _, err := m.prepareMessage("delegate", test.role, test.content, test.data); err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("prepareMessage error = %v, want substring %q", err, test.want)
			}
		})
	}
}

func TestAgentMessengerBoundsPendingResponses(t *testing.T) {
	m := NewAgentMessenger(context.Background(), nil, "source")
	for i := 0; i < maxPendingAgentMessages; i++ {
		m.pending[string(rune(i+1))] = &pendingAgentMessage{response: make(chan agentMessageResponse, 1)}
	}
	if _, _, err := m.registerResponse(); err == nil || !strings.Contains(err.Error(), "too many pending") {
		t.Fatalf("registerResponse error = %v, want pending limit", err)
	}
	if len(m.pending) != maxPendingAgentMessages || m.msgCounter != 0 {
		t.Fatalf("rejected registration mutated registry: pending=%d counter=%d", len(m.pending), m.msgCounter)
	}
}

func TestAgentMessengerExpiresUnclaimedResponse(t *testing.T) {
	m := NewAgentMessenger(context.Background(), nil, "source")
	m.responseRetention = 10 * time.Millisecond
	messageID, owner, err := m.registerResponse()
	if err != nil {
		t.Fatalf("registerResponse returned error: %v", err)
	}

	deadline := time.Now().Add(time.Second)
	for {
		m.mu.RLock()
		_, exists := m.pending[messageID]
		m.mu.RUnlock()
		if !exists {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("unclaimed response did not expire")
		}
		time.Sleep(time.Millisecond)
	}
	select {
	case <-owner.ctx.Done():
	default:
		t.Fatal("expiring the response did not cancel its handler context")
	}
}

func TestAgentMessengerClaimOwnsResponsePastRetention(t *testing.T) {
	m := NewAgentMessenger(context.Background(), nil, "source")
	m.responseRetention = 10 * time.Millisecond
	messageID, owner, err := m.registerResponse()
	if err != nil {
		t.Fatalf("registerResponse returned error: %v", err)
	}
	claimed, err := m.claimResponse(messageID)
	if err != nil || claimed != owner {
		t.Fatalf("claimResponse = (%p, %v), want owner", claimed, err)
	}
	time.Sleep(30 * time.Millisecond)
	m.mu.RLock()
	current := m.pending[messageID]
	m.mu.RUnlock()
	if current != owner {
		t.Fatal("retention timer removed a claimed response")
	}
	m.cleanupResponse(messageID, owner)
}

func TestAgentMessengerHandlerPanicBecomesResponse(t *testing.T) {
	m := NewAgentMessenger(context.Background(), nil, "source")
	messageID, owner, err := m.registerResponse()
	if err != nil {
		t.Fatalf("registerResponse returned error: %v", err)
	}
	m.runMessageHandler(Message{ID: messageID, Type: "delegate"}, owner, func(Message, *pendingAgentMessage) {
		panic("boom")
	})
	response, err := m.ReceiveResponse(context.Background(), messageID)
	if response != "" || err == nil || !strings.Contains(err.Error(), "handler panicked") {
		t.Fatalf("ReceiveResponse = (%q, %v), want panic error", response, err)
	}
}

func TestAgentMessengerReceiveResponseAcceptsNilContext(t *testing.T) {
	m := NewAgentMessenger(context.Background(), nil, "source")
	owner := &pendingAgentMessage{response: make(chan agentMessageResponse, 1)}
	m.pending["message"] = owner
	owner.response <- agentMessageResponse{content: "done"}

	response, err := m.ReceiveResponse(nil, "message")
	if err != nil || response != "done" {
		t.Fatalf("ReceiveResponse = (%q, %v), want done", response, err)
	}
}

func TestAgentMessengerDelegationFailureIsDeliveredAsError(t *testing.T) {
	dir := t.TempDir()
	runner := NewRunner(context.Background(), &panicAgentClient{}, tools.DefaultRegistry(dir), dir)
	m := NewAgentMessenger(context.Background(), runner, "source")

	messageID, err := m.SendMessage("delegate", "general", "trigger provider panic", map[string]any{"max_turns": 1})
	if err != nil {
		t.Fatalf("SendMessage returned error: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	response, err := m.ReceiveResponse(ctx, messageID)
	if response != "" || err == nil || !strings.Contains(err.Error(), "delegation") || !strings.Contains(err.Error(), "provider exploded") {
		t.Fatalf("ReceiveResponse = (%q, %v), want delivered provider error", response, err)
	}
}

func TestAgentMessengerDepthFailureIsDeliveredAsErrorAndCleaned(t *testing.T) {
	runner := NewRunner(context.Background(), nil, nil, "")
	m := NewAgentMessenger(context.Background(), runner, "source")
	messageID, err := m.SendMessage("delegate", "general", "must not spawn", map[string]any{
		"delegation_depth": MaxDelegationDepth,
	})
	if err != nil {
		t.Fatalf("SendMessage returned error: %v", err)
	}
	response, err := m.ReceiveResponse(context.Background(), messageID)
	if response != "" || err == nil || !strings.Contains(err.Error(), "maximum depth") {
		t.Fatalf("ReceiveResponse = (%q, %v), want depth error", response, err)
	}
	m.mu.RLock()
	_, pending := m.pending[messageID]
	m.mu.RUnlock()
	if pending {
		t.Fatal("terminal error response remained pending")
	}
	runner.mu.RLock()
	agentCount := len(runner.agents)
	runner.mu.RUnlock()
	if agentCount != 0 {
		t.Fatalf("depth failure spawned %d agents", agentCount)
	}
}

func TestAgentMessengerReceiverCancellationStopsDelegatedAgent(t *testing.T) {
	dir := t.TempDir()
	provider := &blockingAgentClient{entered: make(chan struct{})}
	runner := NewRunner(context.Background(), provider, tools.DefaultRegistry(dir), dir)
	m := NewAgentMessenger(context.Background(), runner, "source")

	messageID, err := m.SendMessage("delegate", "general", "wait for cancellation", map[string]any{"max_turns": 1})
	if err != nil {
		t.Fatalf("SendMessage returned error: %v", err)
	}
	receiveCtx, cancelReceive := context.WithCancel(context.Background())
	received := make(chan error, 1)
	go func() {
		_, receiveErr := m.ReceiveResponse(receiveCtx, messageID)
		received <- receiveErr
	}()

	select {
	case <-provider.entered:
	case <-time.After(3 * time.Second):
		t.Fatal("delegated agent did not enter the provider")
	}
	cancelReceive()
	select {
	case receiveErr := <-received:
		if receiveErr != context.Canceled {
			t.Fatalf("ReceiveResponse error = %v, want context.Canceled", receiveErr)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("ReceiveResponse did not observe cancellation")
	}

	deadline := time.Now().Add(3 * time.Second)
	for {
		runner.mu.RLock()
		active := len(runner.activeExecutions)
		runner.mu.RUnlock()
		m.mu.RLock()
		_, pending := m.pending[messageID]
		m.mu.RUnlock()
		if active == 0 && !pending {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("cancelled delegation remained active: active=%d pending=%v", active, pending)
		}
		time.Sleep(time.Millisecond)
	}
}

func TestAgentMessengerResponseHasSingleReceiver(t *testing.T) {
	m := NewAgentMessenger(context.Background(), nil, "source")
	owner := &pendingAgentMessage{response: make(chan agentMessageResponse, 1)}
	m.pending["message"] = owner

	claimed, err := m.claimResponse("message")
	if err != nil || claimed != owner {
		t.Fatalf("first claim = (%p, %v), want owner", claimed, err)
	}
	if _, err := m.claimResponse("message"); err == nil || !strings.Contains(err.Error(), "already being received") {
		t.Fatalf("second claim error = %v, want already-being-received", err)
	}
}

func TestAgentMessengerStaleCleanupCannotDeleteReplacement(t *testing.T) {
	m := NewAgentMessenger(context.Background(), nil, "source")
	old := &pendingAgentMessage{response: make(chan agentMessageResponse, 1)}
	m.pending["reused"] = old
	m.cleanupResponse("reused", old)

	replacement := &pendingAgentMessage{response: make(chan agentMessageResponse, 1)}
	m.pending["reused"] = replacement
	m.cleanupResponse("reused", old)
	m.mu.RLock()
	current := m.pending["reused"]
	m.mu.RUnlock()
	if current != replacement {
		t.Fatal("stale cleanup deleted the replacement owner")
	}
}

func TestAgentMessengerStaleHandlerCannotAnswerReplacement(t *testing.T) {
	m := NewAgentMessenger(context.Background(), nil, "source")
	old := &pendingAgentMessage{response: make(chan agentMessageResponse, 1)}
	replacement := &pendingAgentMessage{response: make(chan agentMessageResponse, 1)}
	m.pending["reused"] = replacement

	if m.deliverResponse("reused", old, "stale") {
		t.Fatal("stale owner delivered a response")
	}
	select {
	case got := <-replacement.response:
		t.Fatalf("replacement received stale response %+v", got)
	default:
	}
	if !m.deliverResponse("reused", replacement, "current") {
		t.Fatal("current owner could not deliver")
	}
	if got := <-replacement.response; got.content != "current" || got.err != nil {
		t.Fatalf("replacement received %+v", got)
	}
}

func TestAgentMessengerReceiveResponseCleansItsOwner(t *testing.T) {
	m := NewAgentMessenger(context.Background(), nil, "source")
	owner := &pendingAgentMessage{response: make(chan agentMessageResponse, 1)}
	m.pending["message"] = owner
	owner.response <- agentMessageResponse{content: "done"}

	got, err := m.ReceiveResponse(context.Background(), "message")
	if err != nil || got != "done" {
		t.Fatalf("ReceiveResponse = (%q, %v), want done", got, err)
	}
	m.mu.RLock()
	_, exists := m.pending["message"]
	m.mu.RUnlock()
	if exists {
		t.Fatal("completed response owner remains registered")
	}
}

func TestAgentMessengerCancelledReceiverCleansItsOwner(t *testing.T) {
	m := NewAgentMessenger(context.Background(), nil, "source")
	owner := &pendingAgentMessage{response: make(chan agentMessageResponse, 1)}
	m.pending["message"] = owner
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if _, err := m.ReceiveResponse(ctx, "message"); err != context.Canceled {
		t.Fatalf("ReceiveResponse error = %v, want context.Canceled", err)
	}
	m.mu.RLock()
	_, exists := m.pending["message"]
	m.mu.RUnlock()
	if exists {
		t.Fatal("cancelled response owner remains registered")
	}
}

func TestAgentMessengerBroadcastSupportsDynamicTypes(t *testing.T) {
	runner := NewRunner(context.Background(), nil, nil, "")
	if err := runner.GetTypeRegistry().RegisterDynamic("custom", "custom agent", nil, "custom prompt"); err != nil {
		t.Fatalf("RegisterDynamic returned error: %v", err)
	}
	runner.agents["target"] = &Agent{ID: "target", Type: AgentType("custom"), status: AgentStatusRunning}
	m := NewAgentMessenger(context.Background(), runner, "source")
	inbox := m.RegisterInbox("target")

	if err := m.Broadcast("notice", " CUSTOM ", "hello"); err != nil {
		t.Fatalf("Broadcast returned error: %v", err)
	}
	select {
	case message := <-inbox:
		if message.ID != "broadcast_source_1" || message.From != "source" || message.To != "target" || message.Type != "notice" || message.Content != "hello" {
			t.Fatalf("broadcast message = %+v", message)
		}
	case <-time.After(time.Second):
		t.Fatal("dynamic agent did not receive broadcast")
	}
}

func TestAgentMessengerBroadcastRejectsMalformedInputWithoutMutation(t *testing.T) {
	runner := NewRunner(context.Background(), nil, nil, "")
	m := NewAgentMessenger(context.Background(), runner, "source")
	tests := []struct {
		name       string
		msgType    string
		targetType string
		content    string
		want       string
	}{
		{name: "blank message type", msgType: " ", targetType: "general", content: "hello", want: "message type"},
		{name: "nul message type", msgType: "bad\x00type", targetType: "general", content: "hello", want: "message type"},
		{name: "blank content", msgType: "notice", targetType: "general", content: " ", want: "content"},
		{name: "unknown target", msgType: "notice", targetType: "missing", content: "hello", want: "unknown agent type"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if err := m.Broadcast(test.msgType, test.targetType, test.content); err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("Broadcast error = %v, want substring %q", err, test.want)
			}
		})
	}
	if m.msgCounter != 0 {
		t.Fatalf("rejected broadcasts advanced counter to %d", m.msgCounter)
	}
}

func TestAgentMessengerBroadcastRejectsUnavailableRunner(t *testing.T) {
	m := NewAgentMessenger(context.Background(), nil, "source")
	if err := m.Broadcast("notice", "general", "hello"); err == nil || !strings.Contains(err.Error(), "runner") {
		t.Fatalf("Broadcast error = %v, want unavailable runner", err)
	}
}

func TestAgentMessengerRegisterInboxIsIdempotent(t *testing.T) {
	m := NewAgentMessenger(context.Background(), nil, "source")
	first := m.RegisterInbox("target")
	second := m.RegisterInbox("target")
	if first != second || len(m.inbox) != 1 {
		t.Fatalf("duplicate registration = (%p, %p), inboxes=%d", first, second, len(m.inbox))
	}

	m.UnregisterInbox("target")
	select {
	case _, open := <-first:
		if open {
			t.Fatal("unregistered inbox remains open")
		}
	case <-time.After(time.Second):
		t.Fatal("unregistered inbox was not closed")
	}
	m.UnregisterInbox("target")
}
