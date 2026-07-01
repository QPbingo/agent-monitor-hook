// Package installer manages automatic hook registration for supported AI agents.
// Each agent has a dedicated Installer that knows its config format and hook location.
// Managed hooks carry a "name":"agent-monitor" marker so uninstall only removes our own entries.
// All config modifications are backed up before writing.
package installer

import (
	"encoding/json"
	"os"
	"path/filepath"
	"time"
)

const ManagedMarker = "agent-monitor"

type Manifest struct {
	Agent        string `json:"agent"`
	HookCommand  string `json:"hook_command"`
	InstalledAt  string `json:"installed_at"`
}

type Installer interface {
	Name() string
	Detect() bool
	IsInstalled() (bool, error)
	Install(hookBinPath string) error
	Uninstall() error
	Status() string
}

func readManifest(dir, filename string) *Manifest {
	data, err := os.ReadFile(filepath.Join(dir, filename))
	if err != nil {
		return nil
	}
	var m Manifest
	if err := json.Unmarshal(data, &m); err != nil {
		return nil
	}
	return &m
}

func writeManifest(dir, filename string, m *Manifest) error {
	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, filename), data, 0644)
}

func removeManifest(dir, filename string) {
	os.Remove(filepath.Join(dir, filename))
}

func backupFile(path string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	backupPath := path + ".backup." + time.Now().Format("2006-01-02T15-04-05")
	return os.WriteFile(backupPath, data, 0644)
}

func FindHookBin() string {
	paths := []string{
		"agent-monitor-hook",
		"/usr/local/bin/agent-monitor-hook",
	}

	homeDir, _ := os.UserHomeDir()
	if homeDir != "" {
		paths = append(paths, filepath.Join(homeDir, ".local", "bin", "agent-monitor-hook"))
	}

	for _, p := range paths {
		if fi, err := os.Stat(p); err == nil && !fi.IsDir() && fi.Mode()&0111 != 0 {
			return p
		}
	}

	exe, err := os.Executable()
	if err == nil {
		dir := filepath.Dir(exe)
		candidate := filepath.Join(dir, "agent-monitor-hook")
		if fi, err := os.Stat(candidate); err == nil && !fi.IsDir() {
			return candidate
		}
	}

	return "agent-monitor-hook"
}

func isManagedHook(hookCmd string) bool {
	return containsAny(hookCmd, ManagedMarker, "agent-monitor-hook")
}

func nowISO() string {
	return time.Now().Format(time.RFC3339)
}

func containsAny(s string, substrs ...string) bool {
	for _, sub := range substrs {
		if len(sub) > 0 && len(s) >= len(sub) {
			for i := 0; i <= len(s)-len(sub); i++ {
				if s[i:i+len(sub)] == sub {
					return true
				}
			}
		}
	}
	return false
}
