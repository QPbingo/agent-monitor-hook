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

// ClaudeSDK controls Claude Code via its headless CLI (subprocess mode).
//
// Communication: spawns `claude` with --output-format stream-json.
// Each prompt is a separate subprocess invocation. Session continuity is
// maintained via --resume <session_id>.
//
// The Claude Code CLI must be installed and available in PATH, or the
// path can be specified via ClaudeCodeOptions.BinPath.

type activeProcess struct {
	cmd       *exec.Cmd
	sessionID string
}

type ClaudeSDK struct {
	binPath    string
	activeMu   sync.Mutex
	active     map[string]*activeProcess // execID → running subprocess
	sessionsMu sync.RWMutex
	sessions   map[string]*Session
}

// ClaudeOptions configures the Claude Code CLI integration.
type ClaudeOptions struct {
	// BinPath is the path to the claude binary. Default: "claude".
	BinPath string
}

// NewClaudeSDK creates a new Claude Code SDK controller.
func NewClaudeSDK(opts ClaudeOptions) *ClaudeSDK {
	binPath := opts.BinPath
	if binPath == "" {
		binPath = "claude"
	}
	return &ClaudeSDK{
		binPath:  binPath,
		active:   make(map[string]*activeProcess),
		sessions: make(map[string]*Session),
	}
}

func (c *ClaudeSDK) AgentType() AgentType { return AgentClaude }

