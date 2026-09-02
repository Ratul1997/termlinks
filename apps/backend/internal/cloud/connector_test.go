package cloud

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"termlinks/backend/internal/auth"
	"termlinks/backend/internal/client"
	"termlinks/backend/internal/server"
	"termlinks/backend/internal/session"
)

func TestAllowedRoutes(t *testing.T) {
	sessionID := "0123456789abcdef0123456789abcdef"
	allowedHTTP := []struct{ method, path string }{
		{"POST", "/api/logout"},
		{"GET", "/api/me"},
		{"GET", "/api/sessions"},
		{"POST", "/api/sessions"},
		{"POST", "/api/sessions/" + sessionID + "/stop"},
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
		{"POST", "/api/sessions/not-an-id/stop"},
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

func TestEncryptedPortalCreatesInteractiveShellThroughControlSocket(t *testing.T) {
	manager := session.NewManager()
	handler, err := server.New(manager, auth.New("unused"), slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	temporary, err := os.MkdirTemp("/tmp", "tl-cloud-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(temporary) })
	socketPath := filepath.Join(temporary, "control.sock")
	listener, err := net.Listen("unix", socketPath)
	if err != nil {
		t.Fatal(err)
	}
	httpServer := &http.Server{Handler: handler.ControlHandler()}
	go func() { _ = httpServer.Serve(listener) }()
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = httpServer.Shutdown(ctx)
	})

	channelID := "01234567-89ab-cdef-0123-456789abcdef"
	key := deriveKey("abcdefghijklmnopqrstuvwxyz1234567890")
	state := &connectionState{
		ctx:      context.Background(),
		control:  client.New(socketPath),
		key:      key,
		outgoing: make(chan []byte, 1),
		channels: map[string]*browserChannel{channelID: {}},
	}
	state.createInteractiveShell(channelID, httpRequestMessage{
		Version: protocolVersion,
		Type:    "http_request",
		ID:      "11111111-1111-4111-8111-111111111111",
		Method:  http.MethodPost,
		Path:    "/api/sessions",
		Body:    `{"name":"cloud shell","cwd":"` + t.TempDir() + `"}`,
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
	t.Cleanup(func() { _ = current.Stop() })
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
