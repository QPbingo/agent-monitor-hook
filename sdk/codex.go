package sdk

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"
)

// CodexSDK controls Codex via `codex exec` (non-interactive mode).
//
// Communication: spawns `codex exec --json` for each prompt. Session
// continuity is maintained via --resume <session_id>.
//
// The Codex CLI must be installed. Set the path via CodexOptions.BinPath
// if not in PATH.

type codexActiveProcess struct {
	cmd       *exec.Cmd
	sessionID string
}

type CodexSDK struct {
	binPath    string
	activeMu   sync.Mutex
	active     map[string]*codexActiveProcess
	sessions   map[string]*Session
	sessionsMu sync.RWMutex
}

// CodexOptions configures the Codex CLI integration.
type CodexOptions struct {
	BinPath string // Default: "codex"
}

// NewCodexSDK creates a new Codex SDK controller.
func NewCodexSDK(opts CodexOptions) *CodexSDK {
	binPath := opts.BinPath
	if binPath == "" {
		binPath = "codex"
	}
	return &CodexSDK{
		binPath:  binPath,
		active:   make(map[string]*codexActiveProcess),
		sessions: make(map[string]*Session),
	}
}

func (c *CodexSDK) AgentType() AgentType { return AgentCodex }

// CreateSession initializes a new Codex thread (session).
//
// Codex threads persist on disk and are resumed via --resume.
func (c *CodexSDK) CreateSession(ctx context.Context, opts SessionOptions) (*Session, error) {
	id := generateSessionID("codex")
	sess := &Session{
		ID:        id,
		AgentType: AgentCodex,
		Title:     opts.Title,
		CWD:       opts.CWD,
		CreatedAt: time.Now(),
		Options:   opts,
	}
	c.sessionsMu.Lock()
	c.sessions[id] = sess
	c.sessionsMu.Unlock()
	return sess, nil
}

// SendPrompt sends a prompt by spawning `codex exec --json`.
//
// The subprocess is launched with:
//
//	codex exec --json "<prompt>" [--model <m>] [--sandbox <mode>] ...
//
// Output lines are parsed as JSON and emitted as Message chunks.
func (c *CodexSDK) SendPrompt(ctx context.Context, sessionID string, prompt string) (<-chan Message, error) {
	c.sessionsMu.RLock()
	sess, ok := c.sessions[sessionID]
	c.sessionsMu.RUnlock()
	if !ok {
		return nil, fmt.Errorf("codex: session %s not found", sessionID)
	}

	ch := make(chan Message, 64)
	go func() {
		defer close(ch)
		execID := generateExecID()

		args := c.buildArgs(sess, prompt)
		cmd := exec.CommandContext(ctx, c.binPath, args...)
		defer cmd.Wait() // ensure process is reaped even on early return (cancellation)
		if sess.Options.CWD != "" {
			cmd.Dir = sess.Options.CWD
		}
		cmd.Env = c.buildEnv(sess.Options.Env)

		stdout, err := cmd.StdoutPipe()
		if err != nil {
			ch <- Message{Type: MessageTypeError, Error: err.Error(), Timestamp: time.Now()}
			return
		}
		stderr, err := cmd.StderrPipe()
		if err != nil {
			ch <- Message{Type: MessageTypeError, Error: err.Error(), Timestamp: time.Now()}
			return
		}

		c.activeMu.Lock()
		c.active[execID] = &codexActiveProcess{cmd, sessionID}
		c.activeMu.Unlock()
		defer func() {
			c.activeMu.Lock()
			delete(c.active, execID)
			c.activeMu.Unlock()
		}()

		if err := cmd.Start(); err != nil {
			ch <- Message{Type: MessageTypeError, Error: err.Error(), Timestamp: time.Now()}
			return
		}

		go func() { io.Copy(io.Discard, stderr) }()

		scanner := bufio.NewScanner(stdout)
		scanner.Buffer(make([]byte, 2*1024*1024), 2*1024*1024)

		for scanner.Scan() {
			line := scanner.Bytes()
			if len(line) == 0 {
				continue
			}

			var raw map[string]interface{}
			if err := json.Unmarshal(line, &raw); err != nil {
				continue
			}

			msg := c.parseMessage(raw, sessionID)
			select {
			case ch <- msg:
			case <-ctx.Done():
				return
			}
		}

		if err := cmd.Wait(); err != nil {
			if ctx.Err() == nil {
				ch <- Message{Type: MessageTypeError, Error: err.Error(), IsFinal: true, Timestamp: time.Now()}
			}
		}
	}()

	return ch, nil
}

