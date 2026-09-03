package server

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"path"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"

	"termlinks/backend/internal/auth"
	"termlinks/backend/internal/coordinator"
	"termlinks/backend/internal/remote"
	"termlinks/backend/internal/session"
	"termlinks/backend/internal/terminalhistory"
	"termlinks/backend/internal/webui"
)

const cookieName = "termlinks_session"

type Server struct {
	sessions            *session.Manager
	auth                *auth.Manager
	logger              *slog.Logger
	web                 http.Handler
	openVisibleTerminal func(string) error
	coordinator         *coordinator.Manager
	terminalHistory     *terminalhistory.Store
	openHistoryMu       sync.Mutex
}

func (s *Server) SetCoordinator(manager *coordinator.Manager)     { s.coordinator = manager }
func (s *Server) SetTerminalHistory(store *terminalhistory.Store) { s.terminalHistory = store }

type terminalControl struct {
	Type string `json:"type"`
	Cols uint16 `json:"cols,omitempty"`
	Rows uint16 `json:"rows,omitempty"`
}

func New(sessions *session.Manager, authManager *auth.Manager, logger *slog.Logger, visibleTerminal ...func(string) error) (*Server, error) {
	root, err := fs.Sub(webui.Files, "dist")
	if err != nil {
		return nil, fmt.Errorf("load embedded web client: %w", err)
	}
	server := &Server{
		sessions: sessions,
		auth:     authManager,
		logger:   logger,
		web:      spaHandler(root),
	}
	if len(visibleTerminal) > 0 {
		server.openVisibleTerminal = visibleTerminal[0]
	}
	return server, nil
}

func (s *Server) ControlHandler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/health", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	})
	mux.HandleFunc("GET /v1/sessions", s.listSessions)
	mux.HandleFunc("POST /v1/sessions", s.createSession)
	mux.HandleFunc("POST /v1/sessions/{id}/stop", s.stopSession)
	mux.HandleFunc("GET /v1/sessions/{id}/attach", func(w http.ResponseWriter, r *http.Request) {
		s.terminal(w, r, false)
	})
	return s.securityHeaders(mux)
}

func (s *Server) WebHandler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/mode", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{"mode": "direct"})
	})
	mux.HandleFunc("POST /api/login", s.login)
	mux.HandleFunc("POST /api/logout", s.requireWebAuth(s.logout))
	mux.HandleFunc("GET /api/me", s.requireWebAuth(func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]bool{"authenticated": true})
	}))
	mux.HandleFunc("GET /api/sessions", s.requireWebAuth(s.listSessions))
	mux.HandleFunc("POST /api/sessions", s.requireWebAuth(s.createWebSession))
	mux.HandleFunc("PATCH /api/sessions/{id}", s.requireWebAuth(s.renameWebSession))
	mux.HandleFunc("POST /api/sessions/{id}/stop", s.requireWebAuth(s.stopSession))
	mux.HandleFunc("GET /api/terminal-history", s.requireWebAuth(s.listTerminalHistory))
	mux.HandleFunc("POST /api/terminal-history/session/{sessionID}/favorite", s.requireWebAuth(s.favoriteSession))
	mux.HandleFunc("PATCH /api/terminal-history/{id}", s.requireWebAuth(s.updateTerminalHistory))
	mux.HandleFunc("DELETE /api/terminal-history/{id}", s.requireWebAuth(s.deleteTerminalHistory))
	mux.HandleFunc("POST /api/terminal-history/{id}/open", s.requireWebAuth(s.openTerminalHistory))
	mux.HandleFunc("GET /api/agents", s.requireWebAuth(s.listAgents))
	mux.HandleFunc("POST /api/agents/refresh", s.requireWebAuth(s.refreshAgents))
	mux.HandleFunc("GET /api/projects/suggestions", s.requireWebAuth(s.workspaceSuggestions))
	mux.HandleFunc("POST /api/workflows/compile", s.requireWebAuth(s.compileWorkflow))
	mux.HandleFunc("GET /api/workflows", s.requireWebAuth(s.listWorkflows))
	mux.HandleFunc("POST /api/workflows", s.requireWebAuth(s.createWorkflow))
	mux.HandleFunc("GET /api/workflows/{id}", s.requireWebAuth(s.getWorkflow))
	mux.HandleFunc("POST /api/workflows/{id}/cancel", s.requireWebAuth(s.cancelWorkflow))
	mux.HandleFunc("POST /api/workflows/{id}/stages/{stageID}/input", s.requireWebAuth(s.sendWorkflowInput))
	mux.HandleFunc("GET /ws/sessions/{id}", s.requireWebAuth(func(w http.ResponseWriter, r *http.Request) {
		s.terminal(w, r, true)
	}))
	mux.Handle("/", s.web)
	return s.securityHeaders(mux)
}

