package cloud

import (
	"context"
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
