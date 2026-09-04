package coordinator

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"termlinks/backend/internal/session"
)

const maxStoredStageOutput = 96 << 10

type Manager struct {
	store      *Store
	sessions   *session.Manager
	logger     *slog.Logger
	mu         sync.Mutex
	wg         sync.WaitGroup
	running    map[string]*runningWorkflow
	workspaces map[string]string
}

type runningWorkflow struct {
	cancel  context.CancelFunc
	current *session.Session
}

func NewManager(store *Store, sessions *session.Manager, logger *slog.Logger) *Manager {
	return &Manager{
		store: store, sessions: sessions, logger: logger,
		running: make(map[string]*runningWorkflow), workspaces: make(map[string]string),
	}
}

func (m *Manager) Close() error {
	m.mu.Lock()
	for _, running := range m.running {
		running.cancel()
		if running.current != nil {
			_ = running.current.Stop()
		}
	}
	m.mu.Unlock()
	m.wg.Wait()
	return m.store.Close()
}

func (m *Manager) RefreshAgents(ctx context.Context) ([]Agent, error) {
	agents := DiscoverAgents(ctx)
	if err := m.store.ReplaceAgents(ctx, agents); err != nil {
		return nil, err
	}
	return agents, nil
}

func (m *Manager) Agents(ctx context.Context) ([]Agent, error) {
	agents, err := m.store.Agents(ctx)
	if err != nil {
		return nil, err
	}
	if len(agents) == 0 {
		return m.RefreshAgents(ctx)
	}
	return agents, nil
}

func (m *Manager) Compile(ctx context.Context, input CreateInput) (Draft, error) {
	cwd, err := validateWorkspace(input.Cwd)
	if err != nil {
		return Draft{}, err
	}
	input.Cwd = cwd
	agents, err := m.Agents(ctx)
	if err != nil {
		return Draft{}, err
	}
	return Compile(input, agents)
}

func (m *Manager) Create(ctx context.Context, input CreateInput) (Workflow, error) {
	draft, err := m.Compile(ctx, input)
	if err != nil {
		return Workflow{}, err
	}
	id, err := randomCoordinatorID()
	if err != nil {
		return Workflow{}, err
	}
	now := time.Now().UTC()
	workflow := Workflow{ID: id, Request: draft.Request, Cwd: draft.Cwd, Status: WorkflowQueued, CreatedAt: now, UpdatedAt: now, Stages: draft.Stages}
	for index := range workflow.Stages {
		workflow.Stages[index].WorkflowID = id
	}
	workspace := workspaceKey(ctx, workflow.Cwd)
	runCtx, cancel := context.WithCancel(context.Background())
	m.mu.Lock()
	if m.workspaces[workspace] != "" {
		m.mu.Unlock()
		cancel()
		return Workflow{}, errors.New("another AI workflow is already active in this project")
	}
	if len(m.running) >= 2 {
		m.mu.Unlock()
		cancel()
		return Workflow{}, errors.New("two AI workflows are already active; wait for one to finish")
	}
	m.workspaces[workspace] = id
	m.running[id] = &runningWorkflow{cancel: cancel}
	m.mu.Unlock()
	if err := m.store.CreateWorkflow(ctx, workflow); err != nil {
		m.mu.Lock()
		delete(m.running, id)
		delete(m.workspaces, workspace)
		m.mu.Unlock()
		cancel()
		return Workflow{}, err
	}
	m.wg.Add(1)
	go func() {
		defer m.wg.Done()
		m.run(runCtx, id, workspace)
	}()
	return workflow, nil
}

func (m *Manager) List(ctx context.Context) ([]Workflow, error) {
	return m.store.ListWorkflows(ctx, 50)
}
func (m *Manager) Get(ctx context.Context, id string) (Workflow, error) {
	return m.store.Workflow(ctx, id)
}
func (m *Manager) Events(ctx context.Context, id string, after int64) ([]Event, error) {
	return m.store.Events(ctx, id, after)
}

func (m *Manager) WorkspaceSuggestions(ctx context.Context) ([]WorkspaceSuggestion, error) {
	items, err := m.store.WorkspaceSuggestions(ctx, 30)
	if err != nil {
		return nil, err
	}
	seen := make(map[string]bool)
	result := make([]WorkspaceSuggestion, 0, len(items)+len(m.sessions.List()))
	for _, info := range m.sessions.List() {
		if seen[info.Cwd] {
			continue
		}
		seen[info.Cwd] = true
		result = append(result, WorkspaceSuggestion{Path: info.Cwd, Name: filepath.Base(info.Cwd), LastUsedAt: info.StartedAt})
	}
	for _, item := range items {
		if !seen[item.Path] {
			seen[item.Path] = true
			result = append(result, item)
		}
	}
	return result, nil
}

