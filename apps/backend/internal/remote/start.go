package remote

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"termlinks/backend/internal/session"
)

const (
	maxNameLength = 80
	maxCwdLength  = 4096
)

// StartRequest is the deliberately narrow session-creation shape exposed to
// the browser. It always creates an interactive shell; callers cannot choose a
// binary, arguments, or environment variables outside that shell.
type StartRequest struct {
	Name string `json:"name"`
	Cwd  string `json:"cwd"`
}

func DecodeStartRequest(reader io.Reader) (StartRequest, error) {
	var request StartRequest
	decoder := json.NewDecoder(reader)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&request); err != nil {
		return StartRequest{}, errors.New("invalid session request")
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return StartRequest{}, errors.New("invalid session request")
	}
	return request, nil
}

func (request StartRequest) Options() (session.StartOptions, error) {
	name := strings.TrimSpace(request.Name)
	cwd := strings.TrimSpace(request.Cwd)
	if len(name) > maxNameLength || strings.ContainsRune(name, 0) {
		return session.StartOptions{}, errors.New("session name is invalid")
	}
	if len(cwd) > maxCwdLength || strings.ContainsRune(cwd, 0) {
		return session.StartOptions{}, errors.New("working directory is invalid")
	}
	if cwd == "" || cwd == "~" || strings.HasPrefix(cwd, "~/") {
		home, err := os.UserHomeDir()
		if err != nil {
			return session.StartOptions{}, errors.New("could not resolve home directory")
		}
		relative := ""
		if strings.HasPrefix(cwd, "~/") {
			relative = strings.TrimPrefix(cwd, "~/")
		}
		cwd = filepath.Join(home, relative)
	}
	if !filepath.IsAbs(cwd) {
		return session.StartOptions{}, errors.New("working directory must be an absolute path or start with ~/")
	}
	info, err := os.Stat(cwd)
	if err != nil || !info.IsDir() {
		return session.StartOptions{}, fmt.Errorf("working directory is not accessible: %s", cwd)
	}
	shell := strings.TrimSpace(os.Getenv("SHELL"))
	if shell == "" || !filepath.IsAbs(shell) {
		shell = "/bin/sh"
	}
	return session.StartOptions{
		Name:        name,
		Command:     []string{shell},
		Cwd:         cwd,
		Environment: os.Environ(),
		Cols:        100,
		Rows:        30,
	}, nil
}
