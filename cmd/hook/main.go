// agent-monitor-hook is called by each agent's hook system to record events.
// It reads the agent's hook payload from stdin, auto-detects session_id and
// event type, appends a JSON line to events.jsonl under a file lock.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

type hookOutput struct {
	Event       string          `json:"event"`
	AgentType   string          `json:"agent_type"`
	SessionID   string          `json:"session_id"`
	DaemonToken string          `json:"daemon_token"`
	PID         int             `json:"pid"`
	CWD         string          `json:"cwd"`
	TimestampMs int64           `json:"timestamp_ms"`
	Payload     json.RawMessage `json:"payload"`
}

func main() {
	agentType := flag.String("agent-type", "", "Agent type (opencode/codex/claude)")
	sessionID := flag.String("session-id", "", "Agent session ID (auto-detected from stdin if not set)")
	event := flag.String("event", "", "Event name (auto-detected from stdin if not set)")
	daemonToken := flag.String("daemon-token", "", "Daemon token (auto-read from ~/.agent-monitor/local-token if not set)")
	flag.Parse()

	debugLog("called agent=%s event=%s session=%s", *agentType, *event, *sessionID)

	if *agentType == "" {
		debugLog("ERROR: --agent-type is required")
		fmt.Fprintf(os.Stderr, "agent-monitor-hook: --agent-type is required\n")
		os.Exit(1)
	}

	payload, err := io.ReadAll(os.Stdin)
	if err != nil {
		debugLog("ERROR: read stdin: %v", err)
		fmt.Fprintf(os.Stderr, "agent-monitor-hook: read stdin: %v\n", err)
		os.Exit(1)
	}
	if len(payload) == 0 {
		payload = []byte("{}")
	}

	debugLog("stdin: %s", truncate(string(payload), 500))

	if !json.Valid(payload) {
		debugLog("WARN: stdin is not valid JSON")
		fmt.Fprintf(os.Stderr, "agent-monitor-hook: stdin is not valid JSON, using raw text\n")
		payload = json.RawMessage(fmt.Sprintf(`{"raw": %q}`, string(payload)))
	}

	extracted := extractFromStdin(payload)
	debugLog("extracted: event=%q session=%q", extracted.event, extracted.sessionID)

	finalSessionID := *sessionID
	if finalSessionID == "" {
		finalSessionID = extracted.sessionID
	}
	finalEvent := *event
	if finalEvent == "" {
		finalEvent = extracted.event
	}
	finalToken := *daemonToken
	if finalToken == "" {
		finalToken = readToken()
	}

	if finalSessionID == "" {
		debugLog("ERROR: session-id not found")
		fmt.Fprintf(os.Stderr, "agent-monitor-hook: session-id not provided and not found in stdin\n")
		os.Exit(1)
	}
	if finalEvent == "" {
		debugLog("ERROR: event not found in stdin")
		fmt.Fprintf(os.Stderr, "agent-monitor-hook: event not provided and not found in stdin\n")
		os.Exit(1)
	}
	if finalToken == "" {
		debugLog("ERROR: token not found")
		fmt.Fprintf(os.Stderr, "agent-monitor-hook: daemon-token not provided and local-token not found\n")
		os.Exit(1)
	}

	pid := os.Getppid()

	cwd, err := os.Getwd()
	if err != nil {
		cwd = ""
	}

	output := hookOutput{
		Event:       finalEvent,
		AgentType:   *agentType,
		SessionID:   finalSessionID,
		DaemonToken: finalToken,
		PID:         pid,
		CWD:         cwd,
		TimestampMs: time.Now().UnixMilli(),
		Payload:     json.RawMessage(payload),
	}

	line, err := json.Marshal(output)
	if err != nil {
		debugLog("ERROR: marshal: %v", err)
		fmt.Fprintf(os.Stderr, "agent-monitor-hook: marshal JSON: %v\n", err)
		os.Exit(1)
	}

	homeDir, err := os.UserHomeDir()
	if err != nil {
		debugLog("ERROR: home dir: %v", err)
		fmt.Fprintf(os.Stderr, "agent-monitor-hook: get home dir: %v\n", err)
		os.Exit(1)
	}

	eventsPath := filepath.Join(homeDir, ".agent-monitor", "events.jsonl")
	if err := os.MkdirAll(filepath.Dir(eventsPath), 0700); err != nil {
		debugLog("ERROR: mkdir: %v", err)
		fmt.Fprintf(os.Stderr, "agent-monitor-hook: create dir: %v\n", err)
		os.Exit(1)
	}

	f, err := os.OpenFile(eventsPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0600)
	if err != nil {
		debugLog("ERROR: open events.jsonl: %v", err)
		fmt.Fprintf(os.Stderr, "agent-monitor-hook: open events.jsonl: %v\n", err)
		os.Exit(1)
	}
	defer f.Close()

	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		debugLog("ERROR: flock: %v", err)
		fmt.Fprintf(os.Stderr, "agent-monitor-hook: lock file: %v\n", err)
		os.Exit(1)
	}
	defer syscall.Flock(int(f.Fd()), syscall.LOCK_UN)

	if _, err := f.Write(line); err != nil {
		debugLog("ERROR: write: %v", err)
		fmt.Fprintf(os.Stderr, "agent-monitor-hook: write: %v\n", err)
		os.Exit(1)
	}
	if _, err := f.Write([]byte("\n")); err != nil {
		fmt.Fprintf(os.Stderr, "agent-monitor-hook: write newline: %v\n", err)
		os.Exit(1)
	}

	debugLog("OK: wrote event=%s session=%s", finalEvent, truncate(finalSessionID, 32))
}

type stdinExtract struct {
	sessionID string
	event     string
}

func extractFromStdin(payload []byte) stdinExtract {
	var data map[string]interface{}
	if err := json.Unmarshal(payload, &data); err != nil {
		return stdinExtract{}
	}

	return stdinExtract{
		// `id` is intentionally NOT in the fallback list: many agent payloads
		// carry a top-level `id` field that is unrelated to the session
		// (e.g. OpenCode plugin.added events have `{"id":"vercel"}`). Treating
		// those as session_id creates dozens of bogus sessions per agent start.
		// Callers that genuinely have a session id must set it on one of the
		// explicit session_id/sessionId/sessionID/sid fields.
		sessionID: extractString(data, "session_id", "sessionId", "sessionID", "sid"),
		event:     extractString(data, "hook_event_name", "hookEventName", "event_name", "eventName", "event_type", "eventType", "event", "type", "name"),
	}
}

func extractString(data map[string]interface{}, keys ...string) string {
	for _, k := range keys {
		if v, ok := data[k].(string); ok && v != "" {
			return v
		}
	}
	return ""
}

func readToken() string {
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	data, err := os.ReadFile(filepath.Join(homeDir, ".agent-monitor", "local-token"))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(data))
}

func debugLog(format string, args ...interface{}) {
	f, err := os.OpenFile("/tmp/agent-monitor-hook.log", os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	if err != nil {
		return
	}
	defer f.Close()
	msg := fmt.Sprintf(format, args...)
	fmt.Fprintf(f, "%s [%d] %s\n", time.Now().Format("15:04:05.000"), os.Getpid(), msg)
}

func truncate(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen] + "..."
}