// buildArgs constructs the CLI arguments for a Codex invocation.
func (c *CodexSDK) buildArgs(sess *Session, prompt string) []string {
	args := []string{
		"exec", "--json", prompt,
	}

	if sess.Options.Model != "" {
		args = append(args, "--model", sess.Options.Model)
	}
	switch sess.Options.PermissionMode {
	case PermissionReadOnly:
		args = append(args, "--sandbox", "read-only")
	case PermissionBypass:
		args = append(args, "--dangerously-skip-permissions")
	}

	// Session continuity
	args = append(args, "--resume", sess.ID)

	args = append(args, sess.Options.ExtraArgs...)
	return args
}

func (c *CodexSDK) buildEnv(extra map[string]string) []string {
	env := os.Environ()
	for k, v := range extra {
		env = append(env, k+"="+v)
	}
	return env
}

// parseMessage converts a raw codex exec --json output line into a Message.
func (c *CodexSDK) parseMessage(raw map[string]interface{}, sessionID string) Message {
	msg := Message{
		SessionID: sessionID,
		Timestamp: time.Now(),
	}
	if b, err := json.Marshal(raw); err == nil {
		msg.RawJSON = b
	}

	msgType, _ := raw["type"].(string)
	switch msgType {
	case "assistant":
		msg.Type = MessageTypeText
		if content, ok := raw["content"].(string); ok {
			msg.Content = content
		} else if message, ok := raw["message"].(map[string]interface{}); ok {
			if content, ok := message["content"].(string); ok {
				msg.Content = content
			}
		}
	case "tool_call", "tool_use":
		msg.Type = MessageTypeToolUse
		if name, ok := raw["name"].(string); ok {
			msg.ToolName = name
		} else if name, ok := raw["tool_name"].(string); ok {
			msg.ToolName = name
		}
		if input, ok := raw["input"].(map[string]interface{}); ok {
			if b, err := json.Marshal(input); err == nil {
				msg.ToolInput = string(b)
			}
		}
	case "tool_result":
		msg.Type = MessageTypeToolResult
		if content, ok := raw["content"].(string); ok {
			msg.Content = content
		}
	case "result", "done":
		msg.Type = MessageTypeResult
		msg.IsFinal = true
		if content, ok := raw["content"].(string); ok {
			msg.Content = content
		} else if result, ok := raw["result"].(string); ok {
			msg.Content = result
		}
	case "error":
		msg.Type = MessageTypeError
		if errMsg, ok := raw["message"].(string); ok {
			msg.Error = errMsg
		}
	case "thinking", "reasoning":
		msg.Type = MessageTypeThinking
		if content, ok := raw["content"].(string); ok {
			msg.Content = content
		}
	case "system":
		msg.Type = MessageTypeSystem
		if content, ok := raw["content"].(string); ok {
			msg.Content = content
		}
	default:
		msg.Type = MessageTypeText
		if content, ok := raw["content"].(string); ok {
			msg.Content = content
		} else if text, ok := raw["text"].(string); ok {
			msg.Content = text
		}
	}

	return msg
}

