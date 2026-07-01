package sdk

import (
	"encoding/json"
	"testing"
)

func TestClaudeParseMessageCombinesAllAssistantTextBlocks(t *testing.T) {
	c := NewClaudeSDK(ClaudeOptions{})
	msg := c.parseMessage(map[string]interface{}{
		"type": "assistant",
		"message": map[string]interface{}{
			"content": []interface{}{
				map[string]interface{}{"type": "text", "text": "first"},
				map[string]interface{}{"type": "text", "text": "second"},
			},
		},
	}, "sid")

	if msg.Type != MessageTypeText {
		t.Fatalf("Type=%s, want text", msg.Type)
	}
	if msg.Content != "first\nsecond" {
		t.Fatalf("Content=%q, want combined text blocks", msg.Content)
	}
}

func TestClaudeParseResultKeepsResultContentOverStopReason(t *testing.T) {
	c := NewClaudeSDK(ClaudeOptions{})
	msg := c.parseMessage(map[string]interface{}{
		"type":        "result",
		"result":      "complete final answer",
		"stop_reason": "end_turn",
	}, "sid")

	if msg.Type != MessageTypeResult || !msg.IsFinal {
		t.Fatalf("result metadata mismatch: %+v", msg)
	}
	if msg.Content != "complete final answer" {
		t.Fatalf("Content=%q, want result content", msg.Content)
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

	msg := c.parseMessage(raw, "sid")

	if len(msg.RawJSON) == 0 {
		t.Fatal("RawJSON is empty, want original raw message bytes")
	}
	var got map[string]interface{}
	if err := json.Unmarshal(msg.RawJSON, &got); err != nil {
		t.Fatalf("RawJSON is not valid JSON: %v", err)
	}
	if _, ok := got["usage"]; !ok {
		t.Fatalf("RawJSON missing usage field: %s", msg.RawJSON)
	}
}
