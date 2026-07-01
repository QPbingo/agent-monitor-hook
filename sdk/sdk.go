// Package sdk provides a unified interface for controlling AI coding agents
// (Claude Code, OpenCode, Codex) programmatically from Go.
//
// Each agent is implemented as a subprocess controller that communicates via
// the agent's native CLI protocol (stream-json for Claude Code, ACP JSON-RPC
// for OpenCode, app-server JSON-RPC for Codex).
//
// The AgentManager layer provides a single entry point for multi-agent
// orchestration with session lifecycle management.
package sdk

import (
	"context"
	"encoding/json"
	"time"
)

// AgentType identifies the agent implementation.
type AgentType string

const (
	AgentClaude   AgentType = "claude"
	AgentOpenCode AgentType = "opencode"
	AgentCodex    AgentType = "codex"
)

// PermissionMode controls tool execution authorization.
type PermissionMode string

const (
	PermissionDefault     PermissionMode = "default"
	PermissionAcceptEdits PermissionMode = "acceptEdits"
	PermissionBypass      PermissionMode = "bypassPermissions"
	PermissionPlan        PermissionMode = "plan"
	PermissionReadOnly    PermissionMode = "readOnly"
	PermissionAuto        PermissionMode = "auto"
)

// SessionOptions configures a new session.
type SessionOptions struct {
	// CWD is the working directory for the agent.
	CWD string `json:"cwd,omitempty"`
	// Model overrides the default model.
	Model string `json:"model,omitempty"`
	// PermissionMode sets the tool authorization level.
	PermissionMode PermissionMode `json:"permission_mode,omitempty"`
	// AllowedTools pre-approves specific tool names.
	AllowedTools []string `json:"allowed_tools,omitempty"`
	// MaxTurns limits the number of agentic turns.
	MaxTurns int `json:"max_turns,omitempty"`
	// SystemPrompt overrides the default system prompt.
	SystemPrompt string `json:"system_prompt,omitempty"`
	// Env sets additional environment variables.
	Env map[string]string `json:"env,omitempty"`
	// ExtraArgs passes additional CLI arguments.
	ExtraArgs []string `json:"extra_args,omitempty"`
	// Title is an optional display title for the session.
	Title string `json:"title,omitempty"`
}

// Session represents an active agent session.
type Session struct {
	ID        string         `json:"id"`
	AgentType AgentType      `json:"agent_type"`
	Title     string         `json:"title"`
	CWD       string         `json:"cwd"`
	CreatedAt time.Time      `json:"created_at"`
	Options   SessionOptions `json:"-"`
}

// SessionInfo is metadata about a past or active session.
type SessionInfo struct {
	ID           string    `json:"id"`
	Title        string    `json:"title"`
	Summary      string    `json:"summary"`
	CWD          string    `json:"cwd,omitempty"`
	LastModified time.Time `json:"last_modified"`
	TurnCount    int       `json:"turn_count,omitempty"`
}

// MessageType classifies the kind of message chunk.
type MessageType string

const (
	MessageTypeText       MessageType = "text"
	MessageTypeToolUse    MessageType = "tool_use"
	MessageTypeToolResult MessageType = "tool_result"
	MessageTypeError      MessageType = "error"
	MessageTypeResult     MessageType = "result"
	MessageTypeSystem     MessageType = "system"
	MessageTypeThinking   MessageType = "thinking"
)

// Message is a single chunk from the agent's output stream.
type Message struct {
	Type      MessageType     `json:"type"`
	SessionID string          `json:"session_id,omitempty"`
	Content   string          `json:"content,omitempty"`
	ToolName  string          `json:"tool_name,omitempty"`
	ToolInput string          `json:"tool_input,omitempty"`
	RawJSON   json.RawMessage `json:"raw_json,omitempty"`
	Timestamp time.Time       `json:"timestamp"`
	IsFinal   bool            `json:"is_final,omitempty"`
	Error     string          `json:"error,omitempty"`
}

// Result contains the final result of agent execution.
type Result struct {
	SessionID  string  `json:"session_id"`
	Output     string  `json:"output"`
	StopReason string  `json:"stop_reason"`
	NumTurns   int     `json:"num_turns"`
	DurationMs int64   `json:"duration_ms"`
	CostUSD    float64 `json:"cost_usd,omitempty"`
	IsError    bool    `json:"is_error"`
}

// AgentSDK is the unified interface for controlling an AI coding agent.
// Each agent type (Claude Code, OpenCode, Codex) implements this interface.
type AgentSDK interface {
	// AgentType returns the type of agent this SDK controls.
	AgentType() AgentType

	// CreateSession creates a new agent session with the given options.
	// Returns the session metadata.
	CreateSession(ctx context.Context, opts SessionOptions) (*Session, error)

	// SendPrompt sends a user prompt to an existing session and returns a
	// channel that streams Message chunks as the agent processes.
	// The channel is closed when the turn completes.
	SendPrompt(ctx context.Context, sessionID string, prompt string) (<-chan Message, error)

	// ResumeSession resumes an existing session by ID.
	// All previous context (conversation history, file reads, tool results)
	// is restored.
	ResumeSession(ctx context.Context, sessionID string) (*Session, error)

	// CancelExecution cancels the currently running turn in the given session.
	CancelExecution(ctx context.Context, sessionID string) error

	// RenameSession sets a new display title for the session.
	RenameSession(ctx context.Context, sessionID string, title string) error

	// ListSessions returns metadata for past sessions, optionally scoped to a
	// specific working directory. If dir is empty, all sessions are listed.
	ListSessions(ctx context.Context, dir string) ([]SessionInfo, error)

	// SetPermissionMode changes the permission mode for an active session.
	SetPermissionMode(sessionID string, mode PermissionMode) error

	// Close terminates the underlying agent process and cleans up resources.
	Close() error
}
