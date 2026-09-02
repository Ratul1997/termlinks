package cloud

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

// TestCloudPortalEndToEnd is opt-in because it targets a deployed personal portal.
// It verifies E2E authentication, encrypted session listing, and encrypted terminal
// output. It sends no terminal input unless TERMLINKS_E2E_SEND is also provided.
func TestCloudPortalEndToEnd(t *testing.T) {
	portal := strings.TrimRight(os.Getenv("TERMLINKS_E2E_PORTAL"), "/")
	token := os.Getenv("TERMLINKS_E2E_TOKEN")
	if portal == "" || token == "" {
		t.Skip("set TERMLINKS_E2E_PORTAL and TERMLINKS_E2E_TOKEN to run")
	}
	portalURL, err := url.Parse(portal)
	if err != nil || portalURL.Scheme != "https" || portalURL.Host == "" {
		t.Fatal("TERMLINKS_E2E_PORTAL must be an https URL")
	}
	websocketURL := *portalURL
	websocketURL.Scheme = "wss"
	websocketURL.Path = "/ws/bridge"
	connection, response, err := (&websocket.Dialer{HandshakeTimeout: 15 * time.Second}).Dial(websocketURL.String(), http.Header{"Origin": {portal}})
	if err != nil {
		if response != nil {
			_ = response.Body.Close()
			t.Fatalf("encrypted bridge returned HTTP %d: %v", response.StatusCode, err)
		}
		t.Fatal(err)
	}
	defer connection.Close()
	_ = connection.SetReadDeadline(time.Now().Add(20 * time.Second))

	_, readyData, err := connection.ReadMessage()
	if err != nil {
		t.Fatal(err)
	}
	var ready struct {
		Type     string `json:"type"`
		ID       string `json:"id"`
		Protocol string `json:"protocol"`
	}
	if json.Unmarshal(readyData, &ready) != nil || ready.Type != "bridge_ready" || ready.Protocol != "e2e-v1" || !validMessageID(ready.ID) {
		t.Fatal("relay returned an invalid encrypted-bridge greeting")
	}
	key := deriveKey(token)
	var sendSequence uint32
	var receiveSequence uint32
	challenge := base64.RawURLEncoding.EncodeToString([]byte("termlinks-e2e-smoke-challenge"))
	writeEncryptedForTest(t, connection, key, ready.ID, &sendSequence, authenticateMessage{Version: protocolVersion, Type: "authenticate", Challenge: challenge})
	var authenticated authenticatedMessage
	readEncryptedForTest(t, connection, key, ready.ID, &receiveSequence, &authenticated)
	if authenticated.Type != "authenticated" || authenticated.Challenge != challenge {
		t.Fatal("connector did not prove possession of the E2E key")
	}

	requestID := "11111111-1111-4111-8111-111111111111"
	writeEncryptedForTest(t, connection, key, ready.ID, &sendSequence, httpRequestMessage{
		Version: protocolVersion, Type: "http_request", ID: requestID, Method: http.MethodGet, Path: "/api/sessions",
	})
	var apiResponse httpResponseMessage
	readEncryptedForTest(t, connection, key, ready.ID, &receiveSequence, &apiResponse)
	if apiResponse.Type != "http_response" || apiResponse.ID != requestID || apiResponse.Status != http.StatusOK {
		t.Fatalf("encrypted session listing returned status %d", apiResponse.Status)
	}
	var output struct {
		Sessions []struct {
			ID   string `json:"id"`
			Name string `json:"name"`
		} `json:"sessions"`
	}
	if json.Unmarshal([]byte(apiResponse.Body), &output) != nil || len(output.Sessions) == 0 {
		t.Fatal("encrypted session listing did not contain a session")
	}
	targetSession := output.Sessions[0]
	if wanted := os.Getenv("TERMLINKS_E2E_SESSION_NAME"); wanted != "" {
		found := false
		for _, session := range output.Sessions {
			if session.Name == wanted {
				targetSession = session
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("requested smoke-test session %q was not found", wanted)
		}
	}

	terminalID := "22222222-2222-4222-8222-222222222222"
	writeEncryptedForTest(t, connection, key, ready.ID, &sendSequence, terminalOpenMessage{
		Version: protocolVersion, Type: "terminal_open", ID: terminalID, SessionID: targetSession.ID,
	})
	input := os.Getenv("TERMLINKS_E2E_SEND")
	opened := false
	var terminalOutput []byte
	for !opened || len(terminalOutput) == 0 || (input != "" && !bytes.Contains(terminalOutput, []byte(input))) {
		var raw json.RawMessage
		readEncryptedForTest(t, connection, key, ready.ID, &receiveSequence, &raw)
		var kind innerMessageType
		if json.Unmarshal(raw, &kind) != nil {
			t.Fatal("connector returned invalid encrypted terminal JSON")
		}
		switch kind.Type {
		case "terminal_opened":
			opened = true
			if input != "" {
				writeEncryptedForTest(t, connection, key, ready.ID, &sendSequence, terminalDataMessage{
					Version: protocolVersion, Type: "terminal_data", ID: terminalID, Binary: true,
					Data: base64.RawURLEncoding.EncodeToString([]byte(input + "\n")),
				})
			}
		case "terminal_data":
			var message terminalDataMessage
			if json.Unmarshal(raw, &message) != nil || message.ID != terminalID {
				t.Fatal("connector returned invalid encrypted terminal data")
			}
			data := []byte(message.Data)
			if message.Binary {
				data, err = base64.RawURLEncoding.DecodeString(message.Data)
				if err != nil {
					t.Fatal(err)
				}
			}
			terminalOutput = append(terminalOutput, data...)
		case "terminal_close":
			t.Fatal("terminal closed before the E2E smoke test completed")
		}
	}
	writeEncryptedForTest(t, connection, key, ready.ID, &sendSequence, terminalCloseMessage{
		Version: protocolVersion, Type: "terminal_close", ID: terminalID, Code: websocket.CloseNormalClosure, Reason: "Smoke test complete",
	})
	_ = connection.WriteControl(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.CloseNormalClosure, "Smoke test complete"), time.Now().Add(2*time.Second))
}

func writeEncryptedForTest(t *testing.T, connection *websocket.Conn, key [32]byte, channel string, sequence *uint32, value any) {
	t.Helper()
	packet, err := encryptPacket(key, channel, "browser", *sequence, value)
	if err != nil {
		t.Fatal(err)
	}
	if err := connection.WriteMessage(websocket.TextMessage, []byte(packet)); err != nil {
		t.Fatal(err)
	}
	*sequence++
}

func readEncryptedForTest(t *testing.T, connection *websocket.Conn, key [32]byte, channel string, sequence *uint32, output any) {
	t.Helper()
	kind, packet, err := connection.ReadMessage()
	if err != nil {
		t.Fatal(err)
	}
	if kind != websocket.TextMessage {
		t.Fatal("relay returned a non-text encrypted envelope")
	}
	plaintext, err := decryptPacket(key, channel, "connector", *sequence, string(packet))
	if err != nil {
		t.Fatalf("relay payload was not valid E2E ciphertext: %v", err)
	}
	if raw, ok := output.(*json.RawMessage); ok {
		*raw = append((*raw)[:0], plaintext...)
		*sequence++
		return
	}
	if err := json.Unmarshal(plaintext, output); err != nil {
		t.Fatal(err)
	}
	*sequence++
}
