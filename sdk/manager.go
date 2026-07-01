package sdk

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"sync"
)

// AgentManager provides unified multi-agent orchestration.
//
// It wraps one AgentSDK instance per agent type and exposes the same
// interface with an additional agentType parameter for routing.
//
// Usage:
//
//	mgr := NewAgentManager()
//	mgr.Register(AgentClaude, NewClaudeSDK(ClaudeOptions{}))
//	mgr.Register(AgentOpenCode, NewOpenCodeSDK(OpenCodeOptions{}))
//	mgr.Register(AgentCodex, NewCodexSDK(CodexOptions{}))
//	defer mgr.CloseAll()
//
//	sess, _ := mgr.CreateSession(ctx, AgentClaude, SessionOptions{CWD: "/project"})
//	ch, _ := mgr.SendPrompt(ctx, AgentClaude, sess.ID, "Fix the bug")
//	for msg := range ch { ... }
type AgentManager struct {
	mu         sync.RWMutex
	agents     map[AgentType]AgentSDK
	Executions *ExecutionStore
}

// NewAgentManager creates a new empty agent manager.
func NewAgentManager() *AgentManager {
	return &AgentManager{
		agents:     make(map[AgentType]AgentSDK),
		Executions: NewExecutionStore(),
	}
}

// Register adds an agent SDK to the manager. Must be called before use.
func (m *AgentManager) Register(agentType AgentType, sdk AgentSDK) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.agents[agentType] = sdk
}

// Get returns the SDK for a specific agent type, or nil if not registered.
func (m *AgentManager) Get(agentType AgentType) AgentSDK {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.agents[agentType]
}

// CreateSession creates a new session on the specified agent.
func (m *AgentManager) CreateSession(ctx context.Context, agentType AgentType, opts SessionOptions) (*Session, error) {
	sdk := m.Get(agentType)
	if sdk == nil {
		return nil, fmt.Errorf("agent %s not registered", agentType)
	}
	return sdk.CreateSession(ctx, opts)
}

// SendPrompt sends a prompt to an existing session on the specified agent.
func (m *AgentManager) SendPrompt(ctx context.Context, agentType AgentType, sessionID string, prompt string) (<-chan Message, error) {
	sdk := m.Get(agentType)
	if sdk == nil {
		return nil, fmt.Errorf("agent %s not registered", agentType)
	}
	return sdk.SendPrompt(ctx, sessionID, prompt)
}

// ResumeSession resumes an existing session on the specified agent.
func (m *AgentManager) ResumeSession(ctx context.Context, agentType AgentType, sessionID string) (*Session, error) {
	sdk := m.Get(agentType)
	if sdk == nil {
		return nil, fmt.Errorf("agent %s not registered", agentType)
	}
	return sdk.ResumeSession(ctx, sessionID)
}

// CancelExecution cancels the running turn on the specified agent.
func (m *AgentManager) CancelExecution(ctx context.Context, agentType AgentType, sessionID string) error {
	sdk := m.Get(agentType)
	if sdk == nil {
		return fmt.Errorf("agent %s not registered", agentType)
	}
	return sdk.CancelExecution(ctx, sessionID)
}

// RenameSession renames a session on the specified agent.
func (m *AgentManager) RenameSession(ctx context.Context, agentType AgentType, sessionID string, title string) error {
	sdk := m.Get(agentType)
	if sdk == nil {
		return fmt.Errorf("agent %s not registered", agentType)
	}
	return sdk.RenameSession(ctx, sessionID, title)
}

// ListSessions lists sessions for the specified agent, or all agents if agentType is empty.
func (m *AgentManager) ListSessions(ctx context.Context, agentType AgentType, dir string) ([]SessionInfo, error) {
	if agentType != "" {
		sdk := m.Get(agentType)
		if sdk == nil {
			return nil, fmt.Errorf("agent %s not registered", agentType)
		}
		return sdk.ListSessions(ctx, dir)
	}

	// List from all registered agents
	var all []SessionInfo
	m.mu.RLock()
	defer m.mu.RUnlock()
	for _, sdk := range m.agents {
		list, err := sdk.ListSessions(ctx, dir)
		if err != nil {
			continue
		}
		all = append(all, list...)
	}
	return all, nil
}

// SetPermissionMode sets the permission mode for a session.
func (m *AgentManager) SetPermissionMode(agentType AgentType, sessionID string, mode PermissionMode) error {
	sdk := m.Get(agentType)
	if sdk == nil {
		return fmt.Errorf("agent %s not registered", agentType)
	}
	return sdk.SetPermissionMode(sessionID, mode)
}

// CloseAll closes all registered agent SDKs.
func (m *AgentManager) CloseAll() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, sdk := range m.agents {
		sdk.Close()
	}
	m.agents = make(map[AgentType]AgentSDK)
	return nil
}

// ListAgents returns all registered agent types.
func (m *AgentManager) ListAgents() []AgentType {
	m.mu.RLock()
	defer m.mu.RUnlock()
	types := make([]AgentType, 0, len(m.agents))
	for t := range m.agents {
		types = append(types, t)
	}
	return types
}

// generateSessionID creates a unique session identifier with an agent prefix.
func generateSessionID(prefix string) string {
	b := make([]byte, 12)
	rand.Read(b)
	return prefix + "_" + hex.EncodeToString(b)
}

// generateExecID creates a unique execution identifier.
func generateExecID() string {
	b := make([]byte, 8)
	rand.Read(b)
	return "exec_" + hex.EncodeToString(b)
}
