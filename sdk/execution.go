package sdk

import (
	"sync"
	"time"
)

// ExecutionStatus tracks the lifecycle of an agent execution.
type ExecutionStatus string

const (
	ExecutionRunning   ExecutionStatus = "running"
	ExecutionCompleted ExecutionStatus = "completed"
	ExecutionError     ExecutionStatus = "error"
	ExecutionCancelled ExecutionStatus = "cancelled"
)

// AgentExecution records a single prompt execution with all messages.
// Survives WebSocket disconnects — results are stored in memory until
// the daemon restarts.
type AgentExecution struct {
	ID         string          `json:"id"`
	AgentType  AgentType       `json:"agent_type"`
	SessionID  string          `json:"session_id"`
	Prompt     string          `json:"prompt"`
	Status     ExecutionStatus `json:"status"`
	Messages   []Message       `json:"messages"`
	CreatedAt  time.Time       `json:"created_at"`
	FinishedAt *time.Time      `json:"finished_at,omitempty"`
	Error      string          `json:"error,omitempty"`

	cancelFn func() // cancels the underlying context
}

// ExecutionStore holds in-memory execution history across WebSocket reconnects.
type ExecutionStore struct {
	mu         sync.RWMutex
	executions map[string]*AgentExecution // keyed by execution ID
}

// NewExecutionStore creates a new in-memory execution store.
func NewExecutionStore() *ExecutionStore {
	return &ExecutionStore{
		executions: make(map[string]*AgentExecution),
	}
}

// maxExecutions caps the in-memory execution history.
const maxExecutions = 500

// Create starts tracking a new execution. Evicts oldest entries if at capacity.
func (s *ExecutionStore) Create(id, sessionID string, agentType AgentType, prompt string, cancelFn func()) *AgentExecution {
	e := &AgentExecution{
		ID:        id,
		AgentType: agentType,
		SessionID: sessionID,
		Prompt:    prompt,
		Status:    ExecutionRunning,
		Messages:  make([]Message, 0),
		CreatedAt: time.Now(),
		cancelFn:  cancelFn,
	}
	s.mu.Lock()
	s.executions[id] = e
	// Evict oldest entries if over capacity, skipping running ones.
	if len(s.executions) > maxExecutions {
		var oldest *AgentExecution
		var oldestKey string
		for k, v := range s.executions {
			if v.Status == ExecutionRunning {
				continue
			}
			if oldest == nil || v.CreatedAt.Before(oldest.CreatedAt) {
				oldest = v
				oldestKey = k
			}
		}
		if oldestKey == "" {
			// All entries are running — still need to evict oldest.
			for k, v := range s.executions {
				if oldest == nil || v.CreatedAt.Before(oldest.CreatedAt) {
					oldest = v
					oldestKey = k
				}
			}
		}
		if oldestKey != "" {
			delete(s.executions, oldestKey)
		}
	}
	s.mu.Unlock()
	return e
}

// AppendMessage adds a message to an execution's history.
func (s *ExecutionStore) AppendMessage(execID string, msg Message) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if e, ok := s.executions[execID]; ok {
		e.Messages = append(e.Messages, msg)
	}
}

// Complete marks an execution as completed.
func (s *ExecutionStore) Complete(execID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if e, ok := s.executions[execID]; ok {
		// Don't overwrite already-terminal status (cancelled, failed, error).
		if e.Status != ExecutionCancelled && e.Status != ExecutionError {
			e.Status = ExecutionCompleted
		}
		now := time.Now()
		e.FinishedAt = &now
	}
}

// Fail marks an execution as failed.
func (s *ExecutionStore) Fail(execID string, errMsg string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if e, ok := s.executions[execID]; ok {
		e.Status = ExecutionError
		e.Error = errMsg
		now := time.Now()
		e.FinishedAt = &now
	}
}

// Cancel cancels a running execution.
func (s *ExecutionStore) Cancel(execID string) {
	s.mu.RLock()
	e, ok := s.executions[execID]
	s.mu.RUnlock()
	if ok && e.cancelFn != nil {
		e.cancelFn()
	}
	s.mu.Lock()
	if e != nil {
		e.Status = ExecutionCancelled
		now := time.Now()
		e.FinishedAt = &now
	}
	s.mu.Unlock()
}

// Get returns an execution by ID.
func (s *ExecutionStore) Get(execID string) *AgentExecution {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.executions[execID]
}

// List returns all executions, newest first.
func (s *ExecutionStore) List() []*AgentExecution {
	s.mu.RLock()
	defer s.mu.RUnlock()
	list := make([]*AgentExecution, 0, len(s.executions))
	for _, e := range s.executions {
		list = append(list, e)
	}
	// Sort newest first
	for i := 0; i < len(list); i++ {
		for j := i + 1; j < len(list); j++ {
			if list[j].CreatedAt.After(list[i].CreatedAt) {
				list[i], list[j] = list[j], list[i]
			}
		}
	}
	return list
}

// GetBySession returns the latest execution for a given session.
func (s *ExecutionStore) GetBySession(sessionID string) *AgentExecution {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var latest *AgentExecution
	for _, e := range s.executions {
		if e.SessionID == sessionID {
			if latest == nil || e.CreatedAt.After(latest.CreatedAt) {
				latest = e
			}
		}
	}
	return latest
}
