package installer

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestClaudeInstallReplacesExistingManagedHooks(t *testing.T) {
	claudeHome := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", claudeHome)
	if err := os.WriteFile(filepath.Join(claudeHome, "settings.json"), []byte(`{"hooks":{}}`), 0600); err != nil {
		t.Fatalf("write settings: %v", err)
	}
	existing := map[string]interface{}{
		"hooks": map[string]interface{}{
			"Stop": []interface{}{
				map[string]interface{}{
					"hooks": []interface{}{
						map[string]interface{}{"type": "command", "command": "/old/agent-monitor-hook --agent-type claude", "name": ManagedMarker},
					},
				},
			},
		},
	}
	data, _ := json.Marshal(existing)
	if err := os.WriteFile(filepath.Join(claudeHome, "settings.json"), data, 0600); err != nil {
		t.Fatalf("write settings: %v", err)
	}

	if err := NewClaudeInstaller().Install("/new/agent-monitor-hook"); err != nil {
		t.Fatalf("Install: %v", err)
	}

	written := NewClaudeInstaller().readSettings(filepath.Join(claudeHome, "settings.json"))
	hooks := written["hooks"].(map[string]interface{})
	groups, _ := hooks["Stop"].([]interface{})
	if len(groups) != 1 {
		t.Fatalf("Stop groups=%d, want exactly one managed replacement", len(groups))
	}
	group := groups[0].(map[string]interface{})
	hookList := group["hooks"].([]interface{})
	cmd := hookList[0].(map[string]interface{})["command"]
	if cmd != "/new/agent-monitor-hook --agent-type claude" {
		t.Fatalf("command=%v, want new hook command", cmd)
	}
}

func TestClaudeHookMatcherOnlyLivesOnGroup(t *testing.T) {
	hooks := map[string]interface{}{}
	NewClaudeInstaller().addHookEntry(hooks, "PreToolUse", "*", "/tmp/agent-monitor-hook")

	groups := hooks["PreToolUse"].([]interface{})
	group := groups[0].(map[string]interface{})
	if group["matcher"] != "*" {
		t.Fatalf("group matcher=%v, want *", group["matcher"])
	}
	hook := group["hooks"].([]interface{})[0].(map[string]interface{})
	if _, ok := hook["matcher"]; ok {
		t.Fatalf("handler-level matcher should not be emitted: %#v", hook)
	}
	if _, ok := hook["args"]; ok {
		t.Fatalf("handler-level args should not be emitted: %#v", hook)
	}
	if hook["command"] != "/tmp/agent-monitor-hook --agent-type claude" {
		t.Fatalf("command=%v, want inline agent type argument", hook["command"])
	}
}