func (s *Server) listAgents(w http.ResponseWriter, r *http.Request) {
	if !s.requireCoordinator(w) {
		return
	}
	agents, err := s.coordinator.Agents(r.Context())
	if err != nil {
		s.coordinatorError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"agents": agents})
}

func (s *Server) refreshAgents(w http.ResponseWriter, r *http.Request) {
	if !sameOrigin(r) {
		writeError(w, http.StatusForbidden, "cross-origin request rejected")
		return
	}
	if !s.requireCoordinator(w) {
		return
	}
	agents, err := s.coordinator.RefreshAgents(r.Context())
	if err != nil {
		s.coordinatorError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"agents": agents})
}

func (s *Server) workspaceSuggestions(w http.ResponseWriter, r *http.Request) {
	if !s.requireCoordinator(w) {
		return
	}
	items, err := s.coordinator.WorkspaceSuggestions(r.Context())
	if err != nil {
		s.coordinatorError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"projects": items})
}

func (s *Server) compileWorkflow(w http.ResponseWriter, r *http.Request) {
	if !sameOrigin(r) {
		writeError(w, http.StatusForbidden, "cross-origin request rejected")
		return
	}
	if !s.requireCoordinator(w) {
		return
	}
	var input coordinator.CreateInput
	if !decodeCoordinatorJSON(w, r, &input) {
		return
	}
	draft, err := s.coordinator.Compile(r.Context(), input)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, draft)
}

func (s *Server) createWorkflow(w http.ResponseWriter, r *http.Request) {
	if !sameOrigin(r) {
		writeError(w, http.StatusForbidden, "cross-origin request rejected")
		return
	}
	if !s.requireCoordinator(w) {
		return
	}
	var input coordinator.CreateInput
	if !decodeCoordinatorJSON(w, r, &input) {
		return
	}
	workflow, err := s.coordinator.Create(r.Context(), input)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, workflow)
}

func (s *Server) listWorkflows(w http.ResponseWriter, r *http.Request) {
	if !s.requireCoordinator(w) {
		return
	}
	workflows, err := s.coordinator.List(r.Context())
	if err != nil {
		s.coordinatorError(w, err)
		return
	}
	for workflowIndex := range workflows {
		for stageIndex := range workflows[workflowIndex].Stages {
			workflows[workflowIndex].Stages[stageIndex].Output = ""
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"workflows": workflows})
}

func (s *Server) getWorkflow(w http.ResponseWriter, r *http.Request) {
	if !s.requireCoordinator(w) {
		return
	}
	if !validCoordinatorID(r.PathValue("id")) {
		writeError(w, http.StatusBadRequest, "invalid workflow ID")
		return
	}
	workflow, err := s.coordinator.Get(r.Context(), r.PathValue("id"))
	if err != nil {
		s.coordinatorError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, workflow)
}

func (s *Server) cancelWorkflow(w http.ResponseWriter, r *http.Request) {
	if !sameOrigin(r) {
		writeError(w, http.StatusForbidden, "cross-origin request rejected")
		return
	}
	if !s.requireCoordinator(w) {
		return
	}
	if !validCoordinatorID(r.PathValue("id")) {
		writeError(w, http.StatusBadRequest, "invalid workflow ID")
		return
	}
	if err := s.coordinator.Cancel(r.Context(), r.PathValue("id")); err != nil {
		s.coordinatorError(w, err)
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]string{"status": "cancelling"})
}

