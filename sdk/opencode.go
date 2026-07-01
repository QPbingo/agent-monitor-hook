package sdk

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"sync"
	"time"
)

// OpenCodeSDK controls OpenCode via ACP (Agent Client Protocol) JSON-RPC 2.0.
//
// Communication: spawns `opencode acp` which starts a persistent JSON-RPC
// server over stdin/stdout. All session operations use JSON-RPC request/
// response pairs with auto-incrementing message IDs.
type OpenCodeSDK struct {
	binPath string

	mu         sync.Mutex
	cmd        *exec.Cmd
	stdin      io.WriteCloser
	stdout     *bufio.Scanner
	idSeq      int
	sessions   map[string]*Session
	sessionsMu sync.RWMutex
	running    bool

	pending  map[int]chan json.RawMessage
	notifyCh chan json.RawMessage
}

type OpenCodeOptions struct {
	BinPath string
}

func NewOpenCodeSDK(opts OpenCodeOptions) *OpenCodeSDK {
	binPath := opts.BinPath
	if binPath == "" {
		binPath = "opencode"
	}
	return &OpenCodeSDK{
		binPath:  binPath,
		sessions: make(map[string]*Session),
		pending:  make(map[int]chan json.RawMessage),
		notifyCh: make(chan json.RawMessage, 512),
	}
}

func (o *OpenCodeSDK) AgentType() AgentType { return AgentOpenCode }

// start launches the `opencode acp` subprocess if not already running.
// Sets running=true only after successful ACP initialization.
func (o *OpenCodeSDK) start(ctx context.Context, cwd string) error {
	o.mu.Lock()
	defer o.mu.Unlock()

	if o.running {
		return nil
	}

	o.cmd = exec.CommandContext(ctx, o.binPath, "acp")
	if cwd != "" {
		o.cmd.Dir = cwd
	}
	o.cmd.Env = os.Environ()

	var err error
	o.stdin, err = o.cmd.StdinPipe()
	if err != nil {
		return fmt.Errorf("opencode stdin pipe: %w", err)
	}
	stdout, err := o.cmd.StdoutPipe()
	if err != nil {
		return fmt.Errorf("opencode stdout pipe: %w", err)
	}
	stderr, err := o.cmd.StderrPipe()
	if err != nil {
		return fmt.Errorf("opencode stderr pipe: %w", err)
	}
	go func() { io.Copy(io.Discard, stderr) }()

	if err := o.cmd.Start(); err != nil {
		return fmt.Errorf("opencode start: %w", err)
	}

	o.stdout = bufio.NewScanner(stdout)
	o.stdout.Buffer(make([]byte, 2*1024*1024), 2*1024*1024)

	// Start background reader BEFORE initialize so it can receive the response
	go o.readLoop()

	_, err = o.call(ctx, "initialize", map[string]interface{}{
		"protocolVersion": "1.0",
		"clientInfo":      map[string]string{"name": "agent-monitor", "version": "1.0.0"},
	})
	if err != nil {
		o.cmd.Process.Kill()
		return fmt.Errorf("opencode initialize: %w", err)
	}

	o.running = true
	return nil
}

// readLoop reads JSON-RPC messages from stdout. Dispatches responses to
// pending callers and forwards notifications to notifyCh.
func (o *OpenCodeSDK) readLoop() {
	defer func() {
		o.mu.Lock()
		o.running = false
		o.mu.Unlock()
	}()
	for o.stdout.Scan() {
		line := o.stdout.Bytes()
		if len(line) == 0 {
			continue
		}
		var raw map[string]interface{}
		if err := json.Unmarshal(line, &raw); err != nil {
			continue
		}
		// Response (has "id") or notification (no "id")
		if idRaw, hasID := raw["id"]; hasID {
			f, ok := idRaw.(float64)
			if !ok {
				continue
			}
			id := int(f)
			o.mu.Lock()
			ch, exists := o.pending[id]
			if exists {
				delete(o.pending, id)
			}
			o.mu.Unlock()
			if exists {
				b, _ := json.Marshal(raw)
				select {
				case ch <- b:
				default:
				}
			}
		} else {
			b, _ := json.Marshal(raw)
			select {
			case o.notifyCh <- b:
			default:
			}
		}
	}
}