func (m *Manager) Cancel(ctx context.Context, id string) error {
	workflow, err := m.store.Workflow(ctx, id)
	if err != nil {
		return err
	}
	if workflow.Status != WorkflowQueued && workflow.Status != WorkflowRunning {
		return errors.New("workflow is not active")
	}
	m.mu.Lock()
	running := m.running[id]
	if running != nil {
		running.cancel()
		if running.current != nil {
			_ = running.current.Stop()
		}
	}
	m.mu.Unlock()
	if err := m.store.CancelQueuedStages(ctx, id); err != nil {
		return err
	}
	return m.store.SetWorkflowStatus(ctx, id, WorkflowCancelled)
}

func (m *Manager) SendInput(ctx context.Context, workflowID, stageID, input string) error {
	if len(input) == 0 || len(input) > 48<<10 {
		return errors.New("input must contain between 1 byte and 48 KiB")
	}
	workflow, err := m.store.Workflow(ctx, workflowID)
	if err != nil {
		return err
	}
	var target *Stage
	for index := range workflow.Stages {
		if workflow.Stages[index].ID == stageID {
			target = &workflow.Stages[index]
			break
		}
	}
	if target == nil {
		return sql.ErrNoRows
	}
	if target.Status != StageRunning || target.SessionID == "" {
		return errors.New("that agent stage is not accepting input")
	}
	current, ok := m.sessions.Get(target.SessionID)
	if !ok || !current.Info().Running {
		return errors.New("agent terminal is no longer running")
	}
	return current.Write([]byte(input + "\n"))
}

func (m *Manager) run(ctx context.Context, workflowID, workspace string) {
	defer func() {
		m.mu.Lock()
		delete(m.running, workflowID)
		if m.workspaces[workspace] == workflowID {
			delete(m.workspaces, workspace)
		}
		m.mu.Unlock()
	}()
	workflow, err := m.store.Workflow(ctx, workflowID)
	if err != nil {
		m.logger.Error("load queued AI workflow", "workflow", workflowID, "error", err)
		return
	}
	var prior []string
	for _, stage := range workflow.Stages {
		if ctx.Err() != nil {
			return
		}
		output, err := m.runStage(ctx, workflow, stage, prior)
		if err != nil {
			if ctx.Err() != nil {
				_ = m.store.FinishStage(context.Background(), workflow.ID, stage.ID, StageCancelled, output, "Workflow cancelled")
				return
			}
			_ = m.store.SetWorkflowStatus(context.Background(), workflow.ID, WorkflowFailed)
			return
		}
		prior = append(prior, output)
	}
	_ = m.store.SetWorkflowStatus(context.Background(), workflow.ID, WorkflowCompleted)
}

