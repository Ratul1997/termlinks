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
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"termlinks/backend/internal/auth"
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
	request, _ := http.NewRequest(http.MethodGet, web.URL+"/api/sessions", nil)
	response, err := client.Do(request)
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

func TestAuthenticatedWebCreationStartsInteractiveShell(t *testing.T) {
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
	current, ok := manager.Get(created.ID)
	if !ok {
		t.Fatal("created shell is not managed by the daemon")
	}
	t.Cleanup(func() { _ = current.Stop() })
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