func (s *Server) sendWorkflowInput(w http.ResponseWriter, r *http.Request) {
	if !sameOrigin(r) {
		writeError(w, http.StatusForbidden, "cross-origin request rejected")
		return
	}
	if !s.requireCoordinator(w) {
		return
	}
	workflowID, stageID := r.PathValue("id"), r.PathValue("stageID")
	if !validCoordinatorID(workflowID) || !validCoordinatorID(stageID) {
		writeError(w, http.StatusBadRequest, "invalid workflow or stage ID")
		return
	}
	var input struct {
		Input string `json:"input"`
	}
	if !decodeCoordinatorJSON(w, r, &input) {
		return
	}
	if err := s.coordinator.SendInput(r.Context(), workflowID, stageID, input.Input); err != nil {
		s.coordinatorError(w, err)
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]string{"status": "sent"})
}

func (s *Server) requireCoordinator(w http.ResponseWriter) bool {
	if s.coordinator == nil {
		writeError(w, http.StatusServiceUnavailable, "AI workflows are not enabled")
		return false
	}
	return true
}

func (s *Server) coordinatorError(w http.ResponseWriter, err error) {
	if errors.Is(err, sql.ErrNoRows) {
		writeError(w, http.StatusNotFound, "workflow not found")
		return
	}
	s.logger.Error("AI workflow request failed", "error", err)
	writeError(w, http.StatusInternalServerError, "AI workflow request failed")
}

func decodeCoordinatorJSON(w http.ResponseWriter, r *http.Request, target any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, 64<<10)
	defer r.Body.Close()
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		writeError(w, http.StatusBadRequest, "invalid workflow request")
		return false
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		writeError(w, http.StatusBadRequest, "invalid workflow request")
		return false
	}
	return true
}

func validCoordinatorID(id string) bool {
	if len(id) != 24 {
		return false
	}
	for _, character := range id {
		if (character < '0' || character > '9') && (character < 'a' || character > 'f') {
			return false
		}
	}
	return true
}

func (s *Server) createWebSession(w http.ResponseWriter, r *http.Request) {
	if !sameOrigin(r) {
		writeError(w, http.StatusForbidden, "cross-origin request rejected")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 16<<10)
	defer r.Body.Close()
	input, err := remote.DecodeStartRequest(r.Body)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid session request")
		return
	}
	options, err := input.Options()
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	created, err := s.sessions.Start(options)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if s.openVisibleTerminal != nil {
		if err := s.openVisibleTerminal(created.Info().ID); err != nil {
			s.logger.Warn("could not open a visible terminal window", "session", created.Info().ID, "error", err)
		}
	}
	writeJSON(w, http.StatusCreated, created.Info())
}

func (s *Server) renameWebSession(w http.ResponseWriter, r *http.Request) {
	if !sameOrigin(r) {
		writeError(w, http.StatusForbidden, "cross-origin request rejected")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 4096)
	defer r.Body.Close()
	input, err := remote.DecodeRenameRequest(r.Body)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	current, ok := s.sessions.Get(r.PathValue("id"))
	if !ok {
		writeError(w, http.StatusNotFound, "session not found")
		return
	}
	renamed := current.Rename(input.Name)
	if s.terminalHistory != nil {
		if err := s.terminalHistory.RenameSession(r.Context(), renamed.ID, renamed.Name); err != nil {
			s.logger.Warn("could not update saved terminal name", "session", renamed.ID, "error", err)
		}
	}
	writeJSON(w, http.StatusOK, renamed)
}

func (s *Server) listTerminalHistory(w http.ResponseWriter, r *http.Request) {
	if s.terminalHistory == nil {
		writeError(w, http.StatusServiceUnavailable, "terminal history is unavailable")
		return
	}
	if err := s.terminalHistory.Reconcile(r.Context(), s.sessions.List()); err != nil {
		s.logger.Error("could not reconcile terminal history", "error", err)
		writeError(w, http.StatusInternalServerError, "could not load terminal history")
		return
	}
	entries, err := s.terminalHistory.List(r.Context())
	if err != nil {
		s.logger.Error("could not list terminal history", "error", err)
		writeError(w, http.StatusInternalServerError, "could not load terminal history")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"terminals": entries, "maxFavorites": terminalhistory.MaxFavorites, "maxRecent": terminalhistory.MaxRecent})
}