// CreateSession initializes a new Claude Code session.
//
// The session is created lazily — the actual Claude process is spawned on the
// first SendPrompt call. This matches Claude Code's model where a session is
// a JSONL file on disk that persists across CLI invocations.
func (c *ClaudeSDK) CreateSession(ctx context.Context, opts SessionOptions) (*Session, error) {
	id := generateSessionID("claude")
	sess := &Session{
		ID:        id,
		AgentType: AgentClaude,
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

// SendPrompt sends a prompt by spawning a Claude CLI subprocess.
//
// The subprocess is launched with:
//
//	claude -p "<prompt>" --output-format stream-json [--resume <id>] [--model <m>] ...
//
// Output lines are parsed as JSON and emitted as Message chunks on the
// returned channel. The channel closes when the subprocess exits.
func (c *ClaudeSDK) SendPrompt(ctx context.Context, sessionID string, prompt string) (<-chan Message, error) {
	c.sessionsMu.RLock()
	sess, ok := c.sessions[sessionID]
	c.sessionsMu.RUnlock()
	if !ok {
		return nil, fmt.Errorf("claude: session %s not found", sessionID)
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
		c.active[execID] = &activeProcess{cmd, sessionID}
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

		// Read stderr in background for debugging
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

// buildArgs constructs the CLI arguments for a Claude Code invocation.
func (c *ClaudeSDK) buildArgs(sess *Session, prompt string) []string {
	args := []string{
		"-p", prompt,
		"--output-format", "stream-json",
	}

	if sess.Options.Model != "" {
		args = append(args, "--model", sess.Options.Model)
	}
	if sess.Options.MaxTurns > 0 {
		args = append(args, "--max-turns", fmt.Sprintf("%d", sess.Options.MaxTurns))
	}
	switch sess.Options.PermissionMode {
	case PermissionBypass:
		args = append(args, "--dangerously-skip-permissions")
	case PermissionAcceptEdits:
		args = append(args, "--permission-mode", "acceptEdits")
	case PermissionPlan:
		args = append(args, "--permission-mode", "plan")
	default:
		if len(sess.Options.AllowedTools) > 0 {
			args = append(args, "--allowedTools", strings.Join(sess.Options.AllowedTools, ","))
		}
	}
	if sess.Options.SystemPrompt != "" {
		args = append(args, "--system-prompt", sess.Options.SystemPrompt)
	}

	// Session continuity: --resume preserves full context from the first prompt
	args = append(args, "--resume", sess.ID)

	args = append(args, sess.Options.ExtraArgs...)
	return args
}

// buildEnv merges additional environment variables.
func (c *ClaudeSDK) buildEnv(extra map[string]string) []string {
	env := os.Environ()
	for k, v := range extra {
		env = append(env, k+"="+v)
	}
	return env
}

// parseMessage converts a raw stream-json line into a Message.
func (c *ClaudeSDK) parseMessage(raw map[string]interface{}, sessionID string) Message {
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
		if msgBlock, ok := raw["message"].(map[string]interface{}); ok {
			if content, ok := msgBlock["content"].([]interface{}); ok && len(content) > 0 {
				var parts []string
				for _, block := range content {
					if textBlock, ok := block.(map[string]interface{}); ok {
						if text, ok := textBlock["text"].(string); ok && text != "" {
							parts = append(parts, text)
						}
					}
				}
				msg.Content = strings.Join(parts, "\n")
			}
		}
	case "user":
		msg.Type = MessageTypeSystem
	case "result":
		msg.Type = MessageTypeResult
		msg.IsFinal = true
		if result, ok := raw["result"].(string); ok {
			msg.Content = result
		}
		if msg.Content == "" {
			if stopReason, ok := raw["stop_reason"].(string); ok {
				msg.Content = stopReason
			}
		}
	case "system":
		msg.Type = MessageTypeSystem
		if subtype, ok := raw["subtype"].(string); ok && subtype == "init" {
			if data, ok := raw["data"].(map[string]interface{}); ok {
				if sid, ok := data["session_id"].(string); ok {
					msg.SessionID = sid
					msg.Content = "session:" + sid
				}
			}
		}
	case "tool_use":
		msg.Type = MessageTypeToolUse
		if name, ok := raw["name"].(string); ok {
			msg.ToolName = name
		}
		if input, ok := raw["input"].(map[string]interface{}); ok {
			if b, err := json.Marshal(input); err == nil {
				msg.ToolInput = string(b)
			}
		}
	default:
		msg.Type = MessageTypeSystem
	}

	return msg
}

// ResumeSession returns metadata for an existing session.
// Context is automatically restored on the next SendPrompt via --resume.
func (c *ClaudeSDK) ResumeSession(ctx context.Context, sessionID string) (*Session, error) {
	c.sessionsMu.Lock()
	sess, ok := c.sessions[sessionID]
	c.sessionsMu.Unlock()
	if !ok {
		// Session was created externally (e.g., CLI). Create a minimal record.
		sess = &Session{
			ID:        sessionID,
			AgentType: AgentClaude,
			CreatedAt: time.Now(),
		}
		c.sessionsMu.Lock()
		c.sessions[sessionID] = sess
		c.sessionsMu.Unlock()
	}
	return sess, nil
}

// CancelExecution kills the running Claude subprocess for the given session.
func (c *ClaudeSDK) CancelExecution(ctx context.Context, sessionID string) error {
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

// RenameSession is not natively supported by Claude Code CLI.
// The session title is stored in-memory only.
func (c *ClaudeSDK) RenameSession(ctx context.Context, sessionID string, title string) error {
	c.sessionsMu.Lock()
	defer c.sessionsMu.Unlock()
	sess, ok := c.sessions[sessionID]
	if !ok {
		return fmt.Errorf("claude: session %s not found", sessionID)
	}
	sess.Title = title
	return nil
}

// ListSessions is not natively supported by the Claude Code CLI.
// Returns sessions tracked in memory.
func (c *ClaudeSDK) ListSessions(ctx context.Context, dir string) ([]SessionInfo, error) {
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

// SetPermissionMode updates the permission mode for future turns.
func (c *ClaudeSDK) SetPermissionMode(sessionID string, mode PermissionMode) error {
	c.sessionsMu.Lock()
	defer c.sessionsMu.Unlock()
	sess, ok := c.sessions[sessionID]
	if !ok {
		return fmt.Errorf("claude: session %s not found", sessionID)
	}
	sess.Options.PermissionMode = mode
	return nil
}

// Close kills all active Claude subprocesses.
func (c *ClaudeSDK) Close() error {
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