func (m *Manager) runStage(ctx context.Context, workflow Workflow, stage Stage, prior []string) (string, error) {
	agents, err := m.store.Agents(ctx)
	if err != nil {
		return "", err
	}
	var agent Agent
	for _, candidate := range agents {
		if candidate.ID == stage.AgentID {
			agent = candidate
			break
		}
	}
	if !agent.Available || agent.Command == "" {
		err := fmt.Errorf("@%s became unavailable", stage.AgentID)
		_ = m.store.FinishStage(context.Background(), workflow.ID, stage.ID, StageFailed, "", err.Error())
		return "", err
	}
	prompt := stage.Prompt
	if len(prior) > 0 {
		contextText := strings.Join(prior, "\n\n--- Previous stage ---\n")
		if len(contextText) > 64<<10 {
			contextText = contextText[len(contextText)-(64<<10):]
		}
		prompt += "\n\nUse these results from earlier workflow stages as context:\n\n" + contextText
	}
	prompt, err = safePrompt(prompt)
	if err != nil {
		_ = m.store.FinishStage(context.Background(), workflow.ID, stage.ID, StageFailed, "", err.Error())
		return "", err
	}
	command, err := agentCommand(agent)
	if err != nil {
		_ = m.store.FinishStage(context.Background(), workflow.ID, stage.ID, StageFailed, "", err.Error())
		return "", err
	}
	current, err := m.sessions.Start(session.StartOptions{Name: "AI · " + agent.Name, Command: command, Cwd: workflow.Cwd, Rows: 34, Cols: 120})
	if err != nil {
		_ = m.store.FinishStage(context.Background(), workflow.ID, stage.ID, StageFailed, "", err.Error())
		return "", err
	}
	m.mu.Lock()
	if running := m.running[workflow.ID]; running != nil {
		running.current = current
	}
	m.mu.Unlock()
	if err := m.store.StartStage(context.Background(), workflow.ID, stage.ID, current.Info().ID); err != nil {
		_ = current.Stop()
		return "", err
	}
	if err := writePrompt(current, stage.AgentID, prompt); err != nil {
		_ = current.Stop()
		_ = m.store.FinishStage(context.Background(), workflow.ID, stage.ID, StageFailed, "", "Could not deliver the agent prompt")
		return "", err
	}
	select {
	case <-ctx.Done():
		_ = current.Stop()
		<-current.Done()
	case <-current.Done():
	}
	output, _, cancel := current.Subscribe()
	cancel()
	if len(output) > maxStoredStageOutput {
		output = output[len(output)-maxStoredStageOutput:]
	}
	storedOutput := safeStoredOutput(string(output))
	info := current.Info()
	if ctx.Err() != nil {
		_ = m.store.FinishStage(context.Background(), workflow.ID, stage.ID, StageCancelled, storedOutput, "Workflow cancelled")
		return storedOutput, ctx.Err()
	}
	if info.ExitCode == nil || *info.ExitCode != 0 {
		message := "Agent exited without completing"
		if info.ExitCode != nil {
			message = fmt.Sprintf("Agent exited with code %d", *info.ExitCode)
		}
		_ = m.store.FinishStage(context.Background(), workflow.ID, stage.ID, StageFailed, storedOutput, message)
		return storedOutput, errors.New(message)
	}
	if err := m.store.FinishStage(context.Background(), workflow.ID, stage.ID, StageCompleted, storedOutput, "Agent stage completed"); err != nil {
		return storedOutput, err
	}
	return storedOutput, nil
}

func agentCommand(agent Agent) ([]string, error) {
	switch agent.ID {
	case "codex":
		return []string{agent.Command, "exec", "--json", "--sandbox", "workspace-write", "--skip-git-repo-check", "-"}, nil
	case "claude":
		executable, err := os.Executable()
		if err != nil {
			return nil, fmt.Errorf("resolve Termlinks executable: %w", err)
		}
		return []string{executable, "__agent-stdio", "claude", agent.Command}, nil
	default:
		return nil, fmt.Errorf("unsupported agent %q", agent.ID)
	}
}

func safePrompt(prompt string) (string, error) {
	if strings.IndexByte(prompt, 0) >= 0 {
		return "", errors.New("agent prompt contains an unsupported NUL byte")
	}
	return strings.Map(func(character rune) rune {
		if character == '\n' || character == '\t' || character >= 0x20 {
			return character
		}
		return -1
	}, prompt), nil
}

func safeStoredOutput(output string) string {
	return strings.Map(func(character rune) rune {
		if character == '\n' || character == '\t' || character >= 0x20 && character != 0x7f {
			return character
		}
		return -1
	}, output)
}

func writePrompt(current *session.Session, agentID, prompt string) error {
	data := []byte(prompt + "\n")
	if agentID == "claude" {
		data = append([]byte(fmt.Sprintf("TERMLINKS_AGENT_PROMPT_V1 %d\n", len(data))), data...)
	}
	for len(data) > 0 {
		length := len(data)
		if length > 32<<10 {
			length = 32 << 10
		}
		if err := current.Write(data[:length]); err != nil {
			return err
		}
		data = data[length:]
	}
	if agentID == "claude" {
		return nil
	}
	return current.Write([]byte{4})
}

func validateWorkspace(value string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return "", errors.New("starting directory is required")
	}
	if strings.HasPrefix(value, "~") {
		return "", errors.New("use an absolute directory path instead of ~")
	}
	abs, err := filepath.Abs(value)
	if err != nil {
		return "", errors.New("could not resolve that directory")
	}
	clean := filepath.Clean(abs)
	info, err := os.Stat(clean)
	if err != nil || !info.IsDir() {
		return "", errors.New("starting directory is not accessible")
	}
	resolved, err := filepath.EvalSymlinks(clean)
	if err != nil {
		return "", errors.New("could not resolve that directory")
	}
	return resolved, nil
}

func workspaceKey(ctx context.Context, cwd string) string {
	commandContext, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	output, err := exec.CommandContext(commandContext, "git", "-C", cwd, "rev-parse", "--show-toplevel").Output()
	if err == nil {
		root := strings.TrimSpace(string(output))
		if resolved, resolveErr := filepath.EvalSymlinks(root); resolveErr == nil {
			return resolved
		}
	}
	return cwd
}
