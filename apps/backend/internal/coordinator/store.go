package coordinator

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	_ "modernc.org/sqlite"
)

const schemaVersion = 1

type Store struct {
	db   *sql.DB
	path string
}

func OpenStore(path string) (*Store, error) {
	if path == "" || !filepath.IsAbs(path) {
		return nil, errors.New("workflow database path must be absolute")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, fmt.Errorf("create workflow state directory: %w", err)
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("open workflow database: %w", err)
	}
	db.SetMaxOpenConns(1)
	store := &Store{db: db, path: path}
	if err := store.initialize(context.Background()); err != nil {
		_ = db.Close()
		return nil, err
	}
	if err := store.secureFiles(); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("secure workflow database: %w", err)
	}
	return store, nil
}

func (s *Store) Close() error {
	_, _ = s.db.Exec(`PRAGMA wal_checkpoint(TRUNCATE)`)
	err := s.db.Close()
	if secureErr := s.secureFiles(); err == nil {
		err = secureErr
	}
	return err
}

func (s *Store) secureFiles() error {
	for _, path := range []string{s.path, s.path + "-wal", s.path + "-shm"} {
		if err := os.Chmod(path, 0o600); err != nil && !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("secure workflow database file: %w", err)
		}
	}
	return nil
}

func (s *Store) initialize(ctx context.Context) error {
	statements := []string{
		`PRAGMA foreign_keys = ON`,
		`PRAGMA journal_mode = WAL`,
		`PRAGMA synchronous = NORMAL`,
		`PRAGMA busy_timeout = 5000`,
		`CREATE TABLE IF NOT EXISTS schema_meta (version INTEGER NOT NULL)`,
		`INSERT INTO schema_meta(version) SELECT 0 WHERE NOT EXISTS (SELECT 1 FROM schema_meta)`,
		`CREATE TABLE IF NOT EXISTS agents (
			id TEXT PRIMARY KEY, name TEXT NOT NULL, command TEXT NOT NULL,
			version TEXT NOT NULL, available INTEGER NOT NULL, auth_status TEXT NOT NULL,
			transport TEXT NOT NULL, detected_at TEXT NOT NULL
		)`,
		`CREATE TABLE IF NOT EXISTS projects (
			path TEXT PRIMARY KEY, name TEXT NOT NULL, last_used_at TEXT NOT NULL
		)`,
		`CREATE TABLE IF NOT EXISTS workflows (
			id TEXT PRIMARY KEY, request TEXT NOT NULL, cwd TEXT NOT NULL, status TEXT NOT NULL,
			created_at TEXT NOT NULL, updated_at TEXT NOT NULL
		)`,
		`CREATE TABLE IF NOT EXISTS stages (
			id TEXT PRIMARY KEY, workflow_id TEXT NOT NULL REFERENCES workflows(id) ON DELETE CASCADE,
			position INTEGER NOT NULL, agent_id TEXT NOT NULL, title TEXT NOT NULL, prompt TEXT NOT NULL,
			status TEXT NOT NULL, session_id TEXT NOT NULL DEFAULT '', output TEXT NOT NULL DEFAULT '',
			error TEXT NOT NULL DEFAULT '', started_at TEXT, ended_at TEXT,
			UNIQUE(workflow_id, position)
		)`,
		`CREATE TABLE IF NOT EXISTS events (
			id INTEGER PRIMARY KEY AUTOINCREMENT, workflow_id TEXT NOT NULL REFERENCES workflows(id) ON DELETE CASCADE,
			stage_id TEXT NOT NULL DEFAULT '', type TEXT NOT NULL, message TEXT NOT NULL, created_at TEXT NOT NULL
		)`,
		`CREATE INDEX IF NOT EXISTS idx_workflows_updated ON workflows(updated_at DESC)`,
		`CREATE INDEX IF NOT EXISTS idx_events_workflow ON events(workflow_id, id)`,
	}
	for _, statement := range statements {
		if _, err := s.db.ExecContext(ctx, statement); err != nil {
			return fmt.Errorf("initialize workflow database: %w", err)
		}
	}
	if _, err := s.db.ExecContext(ctx, `UPDATE schema_meta SET version = ?`, schemaVersion); err != nil {
		return fmt.Errorf("record workflow schema version: %w", err)
	}
	return s.markInterrupted(ctx)
}

func (s *Store) markInterrupted(ctx context.Context) error {
	now := encodeTime(time.Now().UTC())
	if _, err := s.db.ExecContext(ctx, `UPDATE stages SET status = ?, error = ?, ended_at = ? WHERE status = ?`, StageInterrupted, "Termlinks restarted while this agent was running", now, StageRunning); err != nil {
		return fmt.Errorf("recover interrupted stages: %w", err)
	}
	if _, err := s.db.ExecContext(ctx, `UPDATE workflows SET status = ?, updated_at = ? WHERE status IN (?, ?)`, WorkflowInterrupted, now, WorkflowRunning, WorkflowQueued); err != nil {
		return fmt.Errorf("recover interrupted workflows: %w", err)
	}
	return nil
}