func (s *Server) favoriteSession(w http.ResponseWriter, r *http.Request) {
	if !sameOrigin(r) {
		writeError(w, http.StatusForbidden, "cross-origin request rejected")
		return
	}
	if s.terminalHistory == nil {
		writeError(w, http.StatusServiceUnavailable, "terminal history is unavailable")
		return
	}
	current, ok := s.sessions.Get(r.PathValue("sessionID"))
	if !ok {
		writeError(w, http.StatusNotFound, "session not found")
		return
	}
	entry, err := s.terminalHistory.SaveSession(r.Context(), current.Info(), true)
	if errors.Is(err, terminalhistory.ErrFavoriteLimit) {
		writeError(w, http.StatusConflict, err.Error())
		return
	}
	if err != nil {
		s.logger.Error("could not favorite terminal", "error", err)
		writeError(w, http.StatusInternalServerError, "could not save terminal")
		return
	}
	writeJSON(w, http.StatusCreated, entry)
}

type terminalHistoryUpdate struct {
	Name     *string `json:"name"`
	Favorite *bool   `json:"favorite"`
}

func (s *Server) updateTerminalHistory(w http.ResponseWriter, r *http.Request) {
	if !sameOrigin(r) {
		writeError(w, http.StatusForbidden, "cross-origin request rejected")
		return
	}
	if s.terminalHistory == nil {
		writeError(w, http.StatusServiceUnavailable, "terminal history is unavailable")
		return
	}
	if !terminalhistory.ValidID(r.PathValue("id")) {
		writeError(w, http.StatusNotFound, "saved terminal not found")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 4096)
	defer r.Body.Close()
	var input terminalHistoryUpdate
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&input); err != nil {
		writeError(w, http.StatusBadRequest, "invalid terminal history request")
		return
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) || (input.Name == nil && input.Favorite == nil) {
		writeError(w, http.StatusBadRequest, "invalid terminal history request")
		return
	}
	if input.Name != nil {
		name, err := remote.ValidateSessionName(*input.Name)
		if err != nil || name == "" {
			writeError(w, http.StatusBadRequest, "session name is invalid")
			return
		}
		input.Name = &name
	}
	entry, err := s.terminalHistory.Update(r.Context(), r.PathValue("id"), input.Name, input.Favorite)
	if errors.Is(err, terminalhistory.ErrNotFound) {
		writeError(w, http.StatusNotFound, err.Error())
		return
	}
	if errors.Is(err, terminalhistory.ErrFavoriteLimit) {
		writeError(w, http.StatusConflict, err.Error())
		return
	}
	if err != nil {
		s.logger.Error("could not update terminal history", "error", err)
		writeError(w, http.StatusInternalServerError, "could not update terminal history")
		return
	}
	writeJSON(w, http.StatusOK, entry)
}

func (s *Server) deleteTerminalHistory(w http.ResponseWriter, r *http.Request) {
	if !sameOrigin(r) {
		writeError(w, http.StatusForbidden, "cross-origin request rejected")
		return
	}
	if s.terminalHistory == nil {
		writeError(w, http.StatusServiceUnavailable, "terminal history is unavailable")
		return
	}
	if !terminalhistory.ValidID(r.PathValue("id")) {
		writeError(w, http.StatusNotFound, "saved terminal not found")
		return
	}
	err := s.terminalHistory.Delete(r.Context(), r.PathValue("id"))
	if errors.Is(err, terminalhistory.ErrNotFound) {
		writeError(w, http.StatusNotFound, err.Error())
		return
	}
	if err != nil {
		s.logger.Error("could not remove terminal history", "error", err)
		writeError(w, http.StatusInternalServerError, "could not remove terminal history")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) openTerminalHistory(w http.ResponseWriter, r *http.Request) {
	if !sameOrigin(r) {
		writeError(w, http.StatusForbidden, "cross-origin request rejected")
		return
	}
	if s.terminalHistory == nil {
		writeError(w, http.StatusServiceUnavailable, "terminal history is unavailable")
		return
	}
	if !terminalhistory.ValidID(r.PathValue("id")) {
		writeError(w, http.StatusNotFound, "saved terminal not found")
		return
	}
	s.openHistoryMu.Lock()
	defer s.openHistoryMu.Unlock()
	entry, err := s.terminalHistory.Get(r.Context(), r.PathValue("id"))
	if errors.Is(err, terminalhistory.ErrNotFound) {
		writeError(w, http.StatusNotFound, err.Error())
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not open saved terminal")
		return
	}
	linkedSessionID := entry.ActiveSessionID
	if linkedSessionID == "" {
		linkedSessionID = entry.SourceSessionID
	}
	if current, ok := s.sessions.Get(linkedSessionID); ok && current.Info().Running {
		writeJSON(w, http.StatusOK, current.Info())
		return
	}
	options, err := (remote.StartRequest{Name: entry.Name, Cwd: entry.Cwd}).Options()
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	created, err := s.sessions.Start(options)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if _, err := s.terminalHistory.MarkOpened(r.Context(), entry.ID, created.Info().ID, created.Info().StartedAt); err != nil {
		_ = created.Stop()
		s.logger.Error("could not associate opened terminal history", "error", err)
		writeError(w, http.StatusInternalServerError, "could not open saved terminal")
		return
	}
	if s.openVisibleTerminal != nil {
		if err := s.openVisibleTerminal(created.Info().ID); err != nil {
			s.logger.Warn("could not open a visible terminal window", "session", created.Info().ID, "error", err)
		}
	}
	writeJSON(w, http.StatusCreated, created.Info())
}

func (s *Server) createSession(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	defer r.Body.Close()
	var options session.StartOptions
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&options); err != nil {
		writeError(w, http.StatusBadRequest, "invalid session request")
		return
	}
	created, err := s.sessions.Start(options)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, created.Info())
}

