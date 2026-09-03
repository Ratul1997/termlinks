package coordinator

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"regexp"
	"strings"
)

var mentionPattern = regexp.MustCompile(`(?i)@([a-z][a-z0-9_-]{0,31})`)

func Compile(input CreateInput, agents []Agent) (Draft, error) {
	request := strings.TrimSpace(input.Request)
	if request == "" {
		return Draft{}, errors.New("workflow request is required")
	}
	if len(request) > 48<<10 {
		return Draft{}, errors.New("workflow request is too large")
	}
	available := make(map[string]Agent)
	installed := make(map[string]Agent)
	for _, agent := range agents {
		if agent.Available {
			installed[strings.ToLower(agent.ID)] = agent
		}
		if agent.Available && agent.Runnable && agent.AuthStatus != "needs-login" {
			available[strings.ToLower(agent.ID)] = agent
		}
	}
	matches := mentionPattern.FindAllStringSubmatchIndex(request, -1)
	if len(matches) > 8 {
		return Draft{}, errors.New("a workflow can contain at most 8 agent stages")
	}
	stages := make([]Stage, 0, len(matches))
	for index, match := range matches {
		agentID := strings.ToLower(request[match[2]:match[3]])
		if _, known := definitionFor(agentID); !known {
			return Draft{}, fmt.Errorf("unknown AI agent @%s", agentID)
		}
		if candidate, ok := installed[agentID]; ok && !candidate.Runnable {
			return Draft{}, fmt.Errorf("@%s is installed but its structured Termlinks adapter is not enabled yet", agentID)
		}
		if _, ok := available[agentID]; !ok {
			return Draft{}, fmt.Errorf("@%s is not installed or needs login on this computer", agentID)
		}
		end := len(request)
		if index+1 < len(matches) {
			end = matches[index+1][0]
		}
		prompt := strings.TrimSpace(request[match[1]:end])
		prompt = strings.TrimLeft(prompt, " ,:;-\n\t")
		if prompt == "" {
			prompt = "Continue the requested workflow and report the result."
		}
		stageID, err := randomCoordinatorID()
		if err != nil {
			return Draft{}, err
		}
		stages = append(stages, Stage{ID: stageID, Position: len(stages), AgentID: agentID, Title: stageTitle(agentID, prompt), Prompt: prompt, Status: StageQueued})
	}
	if len(stages) == 0 {
		agentID := ""
		for _, candidate := range []string{"codex", "claude", "opencode", "gemini", "aider"} {
			if _, ok := available[candidate]; ok {
				agentID = candidate
				break
			}
		}
		if agentID == "" {
			return Draft{}, errors.New("no supported AI agent is available on this computer")
		}
		stageID, err := randomCoordinatorID()
		if err != nil {
			return Draft{}, err
		}
		stages = append(stages, Stage{ID: stageID, Position: 0, AgentID: agentID, Title: stageTitle(agentID, request), Prompt: request, Status: StageQueued})
	}
	return Draft{Request: request, Cwd: input.Cwd, Stages: stages}, nil
}

func stageTitle(agentID, prompt string) string {
	title := strings.Join(strings.Fields(prompt), " ")
	runes := []rune(title)
	if len(runes) > 72 {
		title = string(runes[:69]) + "..."
	}
	return "@" + agentID + " · " + title
}

func randomCoordinatorID() (string, error) {
	value := make([]byte, 12)
	if _, err := rand.Read(value); err != nil {
		return "", fmt.Errorf("generate coordinator ID: %w", err)
	}
	return hex.EncodeToString(value), nil
}
