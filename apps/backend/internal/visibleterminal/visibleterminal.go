package visibleterminal

import (
	"encoding/hex"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
)

type Manager struct {
	stateDir string
	mu       sync.Mutex
	windows  map[string]int
}

func New(stateDir string) *Manager {
	return &Manager{stateDir: stateDir, windows: make(map[string]int)}
}

// Open launches a native terminal window attached to an existing Termlinks
// session. Arguments are passed separately to the platform launcher; the only
// shell command used on macOS is built from strictly quoted values.
func (m *Manager) Open(sessionID string) error {
	decodedID, decodeErr := hex.DecodeString(sessionID)
	if decodeErr != nil || len(decodedID) != 16 {
		return errors.New("invalid session ID")
	}
	if !filepath.IsAbs(m.stateDir) || strings.ContainsRune(m.stateDir, '\x00') {
		return errors.New("invalid state directory")
	}
	executable, err := os.Executable()
	if err != nil {
		return err
	}
	var command *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		attach := darwinAttachCommand(executable, m.stateDir, sessionID)
		script := `on run argv
tell application "Terminal"
  activate
  do script (item 1 of argv)
  return id of front window
end tell
end run`
		command = exec.Command("/usr/bin/osascript", "-e", script, attach)
		output, err := command.Output()
		if err != nil {
			return err
		}
		windowID, err := strconv.Atoi(strings.TrimSpace(string(output)))
		if err != nil || windowID < 1 {
			return errors.New("Terminal did not return its new window ID")
		}
		m.mu.Lock()
		m.windows[sessionID] = windowID
		m.mu.Unlock()
		return nil
	case "linux":
		launchers := [][]string{
			{"x-terminal-emulator", "-e", executable, "__viewer", sessionID},
			{"gnome-terminal", "--", executable, "__viewer", sessionID},
			{"konsole", "-e", executable, "__viewer", sessionID},
			{"xterm", "-e", executable, "__viewer", sessionID},
		}
		for _, candidate := range launchers {
			if path, lookupErr := exec.LookPath(candidate[0]); lookupErr == nil {
				command = exec.Command(path, candidate[1:]...)
				break
			}
		}
		if command == nil {
			return errors.New("no supported graphical terminal was found")
		}
		command.Env = append(os.Environ(), "TERMLINKS_STATE_DIR="+m.stateDir)
	default:
		return errors.New("visible terminal windows are not supported on this operating system")
	}
	command.Stdin = nil
	command.Stdout = io.Discard
	command.Stderr = io.Discard
	if err := command.Start(); err != nil {
		return err
	}
	go func() { _ = command.Wait() }()
	return nil
}

// Close closes only the macOS Terminal window created for this managed viewer.
// Linux terminal emulators launched with -e close when __viewer disconnects.
func (m *Manager) Close(sessionID string) error {
	if runtime.GOOS != "darwin" {
		return nil
	}
	m.mu.Lock()
	windowID, ok := m.windows[sessionID]
	delete(m.windows, sessionID)
	m.mu.Unlock()
	if !ok {
		return nil
	}
	script := `on run argv
tell application "Terminal"
  set viewerID to item 1 of argv as integer
  repeat with viewerWindow in windows
    if id of viewerWindow is viewerID then
      close viewerWindow
      return
    end if
  end repeat
end tell
end run`
	return exec.Command("/usr/bin/osascript", "-e", script, strconv.Itoa(windowID)).Run()
}

// darwinAttachCommand ends the dedicated shell cleanly after detaching. The
// manager also closes its recorded window explicitly, so this does not depend
// on the user's Terminal close-on-exit preference.
func darwinAttachCommand(executable, stateDir, sessionID string) string {
	return "env TERMLINKS_STATE_DIR=" + shellQuote(stateDir) + " " + shellQuote(executable) + " __viewer " + shellQuote(sessionID) + "; exit 0"
}

func shellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", `'"'"'`) + "'"
}
