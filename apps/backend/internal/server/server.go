package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"path"
	"strings"
	"time"

	"github.com/gorilla/websocket"

	"termlinks/backend/internal/auth"
	"termlinks/backend/internal/remote"
	"termlinks/backend/internal/session"
	"termlinks/backend/internal/webui"
)

const cookieName = "termlinks_session"

type Server struct {
	sessions *session.Manager
	auth     *auth.Manager
	logger   *slog.Logger
	web      http.Handler
}

type terminalControl struct {
	Type string `json:"type"`
	Cols uint16 `json:"cols,omitempty"`
	Rows uint16 `json:"rows,omitempty"`
}

func New(sessions *session.Manager, authManager *auth.Manager, logger *slog.Logger) (*Server, error) {
	root, err := fs.Sub(webui.Files, "dist")
	if err != nil {
		return nil, fmt.Errorf("load embedded web client: %w", err)
	}
	return &Server{
		sessions: sessions,
		auth:     authManager,
		logger:   logger,
		web:      spaHandler(root),
	}, nil
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
	mux.HandleFunc("POST /api/login", s.login)
	mux.HandleFunc("POST /api/logout", s.requireWebAuth(s.logout))
	mux.HandleFunc("GET /api/me", s.requireWebAuth(func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]bool{"authenticated": true})
	}))
	mux.HandleFunc("GET /api/sessions", s.requireWebAuth(s.listSessions))
	mux.HandleFunc("POST /api/sessions", s.requireWebAuth(s.createWebSession))
	mux.HandleFunc("POST /api/sessions/{id}/stop", s.requireWebAuth(s.stopSession))
	mux.HandleFunc("GET /ws/sessions/{id}", s.requireWebAuth(func(w http.ResponseWriter, r *http.Request) {
		s.terminal(w, r, true)
	}))
	mux.Handle("/", s.web)
	return s.securityHeaders(mux)
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
	writeJSON(w, http.StatusOK, map[string]any{"sessions": s.sessions.List()})
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
