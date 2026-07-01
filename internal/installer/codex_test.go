package installer

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestCodexInstallerUsesCODEXHOME(t *testing.T) {
	home := t.TempDir()
	codexHome := filepath.Join(t.TempDir(), "codex-cli")
	if err := os.MkdirAll(codexHome, 0700); err != nil {
		t.Fatalf("mkdir codex home: %v", err)
	}
	if err := os.WriteFile(filepath.Join(codexHome, "config.toml"), []byte("model = \"gpt-5\"\n"), 0600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	t.Setenv("HOME", home)
	t.Setenv("CODEX_HOME", codexHome)

	inst := NewCodexInstaller()
	if !inst.Detect() {
		t.Fatal("Detect() = false, want true for CODEX_HOME config")
	}
	if err := inst.Install("/tmp/agent-monitor-hook"); err != nil {
		t.Fatalf("Install: %v", err)
	}

	if _, err := os.Stat(filepath.Join(codexHome, "hooks.json")); err != nil {
		t.Fatalf("hooks.json not written under CODEX_HOME: %v", err)
	}
	if _, err := os.Stat(filepath.Join(home, ".codex", "hooks.json")); !os.IsNotExist(err) {
		t.Fatalf("unexpected hooks.json under HOME/.codex: %v", err)
	}
}

func TestCodexInstallReplacesExistingManagedHooks(t *testing.T) {
	codexHome := t.TempDir()
	t.Setenv("CODEX_HOME", codexHome)
	if err := os.WriteFile(filepath.Join(codexHome, "config.toml"), []byte(""), 0600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	existing := map[string]interface{}{
		"hooks": map[string]interface{}{
			"Stop": []interface{}{
				map[string]interface{}{
					"hooks": []interface{}{
						map[string]interface{}{"type": "command", "command": "/old/agent-monitor-hook --agent-type codex", "name": ManagedMarker},
					},
				},
			},
		},
	}
	data, _ := json.Marshal(existing)
	if err := os.WriteFile(filepath.Join(codexHome, "hooks.json"), data, 0600); err != nil {
		t.Fatalf("write hooks: %v", err)
	}

	if err := NewCodexInstaller().Install("/new/agent-monitor-hook"); err != nil {
		t.Fatalf("Install: %v", err)
	}

	written := NewCodexInstaller().readHooks(filepath.Join(codexHome, "hooks.json"))
	groups, _ := written["Stop"].([]interface{})
	if len(groups) != 1 {
		t.Fatalf("Stop groups=%d, want exactly one managed replacement", len(groups))
	}
	group := groups[0].(map[string]interface{})
	hooks := group["hooks"].([]interface{})
	cmd := hooks[0].(map[string]interface{})["command"]
	if cmd != "/new/agent-monitor-hook --agent-type codex" {
		t.Fatalf("command=%v, want new hook command", cmd)
	}
}

func TestCodexHookMatcherOnlyLivesOnGroup(t *testing.T) {
	hooks := map[string]interface{}{}
	NewCodexInstaller().addHookEntry(hooks, "PreToolUse", "*", "/tmp/agent-monitor-hook")

	groups := hooks["PreToolUse"].([]interface{})
	group := groups[0].(map[string]interface{})
	if group["matcher"] != "*" {
		t.Fatalf("group matcher=%v, want *", group["matcher"])
	}
	hook := group["hooks"].([]interface{})[0].(map[string]interface{})
	if _, ok := hook["matcher"]; ok {
		t.Fatalf("handler-level matcher should not be emitted: %#v", hook)
	}
}
