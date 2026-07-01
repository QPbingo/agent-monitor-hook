package installer

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const codexManifestFile = "agent-monitor-codex-hooks.json"

type CodexInstaller struct{}

func NewCodexInstaller() *CodexInstaller { return &CodexInstaller{} }

func (c *CodexInstaller) Name() string { return "Codex" }

func (c *CodexInstaller) Detect() bool {
	dir := c.configDir()
	if _, err := os.Stat(dir); err != nil {
		return false
	}
	_, err := os.Stat(filepath.Join(dir, "config.toml"))
	return err == nil
}

func (c *CodexInstaller) IsInstalled() (bool, error) {
	m := readManifest(c.configDir(), codexManifestFile)
	return m != nil, nil
}

func (c *CodexInstaller) Install(hookBinPath string) error {
	if hookBinPath == "" {
		hookBinPath = FindHookBin()
	}

	dir := c.configDir()
	if err := os.MkdirAll(dir, 0700); err != nil {
		return fmt.Errorf("create codex config dir: %w", err)
	}

	if err := c.enableHooksFeature(dir); err != nil {
		return fmt.Errorf("enable hooks feature: %w", err)
	}

	hooksPath := filepath.Join(dir, "hooks.json")

	if err := backupFile(hooksPath); err != nil {
		return fmt.Errorf("backup hooks.json: %w", err)
	}

	hooks := c.readHooks(hooksPath)
	if hooks == nil {
		hooks = make(map[string]interface{})
	}
	c.removeManagedHooks(hooks, hookBinPath)

	events := []struct {
		event   string
		matcher string
	}{
		{"SessionStart", "startup|resume"},
		{"UserPromptSubmit", ""},
		{"PreToolUse", "*"},
		{"PermissionRequest", "*"},
		{"PostToolUse", "*"},
		{"PreCompact", ""},
		{"PostCompact", ""},
		{"SubagentStart", "*"},
		{"SubagentStop", "*"},
		{"Stop", ""},
	}

	for _, ev := range events {
		c.addHookEntry(hooks, ev.event, ev.matcher, hookBinPath)
	}

	data, err := json.MarshalIndent(map[string]interface{}{"hooks": hooks}, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal hooks: %w", err)
	}
	if err := os.WriteFile(hooksPath, data, 0644); err != nil {
		return fmt.Errorf("write hooks.json: %w", err)
	}

	return writeManifest(dir, codexManifestFile, &Manifest{
		Agent:       "codex",
		HookCommand: hookBinPath,
		InstalledAt: nowISO(),
	})
}

func (c *CodexInstaller) Uninstall() error {
	dir := c.configDir()

	m := readManifest(dir, codexManifestFile)
	if m == nil {
		return fmt.Errorf("not installed")
	}

	hooksPath := filepath.Join(dir, "hooks.json")

	if err := backupFile(hooksPath); err != nil {
		return fmt.Errorf("backup hooks.json: %w", err)
	}

	hooks := c.readHooks(hooksPath)
	if hooks == nil {
		removeManifest(dir, codexManifestFile)
		return nil
	}

	c.removeManagedHooks(hooks, m.HookCommand)

	if len(hooks) == 0 {
		os.Remove(hooksPath)
	} else {
		data, _ := json.MarshalIndent(map[string]interface{}{"hooks": hooks}, "", "  ")
		os.WriteFile(hooksPath, data, 0644)
	}

	removeManifest(dir, codexManifestFile)
	return nil
}

func (c *CodexInstaller) Status() string {
	dir := c.configDir()
	m := readManifest(dir, codexManifestFile)
	if m == nil {
		return "not installed"
	}
	return fmt.Sprintf("installed (%s)", m.InstalledAt)
}

func (c *CodexInstaller) configDir() string {
	if d := os.Getenv("CODEX_HOME"); d != "" {
		return d
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".codex")
}

func (c *CodexInstaller) enableHooksFeature(dir string) error {
	tomlPath := filepath.Join(dir, "config.toml")
	if err := backupFile(tomlPath); err != nil {
		return err
	}

	data, err := os.ReadFile(tomlPath)
	if err != nil {
		if os.IsNotExist(err) {
			return os.WriteFile(tomlPath, []byte("[features]\nhooks = true\n"), 0644)
		}
		return err
	}

	content := string(data)

	if strings.Contains(content, "[features]") {
		if !strings.Contains(content, "hooks = true") && !strings.Contains(content, "codex_hooks = true") {
			content = strings.Replace(content, "[features]", "[features]\nhooks = true", 1)
		}
	} else {
		content += "\n[features]\nhooks = true\n"
	}

	return os.WriteFile(tomlPath, []byte(content), 0644)
}

func (c *CodexInstaller) readHooks(path string) map[string]interface{} {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var wrapper struct {
		Hooks map[string]interface{} `json:"hooks"`
	}
	if err := json.Unmarshal(data, &wrapper); err != nil {
		return nil
	}
	return wrapper.Hooks
}

func (c *CodexInstaller) addHookEntry(hooks map[string]interface{}, event, matcher, hookBinPath string) {
	entries, _ := hooks[event].([]interface{})

	hookObj := map[string]interface{}{
		"type":    "command",
		"command": hookBinPath + " --agent-type codex",
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

func (c *CodexInstaller) removeManagedHooks(hooks map[string]interface{}, hookCommand string) {
	claudeInst := &ClaudeInstaller{}
	claudeInst.removeManagedHooks(hooks, hookCommand)
}
