package cloud

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"termlinks/backend/internal/auth"
	"termlinks/backend/internal/client"
	"termlinks/backend/internal/server"
	"termlinks/backend/internal/session"
)

func TestAllowedRoutes(t *testing.T) {
	sessionID := "0123456789abcdef0123456789abcdef"
	workflowID := "0123456789abcdef01234567"
	stageID := "89abcdef0123456789abcdef"
	allowedHTTP := []struct{ method, path string }{
		{"POST", "/api/logout"},
		{"GET", "/api/me"},
		{"GET", "/api/sessions"},
		{"POST", "/api/sessions"},
		{"PATCH", "/api/sessions/" + sessionID},
		{"POST", "/api/sessions/" + sessionID + "/stop"},
		{"GET", "/api/terminal-history"},
		{"POST", "/api/terminal-history/session/" + sessionID + "/favorite"},
		{"PATCH", "/api/terminal-history/" + sessionID},
		{"DELETE", "/api/terminal-history/" + sessionID},
		{"POST", "/api/terminal-history/" + sessionID + "/open"},
		{"GET", "/api/agents"},
		{"POST", "/api/agents/refresh"},
		{"GET", "/api/projects/suggestions"},
		{"POST", "/api/workflows/compile"},
		{"GET", "/api/workflows"},
		{"POST", "/api/workflows"},
		{"GET", "/api/workflows/" + workflowID},
		{"POST", "/api/workflows/" + workflowID + "/cancel"},
		{"POST", "/api/workflows/" + workflowID + "/stages/" + stageID + "/input"},
	}
	for _, test := range allowedHTTP {
		if !allowedHTTPRoute(test.method, test.path) {
			t.Errorf("expected %s %s to be allowed", test.method, test.path)
		}
	}
	for _, test := range []struct{ method, path string }{
		{"PUT", "/api/sessions"},
		{"GET", "/"},
		{"GET", "/api/sessions/../../etc/passwd"},
		{"PATCH", "/api/sessions/not-an-id"},
		{"GET", "/api/terminal-history/" + sessionID},
		{"POST", "/api/terminal-history/session/not-an-id/favorite"},
		{"DELETE", "/api/terminal-history/not-an-id"},
		{"POST", "/api/terminal-history/" + sessionID + "/wrong"},
		{"POST", "/api/sessions/not-an-id/stop"},
		{"GET", "/api/workflows/../../etc/passwd"},
		{"POST", "/api/workflows/" + workflowID + "/stages/not-an-id/input"},
		{"DELETE", "/api/workflows/" + workflowID},
	} {
		if allowedHTTPRoute(test.method, test.path) {
			t.Errorf("expected %s %s to be rejected", test.method, test.path)
		}
	}
	if !validSessionID(sessionID) || validSessionID("not-an-id") || validSessionID(sessionID+"extra") {
		t.Fatal("terminal session ID validation is incorrect")
	}
}

func TestConnectorAndLocalURLs(t *testing.T) {
	connector, err := connectorURL("https://relay.example.workers.dev")
	if err != nil || connector != "wss://relay.example.workers.dev/connector" {
		t.Fatalf("connector URL = %q, err = %v", connector, err)
	}
	local, err := localURL("http://127.0.0.1:8787", "/api/sessions?active=true")
	if err != nil || local != "http://127.0.0.1:8787/api/sessions?active=true" {
		t.Fatalf("local URL = %q, err = %v", local, err)
	}
	if _, err := localURL("http://127.0.0.1:8787", "https://attacker.example/"); err == nil {
		t.Fatal("absolute local proxy URL was accepted")
	}
}

func TestEncryptedPacketRoundTripAndChannelBinding(t *testing.T) {
	key := deriveKey("abcdefghijklmnopqrstuvwxyz1234567890")
	channel := "01234567-89ab-cdef-0123-456789abcdef"
	want := authenticatedMessage{Version: protocolVersion, Type: "authenticated", Challenge: "challenge-value-123"}
	packet, err := encryptPacket(key, channel, "connector", 0, want)
	if err != nil {
		t.Fatal(err)
	}
	plaintext, err := decryptPacket(key, channel, "connector", 0, packet)
	if err != nil {
		t.Fatal(err)
	}
	var got authenticatedMessage
	if err := json.Unmarshal(plaintext, &got); err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("decrypted message = %#v, want %#v", got, want)
	}
	if _, err := decryptPacket(key, "fedcba98-7654-3210-fedc-ba9876543210", "connector", 0, packet); err == nil {
		t.Fatal("ciphertext was accepted for a different channel")
	}
	wrongKey := deriveKey("different-portal-token-1234567890")
	if _, err := decryptPacket(wrongKey, channel, "connector", 0, packet); err == nil {
		t.Fatal("ciphertext was accepted with a different key")
	}
	if _, err := decryptPacket(key, channel, "browser", 0, packet); err == nil {
		t.Fatal("connector ciphertext was accepted in the browser direction")
	}
	if _, err := decryptPacket(key, channel, "connector", 1, packet); err == nil {
		t.Fatal("replayed ciphertext was accepted with the wrong sequence")
	}
}

