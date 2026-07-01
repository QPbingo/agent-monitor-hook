package installer

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

const claudeManifestFile = "agent-monitor-claude-hooks.json"

type ClaudeInstaller struct{}

func NewClaudeInstaller() *ClaudeInstaller { return &ClaudeInstaller{} }

func (c *ClaudeInstaller) Name() string { return "Claude Code" }

func (c *ClaudeInstaller) Detect() bool {
	dir := c.configDir()
	if _, err := os.Stat(dir); err != nil {
		return false
	}
	_, err := os.Stat(filepath.Join(dir, "settings.json"))
	return err == nil
}

func (c *ClaudeInstaller) IsInstalled() (bool, error) {
	m := readManifest(c.configDir(), claudeManifestFile)
	return m != nil, nil
}

func (c *ClaudeInstaller) Install(hookBinPath string) error {
	if hookBinPath == "" {
		hookBinPath = FindHookBin()
	}

	dir := c.configDir()
	settingsPath := filepath.Join(dir, "settings.json")
	if err := os.MkdirAll(dir, 0700); err != nil {
		return fmt.Errorf("create claude config dir: %w", err)
	}

	if err := backupFile(settingsPath); err != nil {
		return fmt.Errorf("backup settings.json: %w", err)
	}

	settings := c.readSettings(settingsPath)
	if settings == nil {
		settings = make(map[string]interface{})
	}

	hooks, _ := settings["hooks"].(map[string]interface{})
	if hooks == nil {
		hooks = make(map[string]interface{})
		settings["hooks"] = hooks
	}
	c.removeManagedHooks(hooks, hookBinPath)

	events := []struct {
		event   string
		matcher string
	}{
		{"SessionStart", ""},
		{"Setup", ""},
		{"UserPromptSubmit", ""},
		{"UserPromptExpansion", ""},
		{"PreToolUse", "*"},
		{"PermissionRequest", "*"},
		{"PermissionDenied", "*"},
		{"PostToolUse", "*"},
		{"PostToolUseFailure", "*"},
		{"PostToolBatch", ""},
		{"Notification", ""},
		{"MessageDisplay", ""},
		{"SubagentStart", "*"},
		{"SubagentStop", "*"},
		{"TaskCreated", ""},
		{"TaskCompleted", ""},
		{"Stop", ""},
		{"StopFailure", ""},
		{"TeammateIdle", ""},
		{"InstructionsLoaded", ""},
		{"ConfigChange", ""},
		{"CwdChanged", ""},
		{"FileChanged", ""},
		{"WorktreeCreate", ""},
		{"WorktreeRemove", ""},
		{"PreCompact", ""},
		{"PostCompact", ""},
		{"Elicitation", "*"},
		{"ElicitationResult", "*"},
		{"SessionEnd", ""},
	}

	for _, ev := range events {
		c.addHookEntry(hooks, ev.event, ev.matcher, hookBinPath)
	}

	data, err := json.MarshalIndent(settings, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal settings: %w", err)
	}
	if err := os.WriteFile(settingsPath, data, 0644); err != nil {
		return fmt.Errorf("write settings.json: %w", err)
	}

	return writeManifest(dir, claudeManifestFile, &Manifest{
		Agent:       "claude",
		HookCommand: hookBinPath,
		InstalledAt: nowISO(),
	})
}

func (c *ClaudeInstaller) Uninstall() error {
	dir := c.configDir()
	settingsPath := filepath.Join(dir, "settings.json")

	m := readManifest(dir, claudeManifestFile)
	if m == nil {
		return fmt.Errorf("not installed")
	}

	if err := backupFile(settingsPath); err != nil {
		return fmt.Errorf("backup settings.json: %w", err)
	}

	settings := c.readSettings(settingsPath)
	if settings == nil {
		removeManifest(dir, claudeManifestFile)
		return nil
	}

	hooks, _ := settings["hooks"].(map[string]interface{})
	if hooks == nil {
		removeManifest(dir, claudeManifestFile)
		return nil
	}

	c.removeManagedHooks(hooks, m.HookCommand)

	if len(hooks) == 0 {
		delete(settings, "hooks")
	}

	data, _ := json.MarshalIndent(settings, "", "  ")
	os.WriteFile(settingsPath, data, 0644)

	removeManifest(dir, claudeManifestFile)
	return nil
}

func (c *ClaudeInstaller) Status() string {
	dir := c.configDir()
	m := readManifest(dir, claudeManifestFile)
	if m == nil {
		return "not installed"
	}
	return fmt.Sprintf("installed (%s)", m.InstalledAt)
}

func (c *ClaudeInstaller) configDir() string {
	if d := os.Getenv("CLAUDE_CONFIG_DIR"); d != "" {
		return d
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".claude")
}

func (c *ClaudeInstaller) readSettings(path string) map[string]interface{} {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var settings map[string]interface{}
	if err := json.Unmarshal(data, &settings); err != nil {
		return nil
	}
	return settings
}

func (c *ClaudeInstaller) addHookEntry(hooks map[string]interface{}, event, matcher, hookBinPath string) {
	entries, _ := hooks[event].([]interface{})

	hookObj := map[string]interface{}{
		"type":    "command",
		"command": hookBinPath + " --agent-type claude",
		"name":    ManagedMarker,
	}

	newGroup := map[string]interface{}{
		"hooks": []interface{}{hookObj},
	}
	if matcher != "" {
		newGroup["matcher"] = matcher
	}

	hooks[event] = append(entries, newGroup)
}

func (c *ClaudeInstaller) removeManagedHooks(hooks map[string]interface{}, hookCommand string) {
	for event, val := range hooks {
		groups, ok := val.([]interface{})
		if !ok {
			continue
		}

		var kept []interface{}
		for _, g := range groups {
			group, ok := g.(map[string]interface{})
			if !ok {
				kept = append(kept, g)
				continue
			}

			hookList, _ := group["hooks"].([]interface{})
			if hookList == nil {
				kept = append(kept, g)
				continue
			}

			var keptHooks []interface{}
			for _, h := range hookList {
				hook, ok := h.(map[string]interface{})
				if !ok {
					keptHooks = append(keptHooks, h)
					continue
				}
				if name, _ := hook["name"].(string); name == ManagedMarker {
					continue
				}
				if cmd, _ := hook["command"].(string); isManagedHook(cmd) {
					continue
				}
				keptHooks = append(keptHooks, h)
			}

			if len(keptHooks) > 0 {
				group["hooks"] = keptHooks
				kept = append(kept, group)
			}
		}

		if len(kept) == 0 {
			delete(hooks, event)
		} else {
			hooks[event] = kept
		}
	}
}
