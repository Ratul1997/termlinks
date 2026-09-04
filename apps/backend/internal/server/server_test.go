package server

import (
	"bytes"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"termlinks/backend/internal/auth"
	"termlinks/backend/internal/coordinator"
	"termlinks/backend/internal/session"
)

func TestWebAuthenticationAndInteractiveTerminal(t *testing.T) {
	manager := session.NewManager()
	handler, err := New(manager, auth.New("private-token"), slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	web := httptest.NewServer(handler.WebHandler())
	defer web.Close()

	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	client := &http.Client{Jar: jar}
	response, err := client.Get(web.URL + "/api/mode")
	if err != nil {
		t.Fatal(err)
	}
	var mode map[string]string
	if err := json.NewDecoder(response.Body).Decode(&mode); err != nil {
		response.Body.Close()
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusOK || mode["mode"] != "direct" {
		t.Fatalf("unexpected public portal mode response: status=%d body=%v", response.StatusCode, mode)
	}

	request, _ := http.NewRequest(http.MethodGet, web.URL+"/api/sessions", nil)
	response, err = client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusUnauthorized {
		t.Fatalf("unauthenticated status = %d", response.StatusCode)
	}
	if response.Header.Get("X-Frame-Options") != "DENY" {
		t.Fatal("security headers were not applied")
	}

	loginBody := bytes.NewBufferString(`{"token":"private-token"}`)
	request, _ = http.NewRequest(http.MethodPost, web.URL+"/api/login", loginBody)
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Origin", web.URL)
	response, err = client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("login status = %d", response.StatusCode)
	}

	current, err := manager.Start(session.StartOptions{
		Name:    "integration",
		Command: []string{"/bin/sh", "-c", "printf 'web-ready\\n'; IFS= read -r value; printf 'web-got:%s\\n' \"$value\""},
		Cwd:     t.TempDir(), Cols: 80, Rows: 24,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = current.Stop() })
	deadline := time.Now().Add(2 * time.Second)
	for {
		initial, _, cancel := current.Subscribe()
		cancel()
		if bytes.Contains(initial, []byte("web-ready")) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("terminal did not produce its initial output")
		}
		time.Sleep(5 * time.Millisecond)
	}

	parsed, _ := url.Parse(web.URL)
	header := http.Header{"Origin": []string{web.URL}}
	for _, cookie := range jar.Cookies(parsed) {
		header.Add("Cookie", cookie.String())
	}
	wsURL := "ws" + strings.TrimPrefix(web.URL, "http") + "/ws/sessions/" + current.Info().ID
	connection, response, err := websocket.DefaultDialer.Dial(wsURL, header)
	if err != nil {
		if response != nil {
			t.Fatalf("websocket status %d: %v", response.StatusCode, err)
		}
		t.Fatal(err)
	}
	defer connection.Close()
	connection.SetReadDeadline(time.Now().Add(4 * time.Second))
	assertTerminalSnapshot(t, connection, []byte("web-ready"))
	if err := connection.WriteMessage(websocket.BinaryMessage, []byte("browser-input\n")); err != nil {
		t.Fatal(err)
	}
	var output []byte
	for !bytes.Contains(output, []byte("web-got:browser-input")) {
		kind, payload, err := connection.ReadMessage()
		if err != nil {
			t.Fatalf("read terminal output: %v (output %q)", err, output)
		}
		if kind == websocket.BinaryMessage {
			output = append(output, payload...)
		}
	}
}