func TestEncryptedPortalForwardsShellCreationToDaemonWebAPI(t *testing.T) {
	const portalToken = "abcdefghijklmnopqrstuvwxyz1234567890"
	manager := session.NewManager()
	opened := make(chan string, 1)
	handler, err := server.New(manager, auth.New(portalToken), slog.New(slog.NewTextHandler(io.Discard, nil)), func(id string) error {
		opened <- id
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	web := httptest.NewServer(handler.WebHandler())
	defer web.Close()
	jar, _ := cookiejar.New(nil)
	httpClient := &http.Client{Jar: jar}
	login, _ := http.NewRequest(http.MethodPost, web.URL+"/api/login", strings.NewReader(`{"token":"`+portalToken+`"}`))
	login.Header.Set("Origin", web.URL)
	login.Header.Set("Content-Type", "application/json")
	loginResponse, err := httpClient.Do(login)
	if err != nil {
		t.Fatal(err)
	}
	loginResponse.Body.Close()
	if loginResponse.StatusCode != http.StatusOK {
		t.Fatalf("login status = %d", loginResponse.StatusCode)
	}

	channelID := "01234567-89ab-cdef-0123-456789abcdef"
	key := deriveKey(portalToken)
	channel := &browserChannel{httpClient: httpClient}
	state := &connectionState{
		ctx: context.Background(), localOrigin: web.URL, key: key,
		outgoing: make(chan []byte, 1), channels: map[string]*browserChannel{channelID: channel},
	}
	payload, _ := json.Marshal(map[string]string{"name": "cloud shell", "cwd": t.TempDir()})
	state.handleHTTPRequest(channelID, channel, httpRequestMessage{
		Version: protocolVersion,
		Type:    "http_request",
		ID:      "11111111-1111-4111-8111-111111111111",
		Method:  http.MethodPost,
		Path:    "/api/sessions",
		Body:    string(payload),
	})

	var outer encryptedOuterMessage
	if err := json.Unmarshal(<-state.outgoing, &outer); err != nil {
		t.Fatal(err)
	}
	plaintext, err := decryptPacket(key, channelID, "connector", 0, outer.Data)
	if err != nil {
		t.Fatal(err)
	}
	var response httpResponseMessage
	if err := json.Unmarshal(plaintext, &response); err != nil {
		t.Fatal(err)
	}
	if response.Status != http.StatusCreated {
		t.Fatalf("create status = %d: %s", response.Status, response.Body)
	}
	var created session.Info
	if err := json.Unmarshal([]byte(response.Body), &created); err != nil {
		t.Fatal(err)
	}
	current, ok := manager.Get(created.ID)
	if !ok || !created.Running || created.Name != "cloud shell" || len(created.Command) != 1 {
		t.Fatalf("unexpected interactive shell: %#v", created)
	}
	select {
	case openedID := <-opened:
		if openedID != created.ID {
			t.Fatalf("daemon opened visible terminal for %q, want %q", openedID, created.ID)
		}
	case <-time.After(time.Second):
		t.Fatal("cloud creation bypassed the daemon's visible-terminal policy")
	}
	t.Cleanup(func() { _ = current.Stop() })
}

func TestEncryptedPortalLegacyCreationFallbackDoesNotOwnVisibleWindows(t *testing.T) {
	manager := session.NewManager()
	handler, err := server.New(manager, auth.New("unused"), slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	temporary, err := os.MkdirTemp("/tmp", "tl-cloud-legacy-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(temporary) })
	socketPath := filepath.Join(temporary, "control.sock")
	listener, err := net.Listen("unix", socketPath)
	if err != nil {
		t.Fatal(err)
	}
	controlServer := &http.Server{Handler: handler.ControlHandler()}
	go func() { _ = controlServer.Serve(listener) }()
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = controlServer.Shutdown(ctx)
	})

	legacyWeb := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost && r.URL.Path == "/api/sessions" {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusForbidden)
			_, _ = io.WriteString(w, `{"error":"remote session creation is disabled"}`)
			return
		}
		http.NotFound(w, r)
	}))
	defer legacyWeb.Close()

	channelID := "01234567-89ab-cdef-0123-456789abcdef"
	key := deriveKey("abcdefghijklmnopqrstuvwxyz1234567890")
	channel := &browserChannel{httpClient: legacyWeb.Client()}
	state := &connectionState{
		ctx: context.Background(), localOrigin: legacyWeb.URL, control: client.New(socketPath), key: key,
		outgoing: make(chan []byte, 1), channels: map[string]*browserChannel{channelID: channel},
	}
	payload, _ := json.Marshal(map[string]string{"name": "legacy cloud shell", "cwd": t.TempDir()})
	state.handleHTTPRequest(channelID, channel, httpRequestMessage{
		Version: protocolVersion, Type: "http_request", ID: "11111111-1111-4111-8111-111111111111",
		Method: http.MethodPost, Path: "/api/sessions", Body: string(payload),
	})

	var outer encryptedOuterMessage
	if err := json.Unmarshal(<-state.outgoing, &outer); err != nil {
		t.Fatal(err)
	}
	plaintext, err := decryptPacket(key, channelID, "connector", 0, outer.Data)
	if err != nil {
		t.Fatal(err)
	}
	var response httpResponseMessage
	if err := json.Unmarshal(plaintext, &response); err != nil {
		t.Fatal(err)
	}
	if response.Status != http.StatusCreated {
		t.Fatalf("legacy create status = %d: %s", response.Status, response.Body)
	}
	var created session.Info
	if err := json.Unmarshal([]byte(response.Body), &created); err != nil {
		t.Fatal(err)
	}
	current, ok := manager.Get(created.ID)
	if !ok || !created.Running || created.Name != "legacy cloud shell" {
		t.Fatalf("unexpected legacy interactive shell: %#v", created)
	}
	t.Cleanup(func() { _ = current.Stop() })
}

