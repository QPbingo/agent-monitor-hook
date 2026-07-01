// agent-monitor-setup manages the agent-monitor environment.
// Subcommands: init (device-id + token), install/uninstall/status (per-agent hooks).
package main

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"

	"github.com/heybox/agent-monitor-hook/internal/installer"
	"github.com/heybox/agent-monitor-hook/internal/token"
)

func main() {
	if len(os.Args) < 2 {
		printUsage()
		os.Exit(1)
	}

	cmd := os.Args[1]

	switch cmd {
	case "init":
		runInit()

	case "install":
		runInstall(os.Args[2:])

	case "uninstall":
		runUninstall(os.Args[2:])

	case "status":
		runStatus(os.Args[2:])

	default:
		printUsage()
		os.Exit(1)
	}
}

func printUsage() {
	fmt.Println(`agent-monitor-setup — manage agent hooks for agent-monitor-daemon

Usage:
  agent-monitor-setup init                      Initialize ~/.agent-monitor (device-id + token)
  agent-monitor-setup install [--claude] [--codex] [--opencode] [--all]
  agent-monitor-setup uninstall [--claude] [--codex] [--opencode]
  agent-monitor-setup status`)
}

func runInit() {
	homeDir, err := os.UserHomeDir()
	if err != nil {
		fmt.Fprintf(os.Stderr, "get home dir: %v\n", err)
		os.Exit(1)
	}

	monitorDir := filepath.Join(homeDir, ".agent-monitor")

	if err := os.MkdirAll(monitorDir, 0700); err != nil {
		fmt.Fprintf(os.Stderr, "create dir %s: %v\n", monitorDir, err)
		os.Exit(1)
	}

	deviceIDPath := filepath.Join(monitorDir, "device-id")
	tokenPath := filepath.Join(monitorDir, "local-token")

	needDeviceID := false
	if _, err := os.Stat(deviceIDPath); os.IsNotExist(err) {
		needDeviceID = true
	}

	needToken := false
	if _, err := os.Stat(tokenPath); os.IsNotExist(err) {
		needToken = true
	}

	if !needDeviceID && !needToken {
		fmt.Println("agent-monitor-setup: already initialized.")
		return
	}

	if needDeviceID {
		uuid := generateUUIDv4()
		if err := os.WriteFile(deviceIDPath, []byte(uuid), 0600); err != nil {
			fmt.Fprintf(os.Stderr, "write device-id: %v\n", err)
			os.Exit(1)
		}
		fmt.Printf("Created: %s\n", deviceIDPath)
	}

	if needToken {
		tok, err := token.Generate()
		if err != nil {
			fmt.Fprintf(os.Stderr, "generate token: %v\n", err)
			os.Exit(1)
		}
		if err := token.Write(monitorDir, tok); err != nil {
			fmt.Fprintf(os.Stderr, "write token: %v\n", err)
			os.Exit(1)
		}
		fmt.Printf("Created: %s\n", tokenPath)
	}

	fmt.Println("agent-monitor-setup: initialization complete.")
}

func runInstall(args []string) {
	hookBinPath := installer.FindHookBin()

	agents := parseAgentFlags(args, "install")

	for _, inst := range agents {
		fmt.Printf("Installing hooks for %s...\n", inst.Name())

		installed, err := inst.IsInstalled()
		if err != nil {
			fmt.Fprintf(os.Stderr, "  check status: %v\n", err)
			continue
		}
		if installed {
			fmt.Printf("  already installed, refreshing\n")
		}

		if err := inst.Install(hookBinPath); err != nil {
			fmt.Fprintf(os.Stderr, "  install failed: %v\n", err)
			continue
		}
		fmt.Printf("  installed ✓ (hook: %s)\n", hookBinPath)
	}
}

func runUninstall(args []string) {
	agents := parseAgentFlags(args, "uninstall")

	for _, inst := range agents {
		fmt.Printf("Uninstalling hooks for %s...\n", inst.Name())

		installed, err := inst.IsInstalled()
		if err != nil {
			fmt.Fprintf(os.Stderr, "  check status: %v\n", err)
			continue
		}
		if !installed {
			fmt.Printf("  not installed, skipping\n")
			continue
		}

		if err := inst.Uninstall(); err != nil {
			fmt.Fprintf(os.Stderr, "  uninstall failed: %v\n", err)
			continue
		}
		fmt.Printf("  uninstalled ✓\n")
	}
}

func runStatus(args []string) {
	allInst := allInstallers()

	if len(args) == 0 {
		fmt.Println("Hook installation status:")
		for _, inst := range allInst {
			detected := inst.Detect()
			status := inst.Status()
			marker := " "
			if detected && status == "not installed" {
				marker = "○"
			} else if detected {
				marker = "●"
			} else {
				marker = "-"
			}
			fmt.Printf("  %s %-12s: %s\n", marker, inst.Name(), status)
		}
		return
	}

	agents := parseAgentFlags(args, "status")
	for _, inst := range agents {
		fmt.Printf("%s: %s\n", inst.Name(), inst.Status())
	}
}

func parseAgentFlags(args []string, cmd string) []installer.Installer {
	all := allInstallers()
	includeAll := false
	includeClaude := false
	includeCodex := false
	includeOpenCode := false

	for _, arg := range args {
		switch arg {
		case "--all":
			includeAll = true
		case "--claude":
			includeClaude = true
		case "--codex":
			includeCodex = true
		case "--opencode":
			includeOpenCode = true
		}
	}

	if !includeAll && !includeClaude && !includeCodex && !includeOpenCode {
		if cmd == "status" {
			return all
		}
		fmt.Fprintf(os.Stderr, "Specify agents: --claude, --codex, --opencode, or --all\n")
		os.Exit(1)
	}

	if includeAll {
		return all
	}

	var result []installer.Installer
	for _, inst := range all {
		switch inst.Name() {
		case "Claude Code":
			if includeClaude {
				result = append(result, inst)
			}
		case "Codex":
			if includeCodex {
				result = append(result, inst)
			}
		case "OpenCode":
			if includeOpenCode {
				result = append(result, inst)
			}
		}
	}
	return result
}

func allInstallers() []installer.Installer {
	return []installer.Installer{
		installer.NewClaudeInstaller(),
		installer.NewCodexInstaller(),
		installer.NewOpenCodeInstaller(),
	}
}

func generateUUIDv4() string {
	u := make([]byte, 16)
	rand.Read(u)
	u[6] = (u[6] & 0x0f) | 0x40
	u[8] = (u[8] & 0x3f) | 0x80
	return fmt.Sprintf("%s-%s-%s-%s-%s",
		hex.EncodeToString(u[0:4]),
		hex.EncodeToString(u[4:6]),
		hex.EncodeToString(u[6:8]),
		hex.EncodeToString(u[8:10]),
		hex.EncodeToString(u[10:16]),
	)
}