// ResumeSession returns metadata for an existing session.
func (c *CodexSDK) ResumeSession(ctx context.Context, sessionID string) (*Session, error) {
	c.sessionsMu.Lock()
	sess, ok := c.sessions[sessionID]
	c.sessionsMu.Unlock()
	if !ok {
		sess = &Session{
			ID:        sessionID,
			AgentType: AgentCodex,
			CreatedAt: time.Now(),
		}
		c.sessionsMu.Lock()
		c.sessions[sessionID] = sess
		c.sessionsMu.Unlock()
	}
	return sess, nil
}

// CancelExecution kills the running Codex subprocess.
func (c *CodexSDK) CancelExecution(ctx context.Context, sessionID string) error {
	c.activeMu.Lock()
	defer c.activeMu.Unlock()

	var lastErr error
	for _, ap := range c.active {
		if ap.sessionID == sessionID && ap.cmd.Process != nil {
			if err := ap.cmd.Process.Kill(); err != nil {
				lastErr = err
			}
		}
	}
	return lastErr
}

// RenameSession updates the session title in-memory.
func (c *CodexSDK) RenameSession(ctx context.Context, sessionID string, title string) error {
	c.sessionsMu.Lock()
	defer c.sessionsMu.Unlock()
	sess, ok := c.sessions[sessionID]
	if !ok {
		return fmt.Errorf("codex: session %s not found", sessionID)
	}
	sess.Title = title
	return nil
}

// ListSessions lists sessions by running `codex session list`.
func (c *CodexSDK) ListSessions(ctx context.Context, dir string) ([]SessionInfo, error) {
	args := []string{"session", "list", "--json"}
	cmd := exec.CommandContext(ctx, c.binPath, args...)
	if dir != "" {
		cmd.Dir = dir
	}

	output, err := cmd.Output()
	if err != nil {
		// Fall back to in-memory sessions
		c.sessionsMu.RLock()
		defer c.sessionsMu.RUnlock()
		var list []SessionInfo
		for _, s := range c.sessions {
			if dir != "" && s.CWD != dir {
				continue
			}
			list = append(list, SessionInfo{
				ID:           s.ID,
				Title:        s.Title,
				CWD:          s.CWD,
				LastModified: s.CreatedAt,
			})
		}
		return list, nil
	}

	var sessions []struct {
		ID           string `json:"id"`
		Title        string `json:"title"`
		LastModified int64  `json:"lastModified"`
	}
	if err := json.Unmarshal(output, &sessions); err != nil {
		// Try line-delimited JSON
		var list []SessionInfo
		for _, line := range strings.Split(strings.TrimSpace(string(output)), "\n") {
			var s struct {
				ID           string `json:"id"`
				Title        string `json:"title"`
				LastModified int64  `json:"lastModified"`
			}
			if json.Unmarshal([]byte(line), &s) == nil {
				list = append(list, SessionInfo{
					ID:           s.ID,
					Title:        s.Title,
					LastModified: time.UnixMilli(s.LastModified),
				})
			}
		}
		return list, nil
	}

	var list []SessionInfo
	for _, s := range sessions {
		list = append(list, SessionInfo{
			ID:           s.ID,
			Title:        s.Title,
			LastModified: time.UnixMilli(s.LastModified),
		})
	}
	return list, nil
}

// SetPermissionMode updates the permission mode for future turns.
func (c *CodexSDK) SetPermissionMode(sessionID string, mode PermissionMode) error {
	c.sessionsMu.RLock()
	sess, ok := c.sessions[sessionID]
	c.sessionsMu.RUnlock()
	if !ok {
		return fmt.Errorf("codex: session %s not found", sessionID)
	}
	sess.Options.PermissionMode = mode
	return nil
}

// Close kills all active Codex subprocesses.
func (c *CodexSDK) Close() error {
	c.activeMu.Lock()
	defer c.activeMu.Unlock()
	for id, ap := range c.active {
		if ap.cmd.Process != nil {
			ap.cmd.Process.Kill()
		}
		delete(c.active, id)
	}
	return nil
}
