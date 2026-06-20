package studio

import (
	"context"
	"fmt"
	"strings"
	"sync"

	"github.com/google/uuid"
)

// StudioMessenger implements tools.Messenger for inter-project agent communication.
// It bridges the ask_agent tool to the studio's dispatch system.
type StudioMessenger struct {
	studio    *Studio
	projectID string // ID of the project this messenger belongs to

	pending map[string]chan string // messageID → response channel
	mu      sync.Mutex
}

// NewStudioMessenger creates a messenger for a project.
func NewStudioMessenger(studio *Studio, projectID string) *StudioMessenger {
	return &StudioMessenger{
		studio:    studio,
		projectID: projectID,
		pending:   make(map[string]chan string),
	}
}

// SendMessage dispatches a query to another project by name or role.
// In studio mode, "target_role" is mapped to a project name.
func (m *StudioMessenger) SendMessage(msgType string, toRole string, content string, data map[string]any) (string, error) {
	// Find a target project by matching name or role.
	m.studio.mu.RLock()
	var target *Project
	for _, p := range m.studio.projects {
		if p.ID == m.projectID {
			continue // don't send to self
		}
		// iter 980+: read p.Name under p.mu.RLock to avoid a torn read
		// racing with RenameProject. With studio.mu only, the map shape is
		// safe but the field bytes inside *Project are not — RenameProject
		// writes p.Name under p.mu.Lock(), not s.mu. Without this lock,
		// the prefix match could see a half-updated string and route the
		// ask_agent query to the wrong project (or miss its intended
		// target and fall through to the "first other project" branch).
		p.mu.RLock()
		pName := p.Name
		p.mu.RUnlock()
		// Match by name (case-insensitive prefix).
		if len(pName) >= len(toRole) && len(toRole) > 0 {
			if strings.EqualFold(pName, toRole) || strings.EqualFold(pName[:len(toRole)], toRole) {
				target = p
				break
			}
		}
	}
	// Fallback: just pick the first other project.
	if target == nil {
		for _, p := range m.studio.projects {
			if p.ID != m.projectID {
				target = p
				break
			}
		}
	}
	settings := m.studio.config.Settings
	m.studio.mu.RUnlock()

	if target == nil {
		return "", fmt.Errorf("no target project found for role %q", toRole)
	}

	msgID := uuid.New().String()[:8]
	ch := make(chan string, 1)

	m.mu.Lock()
	m.pending[msgID] = ch
	m.mu.Unlock()

	// Run dispatch in background. Tracked via Studio.wg so Shutdown can wait
	// for it to finish instead of dropping an in-flight ask_agent query.
	if m.studio != nil {
		m.studio.wg.Add(1)
	}
	go func() {
		// iter 970+: panic barrier ahead of the cleanup defer so a panic
		// inside the LLM call below still drains the wg counter and removes
		// the pending entry (otherwise Shutdown blocks forever on wg.Wait
		// and ReceiveResponse leaks).
		defer func() {
			if r := recover(); r != nil {
				logFn := func(level, source, message string) {
					if m.studio != nil {
						m.studio.LogEvent(level, source, message)
					}
				}
				// recoverPanic expects a non-nil recovered value to act on;
				// since we already called recover(), we re-panic into it via
				// a temporary func so it can run its stderr + log routines.
				func() {
					defer recoverPanic("messenger-ask-agent", logFn)
					panic(r)
				}()
				// Surface to caller as an error string so the dispatched
				// query unsticks instead of hanging on ch.
				select {
				case ch <- fmt.Sprintf("error: internal panic — %v", r):
				default:
				}
			}
		}()
		defer func() {
			if m.studio != nil {
				m.studio.wg.Done()
			}
			// NOTE: the pending entry is reaped by ReceiveResponse (the
			// consumer), NOT here. Deleting on dispatch-completion raced the
			// window between SendMessage returning and ReceiveResponse looking
			// up the ID: with a fast client the goroutine wrote the (buffered)
			// response and removed the entry before the caller could find it,
			// surfacing as "no pending message with ID" and losing the result.
		}()

		if err := target.initClient(settings); err != nil {
			ch <- fmt.Sprintf("error: %s", err)
			return
		}

		target.mu.RLock()
		c := target.client
		target.mu.RUnlock()

		if c == nil {
			ch <- "error: client not initialized"
			return
		}

		// Bind the inter-project query to the Studio context so a shutdown
		// cancels in-flight calls instead of leaking goroutines. Falls back
		// to Background if Studio hasn't fully initialised (e.g. unit tests).
		askCtx := context.Background()
		if m.studio != nil && m.studio.ctx != nil {
			askCtx = m.studio.ctx
		}
		resp, err := c.SendMessage(askCtx, content)
		if err != nil {
			ch <- fmt.Sprintf("error: %s", err)
			return
		}
		if resp == nil {
			ch <- "error: nil response"
			return
		}

		collected, err := resp.Collect()
		if err != nil {
			ch <- fmt.Sprintf("error: %s", err)
			return
		}
		ch <- collected.Text
	}()

	return msgID, nil
}

// ReceiveResponse waits for a dispatched message response.
func (m *StudioMessenger) ReceiveResponse(ctx context.Context, messageID string) (string, error) {
	m.mu.Lock()
	ch, ok := m.pending[messageID]
	m.mu.Unlock()

	if !ok {
		return "", fmt.Errorf("no pending message with ID %s", messageID)
	}

	// Reap the correlation entry on retrieval (in the consumer). The response
	// channel is buffered(1), so the dispatch goroutine may have already
	// written the result and exited; keeping the entry until ReceiveResponse
	// collects it (or its ctx cancels) closes the race that previously lost
	// the response. Studio always pairs SendMessage with a ReceiveResponse, so
	// this cannot leak in practice.
	defer func() {
		m.mu.Lock()
		delete(m.pending, messageID)
		m.mu.Unlock()
	}()

	select {
	case <-ctx.Done():
		return "", ctx.Err()
	case result := <-ch:
		return result, nil
	}
}