func (s *Server) listSessions(w http.ResponseWriter, _ *http.Request) {
	sessions := s.sessions.List()
	writeJSON(w, http.StatusOK, map[string]any{"sessions": sessions})
}

func (s *Server) stopSession(w http.ResponseWriter, r *http.Request) {
	if strings.HasPrefix(r.URL.Path, "/api/") && !sameOrigin(r) {
		writeError(w, http.StatusForbidden, "cross-origin request rejected")
		return
	}
	current, ok := s.sessions.Get(r.PathValue("id"))
	if !ok {
		writeError(w, http.StatusNotFound, "session not found")
		return
	}
	if err := current.Stop(); err != nil {
		writeError(w, http.StatusInternalServerError, "could not stop session")
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]string{"status": "stopping"})
}

func (s *Server) login(w http.ResponseWriter, r *http.Request) {
	if !sameOrigin(r) {
		writeError(w, http.StatusForbidden, "cross-origin request rejected")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 4096)
	defer r.Body.Close()
	var input struct {
		Token string `json:"token"`
	}
	if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
		writeError(w, http.StatusBadRequest, "invalid login request")
		return
	}
	sessionID, expires, err := s.auth.Login(remoteIP(r), input.Token)
	if errors.Is(err, auth.ErrRateLimited) {
		w.Header().Set("Retry-After", "60")
		writeError(w, http.StatusTooManyRequests, "too many login attempts")
		return
	}
	if err != nil {
		writeError(w, http.StatusUnauthorized, "invalid token")
		return
	}
	http.SetCookie(w, &http.Cookie{
		Name:     cookieName,
		Value:    sessionID,
		Path:     "/",
		Expires:  expires,
		MaxAge:   int(auth.SessionDuration.Seconds()),
		HttpOnly: true,
		Secure:   r.TLS != nil,
		SameSite: http.SameSiteStrictMode,
	})
	writeJSON(w, http.StatusOK, map[string]bool{"authenticated": true})
}

func (s *Server) logout(w http.ResponseWriter, r *http.Request) {
	if !sameOrigin(r) {
		writeError(w, http.StatusForbidden, "cross-origin request rejected")
		return
	}
	if cookie, err := r.Cookie(cookieName); err == nil {
		s.auth.Logout(cookie.Value)
	}
	http.SetCookie(w, &http.Cookie{
		Name:     cookieName,
		Value:    "",
		Path:     "/",
		MaxAge:   -1,
		HttpOnly: true,
		Secure:   r.TLS != nil,
		SameSite: http.SameSiteStrictMode,
	})
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) requireWebAuth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		cookie, err := r.Cookie(cookieName)
		if err != nil || !s.auth.Valid(cookie.Value) {
			writeError(w, http.StatusUnauthorized, "authentication required")
			return
		}
		next(w, r)
	}
}

