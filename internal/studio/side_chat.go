package studio

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/ginkida/gokin-studio/internal/engine/client"
	wailsRuntime "github.com/wailsapp/wails/v2/pkg/runtime"
	"google.golang.org/genai"
)

const (
	sideChatRequestIDMaxBytes = 64
	sideChatMaxConcurrent     = 4
	sideChatHistoryMaxBytes   = 8 << 20
	sideChatPartMaxBytes      = 512 << 10
	sideChatTimeout           = 10 * time.Minute
)

const sideChatSystemPrompt = `You are answering an ephemeral side question about the current conversation.
Use the supplied conversation only as context. Answer the side question directly and concisely.
This answer will not be added to the main conversation. You are read-only: do not call tools, modify files, claim that you performed actions, or continue the main task.`

type sideChatRun struct {
	projectID string
	sessionID string
	cancel    context.CancelFunc
}

// StartSideQuestion starts a one-shot, read-only model request using a safe
// snapshot of the current session. Results are delivered through sidechat:*
// events and are deliberately not appended to the main transcript.
func (s *Studio) StartSideQuestion(projectID, sessionID, requestID, question string) error {
	if err := validateRPCText("side question", question, ChatMessageMaxBytes, true); err != nil {
		return err
	}
	if err := validateSideChatRequestID(requestID); err != nil {
		return err
	}

	s.mu.RLock()
	p, ok := s.projects[projectID]
	settings := Settings{}
	if s.config != nil {
		settings = s.config.Settings
	}
	s.mu.RUnlock()
	if !ok {
		return fmt.Errorf("project not found: %s", projectID)
	}
	if sessionID == "" {
		sessionID = "default"
	}
	p.mu.RLock()
	session, ok := p.sessions[sessionID]
	provider, model, permissionMode := p.Provider, p.Model, p.PermissionMode
	enforce, budget := p.EnforceBudget, p.BudgetUSD
	p.mu.RUnlock()
	if !ok || session == nil {
		return fmt.Errorf("session not found: %s", sessionID)
	}
	if provider == "" {
		provider = settings.DefaultProvider
	}
	if model == "" {
		model = settings.DefaultModel
	}
	provider = strings.ToLower(strings.TrimSpace(provider))
	model = strings.TrimSpace(model)
	if err := validateStudioProviderModelRuntime(provider, model); err != nil {
		return err
	}
	if enforce && budget > 0 {
		// An unpriced model contributes $0, so the comparison below could never
		// trip and the cap would silently do nothing. Same refusal as the main
		// agent loop.
		if !hasPricing(provider, model) {
			return fmt.Errorf("%s", unenforceableBudgetMessage(provider, model, budget))
		}
		spent := p.totalCostUSD()
		if spent >= budget {
			return fmt.Errorf("budget reached: spent $%.4f of $%.2f limit", spent, budget)
		}
	}

	session.mu.RLock()
	history, historyEpoch := sideChatHistorySnapshot(session.history), session.historyEpoch
	session.mu.RUnlock()
	workDir, err := sessionWorkingDirectory(p, session)
	if err != nil {
		return err
	}

	baseCtx := s.ctx
	if baseCtx == nil {
		baseCtx = context.Background()
	}
	ctx, cancel := context.WithTimeout(baseCtx, sideChatTimeout)
	if err := s.claimSideChat(requestID, projectID, sessionID, cancel); err != nil {
		cancel()
		return err
	}

	started := s.startBackground("side-chat", func() {
		defer cancel()
		defer s.releaseSideChat(requestID, projectID, sessionID)
		s.runSideQuestion(ctx, p, session, settings, provider, model, permissionMode, workDir, requestID, strings.TrimSpace(question), history, historyEpoch)
	})
	if !started {
		cancel()
		s.releaseSideChat(requestID, projectID, sessionID)
		return fmt.Errorf("studio is shutting down")
	}
	return nil
}

// CancelSideQuestion is idempotent. Scope matching prevents one stale drawer
// from cancelling a newer request in another project that reused an ID.
func (s *Studio) CancelSideQuestion(projectID, sessionID, requestID string) error {
	if err := validateSideChatRequestID(requestID); err != nil {
		return err
	}
	if sessionID == "" {
		sessionID = "default"
	}
	s.sideChatMu.Lock()
	run, ok := s.sideChatRuns[requestID]
	s.sideChatMu.Unlock()
	if ok && run.projectID == projectID && run.sessionID == sessionID {
		run.cancel()
	}
	return nil
}

