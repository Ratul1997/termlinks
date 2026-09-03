package session

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/creack/pty"
)

const (
	defaultRows     = 30
	defaultCols     = 100
	maxScrollback   = 2 << 20
	maxInputMessage = 64 << 10
	maxSessions     = 64
)

type StartOptions struct {
	Name        string   `json:"name"`
	Command     []string `json:"command"`
	Cwd         string   `json:"cwd"`
	Environment []string `json:"environment,omitempty"`
	Rows        uint16   `json:"rows,omitempty"`
	Cols        uint16   `json:"cols,omitempty"`
}

type Info struct {
	ID        string     `json:"id"`
	Name      string     `json:"name"`
	Command   []string   `json:"command"`
	Cwd       string     `json:"cwd"`
	StartedAt time.Time  `json:"startedAt"`
	EndedAt   *time.Time `json:"endedAt,omitempty"`
	Running   bool       `json:"running"`
	ExitCode  *int       `json:"exitCode,omitempty"`
	Rows      uint16     `json:"rows"`
	Cols      uint16     `json:"cols"`
}

type Session struct {
	mu          sync.RWMutex
	info        Info
	cmd         *exec.Cmd
	ptmx        *os.File
	scrollback  []byte
	subscribers map[uint64]chan []byte
	nextSubID   uint64
	done        chan struct{}
	onEnded     func(Info)
}

type Manager struct {
	mu       sync.RWMutex
	startMu  sync.Mutex
	sessions map[string]*Session
	onEnded  func(Info)
}

func NewManager() *Manager {
	return &Manager{sessions: make(map[string]*Session)}
}

func (m *Manager) SetEndObserver(observer func(Info)) {
	m.mu.Lock()
	m.onEnded = observer
	m.mu.Unlock()
}

func (m *Manager) Start(options StartOptions) (*Session, error) {
	m.startMu.Lock()
	defer m.startMu.Unlock()
	if len(options.Command) == 0 || strings.TrimSpace(options.Command[0]) == "" {
		return nil, errors.New("command is required")
	}
	if options.Rows == 0 {
		options.Rows = defaultRows
	}
	if options.Cols == 0 {
		options.Cols = defaultCols
	}
	if err := validateSize(options.Cols, options.Rows); err != nil {
		return nil, err
	}
	if options.Cwd == "" {
		var err error
		options.Cwd, err = os.Getwd()
		if err != nil {
			return nil, fmt.Errorf("resolve working directory: %w", err)
		}
	}
	absCwd, err := filepath.Abs(options.Cwd)
	if err != nil {
		return nil, fmt.Errorf("resolve working directory: %w", err)
	}
	stat, err := os.Stat(absCwd)
	if err != nil || !stat.IsDir() {
		return nil, fmt.Errorf("working directory is not accessible: %s", absCwd)
	}
	if err := m.makeRoom(); err != nil {
		return nil, err
	}

	id, err := randomID()
	if err != nil {
		return nil, err
	}
	name := strings.TrimSpace(options.Name)
	if name == "" {
		name = filepath.Base(options.Command[0])
	}
	command := exec.Command(options.Command[0], options.Command[1:]...)
	command.Dir = absCwd
	command.Env = withTerminalEnvironment(options.Environment)
	ptmx, err := pty.StartWithSize(command, &pty.Winsize{Rows: options.Rows, Cols: options.Cols})
	if err != nil {
		return nil, fmt.Errorf("start %q: %w", options.Command[0], err)
	}

	m.mu.RLock()
	onEnded := m.onEnded
	m.mu.RUnlock()
	s := &Session{
		info: Info{
			ID:        id,
			Name:      name,
			Command:   append([]string(nil), options.Command...),
			Cwd:       absCwd,
			StartedAt: time.Now().UTC(),
			Running:   true,
			Rows:      options.Rows,
			Cols:      options.Cols,
		},
		cmd:         command,
		ptmx:        ptmx,
		subscribers: make(map[uint64]chan []byte),
		done:        make(chan struct{}),
		onEnded:     onEnded,
	}
	m.mu.Lock()
	m.sessions[id] = s
	m.mu.Unlock()
	go s.capture()
	return s, nil
}

func (m *Manager) makeRoom() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for len(m.sessions) >= maxSessions {
		var oldestID string
		var oldestTime time.Time
		for id, current := range m.sessions {
			info := current.Info()
			if info.Running {
				continue
			}
			if oldestID == "" || info.StartedAt.Before(oldestTime) {
				oldestID = id
				oldestTime = info.StartedAt
			}
		}
		if oldestID == "" {
			return fmt.Errorf("session limit reached (%d running sessions)", maxSessions)
		}
		delete(m.sessions, oldestID)
	}
	return nil
}

func (m *Manager) Get(id string) (*Session, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	s, ok := m.sessions[id]
	return s, ok
}

