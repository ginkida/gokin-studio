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
		// Match by name (case-insensitive prefix).
		if len(p.Name) >= len(toRole) && len(toRole) > 0 {
			if strings.EqualFold(p.Name, toRole) || strings.EqualFold(p.Name[:len(toRole)], toRole) {
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
		defer func() {
			if m.studio != nil {
				m.studio.wg.Done()
			}
			m.mu.Lock()
			delete(m.pending, msgID)
			m.mu.Unlock()
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

	select {
	case <-ctx.Done():
		return "", ctx.Err()
	case result := <-ch:
		return result, nil
	}
}