func TestLegacyCreationFallbackIsExact(t *testing.T) {
	if !legacyRemoteCreationDisabled(http.StatusForbidden, []byte(`{"error":"remote session creation is disabled"}`)) {
		t.Fatal("known legacy response did not enable the compatibility fallback")
	}
	for _, body := range []string{`{"error":"cross-origin request rejected"}`, `{"error":"remote session creation is disabled","extra":true}`, `not json`} {
		if legacyRemoteCreationDisabled(http.StatusForbidden, []byte(body)) {
			t.Fatalf("unsafe legacy fallback accepted %q", body)
		}
	}
	if legacyRemoteCreationDisabled(http.StatusUnauthorized, []byte(`{"error":"remote session creation is disabled"}`)) {
		t.Fatal("legacy fallback accepted the wrong HTTP status")
	}
}

func TestEncryptedDesktopBridgesLoopbackVNCBytes(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()

	accepted := make(chan net.Conn, 1)
	go func() {
		connection, acceptErr := listener.Accept()
		if acceptErr == nil {
			accepted <- connection
		}
	}()

	channelID := "01234567-89ab-cdef-0123-456789abcdef"
	desktopID := "11111111-1111-4111-8111-111111111111"
	key := deriveKey("abcdefghijklmnopqrstuvwxyz1234567890")
	channel := &browserChannel{desktops: make(map[string]*desktopSocket)}
	state := &connectionState{
		ctx:            context.Background(),
		key:            key,
		outgoing:       make(chan []byte, 8),
		channels:       map[string]*browserChannel{channelID: channel},
		desktopEnabled: true,
		vncAddress:     listener.Addr().String(),
	}
	state.openDesktop(channelID, channel, desktopOpenMessage{Version: protocolVersion, Type: "desktop_open", ID: desktopID})

	var server net.Conn
	select {
	case server = <-accepted:
	case <-time.After(time.Second):
		t.Fatal("connector did not dial the loopback VNC server")
	}
	defer server.Close()
	assertEncryptedDesktopMessageType(t, <-state.outgoing, key, channelID, 0, "desktop_opened")

	greeting := []byte("RFB 003.008\n")
	if _, err := server.Write(greeting); err != nil {
		t.Fatal(err)
	}
	dataPlaintext := decryptOutgoingForTest(t, <-state.outgoing, key, channelID, 1)
	var output desktopDataMessage
	if err := json.Unmarshal(dataPlaintext, &output); err != nil {
		t.Fatal(err)
	}
	decoded, err := base64.RawURLEncoding.DecodeString(output.Data)
	if err != nil || string(decoded) != string(greeting) {
		t.Fatalf("desktop output = %q, err = %v", decoded, err)
	}

	reply := []byte("RFB 003.008\n")
	state.writeDesktop(channelID, channel, desktopDataMessage{
		Version: protocolVersion,
		Type:    "desktop_data",
		ID:      desktopID,
		Data:    base64.RawURLEncoding.EncodeToString(reply),
	})
	if err := server.SetReadDeadline(time.Now().Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	received := make([]byte, len(reply))
	if _, err := io.ReadFull(server, received); err != nil {
		t.Fatal(err)
	}
	if string(received) != string(reply) {
		t.Fatalf("desktop input = %q, want %q", received, reply)
	}
	state.closeDesktop(channel, desktopID)
}

func TestEncryptedDesktopIsDisabledByDefault(t *testing.T) {
	channelID := "01234567-89ab-cdef-0123-456789abcdef"
	desktopID := "11111111-1111-4111-8111-111111111111"
	key := deriveKey("abcdefghijklmnopqrstuvwxyz1234567890")
	channel := &browserChannel{desktops: make(map[string]*desktopSocket)}
	state := &connectionState{
		ctx:      context.Background(),
		key:      key,
		outgoing: make(chan []byte, 1),
		channels: map[string]*browserChannel{channelID: channel},
	}
	state.openDesktop(channelID, channel, desktopOpenMessage{Version: protocolVersion, Type: "desktop_open", ID: desktopID})

	plaintext := decryptOutgoingForTest(t, <-state.outgoing, key, channelID, 0)
	var message desktopCloseMessage
	if err := json.Unmarshal(plaintext, &message); err != nil {
		t.Fatal(err)
	}
	if message.Type != "desktop_close" || message.Code != 1008 || message.Reason == "" {
		t.Fatalf("unexpected disabled response: %#v", message)
	}
}

func TestValidWindowInput(t *testing.T) {
	tests := []struct {
		name    string
		message windowInputMessage
		valid   bool
	}{
		{name: "pointer", message: windowInputMessage{Kind: "pointer", Action: "down", X: 0.5, Y: 1, Button: 0}, valid: true},
		{name: "scroll", message: windowInputMessage{Kind: "pointer", Action: "scroll", X: 0, Y: 0, DeltaY: 120}, valid: true},
		{name: "pointer outside", message: windowInputMessage{Kind: "pointer", Action: "move", X: 1.1, Y: 0.5}, valid: false},
		{name: "large scroll", message: windowInputMessage{Kind: "pointer", Action: "scroll", X: 0.5, Y: 0.5, DeltaY: 5000}, valid: false},
		{name: "key", message: windowInputMessage{Kind: "key", Code: "KeyC", Down: true, Meta: true}, valid: true},
		{name: "missing key", message: windowInputMessage{Kind: "key"}, valid: false},
		{name: "text", message: windowInputMessage{Kind: "text", Text: "hello 👋"}, valid: true},
		{name: "clipboard", message: windowInputMessage{Kind: "clipboard", Text: "copy me"}, valid: true},
		{name: "empty text", message: windowInputMessage{Kind: "text"}, valid: false},
		{name: "unknown", message: windowInputMessage{Kind: "shell", Text: "id"}, valid: false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := validWindowInput(test.message); got != test.valid {
				t.Fatalf("validWindowInput() = %v, want %v", got, test.valid)
			}
		})
	}
}