func (s *Studio) runSideQuestion(
	ctx context.Context,
	p *Project,
	session *ChatSession,
	settings Settings,
	provider, model, permissionMode, workDir, requestID, question string,
	history []*genai.Content,
	historyEpoch uint64,
) {
	emitError := func(err error) {
		if err == nil || ctx.Err() == context.Canceled {
			return
		}
		s.emitSideChatEvent(EventSideChatError, SideChatEvent{
			ProjectID: p.ID, SessionID: session.ID, RequestID: requestID,
			Error: humanizeAPIError(err), Provider: provider, Model: model,
		})
	}

	// Production execution clients reuse the project's initialized registry,
	// then filter it with a non-nil empty allowlist. Tests inject the execution
	// factory directly and do not need provider credentials or a base registry.
	if p.testExecutionClientFactory == nil {
		if err := p.initClient(settings); err != nil {
			emitError(err)
			return
		}
	}
	c, _, err := p.newExecutionClient(
		settings, provider, model, permissionMode, sideChatSystemPrompt,
		workDir, map[string]bool{}, true,
	)
	if err != nil {
		emitError(err)
		return
	}
	defer c.Close()
	// Defense in depth for injected clients and future registry changes.
	c.SetTools(nil)

	stream, err := c.SendMessageWithHistory(ctx, history, question)
	if err != nil {
		emitError(err)
		return
	}
	if stream == nil {
		emitError(fmt.Errorf("provider returned an empty response stream"))
		return
	}
	response, err := client.ProcessStream(ctx, stream, &client.StreamHandler{
		OnText: func(text string) {
			if text == "" || ctx.Err() != nil {
				return
			}
			s.emitSideChatEvent(EventSideChatDelta, SideChatEvent{
				ProjectID: p.ID, SessionID: session.ID, RequestID: requestID, Text: text,
			})
		},
	})
	if err != nil {
		emitError(err)
		return
	}
	if response == nil {
		emitError(fmt.Errorf("provider returned an empty response"))
		return
	}
	if len(response.FunctionCalls) > 0 {
		emitError(fmt.Errorf("side chat is read-only and rejected a tool request"))
		return
	}
	if ctx.Err() != nil {
		emitError(ctx.Err())
		return
	}

	cost := EstimateCost(provider, model, response.InputTokens, response.OutputTokens, response.CacheReadInputTokens, response.CacheCreationInputTokens)
	s.persistSideChatUsage(p, session, historyEpoch, cost, response)
	s.emitSideChatEvent(EventSideChatComplete, SideChatEvent{
		ProjectID: p.ID, SessionID: session.ID, RequestID: requestID,
		Text: response.Text, Provider: provider, Model: model,
		InputTokens: response.InputTokens, OutputTokens: response.OutputTokens,
		CacheReadTokens: response.CacheReadInputTokens, CacheWriteTokens: response.CacheCreationInputTokens,
		EstimatedCostUSD: cost,
	})
}

func (s *Studio) persistSideChatUsage(p *Project, session *ChatSession, historyEpoch uint64, cost float64, response *client.Response) {
	// Seed before the new usage reaches disk. bumpTotalCostUSD lazily seeds from
	// persisted stats; doing that after the save would count this request twice.
	_ = p.totalCostUSD()
	// Keep project/session membership stable through the durable commit. Project
	// or session deletion waits, then removes the file after this save; if it
	// already won the race, membership no longer matches and we do nothing.
	s.mu.RLock()
	if s.projects[p.ID] != p {
		s.mu.RUnlock()
		return
	}
	p.mu.RLock()
	if p.sessions[session.ID] != session {
		p.mu.RUnlock()
		s.mu.RUnlock()
		return
	}
	session.mu.Lock()
	if session.historyEpoch != historyEpoch {
		session.mu.Unlock()
		p.mu.RUnlock()
		s.mu.RUnlock()
		return
	}
	if session.usage == nil {
		session.usage = &SessionUsage{}
	}
	session.usage.TotalCostUSD += cost
	session.usage.TotalInputTokens += response.InputTokens
	session.usage.TotalOutputTokens += response.OutputTokens
	session.usage.TotalCacheTokens += response.CacheReadInputTokens + response.CacheCreationInputTokens
	session.usage.TurnCount++
	session.usage.LastTurnAt = time.Now().UnixMilli()
	usageSnapshot := *session.usage
	historySnapshot := append([]*genai.Content(nil), session.history...)
	err := SaveHistoryWithUsage(
		projectSessionStorageKey(p.ID, session.ID), session.Name, session.ParentID,
		&usageSnapshot, historySnapshot,
	)
	session.mu.Unlock()
	p.mu.RUnlock()
	s.mu.RUnlock()
	if err != nil {
		s.logf("error", "side-chat", "failed to save side-chat usage for project %q session %q: %v", p.ID, session.ID, err)
		return
	}
	p.bumpTotalCostUSD(cost)
}

func (s *Studio) claimSideChat(requestID, projectID, sessionID string, cancel context.CancelFunc) error {
	s.sideChatMu.Lock()
	defer s.sideChatMu.Unlock()
	if s.sideChatRuns == nil {
		s.sideChatRuns = make(map[string]sideChatRun)
	}
	if _, exists := s.sideChatRuns[requestID]; exists {
		return fmt.Errorf("side-chat request already exists")
	}
	if len(s.sideChatRuns) >= sideChatMaxConcurrent {
		return fmt.Errorf("too many side-chat requests are already running")
	}
	for _, run := range s.sideChatRuns {
		if run.projectID == projectID && run.sessionID == sessionID {
			return fmt.Errorf("a side question is already running in this chat")
		}
	}
	s.sideChatRuns[requestID] = sideChatRun{projectID: projectID, sessionID: sessionID, cancel: cancel}
	return nil
}