func TestTerminalFramesEmptySnapshot(t *testing.T) {
	manager := session.NewManager()
	handler, err := New(manager, auth.New("token"), slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	current, err := manager.Start(session.StartOptions{Name: "quiet", Command: []string{"/bin/cat"}, Cwd: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = current.Stop() })
	web := httptest.NewServer(handler.ControlHandler())
	defer web.Close()
	connection, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(web.URL, "http")+"/v1/sessions/"+current.Info().ID+"/attach", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Close()
	connection.SetReadDeadline(time.Now().Add(2 * time.Second))
	assertTerminalSnapshot(t, connection, nil)
}

func assertTerminalSnapshot(t *testing.T, connection *websocket.Conn, contains []byte) {
	t.Helper()
	kind, payload, err := connection.ReadMessage()
	if err != nil {
		t.Fatal(err)
	}
	var start terminalSnapshotControl
	if kind != websocket.TextMessage || json.Unmarshal(payload, &start) != nil || start.Type != "terminal_snapshot_start" {
		t.Fatalf("invalid terminal snapshot start: kind=%d payload=%q", kind, payload)
	}
	if start.Bytes == nil {
		t.Fatal("terminal snapshot start omitted its byte count")
	}
	if len(contains) == 0 {
		if *start.Bytes != 0 {
			t.Fatalf("empty terminal snapshot declared %d bytes", *start.Bytes)
		}
	} else {
		kind, payload, err = connection.ReadMessage()
		if err != nil {
			t.Fatal(err)
		}
		if kind != websocket.BinaryMessage || len(payload) != *start.Bytes || !bytes.Contains(payload, contains) {
			t.Fatalf("invalid terminal snapshot data: kind=%d bytes=%d declared=%d", kind, len(payload), *start.Bytes)
		}
	}
	kind, payload, err = connection.ReadMessage()
	if err != nil {
		t.Fatal(err)
	}
	var end terminalSnapshotControl
	if kind != websocket.TextMessage || json.Unmarshal(payload, &end) != nil || end.Type != "terminal_snapshot_end" {
		t.Fatalf("invalid terminal snapshot end: kind=%d payload=%q", kind, payload)
	}
	if end.Bytes != nil {
		t.Fatal("terminal snapshot end unexpectedly included a byte count")
	}
}

func TestWebRejectsCrossOriginAndUnauthenticatedCreation(t *testing.T) {
	handler, err := New(session.NewManager(), auth.New("token"), slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	web := httptest.NewServer(handler.WebHandler())
	defer web.Close()

	request, _ := http.NewRequest(http.MethodPost, web.URL+"/api/login", bytes.NewBufferString(`{"token":"token"}`))
	request.Header.Set("Origin", "https://attacker.example")
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusForbidden {
		t.Fatalf("cross-origin login status = %d", response.StatusCode)
	}

	request, _ = http.NewRequest(http.MethodPost, web.URL+"/api/sessions", bytes.NewBufferString(`{"command":["sh"]}`))
	response, err = http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode >= 200 && response.StatusCode < 300 {
		t.Fatal("web portal unexpectedly permits unauthenticated session creation")
	}
}

func TestAuthenticatedWebCreationStartsHeadlessInteractiveShell(t *testing.T) {
	manager := session.NewManager()
	opened := make(chan string, 1)
	handler, err := New(manager, auth.New("token"), slog.New(slog.NewTextHandler(io.Discard, nil)), NativeViewer{Open: func(id string) error {
		opened <- id
		return nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	web := httptest.NewServer(handler.WebHandler())
	defer web.Close()
	jar, _ := cookiejar.New(nil)
	client := &http.Client{Jar: jar}
	request, _ := http.NewRequest(http.MethodPost, web.URL+"/api/login", bytes.NewBufferString(`{"token":"token"}`))
	request.Header.Set("Origin", web.URL)
	response, err := client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()

	cwd := t.TempDir()
	payload, _ := json.Marshal(map[string]string{"name": "phone shell", "cwd": cwd})
	request, _ = http.NewRequest(http.MethodPost, web.URL+"/api/sessions", bytes.NewReader(payload))
	request.Header.Set("Origin", web.URL)
	request.Header.Set("Content-Type", "application/json")
	response, err = client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusCreated {
		body, _ := io.ReadAll(response.Body)
		t.Fatalf("create status = %d: %s", response.StatusCode, body)
	}
	var created session.Info
	if err := json.NewDecoder(response.Body).Decode(&created); err != nil {
		t.Fatal(err)
	}
	if !created.Running || created.Name != "phone shell" || created.Cwd != cwd || len(created.Command) != 1 {
		t.Fatalf("unexpected created shell: %#v", created)
	}
	select {
	case openedID := <-opened:
		t.Fatalf("web-created shell unexpectedly opened a native viewer for %q", openedID)
	case <-time.After(50 * time.Millisecond):
	}
	if created.Viewer != "hidden" {
		t.Fatalf("created viewer status = %q, want hidden", created.Viewer)
	}
	current, ok := manager.Get(created.ID)
	if !ok {
		t.Fatal("created shell is not managed by the daemon")
	}
	t.Cleanup(func() { _ = current.Stop() })
}

func TestManagedNativeViewerShowHideDoesNotStopSessionOrManualAttach(t *testing.T) {
	manager := session.NewManager()
	launched := make(chan string, 2)
	closed := make(chan string, 2)
	handler, err := New(manager, auth.New("token"), slog.New(slog.NewTextHandler(io.Discard, nil)), NativeViewer{Open: func(id string) error {
		launched <- id
		return nil
	}, Close: func(id string) error {
		closed <- id
		return nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	control := httptest.NewServer(handler.ControlHandler())
	defer control.Close()

	current, err := manager.Start(session.StartOptions{
		Name: "viewer lifecycle", Command: []string{"/bin/sh", "-c", "printf 'ready\\n'; while IFS= read -r value; do printf 'got:%s\\n' \"$value\"; done"},
		Cwd: t.TempDir(), Cols: 80, Rows: 24,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = current.Stop() })
	id := current.Info().ID

	postViewer := func(action string) (int, string) {
		t.Helper()
		response, err := http.Post(control.URL+"/v1/sessions/"+id+"/"+action, "application/json", nil)
		if err != nil {
			t.Fatal(err)
		}
		defer response.Body.Close()
		var output struct {
			Viewer string `json:"viewer"`
		}
		_ = json.NewDecoder(response.Body).Decode(&output)
		return response.StatusCode, output.Viewer
	}
	if status, viewer := postViewer("show"); status != http.StatusAccepted || viewer != "opening" {
		t.Fatalf("first show = HTTP %d %q", status, viewer)
	}
	if got := <-launched; got != id {
		t.Fatalf("launched viewer %q, want %q", got, id)
	}
	if status, viewer := postViewer("show"); status != http.StatusAccepted || viewer != "opening" {
		t.Fatalf("idempotent show = HTTP %d %q", status, viewer)
	}
	select {
	case duplicate := <-launched:
		t.Fatalf("show opened duplicate native viewer %q", duplicate)
	case <-time.After(50 * time.Millisecond):
	}

	wsOrigin := "ws" + strings.TrimPrefix(control.URL, "http")
	viewer, _, err := websocket.DefaultDialer.Dial(wsOrigin+"/v1/sessions/"+id+"/viewer", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer viewer.Close()
	assertTerminalSnapshot(t, viewer, []byte("ready"))
	manual, _, err := websocket.DefaultDialer.Dial(wsOrigin+"/v1/sessions/"+id+"/attach", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer manual.Close()
	assertTerminalSnapshot(t, manual, []byte("ready"))

	listResponse, err := http.Get(control.URL + "/v1/sessions")
	if err != nil {
		t.Fatal(err)
	}
	var listed struct {
		Sessions []session.Info `json:"sessions"`
	}
	if err := json.NewDecoder(listResponse.Body).Decode(&listed); err != nil {
		listResponse.Body.Close()
		t.Fatal(err)
	}
	listResponse.Body.Close()
	if len(listed.Sessions) != 1 || listed.Sessions[0].Viewer != "visible" {
		t.Fatalf("viewer list status = %#v", listed.Sessions)
	}

	if status, viewerStatus := postViewer("hide"); status != http.StatusOK || viewerStatus != "hidden" {
		t.Fatalf("hide = HTTP %d %q", status, viewerStatus)
	}
	select {
	case closedID := <-closed:
		if closedID != id {
			t.Fatalf("closed native window %q, want %q", closedID, id)
		}
	case <-time.After(time.Second):
		t.Fatal("hide did not close the managed native window")
	}
	viewer.SetReadDeadline(time.Now().Add(time.Second))
	if _, _, err := viewer.ReadMessage(); err == nil {
		t.Fatal("managed viewer remained connected after hide")
	}
	if !current.Info().Running {
		t.Fatal("hiding the native viewer stopped the PTY session")
	}
	if err := manual.WriteMessage(websocket.BinaryMessage, []byte("still-running\n")); err != nil {
		t.Fatalf("manual attach was closed by hide: %v", err)
	}
	manual.SetReadDeadline(time.Now().Add(2 * time.Second))
	var output []byte
	for !bytes.Contains(output, []byte("got:still-running")) {
		kind, payload, err := manual.ReadMessage()
		if err != nil {
			t.Fatalf("manual attach failed after hide: %v", err)
		}
		if kind == websocket.BinaryMessage {
			output = append(output, payload...)
		}
	}
	select {
	case duplicateClose := <-closed:
		t.Fatalf("managed native window was closed twice for %q", duplicateClose)
	case <-time.After(50 * time.Millisecond):
	}
}

func TestNativeViewerRequiresRunningSessionAndPlatformSupport(t *testing.T) {
	handler, err := New(session.NewManager(), auth.New("token"), slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	control := httptest.NewServer(handler.ControlHandler())
	defer control.Close()
	response, err := http.Post(control.URL+"/v1/sessions/0123456789abcdef0123456789abcdef/show", "application/json", nil)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusNotFound {
		t.Fatalf("missing session show status = %d", response.StatusCode)
	}

	current, err := handler.sessions.Start(session.StartOptions{Command: []string{"/bin/sh"}, Cwd: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = current.Stop() })
	response, err = http.Post(control.URL+"/v1/sessions/"+current.Info().ID+"/show", "application/json", nil)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusNotImplemented {
		t.Fatalf("unsupported viewer show status = %d", response.StatusCode)
	}
}

func TestAuthenticatedWebRenameUpdatesSessionName(t *testing.T) {
	manager := session.NewManager()
	handler, err := New(manager, auth.New("token"), slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	web := httptest.NewServer(handler.WebHandler())
	defer web.Close()
	jar, _ := cookiejar.New(nil)
	client := &http.Client{Jar: jar}
	request, _ := http.NewRequest(http.MethodPost, web.URL+"/api/login", bytes.NewBufferString(`{"token":"token"}`))
	request.Header.Set("Origin", web.URL)
	response, err := client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()

	current, err := manager.Start(session.StartOptions{Name: "old name", Command: []string{"/bin/sh"}, Cwd: t.TempDir(), Cols: 80, Rows: 24})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = current.Stop() })

	payload := bytes.NewBufferString(`{"name":" renamed terminal "}`)
	request, _ = http.NewRequest(http.MethodPatch, web.URL+"/api/sessions/"+current.Info().ID, payload)
	request.Header.Set("Origin", web.URL)
	request.Header.Set("Content-Type", "application/json")
	response, err = client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(response.Body)
		t.Fatalf("rename status = %d: %s", response.StatusCode, body)
	}
	var renamed session.Info
	if err := json.NewDecoder(response.Body).Decode(&renamed); err != nil {
		t.Fatal(err)
	}
	if renamed.Name != "renamed terminal" || current.Info().Name != "renamed terminal" {
		t.Fatalf("session was not renamed: response=%q manager=%q", renamed.Name, current.Info().Name)
	}
}

func TestWebRenameRejectsInvalidMissingAndCrossOriginRequests(t *testing.T) {
	manager := session.NewManager()
	handler, err := New(manager, auth.New("token"), slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	web := httptest.NewServer(handler.WebHandler())
	defer web.Close()
	jar, _ := cookiejar.New(nil)
	client := &http.Client{Jar: jar}
	request, _ := http.NewRequest(http.MethodPost, web.URL+"/api/login", bytes.NewBufferString(`{"token":"token"}`))
	request.Header.Set("Origin", web.URL)
	response, err := client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()

	current, err := manager.Start(session.StartOptions{Name: "old name", Command: []string{"/bin/sh"}, Cwd: t.TempDir(), Cols: 80, Rows: 24})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = current.Stop() })

	for _, test := range []struct {
		name       string
		path       string
		origin     string
		body       string
		wantStatus int
	}{
		{"empty", "/api/sessions/" + current.Info().ID, web.URL, `{"name":" "}`, http.StatusBadRequest},
		{"too long", "/api/sessions/" + current.Info().ID, web.URL, `{"name":"` + strings.Repeat("x", 81) + `"}`, http.StatusBadRequest},
		{"missing", "/api/sessions/0123456789abcdef0123456789abcdef", web.URL, `{"name":"new name"}`, http.StatusNotFound},
		{"cross origin", "/api/sessions/" + current.Info().ID, "https://attacker.example", `{"name":"new name"}`, http.StatusForbidden},
	} {
		t.Run(test.name, func(t *testing.T) {
			request, _ := http.NewRequest(http.MethodPatch, web.URL+test.path, bytes.NewBufferString(test.body))
			request.Header.Set("Origin", test.origin)
			request.Header.Set("Content-Type", "application/json")
			response, err := client.Do(request)
			if err != nil {
				t.Fatal(err)
			}
			response.Body.Close()
			if response.StatusCode != test.wantStatus {
				t.Fatalf("status = %d, want %d", response.StatusCode, test.wantStatus)
			}
		})
	}
}

func TestControlAPIStartsOnlyExplicitCommand(t *testing.T) {
	manager := session.NewManager()
	handler, err := New(manager, auth.New("unused"), slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	control := httptest.NewServer(handler.ControlHandler())
	defer control.Close()
	payload, _ := json.Marshal(session.StartOptions{Command: []string{"/bin/echo", "controlled"}, Cwd: t.TempDir(), Cols: 80, Rows: 24})
	response, err := http.Post(control.URL+"/v1/sessions", "application/json", bytes.NewReader(payload))
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusCreated {
		t.Fatalf("create status = %d", response.StatusCode)
	}
	if len(manager.List()) != 1 {
		t.Fatal("control API did not create the requested session")
	}
}

func TestAIWorkflowRoutesRequireAuthenticationAndSameOrigin(t *testing.T) {
	manager := session.NewManager()
	root := t.TempDir()
	store, err := coordinator.OpenStore(filepath.Join(root, "workflows.db"))
	if err != nil {
		t.Fatal(err)
	}
	workflowManager := coordinator.NewManager(store, manager, slog.New(slog.NewTextHandler(io.Discard, nil)))
	defer workflowManager.Close()
	handler, err := New(manager, auth.New("private-token"), slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	handler.SetCoordinator(workflowManager)
	web := httptest.NewServer(handler.WebHandler())
	defer web.Close()

	response, err := http.Get(web.URL + "/api/workflows")
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusUnauthorized {
		t.Fatalf("unauthenticated workflow status = %d", response.StatusCode)
	}

	jar, _ := cookiejar.New(nil)
	client := &http.Client{Jar: jar}
	request, _ := http.NewRequest(http.MethodPost, web.URL+"/api/login", bytes.NewBufferString(`{"token":"private-token"}`))
	request.Header.Set("Origin", web.URL)
	response, err = client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()

	request, _ = http.NewRequest(http.MethodPost, web.URL+"/api/workflows", bytes.NewBufferString(`{"request":"test","cwd":"`+root+`"}`))
	request.Header.Set("Origin", "https://attacker.example")
	response, err = client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusForbidden {
		t.Fatalf("cross-origin workflow status = %d", response.StatusCode)
	}

	request, _ = http.NewRequest(http.MethodGet, web.URL+"/api/workflows/../../etc/passwd", nil)
	response, err = client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if strings.Contains(response.Header.Get("Content-Type"), "application/json") {
		t.Fatal("path traversal unexpectedly reached the workflow API")
	}
}

func TestAttachToFinishedSessionReportsAlreadyExited(t *testing.T) {
	manager := session.NewManager()
	handler, err := New(manager, auth.New("token"), slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	current, err := manager.Start(session.StartOptions{Name: "gone", Command: []string{"/bin/sh", "-c", "sleep 30"}, Cwd: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	if err := current.Stop(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-current.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("session did not stop")
	}
	web := httptest.NewServer(handler.ControlHandler())
	defer web.Close()
	connection, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(web.URL, "http")+"/v1/sessions/"+current.Info().ID+"/attach", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Close()
	connection.SetReadDeadline(time.Now().Add(2 * time.Second))
	assertTerminalSnapshot(t, connection, nil)
	kind, payload, err := connection.ReadMessage()
	if err != nil {
		t.Fatal(err)
	}
	var status struct {
		Type          string `json:"type"`
		Running       bool   `json:"running"`
		ExitCode      *int   `json:"exitCode"`
		Signal        string `json:"signal"`
		AlreadyExited bool   `json:"alreadyExited"`
	}
	if kind != websocket.TextMessage || json.Unmarshal(payload, &status) != nil {
		t.Fatalf("expected a text status frame, got kind %d payload %q", kind, payload)
	}
	if status.Type != "status" || status.Running || !status.AlreadyExited {
		t.Fatalf("unexpected status frame: %+v", status)
	}
	if status.Signal != "SIGTERM" || status.ExitCode == nil || *status.ExitCode != 143 {
		t.Fatalf("expected SIGTERM/143, got signal %q code %v", status.Signal, status.ExitCode)
	}
}
