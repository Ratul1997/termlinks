package coordinator

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"termlinks/backend/internal/session"
)

func TestStoreIsPrivateAndRecoversInterruptedWorkflow(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "private", "workflows.db")
	store, err := OpenStore(path)
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" {
		for _, databaseFile := range []string{path, path + "-wal", path + "-shm"} {
			info, err := os.Stat(databaseFile)
			if err != nil {
				t.Fatal(err)
			}
			if info.Mode().Perm() != 0o600 {
				t.Fatalf("database permissions for %s = %o, want 600", filepath.Base(databaseFile), info.Mode().Perm())
			}
		}
	}
	now := time.Now().UTC()
	workflow := Workflow{ID: strings.Repeat("a", 24), Request: "test", Cwd: root, Status: WorkflowQueued, CreatedAt: now, UpdatedAt: now, Stages: []Stage{{ID: strings.Repeat("b", 24), Position: 0, AgentID: "codex", Title: "test", Prompt: "test", Status: StageQueued}}}
	if err := store.CreateWorkflow(context.Background(), workflow); err != nil {
		t.Fatal(err)
	}
	if err := store.StartStage(context.Background(), workflow.ID, workflow.Stages[0].ID, strings.Repeat("c", 32)); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	reopened, err := OpenStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	recovered, err := reopened.Workflow(context.Background(), workflow.ID)
	if err != nil {
		t.Fatal(err)
	}
	if recovered.Status != WorkflowInterrupted || recovered.Stages[0].Status != StageInterrupted {
		t.Fatalf("recovered states = %s/%s, want interrupted", recovered.Status, recovered.Stages[0].Status)
	}
	listed, err := reopened.ListWorkflows(context.Background(), 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(listed) != 1 || len(listed[0].Stages) != 1 {
		t.Fatalf("listed workflows = %#v", listed)
	}
	if listed[0].MessageCount != 1 || listed[0].LastMessage == nil || listed[0].LastMessage.SenderType != MessageSenderHuman {
		t.Fatalf("initial team-room summary = %#v", listed[0])
	}
}