func (s *Store) ReplaceAgents(ctx context.Context, agents []Agent) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `UPDATE agents SET available = 0, auth_status = 'unknown'`); err != nil {
		return err
	}
	for _, agent := range agents {
		_, err := tx.ExecContext(ctx, `INSERT INTO agents(id,name,command,version,available,auth_status,transport,detected_at)
			VALUES(?,?,?,?,?,?,?,?) ON CONFLICT(id) DO UPDATE SET name=excluded.name,command=excluded.command,
			version=excluded.version,available=excluded.available,auth_status=excluded.auth_status,
			transport=excluded.transport,detected_at=excluded.detected_at`, agent.ID, agent.Name, agent.Command,
			agent.Version, boolInt(agent.Available), agent.AuthStatus, agent.Transport, encodeTime(agent.DetectedAt))
		if err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s *Store) Agents(ctx context.Context) ([]Agent, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id,name,command,version,available,auth_status,transport,detected_at FROM agents ORDER BY available DESC, name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	agents := make([]Agent, 0)
	for rows.Next() {
		var agent Agent
		var available int
		var detected string
		if err := rows.Scan(&agent.ID, &agent.Name, &agent.Command, &agent.Version, &available, &agent.AuthStatus, &agent.Transport, &detected); err != nil {
			return nil, err
		}
		agent.Available = available != 0
		agent.Runnable = agent.Available && supportsExecution(agent.ID)
		agent.DetectedAt, _ = decodeTime(detected)
		agents = append(agents, agent)
	}
	return agents, rows.Err()
}

