package coordinator

import "time"

const (
	WorkflowQueued      = "queued"
	WorkflowRunning     = "running"
	WorkflowCompleted   = "completed"
	WorkflowFailed      = "failed"
	WorkflowCancelled   = "cancelled"
	WorkflowInterrupted = "interrupted"

	StageQueued      = "queued"
	StageRunning     = "running"
	StageCompleted   = "completed"
	StageFailed      = "failed"
	StageCancelled   = "cancelled"
	StageInterrupted = "interrupted"
)

type Agent struct {
	ID         string    `json:"id"`
	Name       string    `json:"name"`
	Command    string    `json:"command"`
	Version    string    `json:"version,omitempty"`
	Available  bool      `json:"available"`
	Runnable   bool      `json:"runnable"`
	AuthStatus string    `json:"authStatus"`
	Transport  string    `json:"transport"`
	DetectedAt time.Time `json:"detectedAt"`
}

type Stage struct {
	ID         string     `json:"id"`
	WorkflowID string     `json:"workflowId"`
	Position   int        `json:"position"`
	AgentID    string     `json:"agentId"`
	Title      string     `json:"title"`
	Prompt     string     `json:"prompt"`
	Status     string     `json:"status"`
	SessionID  string     `json:"sessionId,omitempty"`
	Output     string     `json:"output,omitempty"`
	Error      string     `json:"error,omitempty"`
	StartedAt  *time.Time `json:"startedAt,omitempty"`
	EndedAt    *time.Time `json:"endedAt,omitempty"`
}

type Workflow struct {
	ID        string    `json:"id"`
	Request   string    `json:"request"`
	Cwd       string    `json:"cwd"`
	Status    string    `json:"status"`
	CreatedAt time.Time `json:"createdAt"`
	UpdatedAt time.Time `json:"updatedAt"`
	Stages    []Stage   `json:"stages"`
}

type Event struct {
	ID         int64     `json:"id"`
	WorkflowID string    `json:"workflowId"`
	StageID    string    `json:"stageId,omitempty"`
	Type       string    `json:"type"`
	Message    string    `json:"message"`
	CreatedAt  time.Time `json:"createdAt"`
}

type Draft struct {
	Request string  `json:"request"`
	Cwd     string  `json:"cwd"`
	Stages  []Stage `json:"stages"`
}

type CreateInput struct {
	Request string `json:"request"`
	Cwd     string `json:"cwd"`
}

type WorkspaceSuggestion struct {
	Path       string    `json:"path"`
	Name       string    `json:"name"`
	LastUsedAt time.Time `json:"lastUsedAt"`
}
