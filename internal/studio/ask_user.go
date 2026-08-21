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
	routes  map[askUserRoute]*askUserRouteGate
}

func newAskUserRegistry() *askUserRegistry {
	return &askUserRegistry{
		pending: make(map[string]chan string),
		routes:  make(map[askUserRoute]*askUserRouteGate),
	}
}

// register installs one exact owner for id. A duplicate must never replace an
// existing question: its waiter would otherwise remain blocked with no way for
// the frontend to address the orphaned channel.
func (r *askUserRegistry) register(id string) (chan string, bool) {
	ch := make(chan string, 1)
	r.mu.Lock()
	if _, exists := r.pending[id]; exists {
		r.mu.Unlock()
		return nil, false
	}
	r.pending[id] = ch
	r.mu.Unlock()
	return ch, true
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

// cleanup removes id only when ch is still its exact owner. This protects a
// newly registered owner from an old waiter's deferred cleanup (the ABA case).
func (r *askUserRegistry) cleanup(id string, ch chan string) {
	r.mu.Lock()
	if current, ok := r.pending[id]; ok && current == ch {
		delete(r.pending, id)
	}
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

// The frontend intentionally renders one question card per chat. Route gates
// serialize questions for that exact project/session so concurrent MCP App,
// browser, and agent approvals cannot overwrite one another. Other chats stay
// independent.
type askUserRoute struct {
	projectID string
	sessionID string
}

type askUserRouteGate struct {
	token chan struct{}
	refs  int
}

func (r *askUserRegistry) acquireRoute(ctx context.Context, projectID, sessionID string) (func(), error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	route := askUserRoute{projectID: projectID, sessionID: sessionID}
	r.mu.Lock()
	gate := r.routes[route]
	if gate == nil {
		gate = &askUserRouteGate{token: make(chan struct{}, 1)}
		gate.token <- struct{}{}
		r.routes[route] = gate
	}
	gate.refs++
	r.mu.Unlock()

	var once sync.Once
	release := func() {
		once.Do(func() { r.releaseRoute(route, gate) })
	}
	select {
	case <-gate.token:
		// Prefer cancellation if it raced with acquisition; do not surface a
		// question after its owning operation has already ended.
		if err := ctx.Err(); err != nil {
			release()
			return nil, err
		}
		return release, nil
	case <-ctx.Done():
		r.abandonRoute(route, gate)
		return nil, ctx.Err()
	}
}

func (r *askUserRegistry) releaseRoute(route askUserRoute, gate *askUserRouteGate) {
	r.mu.Lock()
	gate.refs--
	if gate.refs == 0 {
		if r.routes[route] == gate {
			delete(r.routes, route)
		}
		r.mu.Unlock()
		return
	}
	r.mu.Unlock()
	gate.token <- struct{}{}
}

func (r *askUserRegistry) abandonRoute(route askUserRoute, gate *askUserRouteGate) {
	r.mu.Lock()
	gate.refs--
	if gate.refs == 0 && r.routes[route] == gate {
		delete(r.routes, route)
	}
	r.mu.Unlock()
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
	if event.SessionID == "" {
		event.SessionID = "default"
	}
	if s.askUsers == nil {
		return "", fmt.Errorf("ask-user registry not initialised")
	}
	releaseRoute, err := s.askUsers.acquireRoute(ctx, event.ProjectID, event.SessionID)
	if err != nil {
		return "", err
	}
	defer releaseRoute()

	var ch chan string
	for {
		event.QuestionID = uuid.New().String()
		var registered bool
		ch, registered = s.askUsers.register(event.QuestionID)
		if registered {
			break
		}
	}
	defer func() {
		// Remove this exact owner before telling the UI it closed, then release
		// the route only after the close event has been emitted. This preserves
		// lifecycle order even when another approval is already queued.
		s.askUsers.cleanup(event.QuestionID, ch)
		wailsRuntime.EventsEmit(wailsCtx, EventAskUserClosed, AskUserClosedEvent{
			ProjectID:  event.ProjectID,
			SessionID:  event.SessionID,
			QuestionID: event.QuestionID,
		})
	}()

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