func (m *Manager) List() []Info {
	m.mu.RLock()
	sessions := make([]*Session, 0, len(m.sessions))
	for _, s := range m.sessions {
		sessions = append(sessions, s)
	}
	m.mu.RUnlock()
	infos := make([]Info, 0, len(sessions))
	for _, s := range sessions {
		infos = append(infos, s.Info())
	}
	sort.Slice(infos, func(i, j int) bool { return infos[i].StartedAt.After(infos[j].StartedAt) })
	return infos
}

func (s *Session) Info() Info {
	s.mu.RLock()
	defer s.mu.RUnlock()
	info := s.info
	info.Command = append([]string(nil), s.info.Command...)
	return info
}

func (s *Session) Done() <-chan struct{} { return s.done }

func (s *Session) Rename(name string) Info {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.info.Name = name
	info := s.info
	info.Command = append([]string(nil), s.info.Command...)
	return info
}

func (s *Session) Write(data []byte) error {
	if len(data) == 0 {
		return nil
	}
	if len(data) > maxInputMessage {
		return errors.New("input message is too large")
	}
	s.mu.RLock()
	running := s.info.Running
	ptmx := s.ptmx
	s.mu.RUnlock()
	if !running || ptmx == nil {
		return errors.New("session is not running")
	}
	_, err := ptmx.Write(data)
	return err
}

func (s *Session) Resize(cols, rows uint16) error {
	if err := validateSize(cols, rows); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.info.Running || s.ptmx == nil {
		return errors.New("session is not running")
	}
	if err := pty.Setsize(s.ptmx, &pty.Winsize{Rows: rows, Cols: cols}); err != nil {
		return err
	}
	s.info.Cols = cols
	s.info.Rows = rows
	return nil
}

func (s *Session) Stop() error {
	s.mu.RLock()
	running := s.info.Running
	process := s.cmd.Process
	s.mu.RUnlock()
	if !running || process == nil {
		return nil
	}
	if err := syscall.Kill(-process.Pid, syscall.SIGTERM); err != nil && !errors.Is(err, syscall.ESRCH) {
		return err
	}
	go func() {
		timer := time.NewTimer(3 * time.Second)
		defer timer.Stop()
		select {
		case <-s.done:
		case <-timer.C:
			_ = syscall.Kill(-process.Pid, syscall.SIGKILL)
		}
	}()
	return nil
}

func (s *Session) Subscribe() (initial []byte, updates <-chan []byte, cancel func()) {
	s.mu.Lock()
	id := s.nextSubID
	s.nextSubID++
	channel := make(chan []byte, 256)
	s.subscribers[id] = channel
	initial = append([]byte(nil), s.scrollback...)
	s.mu.Unlock()
	var once sync.Once
	return initial, channel, func() {
		once.Do(func() {
			s.mu.Lock()
			delete(s.subscribers, id)
			s.mu.Unlock()
		})
	}
}

func (s *Session) capture() {
	buffer := make([]byte, 32<<10)
	for {
		n, err := s.ptmx.Read(buffer)
		if n > 0 {
			s.publish(buffer[:n])
		}
		if err != nil {
			if !errors.Is(err, io.EOF) {
				// PTYs commonly return EIO after the child exits; cmd.Wait carries the useful status.
			}
			break
		}
	}
	err := s.cmd.Wait()
	ended := time.Now().UTC()
	exitCode := 0
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			exitCode = exitErr.ExitCode()
		} else {
			exitCode = -1
		}
	}
	s.mu.Lock()
	s.info.Running = false
	s.info.EndedAt = &ended
	s.info.ExitCode = &exitCode
	info := s.info
	info.Command = append([]string(nil), s.info.Command...)
	_ = s.ptmx.Close()
	s.ptmx = nil
	close(s.done)
	s.mu.Unlock()
	if s.onEnded != nil {
		s.onEnded(info)
	}
}

func (s *Session) publish(data []byte) {
	chunk := append([]byte(nil), data...)
	s.mu.Lock()
	s.scrollback = append(s.scrollback, chunk...)
	if len(s.scrollback) > maxScrollback {
		s.scrollback = append([]byte(nil), s.scrollback[len(s.scrollback)-maxScrollback:]...)
	}
	for _, subscriber := range s.subscribers {
		select {
		case subscriber <- chunk:
		default:
		}
	}
	s.mu.Unlock()
}

func validateSize(cols, rows uint16) error {
	if cols < 20 || cols > 500 || rows < 5 || rows > 300 {
		return errors.New("terminal size is outside allowed bounds")
	}
	return nil
}

func withTerminalEnvironment(environment []string) []string {
	if len(environment) == 0 {
		environment = os.Environ()
	}
	result := append([]string(nil), environment...)
	hasTerm := false
	for _, entry := range result {
		if strings.HasPrefix(entry, "TERM=") {
			hasTerm = true
			break
		}
	}
	if !hasTerm {
		result = append(result, "TERM=xterm-256color")
	}
	return result
}

func randomID() (string, error) {
	data := make([]byte, 16)
	if _, err := rand.Read(data); err != nil {
		return "", fmt.Errorf("generate session id: %w", err)
	}
	return hex.EncodeToString(data), nil
}