func (s *Server) terminal(w http.ResponseWriter, r *http.Request, browser bool) {
	if browser && !sameOrigin(r) {
		writeError(w, http.StatusForbidden, "cross-origin websocket rejected")
		return
	}
	current, ok := s.sessions.Get(r.PathValue("id"))
	if !ok {
		writeError(w, http.StatusNotFound, "session not found")
		return
	}
	upgrader := websocket.Upgrader{
		ReadBufferSize:  4096,
		WriteBufferSize: 32 << 10,
		CheckOrigin: func(*http.Request) bool {
			return !browser || sameOrigin(r)
		},
	}
	connection, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	defer connection.Close()
	connection.SetReadLimit(64 << 10)

	initial, updates, cancel := current.Subscribe()
	defer cancel()
	if len(initial) > 0 {
		if err := connection.WriteMessage(websocket.BinaryMessage, initial); err != nil {
			return
		}
	}
	if !current.Info().Running {
		s.writeTerminalStatus(connection, current.Info())
		return
	}

	readErrors := make(chan error, 1)
	go func() { readErrors <- s.readTerminal(connection, current) }()
	ping := time.NewTicker(20 * time.Second)
	defer ping.Stop()
	for {
		select {
		case data := <-updates:
			if err := connection.WriteMessage(websocket.BinaryMessage, data); err != nil {
				return
			}
		case <-current.Done():
			s.writeTerminalStatus(connection, current.Info())
			return
		case <-ping.C:
			if err := connection.WriteControl(websocket.PingMessage, nil, time.Now().Add(5*time.Second)); err != nil {
				return
			}
		case <-readErrors:
			return
		case <-r.Context().Done():
			return
		}
	}
}

func (s *Server) readTerminal(connection *websocket.Conn, current *session.Session) error {
	for {
		messageType, data, err := connection.ReadMessage()
		if err != nil {
			return err
		}
		switch messageType {
		case websocket.BinaryMessage:
			if err := current.Write(data); err != nil {
				return err
			}
		case websocket.TextMessage:
			var control terminalControl
			if err := json.Unmarshal(data, &control); err != nil {
				return errors.New("invalid terminal control message")
			}
			if control.Type != "resize" {
				return errors.New("unsupported terminal control message")
			}
			if err := current.Resize(control.Cols, control.Rows); err != nil {
				return err
			}
		}
	}
}

func (s *Server) writeTerminalStatus(connection *websocket.Conn, info session.Info) {
	payload, _ := json.Marshal(map[string]any{
		"type":     "status",
		"running":  info.Running,
		"exitCode": info.ExitCode,
	})
	_ = connection.WriteMessage(websocket.TextMessage, payload)
}

func (s *Server) securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'self'; style-src 'self' 'unsafe-inline'; img-src 'self' data:; connect-src 'self' ws: wss:; object-src 'none'; base-uri 'none'; frame-ancestors 'none'; form-action 'self'")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("Permissions-Policy", "camera=(), microphone=(), geolocation=(), payment=(), usb=()")
		if strings.HasPrefix(r.URL.Path, "/api/") || strings.HasPrefix(r.URL.Path, "/ws/") {
			w.Header().Set("Cache-Control", "no-store")
		}
		next.ServeHTTP(w, r)
	})
}

func sameOrigin(r *http.Request) bool {
	origin := r.Header.Get("Origin")
	if origin == "" {
		return false
	}
	parsed, err := url.Parse(origin)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") {
		return false
	}
	return strings.EqualFold(parsed.Host, r.Host)
}

func remoteIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err == nil {
		return host
	}
	return r.RemoteAddr
}

func spaHandler(root fs.FS) http.Handler {
	files := http.FileServer(http.FS(root))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cleaned := strings.TrimPrefix(path.Clean(r.URL.Path), "/")
		if cleaned == "." {
			cleaned = "index.html"
		}
		if _, err := fs.Stat(root, cleaned); err != nil {
			r.URL.Path = "/"
		}
		files.ServeHTTP(w, r)
	})
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func writeError(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, map[string]string{"error": message})
}

func Shutdown(ctx context.Context, servers ...*http.Server) error {
	var combined error
	for _, current := range servers {
		if current != nil {
			combined = errors.Join(combined, current.Shutdown(ctx))
		}
	}
	return combined
}