func (s *Studio) releaseSideChat(requestID, projectID, sessionID string) {
	s.sideChatMu.Lock()
	if run, ok := s.sideChatRuns[requestID]; ok && run.projectID == projectID && run.sessionID == sessionID {
		delete(s.sideChatRuns, requestID)
	}
	s.sideChatMu.Unlock()
}

func (s *Studio) cancelSideQuestions(projectID, sessionID string) {
	s.sideChatMu.Lock()
	cancels := make([]context.CancelFunc, 0)
	for _, run := range s.sideChatRuns {
		if run.projectID == projectID && (sessionID == "" || run.sessionID == sessionID) {
			cancels = append(cancels, run.cancel)
		}
	}
	s.sideChatMu.Unlock()
	for _, cancel := range cancels {
		cancel()
	}
}

func (s *Studio) emitSideChatEvent(event string, data SideChatEvent) {
	if s.testSideChatEmitter != nil {
		s.testSideChatEmitter(event, data)
		return
	}
	if s.ctx != nil {
		wailsRuntime.EventsEmit(s.ctx, event, data)
	}
}

func validateSideChatRequestID(id string) error {
	if err := validateRPCText("side-chat request ID", id, sideChatRequestIDMaxBytes, true); err != nil {
		return err
	}
	for _, r := range id {
		if !(r >= 'a' && r <= 'z') && !(r >= 'A' && r <= 'Z') && !(r >= '0' && r <= '9') && r != '-' && r != '_' {
			return fmt.Errorf("side-chat request ID contains invalid characters")
		}
	}
	return nil
}

// sideChatHistorySnapshot strips hidden reasoning/signatures and turns tool
// protocol records into bounded plain text. This gives the side question the
// visible context without enabling tools or replaying provider-specific tool
// state into an independent client.
func sideChatHistorySnapshot(history []*genai.Content) []*genai.Content {
	clean := make([]*genai.Content, 0, len(history))
	for _, content := range history {
		if content == nil || (content.Role != "user" && content.Role != "model") {
			continue
		}
		parts := make([]*genai.Part, 0, len(content.Parts))
		for _, part := range content.Parts {
			if part == nil || part.Thought {
				continue
			}
			switch {
			case part.Text != "":
				parts = append(parts, genai.NewPartFromText(sideChatBoundText(part.Text)))
			case part.InlineData != nil:
				if len(part.InlineData.Data) <= sideChatPartMaxBytes {
					parts = append(parts, genai.NewPartFromBytes(append([]byte(nil), part.InlineData.Data...), part.InlineData.MIMEType))
				} else {
					parts = append(parts, genai.NewPartFromText("[Large attachment omitted from side-chat context]"))
				}
			case part.FunctionCall != nil:
				parts = append(parts, genai.NewPartFromText("[Tool call: "+part.FunctionCall.Name+"]\n"+sideChatJSON(part.FunctionCall.Args)))
			case part.FunctionResponse != nil:
				parts = append(parts, genai.NewPartFromText("[Tool result: "+part.FunctionResponse.Name+"]\n"+sideChatJSON(part.FunctionResponse.Response)))
			}
		}
		if len(parts) == 0 {
			continue
		}
		clean = append(clean, &genai.Content{Role: content.Role, Parts: parts})
	}

	// Bound memory and request size by keeping the newest complete contents.
	start, size := len(clean), 0
	for i := len(clean) - 1; i >= 0; i-- {
		entrySize := sideChatContentSize(clean[i])
		if size > 0 && size+entrySize > sideChatHistoryMaxBytes {
			break
		}
		size += entrySize
		start = i
	}
	clean = clean[start:]
	for len(clean) > 0 && clean[0].Role != "user" {
		clean = clean[1:]
	}
	return clean
}

func sideChatBoundText(value string) string {
	if len(value) <= sideChatPartMaxBytes {
		return value
	}
	cut := sideChatPartMaxBytes
	for cut > 0 && !utf8.ValidString(value[:cut]) {
		cut--
	}
	return value[:cut] + "\n[Content truncated for side chat]"
}

func sideChatJSON(value any) string {
	data, err := json.Marshal(value)
	if err != nil {
		return "[unavailable]"
	}
	return sideChatBoundText(string(data))
}

func sideChatContentSize(content *genai.Content) int {
	size := len(content.Role)
	for _, part := range content.Parts {
		if part == nil {
			continue
		}
		size += len(part.Text)
		if part.InlineData != nil {
			size += len(part.InlineData.Data) + len(part.InlineData.MIMEType)
		}
	}
	return size
}