func TestEncryptedFileUploadIsPrivateOrderedAndDoesNotOverwrite(t *testing.T) {
	channelID := "01234567-89ab-cdef-0123-456789abcdef"
	uploadID := "11111111-1111-4111-8111-111111111111"
	key := deriveKey("abcdefghijklmnopqrstuvwxyz1234567890")
	directory := t.TempDir()
	existingPath := filepath.Join(directory, "report.pdf")
	if err := os.WriteFile(existingPath, []byte("existing"), 0o600); err != nil {
		t.Fatal(err)
	}
	channel := &browserChannel{uploads: make(map[string]*fileUpload)}
	state := &connectionState{
		ctx: context.Background(), key: key, outgoing: make(chan []byte, 8),
		channels: map[string]*browserChannel{channelID: channel}, uploadDirectory: directory,
	}
	content := []byte("%PDF-1.7\nprivate upload\n")
	state.startFileUpload(channelID, channel, fileUploadMessage{
		Version: protocolVersion, Type: "file_upload_start", ID: uploadID, Name: "report.pdf", Size: int64(len(content)),
	})
	assertEncryptedFileMessageType(t, <-state.outgoing, key, channelID, 0, "file_upload_ready")
	state.writeFileUpload(channelID, channel, fileUploadMessage{
		Version: protocolVersion, Type: "file_upload_chunk", ID: uploadID, Offset: 0,
		Data: base64.RawURLEncoding.EncodeToString(content),
	})
	assertEncryptedFileMessageType(t, <-state.outgoing, key, channelID, 1, "file_upload_progress")
	state.finishFileUpload(channelID, channel, uploadID)
	plaintext := decryptOutgoingForTest(t, <-state.outgoing, key, channelID, 2)
	var complete fileUploadResponse
	if err := json.Unmarshal(plaintext, &complete); err != nil {
		t.Fatal(err)
	}
	if complete.Type != "file_upload_complete" || complete.Path != filepath.Join(directory, "report (1).pdf") {
		t.Fatalf("unexpected upload completion: %#v", complete)
	}
	got, err := os.ReadFile(complete.Path)
	if err != nil || string(got) != string(content) {
		t.Fatalf("uploaded file = %q, err = %v", got, err)
	}
	unchanged, _ := os.ReadFile(existingPath)
	if string(unchanged) != "existing" {
		t.Fatalf("existing file was overwritten: %q", unchanged)
	}
	info, err := os.Stat(complete.Path)
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("uploaded mode = %v, err = %v", info.Mode().Perm(), err)
	}
	if validUploadName("../escape.pdf") || validUploadName("folder/file.pdf") || validUploadName("bad\x00name") {
		t.Fatal("unsafe upload name was accepted")
	}
}

