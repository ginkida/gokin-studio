package studio

import (
	"context"
	"fmt"
	"sync"

	"github.com/google/uuid"
	wailsRuntime "github.com/wailsapp/wails/v2/pkg/runtime"
)

// askUserRegistry holds pending ask_user questions from agent goroutines.
// Keyed by question ID; one channel per question. The agent handler blocks
// on its channel until the frontend calls Studio.AnswerQuestion or the
// agent's context is cancelled.
type askUserRegistry struct {
	mu      sync.Mutex
	pending map[string]chan string
}

func newAskUserRegistry() *askUserRegistry {
	return &askUserRegistry{pending: make(map[string]chan string)}
}

func (r *askUserRegistry) register(id string) chan string {
	ch := make(chan string, 1)
	r.mu.Lock()
	r.pending[id] = ch
	r.mu.Unlock()
	return ch
}

func (r *askUserRegistry) resolve(id, answer string) bool {
	r.mu.Lock()
	ch, ok := r.pending[id]
	if ok {
		delete(r.pending, id)
	}
	r.mu.Unlock()
	if !ok {
		return false
	}
	// Non-blocking send — the handler goroutine is the only receiver, and
	// we hold the only reference post-delete so this is always ready.
	ch <- answer
	close(ch)
	return true
}

func (r *askUserRegistry) cleanup(id string) {
	r.mu.Lock()
	delete(r.pending, id)
	r.mu.Unlock()
}

// cancel removes the question and closes its channel without sending a value,
// causing the blocked handler to receive ok=false and return an error.
func (r *askUserRegistry) cancel(id string) bool {
	r.mu.Lock()
	ch, ok := r.pending[id]
	if ok {
		delete(r.pending, id)
	}
	r.mu.Unlock()
	if !ok {
		return false
	}
	close(ch)
	return true
}

// Context keys for routing ask_user calls from tool execution back to the
// session that triggered them. The agent loop seeds these values onto the
// ctx it passes to tool.Execute.
type askUserCtxKey string

const (
	askUserProjectIDKey askUserCtxKey = "askUser.projectID"
	askUserSessionIDKey askUserCtxKey = "askUser.sessionID"
)

// withAskUserRouting returns a copy of ctx carrying the project/session IDs
// the ask_user handler should emit under. The agent loop wraps the per-turn
// ctx with this before invoking tools.
func withAskUserRouting(ctx context.Context, projectID, sessionID string) context.Context {
	ctx = context.WithValue(ctx, askUserProjectIDKey, projectID)
	ctx = context.WithValue(ctx, askUserSessionIDKey, sessionID)
	return ctx
}

func askUserRouting(ctx context.Context) (projectID, sessionID string) {
	if v, ok := ctx.Value(askUserProjectIDKey).(string); ok {
		projectID = v
	}
	if v, ok := ctx.Value(askUserSessionIDKey).(string); ok {
		sessionID = v
	}
	return
}

// makeAskUserHandler returns a tools.QuestionHandler that emits chat:ask_user
// events and blocks until AnswerQuestion resolves them or ctx is cancelled.
// Routing info (project + session) is read from ctx, set by the agent loop
// via withAskUserRouting. wailsCtx is the Wails runtime context used for the
// emit call — stable for the lifetime of the studio.
func (s *Studio) makeAskUserHandler(wailsCtx context.Context) func(ctx context.Context, question string, options []string, defaultOpt string) (string, error) {
	return func(ctx context.Context, question string, options []string, defaultOpt string) (string, error) {
		return s.waitForUserAnswer(wailsCtx, ctx, AskUserEvent{
			Question: question,
			Options:  options,
			Default:  defaultOpt,
		})
	}
}

// waitForUserAnswer emits a prepared question and blocks until the frontend
// resolves it. Both ordinary model questions and first-class approval cards
// use the same registry, cancellation, and race-safe single-resolution path.
func (s *Studio) waitForUserAnswer(wailsCtx, ctx context.Context, event AskUserEvent) (string, error) {
	event.ProjectID, event.SessionID = askUserRouting(ctx)
	event.QuestionID = uuid.New().String()[:12]
	ch := s.askUsers.register(event.QuestionID)
	defer s.askUsers.cleanup(event.QuestionID)

	wailsRuntime.EventsEmit(wailsCtx, EventAskUser, event)

	select {
	case answer, ok := <-ch:
		if !ok {
			return "", fmt.Errorf("user dismissed the question")
		}
		return answer, nil
	case <-ctx.Done():
		return "", ctx.Err()
	}
}

// AnswerQuestion resolves a pending ask_user question from the frontend.
// The agent handler blocked on the question returns immediately with the
// provided answer. Returns an error if no question with that ID is pending.
func (s *Studio) AnswerQuestion(questionID, answer string) error {
	if err := validateRPCText("question ID", questionID, QuestionIDMaxBytes, true); err != nil {
		return err
	}
	if err := validateRPCText("answer", answer, QuestionAnswerMaxBytes, true); err != nil {
		return err
	}
	if s.askUsers == nil {
		return fmt.Errorf("ask-user registry not initialised")
	}
	if !s.askUsers.resolve(questionID, answer) {
		return fmt.Errorf("question %s not found (timed out or cancelled)", questionID)
	}
	return nil
}

// CancelQuestion cancels a pending ask_user question by closing its channel,
// which causes the agent handler to receive an error ("user dismissed the
// question") rather than a confusing pseudo-answer string. Used when the user
// dismisses the question card without providing an answer.
func (s *Studio) CancelQuestion(questionID string) error {
	if err := validateRPCText("question ID", questionID, QuestionIDMaxBytes, true); err != nil {
		return err
	}
	if s.askUsers == nil {
		return fmt.Errorf("ask-user registry not initialised")
	}
	if !s.askUsers.cancel(questionID) {
		return fmt.Errorf("question %s not found", questionID)
	}
	return nil
}