// call sends a JSON-RPC request and waits for the response.
func (o *OpenCodeSDK) call(ctx context.Context, method string, params interface{}) (json.RawMessage, error) {
	o.mu.Lock()
	o.idSeq++
	id := o.idSeq
	ch := make(chan json.RawMessage, 1)
	o.pending[id] = ch
	o.mu.Unlock()

	defer func() {
		o.mu.Lock()
		delete(o.pending, id)
		o.mu.Unlock()
	}()

	req := map[string]interface{}{"jsonrpc": "2.0", "id": id, "method": method}
	if params != nil {
		req["params"] = params
	}
	body, _ := json.Marshal(req)
	body = append(body, '\n')

	o.mu.Lock()
	_, err := o.stdin.Write(body)
	o.mu.Unlock()
	if err != nil {
		return nil, fmt.Errorf("write request: %w", err)
	}

	select {
	case resp := <-ch:
		var e struct {
			Error *struct {
				Code    int    `json:"code"`
				Message string `json:"message"`
			} `json:"error"`
		}
		if err := json.Unmarshal(resp, &e); err == nil && e.Error != nil {
			return nil, fmt.Errorf("opencode error %d: %s", e.Error.Code, e.Error.Message)
		}
		return resp, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// CreateSession creates a new session via ACP session/new.
func (o *OpenCodeSDK) CreateSession(ctx context.Context, opts SessionOptions) (*Session, error) {
	if err := o.start(ctx, opts.CWD); err != nil {
		return nil, err
	}
	params := map[string]interface{}{"cwd": opts.CWD}
	if len(opts.AllowedTools) > 0 {
		params["tools"] = opts.AllowedTools
	}
	if opts.Model != "" {
		params["model"] = opts.Model
	}

	resp, err := o.call(ctx, "session/new", params)
	if err != nil {
		return nil, fmt.Errorf("session/new: %w", err)
	}

	var result struct {
		SessionID string `json:"sessionId"`
	}
	json.Unmarshal(resp, &result)
	if result.SessionID == "" {
		var alt struct {
			Result struct {
				SessionID string `json:"sessionId"`
			}
		}
		json.Unmarshal(resp, &alt)
		result.SessionID = alt.Result.SessionID
	}
	id := result.SessionID
	if id == "" {
		id = generateSessionID("opencode")
	}

	sess := &Session{
		ID: id, AgentType: AgentOpenCode, Title: opts.Title,
		CWD: opts.CWD, CreatedAt: time.Now(), Options: opts,
	}
	o.sessionsMu.Lock()
	o.sessions[id] = sess
	o.sessionsMu.Unlock()
	return sess, nil
}

// SendPrompt sends a prompt via ACP session/prompt and streams updates.
// The ACP process must be started first (via CreateSession or start()).
func (o *OpenCodeSDK) SendPrompt(ctx context.Context, sessionID string, prompt string) (<-chan Message, error) {
	o.mu.Lock()
	running := o.running
	o.mu.Unlock()
	if !running {
		return nil, fmt.Errorf("opencode: ACP process not started, call CreateSession first")
	}
	ch := make(chan Message, 64)
	go func() {
		defer close(ch)
		_, err := o.call(ctx, "session/prompt", map[string]interface{}{
			"sessionId": sessionID, "prompt": prompt,
		})
		if err != nil {
			ch <- Message{Type: MessageTypeError, Error: err.Error(), Timestamp: time.Now()}
			return
		}
		for {
			select {
			case raw := <-o.notifyCh:
				var notif struct {
					Method string          `json:"method"`
					Params json.RawMessage `json:"params"`
				}
				if err := json.Unmarshal(raw, &notif); err != nil || notif.Method != "session/update" {
					continue
				}
				msg := o.parseUpdate(notif.Params, sessionID)
				select {
				case ch <- msg:
				case <-ctx.Done():
					return
				}
				if msg.IsFinal {
					return
				}
			case <-ctx.Done():
				return
			case <-time.After(30 * time.Second):
				ch <- Message{Type: MessageTypeError, Error: "timeout waiting for response", Timestamp: time.Now()}
				return
			}
		}
	}()
	return ch, nil
}

func (o *OpenCodeSDK) parseUpdate(params json.RawMessage, sessionID string) Message {
	msg := Message{SessionID: sessionID, Timestamp: time.Now(), RawJSON: append(json.RawMessage(nil), params...)}
	var update map[string]interface{}
	if err := json.Unmarshal(params, &update); err != nil {
		msg.Type = MessageTypeSystem
		return msg
	}
	switch update["type"] {
	case "assistant_message":
		msg.Type = MessageTypeText
		if c, ok := update["content"].(string); ok {
			msg.Content = c
		}
	case "tool_call":
		msg.Type = MessageTypeToolUse
		if n, ok := update["toolName"].(string); ok {
			msg.ToolName = n
		}
		if input, ok := update["input"]; ok {
			if b, err := json.Marshal(input); err == nil {
				msg.ToolInput = string(b)
			}
		}
	case "tool_result":
		msg.Type = MessageTypeToolResult
		if c, ok := update["content"].(string); ok {
			msg.Content = c
		}
	case "turn_complete":
		msg.Type = MessageTypeResult
		msg.IsFinal = true
		if r, ok := update["stopReason"].(string); ok {
			msg.Content = r
		}
	case "error":
		msg.Type = MessageTypeError
		if m, ok := update["message"].(string); ok {
			msg.Error = m
		}
	default:
		msg.Type = MessageTypeSystem
		if b, err := json.Marshal(update); err == nil {
			msg.Content = string(b)
		}
	}
	return msg
}

func (o *OpenCodeSDK) ResumeSession(ctx context.Context, sessionID string) (*Session, error) {
	_, err := o.call(ctx, "session/load", map[string]interface{}{"sessionId": sessionID})
	if err != nil {
		return nil, fmt.Errorf("session/load: %w", err)
	}
	sess := &Session{ID: sessionID, AgentType: AgentOpenCode, CreatedAt: time.Now()}
	o.sessionsMu.Lock()
	o.sessions[sessionID] = sess
	o.sessionsMu.Unlock()
	return sess, nil
}

func (o *OpenCodeSDK) CancelExecution(ctx context.Context, sessionID string) error {
	notif := map[string]interface{}{
		"jsonrpc": "2.0", "method": "session/cancel",
		"params": map[string]interface{}{"sessionId": sessionID},
	}
	body, _ := json.Marshal(notif)
	body = append(body, '\n')
	o.mu.Lock()
	_, err := o.stdin.Write(body)
	o.mu.Unlock()
	return err
}

func (o *OpenCodeSDK) RenameSession(ctx context.Context, sessionID string, title string) error {
	o.sessionsMu.Lock()
	defer o.sessionsMu.Unlock()
	sess, ok := o.sessions[sessionID]
	if !ok {
		return fmt.Errorf("opencode: session %s not found", sessionID)
	}
	sess.Title = title
	return nil
}

func (o *OpenCodeSDK) ListSessions(ctx context.Context, dir string) ([]SessionInfo, error) {
	resp, err := o.call(ctx, "session/list", map[string]interface{}{"cwd": dir})
	if err != nil {
		return nil, fmt.Errorf("session/list: %w", err)
	}
	var result struct {
		Result struct {
			Sessions []struct {
				ID           string `json:"id"`
				Title        string `json:"title"`
				LastModified int64  `json:"lastModified"`
			} `json:"sessions"`
		} `json:"result"`
	}
	if err := json.Unmarshal(resp, &result); err != nil {
		return nil, err
	}
	var list []SessionInfo
	for _, s := range result.Result.Sessions {
		list = append(list, SessionInfo{
			ID: s.ID, Title: s.Title, LastModified: time.UnixMilli(s.LastModified),
		})
	}
	return list, nil
}

func (o *OpenCodeSDK) SetPermissionMode(sessionID string, mode PermissionMode) error {
	o.sessionsMu.Lock()
	defer o.sessionsMu.Unlock()
	sess, ok := o.sessions[sessionID]
	if !ok {
		return fmt.Errorf("opencode: session %s not found", sessionID)
	}
	sess.Options.PermissionMode = mode
	return nil
}

// Close kills the ACP subprocess and cleans up pending channels.
func (o *OpenCodeSDK) Close() error {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.cmd != nil && o.cmd.Process != nil {
		o.cmd.Process.Kill()
	}
	o.running = false
	// Drain and clean pending channels to unblock waiting call() goroutines
	for id, ch := range o.pending {
		close(ch)
		delete(o.pending, id)
	}
	return nil
}