func TestEncryptedFileUploadRejectsOutOfOrderChunkAndCleansTemporaryFile(t *testing.T) {
	channelID := "01234567-89ab-cdef-0123-456789abcdef"
	uploadID := "11111111-1111-4111-8111-111111111111"
	key := deriveKey("abcdefghijklmnopqrstuvwxyz1234567890")
	directory := t.TempDir()
	channel := &browserChannel{uploads: make(map[string]*fileUpload)}
	state := &connectionState{
		ctx: context.Background(), key: key, outgoing: make(chan []byte, 8),
		channels: map[string]*browserChannel{channelID: channel}, uploadDirectory: directory,
	}
	state.startFileUpload(channelID, channel, fileUploadMessage{
		Version: protocolVersion, Type: "file_upload_start", ID: uploadID, Name: "photo.jpg", Size: 3,
	})
	assertEncryptedFileMessageType(t, <-state.outgoing, key, channelID, 0, "file_upload_ready")
	state.writeFileUpload(channelID, channel, fileUploadMessage{
		Version: protocolVersion, Type: "file_upload_chunk", ID: uploadID, Offset: 2,
		Data: base64.RawURLEncoding.EncodeToString([]byte("x")),
	})
	assertEncryptedFileMessageType(t, <-state.outgoing, key, channelID, 1, "file_upload_error")
	entries, err := os.ReadDir(directory)
	if err != nil || len(entries) != 0 {
		t.Fatalf("partial upload was not removed: %v, err = %v", entries, err)
	}
}

func assertEncryptedFileMessageType(t *testing.T, outerData []byte, key [32]byte, channelID string, sequence uint32, want string) {
	t.Helper()
	plaintext := decryptOutgoingForTest(t, outerData, key, channelID, sequence)
	var message fileUploadResponse
	if err := json.Unmarshal(plaintext, &message); err != nil {
		t.Fatal(err)
	}
	if message.Type != want {
		t.Fatalf("message type = %q, want %q", message.Type, want)
	}
}

func assertEncryptedDesktopMessageType(t *testing.T, outerData []byte, key [32]byte, channelID string, sequence uint32, want string) {
	t.Helper()
	plaintext := decryptOutgoingForTest(t, outerData, key, channelID, sequence)
	var message innerMessageType
	if err := json.Unmarshal(plaintext, &message); err != nil {
		t.Fatal(err)
	}
	if message.Type != want {
		t.Fatalf("message type = %q, want %q", message.Type, want)
	}
}

func decryptOutgoingForTest(t *testing.T, outerData []byte, key [32]byte, channelID string, sequence uint32) []byte {
	t.Helper()
	var outer encryptedOuterMessage
	if err := json.Unmarshal(outerData, &outer); err != nil {
		t.Fatal(err)
	}
	plaintext, err := decryptPacket(key, channelID, "connector", sequence, outer.Data)
	if err != nil {
		t.Fatal(err)
	}
	return plaintext
}
