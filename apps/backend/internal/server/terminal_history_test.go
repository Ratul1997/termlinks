package server

import (
	"bytes"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"termlinks/backend/internal/auth"
	"termlinks/backend/internal/session"
	"termlinks/backend/internal/terminalhistory"
)

func TestTerminalHistoryAPIStaysLocalAndOpensHeadlessShell(t *testing.T) {
	manager := session.NewManager()
	opened := make(chan string, 1)
	handler, err := New(manager, auth.New("private-token"), slog.New(slog.NewTextHandler(io.Discard, nil)), NativeViewer{Open: func(id string) error {
		opened <- id
		return nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	store, err := terminalhistory.Open(filepath.Join(t.TempDir(), "terminal-history.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	handler.SetTerminalHistory(store)
	web := httptest.NewServer(handler.WebHandler())
	defer web.Close()
	client := loginHistoryClient(t, web.URL)

	current, err := manager.Start(session.StartOptions{Name: "project", Command: []string{"/bin/sh"}, Cwd: t.TempDir(), Cols: 80, Rows: 24})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = current.Stop() })

	response := historyRequest(t, client, http.MethodPost, web.URL+"/api/terminal-history/session/"+current.Info().ID+"/favorite", web.URL, "")
	body, _ := io.ReadAll(response.Body)
	response.Body.Close()
	if response.StatusCode != http.StatusCreated {
		t.Fatalf("favorite status = %d: %s", response.StatusCode, body)
	}
	if strings.Contains(string(body), "command") {
		t.Fatalf("terminal command was persisted in history response: %s", body)
	}
	var saved terminalhistory.Entry
	if err := json.Unmarshal(body, &saved); err != nil {
		t.Fatal(err)
	}
	if !saved.Favorite || saved.SourceSessionID != current.Info().ID {
		t.Fatalf("unexpected favorite: %#v", saved)
	}

	response = historyRequest(t, client, http.MethodPatch, web.URL+"/api/terminal-history/"+saved.ID, "https://attacker.example", `{"favorite":false}`)
	response.Body.Close()
	if response.StatusCode != http.StatusForbidden {
		t.Fatalf("cross-origin patch status = %d", response.StatusCode)
	}
	response = historyRequest(t, client, http.MethodPatch, web.URL+"/api/terminal-history/"+saved.ID, web.URL, `{"name":"saved project","favorite":false}`)
	if response.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(response.Body)
		response.Body.Close()
		t.Fatalf("update status = %d: %s", response.StatusCode, body)
	}
	response.Body.Close()
	if err := current.Stop(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-current.Done():
	case <-time.After(4 * time.Second):
		t.Fatal("original terminal did not stop")
	}
	response = historyRequest(t, client, http.MethodGet, web.URL+"/api/terminal-history", web.URL, "")
	response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("session reconciliation status = %d", response.StatusCode)
	}

	response = historyRequest(t, client, http.MethodPost, web.URL+"/api/terminal-history/"+saved.ID+"/open", web.URL, "")
	defer response.Body.Close()
	if response.StatusCode != http.StatusCreated {
		body, _ := io.ReadAll(response.Body)
		t.Fatalf("open status = %d: %s", response.StatusCode, body)
	}
	var created session.Info
	if err := json.NewDecoder(response.Body).Decode(&created); err != nil {
		t.Fatal(err)
	}
	if !created.Running || created.Name != "saved project" || created.Cwd != current.Info().Cwd || len(created.Command) != 1 {
		t.Fatalf("unexpected reopened shell: %#v", created)
	}
	select {
	case id := <-opened:
		t.Fatalf("saved terminal unexpectedly opened a native viewer for %q", id)
	case <-time.After(50 * time.Millisecond):
	}
	if created.Viewer != "hidden" {
		t.Fatalf("reopened viewer status = %q, want hidden", created.Viewer)
	}
	if reopened, ok := manager.Get(created.ID); ok {
		t.Cleanup(func() { _ = reopened.Stop() })
	}
}

func TestTerminalHistoryUpdateRejectsUnknownAndEmptyPayloads(t *testing.T) {
	manager := session.NewManager()
	handler, err := New(manager, auth.New("private-token"), slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	store, err := terminalhistory.Open(filepath.Join(t.TempDir(), "terminal-history.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	handler.SetTerminalHistory(store)
	web := httptest.NewServer(handler.WebHandler())
	defer web.Close()
	client := loginHistoryClient(t, web.URL)

	for _, body := range []string{`{}`, `{"name":""}`, `{"unknown":true}`, `{"favorite":true} {}`} {
		response := historyRequest(t, client, http.MethodPatch, web.URL+"/api/terminal-history/0123456789abcdef0123456789abcdef", web.URL, body)
		response.Body.Close()
		if response.StatusCode != http.StatusBadRequest {
			t.Fatalf("payload %s status = %d, want 400", body, response.StatusCode)
		}
	}
}

func loginHistoryClient(t *testing.T, origin string) *http.Client {
	t.Helper()
	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	client := &http.Client{Jar: jar}
	response := historyRequest(t, client, http.MethodPost, origin+"/api/login", origin, `{"token":"private-token"}`)
	response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("login status = %d", response.StatusCode)
	}
	return client
}

func historyRequest(t *testing.T, client *http.Client, method, target, origin, body string) *http.Response {
	t.Helper()
	request, err := http.NewRequest(method, target, bytes.NewBufferString(body))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Origin", origin)
	request.Header.Set("Content-Type", "application/json")
	response, err := client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	return response
}