func TestRoomMessagesAreLocalDurableAndRepliesStayInRoom(t *testing.T) {
	root := t.TempDir()
	store, err := OpenStore(filepath.Join(root, "workflows.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	now := time.Now().UTC()
	first := Workflow{ID: strings.Repeat("a", 24), Request: "build the feature", Cwd: root, Status: WorkflowQueued, CreatedAt: now, UpdatedAt: now,
		Stages: []Stage{{ID: strings.Repeat("b", 24), Position: 0, AgentID: "codex", Title: "plan", Prompt: "plan", Status: StageQueued}}}
	second := Workflow{ID: strings.Repeat("c", 24), Request: "other room", Cwd: root, Status: WorkflowQueued, CreatedAt: now, UpdatedAt: now,
		Stages: []Stage{{ID: strings.Repeat("d", 24), Position: 0, AgentID: "claude", Title: "review", Prompt: "review", Status: StageQueued}}}
	if err := store.CreateWorkflow(context.Background(), first); err != nil {
		t.Fatal(err)
	}
	if err := store.CreateWorkflow(context.Background(), second); err != nil {
		t.Fatal(err)
	}
	messages, err := store.Messages(context.Background(), first.ID, 10)
	if err != nil || len(messages) != 1 || messages[0].Body != first.Request || messages[0].Kind != MessageKindTask {
		t.Fatalf("initial messages = %#v, %v", messages, err)
	}
	replyID := messages[0].ID
	reply, err := store.AddMessage(context.Background(), RoomMessage{WorkflowID: first.ID, SenderID: "codex", SenderType: MessageSenderAgent,
		Recipient: MessageRecipientHuman, Kind: MessageKindQuestion, Body: "Need a decision", ReplyTo: &replyID})
	if err != nil {
		t.Fatal(err)
	}
	if reply.ID <= replyID || reply.ReplyTo == nil || *reply.ReplyTo != replyID {
		t.Fatalf("reply = %#v", reply)
	}
	otherMessages, err := store.Messages(context.Background(), second.ID, 10)
	if err != nil || len(otherMessages) != 1 {
		t.Fatalf("other room messages = %#v, %v", otherMessages, err)
	}
	crossRoomReply := otherMessages[0].ID
	if _, err := store.AddMessage(context.Background(), RoomMessage{WorkflowID: first.ID, SenderID: MessageSenderHuman, SenderType: MessageSenderHuman,
		Recipient: MessageRecipientTeam, Kind: MessageKindMessage, Body: "wrong thread", ReplyTo: &crossRoomReply}); err == nil || !strings.Contains(err.Error(), "another team room") {
		t.Fatalf("cross-room reply error = %v", err)
	}
	loaded, err := store.Workflow(context.Background(), first.ID)
	if err != nil || loaded.MessageCount != 2 || len(loaded.Messages) != 2 || loaded.LastMessage == nil || loaded.LastMessage.ID != reply.ID {
		t.Fatalf("loaded room = %#v, %v", loaded, err)
	}
}

func TestCompileCreatesDirectedStagesAndRejectsUnavailableAgent(t *testing.T) {
	agents := []Agent{{ID: "codex", Available: true, Runnable: true}, {ID: "claude", Available: true, Runnable: true}}
	draft, err := Compile(CreateInput{Request: "@codex inspect and plan, then @claude implement the plan", Cwd: "/tmp"}, agents)
	if err != nil {
		t.Fatal(err)
	}
	if len(draft.Stages) != 2 {
		t.Fatalf("stages = %d, want 2", len(draft.Stages))
	}
	if draft.Stages[0].AgentID != "codex" || draft.Stages[1].AgentID != "claude" {
		t.Fatalf("unexpected stage order: %#v", draft.Stages)
	}
	if _, err := Compile(CreateInput{Request: "@gemini implement", Cwd: "/tmp"}, agents); err == nil || !strings.Contains(err.Error(), "not installed") {
		t.Fatalf("unavailable agent error = %v", err)
	}
}

func TestCodexCommandSupportsNonGitWorkspaces(t *testing.T) {
	command, err := agentCommand(Agent{ID: "codex", Command: "/usr/local/bin/codex"})
	if err != nil {
		t.Fatal(err)
	}
	want := "/usr/local/bin/codex exec --json --sandbox workspace-write --skip-git-repo-check -"
	if strings.Join(command, " ") != want {
		t.Fatalf("command = %q, want %q", strings.Join(command, " "), want)
	}
}

func TestManagerRunsWorkflowWithoutShellInterpolation(t *testing.T) {
	root := t.TempDir()
	store, err := OpenStore(filepath.Join(root, "workflows.db"))
	if err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(root, "must-not-exist")
	fakeAgent := filepath.Join(root, "fake-codex")
	if err := os.WriteFile(fakeAgent, []byte("#!/bin/sh\ncat\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := store.ReplaceAgents(context.Background(), []Agent{{ID: "codex", Name: "Fake Codex", Command: fakeAgent, Available: true, Runnable: true, AuthStatus: "authenticated", Transport: "structured-cli", DetectedAt: time.Now().UTC()}}); err != nil {
		t.Fatal(err)
	}
	manager := NewManager(store, session.NewManager(), slog.New(slog.NewTextHandler(os.Stderr, nil)))
	defer manager.Close()
	workflow, err := manager.Create(context.Background(), CreateInput{Request: "@codex print safe; touch " + marker, Cwd: root})
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		workflow, err = manager.Get(context.Background(), workflow.ID)
		if err != nil {
			t.Fatal(err)
		}
		if workflow.Status == WorkflowCompleted {
			break
		}
		if workflow.Status == WorkflowFailed {
			t.Fatalf("workflow failed: %#v", workflow.Stages)
		}
		if time.Now().After(deadline) {
			t.Fatal("workflow did not complete")
		}
		time.Sleep(20 * time.Millisecond)
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatal("prompt was interpreted by a shell")
	}
	if len(workflow.Stages) != 1 || !strings.Contains(workflow.Stages[0].Output, "touch") {
		t.Fatalf("agent did not receive literal prompt: %#v", workflow.Stages)
	}
}

func TestHumanTeamMessageQueuesAVisibleFollowUpTurn(t *testing.T) {
	root := t.TempDir()
	store, err := OpenStore(filepath.Join(root, "workflows.db"))
	if err != nil {
		t.Fatal(err)
	}
	fakeAgent := filepath.Join(root, "fake-codex")
	if err := os.WriteFile(fakeAgent, []byte("#!/bin/sh\ncat\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := store.ReplaceAgents(context.Background(), []Agent{{ID: "codex", Name: "Fake Codex", Command: fakeAgent, Available: true, Runnable: true, AuthStatus: "authenticated", Transport: "structured-cli", DetectedAt: time.Now().UTC()}}); err != nil {
		t.Fatal(err)
	}
	manager := NewManager(store, session.NewManager(), slog.New(slog.NewTextHandler(os.Stderr, nil)))
	defer manager.Close()
	workflow, err := manager.Create(context.Background(), CreateInput{Request: "@codex answer first", Cwd: root})
	if err != nil {
		t.Fatal(err)
	}
	waitForWorkflowStatus(t, manager, workflow.ID, WorkflowCompleted, 1)
	teamMessage, err := manager.SendMessage(context.Background(), workflow.ID, MessageInput{Body: "Shared note for every teammate", To: "@team"})
	if err != nil || teamMessage.Recipient != MessageRecipientTeam {
		t.Fatalf("team message = %#v, %v", teamMessage, err)
	}
	afterTeamMessage, err := manager.Get(context.Background(), workflow.ID)
	if err != nil || len(afterTeamMessage.Stages) != 1 || afterTeamMessage.Status != WorkflowCompleted {
		t.Fatalf("team message unexpectedly scheduled work: %#v, %v", afterTeamMessage, err)
	}
	message, err := manager.SendMessage(context.Background(), workflow.ID, MessageInput{Body: "Please reconsider the edge case", To: "@codex"})
	if err != nil {
		t.Fatal(err)
	}
	if message.SenderType != MessageSenderHuman || message.Recipient != "codex" {
		t.Fatalf("human message = %#v", message)
	}
	updated := waitForWorkflowStatus(t, manager, workflow.ID, WorkflowCompleted, 2)
	if len(updated.Stages) != 2 || !strings.Contains(updated.Stages[1].Output, "Please reconsider the edge case") {
		t.Fatalf("follow-up stages = %#v", updated.Stages)
	}
	if updated.MessageCount < 7 {
		t.Fatalf("message count = %d, want task/status/agent/team/human/status/agent", updated.MessageCount)
	}
	if _, err := manager.SendMessage(context.Background(), workflow.ID, MessageInput{Body: "hello", To: "@claude"}); err == nil || !strings.Contains(err.Error(), "not a participant") {
		t.Fatalf("unknown participant error = %v", err)
	}
}

func TestAgentRoomMessageExtractsStructuredProviderOutput(t *testing.T) {
	codex := "{\"type\":\"item.completed\",\"item\":{\"type\":\"agent_message\",\"text\":\"Plan is ready for @claude\"}}\n"
	if got := agentRoomMessage(codex); got != "Plan is ready for @claude" {
		t.Fatalf("Codex room message = %q", got)
	}
	claude := "{\"type\":\"assistant\",\"message\":{\"content\":[{\"type\":\"text\",\"text\":\"Should we rotate it, @human?\"}]}}\n"
	if got := agentRoomMessage(claude); got != "Should we rotate it, @human?" {
		t.Fatalf("Claude room message = %q", got)
	}
	workflow := Workflow{Stages: []Stage{{AgentID: "codex"}, {AgentID: "claude"}}}
	if recipient, kind := agentMessageTarget(workflow, "Please review this @claude"); recipient != "claude" || kind != MessageKindHandoff {
		t.Fatalf("handoff target = %q/%q", recipient, kind)
	}
	if recipient, kind := agentMessageTarget(workflow, "I need @human to decide"); recipient != MessageRecipientHuman || kind != MessageKindMessage {
		t.Fatalf("human statement target = %q/%q", recipient, kind)
	}
	if recipient, kind := agentMessageTarget(workflow, "Can @human decide this?"); recipient != MessageRecipientHuman || kind != MessageKindQuestion {
		t.Fatalf("human target = %q/%q", recipient, kind)
	}
	if recipient, kind := agentMessageTarget(workflow, "@human Done. @claude Please review it."); recipient != "claude" || kind != MessageKindHandoff {
		t.Fatalf("multi-party handoff target = %q/%q", recipient, kind)
	}
	longUnicode := strings.Repeat("🙂", 20<<10)
	truncated := truncateRoomMessage(longUnicode)
	if !utf8.ValidString(truncated) || !strings.HasSuffix(truncated, "[Message truncated by Termlinks.]") {
		t.Fatalf("truncated Unicode message is invalid: valid=%v suffix=%v", utf8.ValidString(truncated), strings.HasSuffix(truncated, "[Message truncated by Termlinks.]"))
	}
}

func waitForWorkflowStatus(t *testing.T, manager *Manager, workflowID, status string, minimumStages int) Workflow {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		workflow, err := manager.Get(context.Background(), workflowID)
		if err != nil {
			t.Fatal(err)
		}
		if workflow.Status == status && len(workflow.Stages) >= minimumStages {
			return workflow
		}
		if workflow.Status == WorkflowFailed {
			t.Fatalf("workflow failed: %#v", workflow.Stages)
		}
		if time.Now().After(deadline) {
			t.Fatalf("workflow status = %s with %d stages, want %s with at least %d", workflow.Status, len(workflow.Stages), status, minimumStages)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func TestWorkspaceRequiresExistingAbsoluteDirectory(t *testing.T) {
	if _, err := validateWorkspace("~/secret"); err == nil {
		t.Fatal("tilde path unexpectedly accepted")
	}
	if _, err := validateWorkspace(filepath.Join(t.TempDir(), "missing")); err == nil {
		t.Fatal("missing path unexpectedly accepted")
	}
	valid := t.TempDir()
	want, err := filepath.EvalSymlinks(valid)
	if err != nil {
		t.Fatal(err)
	}
	resolved, err := validateWorkspace(valid)
	if err != nil || resolved != want {
		t.Fatalf("valid workspace = %q, %v", resolved, err)
	}
}

func TestManagerSerializesRepositoriesAndCapsParallelWork(t *testing.T) {
	root := t.TempDir()
	store, err := OpenStore(filepath.Join(root, "workflows.db"))
	if err != nil {
		t.Fatal(err)
	}
	fakeAgent := filepath.Join(root, "slow-codex")
	if err := os.WriteFile(fakeAgent, []byte("#!/bin/sh\ncat\nsleep 10\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := store.ReplaceAgents(context.Background(), []Agent{{ID: "codex", Name: "Fake Codex", Command: fakeAgent, Available: true, Runnable: true, AuthStatus: "authenticated", Transport: "structured-cli", DetectedAt: time.Now().UTC()}}); err != nil {
		t.Fatal(err)
	}
	manager := NewManager(store, session.NewManager(), slog.New(slog.NewTextHandler(os.Stderr, nil)))
	defer manager.Close()
	firstRoot, secondRoot, thirdRoot := t.TempDir(), t.TempDir(), t.TempDir()
	first, err := manager.Create(context.Background(), CreateInput{Request: "@codex first", Cwd: firstRoot})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Create(context.Background(), CreateInput{Request: "@codex collision", Cwd: firstRoot}); err == nil || !strings.Contains(err.Error(), "already active in this project") {
		t.Fatalf("same-project result = %v", err)
	}
	second, err := manager.Create(context.Background(), CreateInput{Request: "@codex second", Cwd: secondRoot})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Create(context.Background(), CreateInput{Request: "@codex third", Cwd: thirdRoot}); err == nil || !strings.Contains(err.Error(), "two AI workflows") {
		t.Fatalf("third-workflow result = %v", err)
	}
	if err := manager.Cancel(context.Background(), first.ID); err != nil {
		t.Fatal(err)
	}
	if err := manager.Cancel(context.Background(), second.ID); err != nil {
		t.Fatal(err)
	}
}
