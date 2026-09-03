package coordinator

import (
	"context"
	"encoding/json"
	"os/exec"
	"strings"
	"time"
)

type agentDefinition struct {
	id          string
	name        string
	commands    []string
	versionArgs []string
	authArgs    []string
	transport   string
}

var knownAgents = []agentDefinition{
	{id: "codex", name: "Codex", commands: []string{"codex"}, versionArgs: []string{"--version"}, authArgs: []string{"login", "status"}, transport: "structured-cli"},
	{id: "claude", name: "Claude Code", commands: []string{"claude"}, versionArgs: []string{"--version"}, authArgs: []string{"auth", "status"}, transport: "structured-cli"},
	{id: "opencode", name: "OpenCode", commands: []string{"opencode"}, versionArgs: []string{"--version"}, transport: "acp"},
	{id: "gemini", name: "Gemini CLI", commands: []string{"gemini"}, versionArgs: []string{"--version"}, transport: "structured-cli"},
	{id: "aider", name: "Aider", commands: []string{"aider"}, versionArgs: []string{"--version"}, transport: "pty"},
}

func DiscoverAgents(ctx context.Context) []Agent {
	now := time.Now().UTC()
	agents := make([]Agent, 0, len(knownAgents))
	for _, definition := range knownAgents {
		agent := Agent{ID: definition.id, Name: definition.name, AuthStatus: "unavailable", Transport: definition.transport, DetectedAt: now}
		for _, command := range definition.commands {
			resolved, err := exec.LookPath(command)
			if err == nil {
				agent.Command = resolved
				agent.Available = true
				break
			}
		}
		if agent.Available {
			agent.Runnable = supportsExecution(agent.ID)
			agent.Version = safeCommandOutput(ctx, agent.Command, definition.versionArgs...)
			agent.AuthStatus = "unknown"
			if len(definition.authArgs) > 0 {
				agent.AuthStatus = detectAuthStatus(ctx, definition, agent.Command)
			}
		}
		agents = append(agents, agent)
	}
	return agents
}

func safeCommandOutput(parent context.Context, command string, args ...string) string {
	ctx, cancel := context.WithTimeout(parent, 3*time.Second)
	defer cancel()
	output, err := exec.CommandContext(ctx, command, args...).CombinedOutput()
	if err != nil {
		return ""
	}
	value := strings.TrimSpace(string(output))
	if newline := strings.IndexByte(value, '\n'); newline >= 0 {
		value = value[:newline]
	}
	if len(value) > 160 {
		value = value[:160]
	}
	return strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return -1
		}
		return r
	}, value)
}

func detectAuthStatus(parent context.Context, definition agentDefinition, command string) string {
	ctx, cancel := context.WithTimeout(parent, 3*time.Second)
	defer cancel()
	output, err := exec.CommandContext(ctx, command, definition.authArgs...).CombinedOutput()
	if err != nil {
		return "needs-login"
	}
	switch definition.id {
	case "codex":
		if strings.Contains(strings.ToLower(string(output)), "logged in") {
			return "authenticated"
		}
	case "claude":
		var status struct {
			LoggedIn bool `json:"loggedIn"`
		}
		if json.Unmarshal(output, &status) == nil && status.LoggedIn {
			return "authenticated"
		}
	}
	return "unknown"
}

func definitionFor(id string) (agentDefinition, bool) {
	for _, definition := range knownAgents {
		if definition.id == id {
			return definition, true
		}
	}
	return agentDefinition{}, false
}

func supportsExecution(id string) bool { return id == "codex" || id == "claude" }