func (s *Store) CreateWorkflow(ctx context.Context, workflow Workflow) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	_, err = tx.ExecContext(ctx, `INSERT INTO workflows(id,request,cwd,status,created_at,updated_at) VALUES(?,?,?,?,?,?)`, workflow.ID, workflow.Request, workflow.Cwd, workflow.Status, encodeTime(workflow.CreatedAt), encodeTime(workflow.UpdatedAt))
	if err != nil {
		return err
	}
	for _, stage := range workflow.Stages {
		_, err = tx.ExecContext(ctx, `INSERT INTO stages(id,workflow_id,position,agent_id,title,prompt,status) VALUES(?,?,?,?,?,?,?)`, stage.ID, workflow.ID, stage.Position, stage.AgentID, stage.Title, stage.Prompt, stage.Status)
		if err != nil {
			return err
		}
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO projects(path,name,last_used_at) VALUES(?,?,?) ON CONFLICT(path) DO UPDATE SET last_used_at=excluded.last_used_at`, workflow.Cwd, filepath.Base(workflow.Cwd), encodeTime(workflow.CreatedAt))
	if err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO events(workflow_id,type,message,created_at) VALUES(?,?,?,?)`, workflow.ID, "workflow.created", "Workflow queued", encodeTime(workflow.CreatedAt)); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) ListWorkflows(ctx context.Context, limit int) ([]Workflow, error) {
	if limit < 1 || limit > 200 {
		limit = 50
	}
	rows, err := s.db.QueryContext(ctx, `SELECT id,request,cwd,status,created_at,updated_at FROM workflows ORDER BY updated_at DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	workflows := make([]Workflow, 0)
	for rows.Next() {
		workflow, err := scanWorkflow(rows)
		if err != nil {
			rows.Close()
			return nil, err
		}
		workflows = append(workflows, workflow)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, err
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	for index := range workflows {
		stages, err := s.stages(ctx, workflows[index].ID)
		if err != nil {
			return nil, err
		}
		workflows[index].Stages = stages
	}
	return workflows, nil
}

func (s *Store) Workflow(ctx context.Context, id string) (Workflow, error) {
	row := s.db.QueryRowContext(ctx, `SELECT id,request,cwd,status,created_at,updated_at FROM workflows WHERE id=?`, id)
	workflow, err := scanWorkflow(row)
	if err != nil {
		return Workflow{}, err
	}
	workflow.Stages, err = s.stages(ctx, id)
	return workflow, err
}

type rowScanner interface{ Scan(...any) error }

func scanWorkflow(row rowScanner) (Workflow, error) {
	var workflow Workflow
	var created, updated string
	if err := row.Scan(&workflow.ID, &workflow.Request, &workflow.Cwd, &workflow.Status, &created, &updated); err != nil {
		return Workflow{}, err
	}
	workflow.CreatedAt, _ = decodeTime(created)
	workflow.UpdatedAt, _ = decodeTime(updated)
	return workflow, nil
}

func (s *Store) stages(ctx context.Context, workflowID string) ([]Stage, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id,workflow_id,position,agent_id,title,prompt,status,session_id,output,error,started_at,ended_at FROM stages WHERE workflow_id=? ORDER BY position`, workflowID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	stages := make([]Stage, 0)
	for rows.Next() {
		var stage Stage
		var started, ended sql.NullString
		if err := rows.Scan(&stage.ID, &stage.WorkflowID, &stage.Position, &stage.AgentID, &stage.Title, &stage.Prompt, &stage.Status, &stage.SessionID, &stage.Output, &stage.Error, &started, &ended); err != nil {
			return nil, err
		}
		if started.Valid {
			value, _ := decodeTime(started.String)
			stage.StartedAt = &value
		}
		if ended.Valid {
			value, _ := decodeTime(ended.String)
			stage.EndedAt = &value
		}
		stages = append(stages, stage)
	}
	return stages, rows.Err()
}

func (s *Store) SetWorkflowStatus(ctx context.Context, id, status string) error {
	result, err := s.db.ExecContext(ctx, `UPDATE workflows SET status=?, updated_at=? WHERE id=?`, status, encodeTime(time.Now().UTC()), id)
	if err != nil {
		return err
	}
	changed, _ := result.RowsAffected()
	if changed == 0 {
		return sql.ErrNoRows
	}
	return nil
}

func (s *Store) StartStage(ctx context.Context, workflowID, stageID, sessionID string) error {
	now := time.Now().UTC()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, `UPDATE workflows SET status=?,updated_at=? WHERE id=?`, WorkflowRunning, encodeTime(now), workflowID); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE stages SET status=?,session_id=?,started_at=?,error='' WHERE id=? AND workflow_id=?`, StageRunning, sessionID, encodeTime(now), stageID, workflowID); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO events(workflow_id,stage_id,type,message,created_at) VALUES(?,?,?,?,?)`, workflowID, stageID, "stage.started", "Agent terminal started", encodeTime(now)); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) FinishStage(ctx context.Context, workflowID, stageID, status, output, message string) error {
	now := time.Now().UTC()
	errorMessage := message
	if status == StageCompleted {
		errorMessage = ""
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, `UPDATE stages SET status=?,output=?,error=?,ended_at=? WHERE id=? AND workflow_id=?`, status, output, errorMessage, encodeTime(now), stageID, workflowID); err != nil {
		return err
	}
	eventType := "stage.completed"
	if status != StageCompleted {
		eventType = "stage." + status
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO events(workflow_id,stage_id,type,message,created_at) VALUES(?,?,?,?,?)`, workflowID, stageID, eventType, message, encodeTime(now)); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE workflows SET updated_at=? WHERE id=?`, encodeTime(now), workflowID); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) CancelQueuedStages(ctx context.Context, workflowID string) error {
	now := encodeTime(time.Now().UTC())
	_, err := s.db.ExecContext(ctx, `UPDATE stages SET status=?,error=?,ended_at=? WHERE workflow_id=? AND status=?`, StageCancelled, "Workflow cancelled", now, workflowID, StageQueued)
	return err
}

func (s *Store) Events(ctx context.Context, workflowID string, after int64) ([]Event, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id,workflow_id,stage_id,type,message,created_at FROM events WHERE workflow_id=? AND id>? ORDER BY id LIMIT 500`, workflowID, after)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	events := make([]Event, 0)
	for rows.Next() {
		var event Event
		var created string
		if err := rows.Scan(&event.ID, &event.WorkflowID, &event.StageID, &event.Type, &event.Message, &created); err != nil {
			return nil, err
		}
		event.CreatedAt, _ = decodeTime(created)
		events = append(events, event)
	}
	return events, rows.Err()
}

func (s *Store) WorkspaceSuggestions(ctx context.Context, limit int) ([]WorkspaceSuggestion, error) {
	if limit < 1 || limit > 100 {
		limit = 30
	}
	rows, err := s.db.QueryContext(ctx, `SELECT path,name,last_used_at FROM projects ORDER BY last_used_at DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	suggestions := make([]WorkspaceSuggestion, 0)
	for rows.Next() {
		var item WorkspaceSuggestion
		var lastUsed string
		if err := rows.Scan(&item.Path, &item.Name, &lastUsed); err != nil {
			return nil, err
		}
		item.LastUsedAt, _ = decodeTime(lastUsed)
		suggestions = append(suggestions, item)
	}
	return suggestions, rows.Err()
}

func encodeTime(value time.Time) string          { return value.UTC().Format(time.RFC3339Nano) }
func decodeTime(value string) (time.Time, error) { return time.Parse(time.RFC3339Nano, value) }
func boolInt(value bool) int {
	if value {
		return 1
	}
	return 0
}
