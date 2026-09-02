package visibleterminal

import (
	"encoding/hex"
	"errors"
	"io"
	"os"
	"os/exec"
	"runtime"
	"strings"
)

// Open launches a native terminal window attached to an existing Termlinks
// session. Arguments are passed separately to the platform launcher; the only
// shell command used on macOS is built from strictly quoted values.
func Open(sessionID string) error {
	decodedID, decodeErr := hex.DecodeString(sessionID)
	if decodeErr != nil || len(decodedID) != 16 {
		return errors.New("invalid session ID")
	}
	executable, err := os.Executable()
	if err != nil {
		return err
	}
	var command *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		attach := shellQuote(executable) + " attach " + shellQuote(sessionID)
		script := `on run argv
tell application "Terminal"
  activate
  do script (item 1 of argv)
end tell
end run`
		command = exec.Command("/usr/bin/osascript", "-e", script, attach)
	case "linux":
		launchers := [][]string{
			{"x-terminal-emulator", "-e", executable, "attach", sessionID},
			{"gnome-terminal", "--", executable, "attach", sessionID},
			{"konsole", "-e", executable, "attach", sessionID},
			{"xterm", "-e", executable, "attach", sessionID},
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

func shellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", `'"'"'`) + "'"
}
