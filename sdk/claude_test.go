package sdk

import (
	"encoding/json"
	"testing"
)

func TestClaudeParseMessageEmitsOneMessagePerTextBlock(t *testing.T) {
	c := NewClaudeSDK(ClaudeOptions{})
	msgs := c.parseMessage(map[string]interface{}{
		"type": "assistant",
		"message": map[string]interface{}{
			"content": []interface{}{
				map[string]interface{}{"type": "text", "text": "first"},
				map[string]interface{}{"type": "text", "text": "second"},
			},
		},
	}, "sid")

	if len(msgs) != 2 {
		t.Fatalf("got %d messages, want 2 (one per text block): %+v", len(msgs), msgs)
	}
	if msgs[0].Type != MessageTypeText || msgs[0].Content != "first" {
		t.Fatalf("msgs[0]=%+v, want text 'first'", msgs[0])
	}
	if msgs[1].Type != MessageTypeText || msgs[1].Content != "second" {
		t.Fatalf("msgs[1]=%+v, want text 'second'", msgs[1])
	}
}

func TestClaudeParseAssistantMessageExtractsToolUse(t *testing.T) {
	c := NewClaudeSDK(ClaudeOptions{})
	msgs := c.parseMessage(map[string]interface{}{
		"type": "assistant",
		"message": map[string]interface{}{
			"content": []interface{}{
				map[string]interface{}{"type": "tool_use", "name": "Read", "input": map[string]interface{}{"file_path": "/foo.go"}},
			},
		},
	}, "sid")

	if len(msgs) != 1 {
		t.Fatalf("got %d messages, want 1: %+v", len(msgs), msgs)
	}
	if msgs[0].Type != MessageTypeToolUse {
		t.Fatalf("Type=%s, want tool_use", msgs[0].Type)
	}
	if msgs[0].ToolName != "Read" {
		t.Fatalf("ToolName=%q, want Read", msgs[0].ToolName)
	}
	var input map[string]interface{}
	if err := json.Unmarshal([]byte(msgs[0].ToolInput), &input); err != nil {
		t.Fatalf("ToolInput is not valid JSON: %v", err)
	}
	if input["file_path"] != "/foo.go" {
		t.Fatalf("ToolInput=%v, want file_path=/foo.go", input)
	}
}

func TestClaudeParseAssistantMessageHandlesMixedTextAndToolUse(t *testing.T) {
	c := NewClaudeSDK(ClaudeOptions{})
	msgs := c.parseMessage(map[string]interface{}{
		"type": "assistant",
		"message": map[string]interface{}{
			"content": []interface{}{
				map[string]interface{}{"type": "text", "text": "Let me check that file."},
				map[string]interface{}{"type": "tool_use", "name": "Read", "input": map[string]interface{}{"file_path": "/foo.go"}},
			},
		},
	}, "sid")

	if len(msgs) != 2 {
		t.Fatalf("got %d messages, want 2 (text then tool_use): %+v", len(msgs), msgs)
	}
	if msgs[0].Type != MessageTypeText || msgs[0].Content != "Let me check that file." {
		t.Fatalf("msgs[0]=%+v, want the text block first", msgs[0])
	}
	if msgs[1].Type != MessageTypeToolUse || msgs[1].ToolName != "Read" {
		t.Fatalf("msgs[1]=%+v, want the tool_use block second", msgs[1])
	}
}

func TestClaudeParseAssistantMessageHandlesParallelToolCalls(t *testing.T) {
	c := NewClaudeSDK(ClaudeOptions{})
	msgs := c.parseMessage(map[string]interface{}{
		"type": "assistant",
		"message": map[string]interface{}{
			"content": []interface{}{
				map[string]interface{}{"type": "tool_use", "name": "Read", "input": map[string]interface{}{"file_path": "/a.go"}},
				map[string]interface{}{"type": "tool_use", "name": "Read", "input": map[string]interface{}{"file_path": "/b.go"}},
			},
		},
	}, "sid")

	if len(msgs) != 2 {
		t.Fatalf("got %d messages, want 2 (one per parallel tool call): %+v", len(msgs), msgs)
	}
	for i, want := range []string{"/a.go", "/b.go"} {
		var input map[string]interface{}
		if err := json.Unmarshal([]byte(msgs[i].ToolInput), &input); err != nil {
			t.Fatalf("msgs[%d].ToolInput not valid JSON: %v", i, err)
		}
		if input["file_path"] != want {
			t.Fatalf("msgs[%d] file_path=%v, want %s", i, input["file_path"], want)
		}
	}
}

func TestClaudeParseResultKeepsResultContentOverStopReason(t *testing.T) {
	c := NewClaudeSDK(ClaudeOptions{})
	msgs := c.parseMessage(map[string]interface{}{
		"type":        "result",
		"result":      "complete final answer",
		"stop_reason": "end_turn",
	}, "sid")

	if len(msgs) != 1 {
		t.Fatalf("got %d messages, want 1", len(msgs))
	}
	if msgs[0].Type != MessageTypeResult || !msgs[0].IsFinal {
		t.Fatalf("result metadata mismatch: %+v", msgs[0])
	}
	if msgs[0].Content != "complete final answer" {
		t.Fatalf("Content=%q, want result content", msgs[0].Content)
	}
}

func TestClaudeParseMessageKeepsRawJSON(t *testing.T) {
	c := NewClaudeSDK(ClaudeOptions{})
	raw := map[string]interface{}{
		"type": "assistant",
		"message": map[string]interface{}{
			"content": []interface{}{map[string]interface{}{"type": "text", "text": "hello"}},
		},
		"usage": map[string]interface{}{"input_tokens": 12},
	}

	msgs := c.parseMessage(raw, "sid")

	if len(msgs) != 1 {
		t.Fatalf("got %d messages, want 1", len(msgs))
	}
	if len(msgs[0].RawJSON) == 0 {
		t.Fatal("RawJSON is empty, want original raw message bytes")
	}
	var got map[string]interface{}
	if err := json.Unmarshal(msgs[0].RawJSON, &got); err != nil {
		t.Fatalf("RawJSON is not valid JSON: %v", err)
	}
	if _, ok := got["usage"]; !ok {
		t.Fatalf("RawJSON missing usage field: %s", msgs[0].RawJSON)
	}
}
