package cloud

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/gorilla/websocket"

	"termlinks/backend/internal/client"
	"termlinks/backend/internal/config"
	"termlinks/backend/internal/remote"
	"termlinks/backend/internal/visibleterminal"
	"termlinks/backend/internal/windowcapture"
)

const (
	protocolVersion    = 1
	keyContext         = "termlinks-e2e-v1\x00"
	aadContext         = "termlinks-e2e-v1:"
	maxControlMessage  = 7 << 20
	maxEncryptedPacket = 5 << 20
	maxHTTPBody        = 64 << 10
	maxHTTPResponse    = 2 << 20
	maxTerminalInput   = 64 << 10
	maxTerminalOutput  = 2 << 20
	maxDesktopInput    = 256 << 10
	maxWindowFrame     = 2 << 20
	maxWindowText      = 16 << 10
	maxUploadSize      = 100 << 20
	maxUploadChunk     = 192 << 10
	maxUploadNameBytes = 240
	desktopReadBuffer  = 64 << 10
	authenticateWithin = 15 * time.Second
)

type messageType struct {
	Type string `json:"type"`
}

type channelOpenMessage struct {
	Type string `json:"type"`
	ID   string `json:"id"`
}

type encryptedOuterMessage struct {
	Type string `json:"type"`
	ID   string `json:"id"`
	Data string `json:"data"`
}

type channelCloseMessage struct {
	Type   string `json:"type"`
	ID     string `json:"id"`
	Code   int    `json:"code,omitempty"`
	Reason string `json:"reason,omitempty"`
}

type innerMessageType struct {
	Version int    `json:"v"`
	Type    string `json:"type"`
}

type authenticateMessage struct {
	Version   int    `json:"v"`
	Type      string `json:"type"`
	Challenge string `json:"challenge"`
}

type authenticatedMessage struct {
	Version   int    `json:"v"`
	Type      string `json:"type"`
	Challenge string `json:"challenge"`
}

type httpRequestMessage struct {
	Version int    `json:"v"`
	Type    string `json:"type"`
	ID      string `json:"id"`
	Method  string `json:"method"`
	Path    string `json:"path"`
	Body    string `json:"body,omitempty"`
}

type httpResponseMessage struct {
	Version int    `json:"v"`
	Type    string `json:"type"`
	ID      string `json:"id"`
	Status  int    `json:"status"`
	Body    string `json:"body,omitempty"`
}

type terminalOpenMessage struct {
	Version   int    `json:"v"`
	Type      string `json:"type"`
	ID        string `json:"id"`
	SessionID string `json:"sessionId"`
}

type terminalOpenedMessage struct {
	Version int    `json:"v"`
	Type    string `json:"type"`
	ID      string `json:"id"`
}

type terminalDataMessage struct {
	Version int    `json:"v"`
	Type    string `json:"type"`
	ID      string `json:"id"`
	Binary  bool   `json:"binary"`
	Data    string `json:"data"`
}

type terminalCloseMessage struct {
	Version int    `json:"v"`
	Type    string `json:"type"`
	ID      string `json:"id"`
	Code    int    `json:"code,omitempty"`
	Reason  string `json:"reason,omitempty"`
}

type desktopOpenMessage struct {
	Version int    `json:"v"`
	Type    string `json:"type"`
	ID      string `json:"id"`
}

type desktopOpenedMessage struct {
	Version int    `json:"v"`
	Type    string `json:"type"`
	ID      string `json:"id"`
}

type desktopDataMessage struct {
	Version int    `json:"v"`
	Type    string `json:"type"`
	ID      string `json:"id"`
	Data    string `json:"data"`
}

type desktopCloseMessage struct {
	Version int    `json:"v"`
	Type    string `json:"type"`
	ID      string `json:"id"`
	Code    int    `json:"code,omitempty"`
	Reason  string `json:"reason,omitempty"`
}

type windowSourcesRequestMessage struct {
	Version int    `json:"v"`
	Type    string `json:"type"`
	ID      string `json:"id"`
}

type fileUploadMessage struct {
	Version int    `json:"v"`
	Type    string `json:"type"`
	ID      string `json:"id"`
	Name    string `json:"name,omitempty"`
	Size    int64  `json:"size,omitempty"`
	Offset  int64  `json:"offset,omitempty"`
	Data    string `json:"data,omitempty"`
}

type fileUploadResponse struct {
	Version  int    `json:"v"`
	Type     string `json:"type"`
	ID       string `json:"id"`
	Received int64  `json:"received"`
	Total    int64  `json:"total"`
	Path     string `json:"path,omitempty"`
	Reason   string `json:"reason,omitempty"`
}

type windowSourcesMessage struct {
	Version     int                       `json:"v"`
	Type        string                    `json:"type"`
	ID          string                    `json:"id"`
	Permissions windowcapture.Permissions `json:"permissions"`
	Sources     []windowcapture.Source    `json:"sources"`
	Error       string                    `json:"error,omitempty"`
}

type windowOpenMessage struct {
	Version   int    `json:"v"`
	Type      string `json:"type"`
	ID        string `json:"id"`
	WindowID  uint32 `json:"windowId"`
	MaxWidth  int    `json:"maxWidth"`
	MaxHeight int    `json:"maxHeight"`
}

type windowOpenedMessage struct {
	Version int    `json:"v"`
	Type    string `json:"type"`
	ID      string `json:"id"`
}

type windowFrameMessage struct {
	Version int    `json:"v"`
	Type    string `json:"type"`
	ID      string `json:"id"`
	Width   int    `json:"width"`
	Height  int    `json:"height"`
	Data    string `json:"data"`
}

type windowInputMessage struct {
	Version int     `json:"v"`
	Type    string  `json:"type"`
	ID      string  `json:"id"`
	Kind    string  `json:"kind"`
	Action  string  `json:"action,omitempty"`
	X       float64 `json:"x,omitempty"`
	Y       float64 `json:"y,omitempty"`
	Button  int     `json:"button,omitempty"`
	DeltaX  float64 `json:"deltaX,omitempty"`
	DeltaY  float64 `json:"deltaY,omitempty"`
	Code    string  `json:"code,omitempty"`
	Down    bool    `json:"down,omitempty"`
	Shift   bool    `json:"shift,omitempty"`
	Ctrl    bool    `json:"ctrl,omitempty"`
	Alt     bool    `json:"alt,omitempty"`
	Meta    bool    `json:"meta,omitempty"`
	Text    string  `json:"text,omitempty"`
}

type windowCloseMessage struct {
	Version int    `json:"v"`
	Type    string `json:"type"`
	ID      string `json:"id"`
	Code    int    `json:"code,omitempty"`
	Reason  string `json:"reason,omitempty"`
}

type localSocket struct {
	connection *websocket.Conn
	writeMu    sync.Mutex
}

type desktopSocket struct {
	connection net.Conn
	writeMu    sync.Mutex
}

type windowSocket struct {
	capture *windowcapture.Capture
	cancel  context.CancelFunc
	mu      sync.Mutex
}

type fileUpload struct {
	mu       sync.Mutex
	file     *os.File
	tempPath string
	name     string
	size     int64
	received int64
	closed   bool
}

type browserChannel struct {
	mu            sync.Mutex
	sendMu        sync.Mutex
	authenticated bool
	httpClient    *http.Client
	sockets       map[string]*localSocket
	desktops      map[string]*desktopSocket
	windows       map[string]*windowSocket
	uploads       map[string]*fileUpload
	authTimer     *time.Timer
	receiveSeq    uint32
	sendSeq       uint32
}

type connectionState struct {
	ctx             context.Context
	localOrigin     string
	portalToken     string
	desktopEnabled  bool
	vncAddress      string
	control         *client.Client
	key             [32]byte
	outgoing        chan []byte
	channelsMu      sync.Mutex
	channels        map[string]*browserChannel
	uploadDirectory string
}

// Run keeps an authenticated outbound WebSocket connected to the Cloudflare relay.
// All browser payloads remain AES-256-GCM encrypted until they reach this connector.
func Run(ctx context.Context, settings config.CloudSettings, localListen, portalToken, controlSocket string, logOutput io.Writer) error {
	if err := config.ValidateCloudSettings(settings); err != nil {
		return err
	}
	if strings.TrimSpace(localListen) == "" {
		return errors.New("local daemon listen address is empty")
	}
	if len(strings.TrimSpace(portalToken)) < 32 {
		return errors.New("portal token is invalid")
	}
	if strings.TrimSpace(controlSocket) == "" {
		return errors.New("local control socket is empty")
	}
	connectorURL, err := connectorURL(settings.RelayURL)
	if err != nil {
		return err
	}
	localOrigin := "http://" + localListen
	key := deriveKey(portalToken)
	backoff := time.Second
	for {
		if ctx.Err() != nil {
			return nil
		}
		connectedAt := time.Now()
		err := runOnce(ctx, connectorURL, settings, localOrigin, portalToken, controlSocket, key)
		if ctx.Err() != nil {
			return nil
		}
		if time.Since(connectedAt) > time.Minute {
			backoff = time.Second
		}
		if logOutput != nil {
			_, _ = fmt.Fprintf(logOutput, "%s cloud connector disconnected: %v; retrying\n", time.Now().UTC().Format(time.RFC3339), err)
		}
		timer := time.NewTimer(backoff)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil
		case <-timer.C:
		}
		if backoff < 30*time.Second {
			backoff *= 2
			if backoff > 30*time.Second {
				backoff = 30 * time.Second
			}
		}
	}
}

func RelayStatus(ctx context.Context, relayURL string) (bool, error) {
	parsed, err := url.Parse(strings.TrimRight(strings.TrimSpace(relayURL), "/") + "/status")
	if err != nil || parsed.Scheme != "https" || parsed.Host == "" {
		return false, errors.New("invalid relay URL")
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, parsed.String(), nil)
	if err != nil {
		return false, err
	}
	response, err := (&http.Client{Timeout: 5 * time.Second}).Do(request)
	if err != nil {
		return false, err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return false, fmt.Errorf("relay status returned HTTP %d", response.StatusCode)
	}
	var status struct {
		Online bool `json:"online"`
	}
	if err := json.NewDecoder(io.LimitReader(response.Body, 4096)).Decode(&status); err != nil {
		return false, fmt.Errorf("decode relay status: %w", err)
	}
	return status.Online, nil
}

func runOnce(ctx context.Context, connectorURL string, settings config.CloudSettings, localOrigin, portalToken, controlSocket string, key [32]byte) error {
	headers := http.Header{}
	headers.Set("Authorization", "Bearer "+settings.ConnectorToken)
	headers.Set("User-Agent", "termlinks-connector/0.3")
	connection, response, err := (&websocket.Dialer{HandshakeTimeout: 10 * time.Second}).DialContext(ctx, connectorURL, headers)
	if err != nil {
		if response != nil {
			_ = response.Body.Close()
			return fmt.Errorf("relay rejected connector with HTTP %d", response.StatusCode)
		}
		return fmt.Errorf("connect to relay: %w", err)
	}
	defer connection.Close()
	connection.SetReadLimit(maxControlMessage)

	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	state := &connectionState{
		ctx: runCtx, localOrigin: localOrigin, portalToken: portalToken, key: key,
		desktopEnabled: settings.DesktopEnabled, vncAddress: settings.VNCAddress,
		control:  client.New(controlSocket),
		outgoing: make(chan []byte, 256), channels: make(map[string]*browserChannel),
		uploadDirectory: defaultUploadDirectory(),
	}
	writerErrors := make(chan error, 1)
	go func() { writerErrors <- writeRelay(runCtx, connection, state.outgoing) }()
	if err := state.sendOuter(messageType{Type: "hello"}); err != nil {
		return err
	}
	readErrors := make(chan error, 1)
	go func() { readErrors <- readRelay(connection, state) }()
	select {
	case <-ctx.Done():
		state.closeAllChannels()
		return nil
	case err := <-writerErrors:
		state.closeAllChannels()
		return err
	case err := <-readErrors:
		state.closeAllChannels()
		return err
	}
}

func writeRelay(ctx context.Context, connection *websocket.Conn, outgoing <-chan []byte) error {
	heartbeat := time.NewTicker(20 * time.Second)
	defer heartbeat.Stop()
	for {
		select {
		case <-ctx.Done():
			_ = connection.WriteControl(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.CloseNormalClosure, "Connector stopped"), time.Now().Add(2*time.Second))
			return nil
		case data := <-outgoing:
			_ = connection.SetWriteDeadline(time.Now().Add(10 * time.Second))
			if err := connection.WriteMessage(websocket.TextMessage, data); err != nil {
				return err
			}
		case <-heartbeat.C:
			_ = connection.SetWriteDeadline(time.Now().Add(10 * time.Second))
			if err := connection.WriteMessage(websocket.TextMessage, []byte(`{"type":"ping"}`)); err != nil {
				return err
			}
		}
	}
}

func readRelay(connection *websocket.Conn, state *connectionState) error {
	for {
		messageKind, data, err := connection.ReadMessage()
		if err != nil {
			return err
		}
		if messageKind != websocket.TextMessage {
			return errors.New("relay sent a non-text control message")
		}
		var kind messageType
		if err := json.Unmarshal(data, &kind); err != nil {
			return errors.New("relay sent invalid JSON")
		}
		switch kind.Type {
		case "connected", "pong":
		case "channel_open":
			var message channelOpenMessage
			if json.Unmarshal(data, &message) != nil || !validMessageID(message.ID) {
				return errors.New("relay sent an invalid channel open")
			}
			state.openChannel(message.ID)
		case "e2e_from_browser":
			var message encryptedOuterMessage
			if json.Unmarshal(data, &message) != nil || !validMessageID(message.ID) || len(message.Data) == 0 || len(message.Data) > maxEncryptedPacket {
				return errors.New("relay sent an invalid encrypted packet")
			}
			state.handleEncrypted(message.ID, message.Data)
		case "channel_close":
			var message channelCloseMessage
			if json.Unmarshal(data, &message) != nil || !validMessageID(message.ID) {
				return errors.New("relay sent an invalid channel close")
			}
			state.closeChannel(message.ID, normalizeCloseCode(message.Code), boundedReason(message.Reason), false)
		default:
			return errors.New("relay sent an unsupported message")
		}
	}
}

func (state *connectionState) openChannel(id string) {
	jar, _ := cookiejar.New(nil)
	channel := &browserChannel{
		httpClient: &http.Client{Jar: jar, Timeout: 12 * time.Second},
		sockets:    make(map[string]*localSocket),
		desktops:   make(map[string]*desktopSocket),
		windows:    make(map[string]*windowSocket),
		uploads:    make(map[string]*fileUpload),
	}
	channel.authTimer = time.AfterFunc(authenticateWithin, func() {
		channel.mu.Lock()
		authenticated := channel.authenticated
		channel.mu.Unlock()
		if !authenticated {
			state.closeChannel(id, websocket.ClosePolicyViolation, "Authentication timed out", true)
		}
	})
	state.channelsMu.Lock()
	if previous := state.channels[id]; previous != nil {
		state.channelsMu.Unlock()
		state.closeChannel(id, websocket.CloseServiceRestart, "Channel replaced", true)
		state.channelsMu.Lock()
	}
	state.channels[id] = channel
	state.channelsMu.Unlock()
}

func (state *connectionState) handleEncrypted(channelID, packet string) {
	state.channelsMu.Lock()
	channel := state.channels[channelID]
	state.channelsMu.Unlock()
	if channel == nil {
		state.closeChannel(channelID, websocket.ClosePolicyViolation, "Unknown channel", true)
		return
	}
	channel.mu.Lock()
	receiveSequence := channel.receiveSeq
	channel.mu.Unlock()
	plaintext, err := decryptPacket(state.key, channelID, "browser", receiveSequence, packet)
	if err != nil || len(plaintext) > maxControlMessage {
		state.closeChannel(channelID, websocket.ClosePolicyViolation, "Authentication failed", true)
		return
	}
	channel.mu.Lock()
	if channel.receiveSeq != receiveSequence {
		channel.mu.Unlock()
		state.closeChannel(channelID, websocket.ClosePolicyViolation, "Encrypted sequence conflict", true)
		return
	}
	channel.receiveSeq++
	channel.mu.Unlock()
	var kind innerMessageType
	if json.Unmarshal(plaintext, &kind) != nil || kind.Version != protocolVersion {
		state.closeChannel(channelID, websocket.CloseUnsupportedData, "Invalid encrypted message", true)
		return
	}
	channel.mu.Lock()
	authenticated := channel.authenticated
	channel.mu.Unlock()
	if !authenticated {
		if kind.Type != "authenticate" {
			state.closeChannel(channelID, websocket.ClosePolicyViolation, "Authentication required", true)
			return
		}
		state.authenticateChannel(channelID, channel, plaintext)
		return
	}
	switch kind.Type {
	case "http_request":
		var message httpRequestMessage
		if json.Unmarshal(plaintext, &message) != nil || !validMessageID(message.ID) {
			state.closeChannel(channelID, websocket.CloseUnsupportedData, "Invalid API request", true)
			return
		}
		go state.handleHTTPRequest(channelID, channel, message)
	case "terminal_open":
		var message terminalOpenMessage
		if json.Unmarshal(plaintext, &message) != nil || !validMessageID(message.ID) || !validSessionID(message.SessionID) {
			state.closeChannel(channelID, websocket.CloseUnsupportedData, "Invalid terminal request", true)
			return
		}
		state.openLocalSocket(channelID, channel, message)
	case "terminal_data":
		var message terminalDataMessage
		if json.Unmarshal(plaintext, &message) != nil || !validMessageID(message.ID) {
			state.closeChannel(channelID, websocket.CloseUnsupportedData, "Invalid terminal data", true)
			return
		}
		state.writeLocalSocket(channelID, channel, message)
	case "terminal_close":
		var message terminalCloseMessage
		if json.Unmarshal(plaintext, &message) != nil || !validMessageID(message.ID) {
			state.closeChannel(channelID, websocket.CloseUnsupportedData, "Invalid terminal close", true)
			return
		}
		state.closeLocalSocket(channel, message.ID, normalizeCloseCode(message.Code), boundedReason(message.Reason))
	case "desktop_open":
		var message desktopOpenMessage
		if json.Unmarshal(plaintext, &message) != nil || !validMessageID(message.ID) {
			state.closeChannel(channelID, websocket.CloseUnsupportedData, "Invalid desktop request", true)
			return
		}
		state.openDesktop(channelID, channel, message)
	case "desktop_data":
		var message desktopDataMessage
		if json.Unmarshal(plaintext, &message) != nil || !validMessageID(message.ID) {
			state.closeChannel(channelID, websocket.CloseUnsupportedData, "Invalid desktop data", true)
			return
		}
		state.writeDesktop(channelID, channel, message)
	case "desktop_close":
		var message desktopCloseMessage
		if json.Unmarshal(plaintext, &message) != nil || !validMessageID(message.ID) {
			state.closeChannel(channelID, websocket.CloseUnsupportedData, "Invalid desktop close", true)
			return
		}
		state.closeDesktop(channel, message.ID)
	case "window_sources_request":
		var message windowSourcesRequestMessage
		if json.Unmarshal(plaintext, &message) != nil || !validMessageID(message.ID) {
			state.closeChannel(channelID, websocket.CloseUnsupportedData, "Invalid window-list request", true)
			return
		}
		go state.listWindows(channelID, message)
	case "window_open":
		var message windowOpenMessage
		if json.Unmarshal(plaintext, &message) != nil || !validMessageID(message.ID) || message.WindowID == 0 || message.MaxWidth < 320 || message.MaxWidth > 2560 || message.MaxHeight < 240 || message.MaxHeight > 1800 {
			state.closeChannel(channelID, websocket.CloseUnsupportedData, "Invalid selected-window request", true)
			return
		}
		go state.openWindow(channelID, channel, message)
	case "window_input":
		var message windowInputMessage
		if json.Unmarshal(plaintext, &message) != nil || !validMessageID(message.ID) || !validWindowInput(message) {
			state.closeChannel(channelID, websocket.CloseUnsupportedData, "Invalid selected-window input", true)
			return
		}
		state.writeWindow(channelID, channel, message)
	case "window_close":
		var message windowCloseMessage
		if json.Unmarshal(plaintext, &message) != nil || !validMessageID(message.ID) {
			state.closeChannel(channelID, websocket.CloseUnsupportedData, "Invalid selected-window close", true)
			return
		}
		state.closeWindow(channel, message.ID)
	case "file_upload_start":
		var message fileUploadMessage
		if json.Unmarshal(plaintext, &message) != nil || !validMessageID(message.ID) {
			state.closeChannel(channelID, websocket.CloseUnsupportedData, "Invalid file upload request", true)
			return
		}
		state.startFileUpload(channelID, channel, message)
	case "file_upload_chunk":
		var message fileUploadMessage
		if json.Unmarshal(plaintext, &message) != nil || !validMessageID(message.ID) {
			state.closeChannel(channelID, websocket.CloseUnsupportedData, "Invalid file upload data", true)
			return
		}
		state.writeFileUpload(channelID, channel, message)
	case "file_upload_finish":
		var message fileUploadMessage
		if json.Unmarshal(plaintext, &message) != nil || !validMessageID(message.ID) {
			state.closeChannel(channelID, websocket.CloseUnsupportedData, "Invalid file upload finish", true)
			return
		}
		state.finishFileUpload(channelID, channel, message.ID)
	case "file_upload_cancel":
		var message fileUploadMessage
		if json.Unmarshal(plaintext, &message) != nil || !validMessageID(message.ID) {
			state.closeChannel(channelID, websocket.CloseUnsupportedData, "Invalid file upload cancel", true)
			return
		}
		state.cancelFileUpload(channel, message.ID)
	default:
		state.closeChannel(channelID, websocket.CloseUnsupportedData, "Unsupported encrypted message", true)
	}
}

func (state *connectionState) openDesktop(channelID string, channel *browserChannel, message desktopOpenMessage) {
	if !state.desktopEnabled {
		_ = state.sendEncrypted(channelID, desktopCloseMessage{Version: protocolVersion, Type: "desktop_close", ID: message.ID, Code: websocket.ClosePolicyViolation, Reason: "Remote desktop is disabled on this computer"})
		return
	}
	if err := config.ValidateVNCAddress(state.vncAddress); err != nil {
		_ = state.sendEncrypted(channelID, desktopCloseMessage{Version: protocolVersion, Type: "desktop_close", ID: message.ID, Code: websocket.ClosePolicyViolation, Reason: "Remote desktop target is not a safe loopback address"})
		return
	}
	channel.mu.Lock()
	if len(channel.desktops) != 0 || len(channel.windows) != 0 {
		channel.mu.Unlock()
		_ = state.sendEncrypted(channelID, desktopCloseMessage{Version: protocolVersion, Type: "desktop_close", ID: message.ID, Code: websocket.ClosePolicyViolation, Reason: "A remote desktop is already open in this portal connection"})
		return
	}
	channel.mu.Unlock()

	dialer := net.Dialer{Timeout: 3 * time.Second}
	connection, err := dialer.DialContext(state.ctx, "tcp", state.vncAddress)
	if err != nil {
		_ = state.sendEncrypted(channelID, desktopCloseMessage{Version: protocolVersion, Type: "desktop_close", ID: message.ID, Code: websocket.CloseTryAgainLater, Reason: "Local Screen Sharing is unavailable"})
		return
	}
	socket := &desktopSocket{connection: connection}
	channel.mu.Lock()
	if len(channel.desktops) != 0 || len(channel.windows) != 0 {
		channel.mu.Unlock()
		_ = connection.Close()
		_ = state.sendEncrypted(channelID, desktopCloseMessage{Version: protocolVersion, Type: "desktop_close", ID: message.ID, Code: websocket.ClosePolicyViolation, Reason: "A remote desktop is already open in this portal connection"})
		return
	}
	channel.desktops[message.ID] = socket
	channel.mu.Unlock()
	if err := state.sendEncrypted(channelID, desktopOpenedMessage{Version: protocolVersion, Type: "desktop_opened", ID: message.ID}); err != nil {
		state.removeDesktop(channel, message.ID, socket)
		return
	}
	go state.readDesktop(channelID, channel, message.ID, socket)
}

func (state *connectionState) listWindows(channelID string, message windowSourcesRequestMessage) {
	response := windowSourcesMessage{
		Version:     protocolVersion,
		Type:        "window_sources",
		ID:          message.ID,
		Permissions: windowcapture.PermissionStatus(),
		Sources:     []windowcapture.Source{},
	}
	if !state.desktopEnabled {
		response.Error = "Remote desktop is disabled on this computer"
		_ = state.sendEncrypted(channelID, response)
		return
	}
	sources, err := windowcapture.List()
	response.Permissions = windowcapture.PermissionStatus()
	if err != nil {
		response.Error = boundedReason(err.Error())
	} else {
		response.Sources = sources
	}
	_ = state.sendEncrypted(channelID, response)
}

func (state *connectionState) openWindow(channelID string, channel *browserChannel, message windowOpenMessage) {
	closeWith := func(code int, reason string) {
		_ = state.sendEncrypted(channelID, windowCloseMessage{Version: protocolVersion, Type: "window_close", ID: message.ID, Code: code, Reason: boundedReason(reason)})
	}
	if !state.desktopEnabled {
		closeWith(websocket.ClosePolicyViolation, "Remote desktop is disabled on this computer")
		return
	}
	permissions := windowcapture.PermissionStatus()
	if !permissions.Supported {
		closeWith(websocket.ClosePolicyViolation, windowcapture.ErrUnsupported.Error())
		return
	}
	if !permissions.ScreenRecording {
		closeWith(websocket.ClosePolicyViolation, "Screen Recording permission is required; run termlinks desktop permissions locally")
		return
	}
	channel.mu.Lock()
	if len(channel.desktops) != 0 || len(channel.windows) != 0 {
		channel.mu.Unlock()
		closeWith(websocket.ClosePolicyViolation, "Another remote view is already open in this portal connection")
		return
	}
	channel.mu.Unlock()

	capture, err := windowcapture.Open(message.WindowID, message.MaxWidth, message.MaxHeight)
	if err != nil {
		closeWith(websocket.CloseTryAgainLater, err.Error())
		return
	}
	captureContext, cancel := context.WithCancel(state.ctx)
	socket := &windowSocket{capture: capture, cancel: cancel}
	channel.mu.Lock()
	if len(channel.desktops) != 0 || len(channel.windows) != 0 {
		channel.mu.Unlock()
		cancel()
		capture.Close()
		closeWith(websocket.ClosePolicyViolation, "Another remote view is already open in this portal connection")
		return
	}
	channel.windows[message.ID] = socket
	channel.mu.Unlock()
	if err := state.sendEncrypted(channelID, windowOpenedMessage{Version: protocolVersion, Type: "window_opened", ID: message.ID}); err != nil {
		state.closeWindow(channel, message.ID)
		return
	}
	state.streamWindow(captureContext, channelID, channel, message.ID, socket)
}

func (state *connectionState) streamWindow(ctx context.Context, channelID string, channel *browserChannel, id string, socket *windowSocket) {
	defer state.removeWindow(channel, id, socket)
	ticker := time.NewTicker(150 * time.Millisecond)
	defer ticker.Stop()
	for {
		socket.mu.Lock()
		frame, err := socket.capture.Frame()
		socket.mu.Unlock()
		if err != nil {
			_ = state.sendEncrypted(channelID, windowCloseMessage{Version: protocolVersion, Type: "window_close", ID: id, Code: websocket.CloseTryAgainLater, Reason: boundedReason(err.Error())})
			return
		}
		if len(frame.Data) == 0 || len(frame.Data) > maxWindowFrame || frame.Width < 1 || frame.Height < 1 {
			_ = state.sendEncrypted(channelID, windowCloseMessage{Version: protocolVersion, Type: "window_close", ID: id, Code: websocket.CloseMessageTooBig, Reason: "Selected-window frame is invalid or too large"})
			return
		}
		if err := state.sendEncrypted(channelID, windowFrameMessage{
			Version: protocolVersion, Type: "window_frame", ID: id,
			Width: frame.Width, Height: frame.Height, Data: base64.RawURLEncoding.EncodeToString(frame.Data),
		}); err != nil {
			return
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (state *connectionState) writeWindow(channelID string, channel *browserChannel, message windowInputMessage) {
	channel.mu.Lock()
	socket := channel.windows[message.ID]
	channel.mu.Unlock()
	if socket == nil {
		return
	}
	socket.mu.Lock()
	var err error
	switch message.Kind {
	case "pointer":
		err = socket.capture.Pointer(windowcapture.PointerEvent{Action: message.Action, X: message.X, Y: message.Y, Button: message.Button, DeltaX: message.DeltaX, DeltaY: message.DeltaY})
	case "key":
		err = socket.capture.Key(windowcapture.KeyEvent{Code: message.Code, Down: message.Down, Shift: message.Shift, Ctrl: message.Ctrl, Alt: message.Alt, Meta: message.Meta})
	case "text":
		err = socket.capture.Text(message.Text)
	case "clipboard":
		err = socket.capture.Clipboard(message.Text)
	}
	socket.mu.Unlock()
	if err != nil {
		_ = state.sendEncrypted(channelID, windowCloseMessage{Version: protocolVersion, Type: "window_notice", ID: message.ID, Code: websocket.ClosePolicyViolation, Reason: boundedReason(err.Error())})
	}
}

func validWindowInput(message windowInputMessage) bool {
	switch message.Kind {
	case "pointer":
		if message.Action != "move" && message.Action != "drag" && message.Action != "down" && message.Action != "up" && message.Action != "scroll" {
			return false
		}
		return !math.IsNaN(message.X) && !math.IsNaN(message.Y) && message.X >= 0 && message.X <= 1 && message.Y >= 0 && message.Y <= 1 && message.Button >= 0 && message.Button <= 2 && math.Abs(message.DeltaX) <= 4000 && math.Abs(message.DeltaY) <= 4000
	case "key":
		return len(message.Code) > 0 && len(message.Code) <= 32
	case "text", "clipboard":
		return len(message.Text) > 0 && len(message.Text) <= maxWindowText && utf8.ValidString(message.Text)
	default:
		return false
	}
}

func (state *connectionState) readDesktop(channelID string, channel *browserChannel, desktopID string, socket *desktopSocket) {
	defer state.removeDesktop(channel, desktopID, socket)
	buffer := make([]byte, desktopReadBuffer)
	for {
		count, err := socket.connection.Read(buffer)
		if count > 0 {
			message := desktopDataMessage{
				Version: protocolVersion,
				Type:    "desktop_data",
				ID:      desktopID,
				Data:    base64.RawURLEncoding.EncodeToString(buffer[:count]),
			}
			if sendErr := state.sendEncrypted(channelID, message); sendErr != nil {
				return
			}
		}
		if err != nil {
			reason := "Remote desktop disconnected"
			if !errors.Is(err, io.EOF) && !errors.Is(err, net.ErrClosed) {
				reason = "Remote desktop connection failed"
			}
			_ = state.sendEncrypted(channelID, desktopCloseMessage{Version: protocolVersion, Type: "desktop_close", ID: desktopID, Code: websocket.CloseNormalClosure, Reason: reason})
			return
		}
	}
}

func (state *connectionState) writeDesktop(channelID string, channel *browserChannel, message desktopDataMessage) {
	channel.mu.Lock()
	socket := channel.desktops[message.ID]
	channel.mu.Unlock()
	if socket == nil {
		return
	}
	data, err := base64.RawURLEncoding.DecodeString(message.Data)
	if err != nil || len(data) == 0 || len(data) > maxDesktopInput {
		state.closeDesktop(channel, message.ID)
		_ = state.sendEncrypted(channelID, desktopCloseMessage{Version: protocolVersion, Type: "desktop_close", ID: message.ID, Code: websocket.CloseMessageTooBig, Reason: "Remote desktop input is invalid or too large"})
		return
	}
	socket.writeMu.Lock()
	for len(data) > 0 && err == nil {
		var written int
		written, err = socket.connection.Write(data)
		if written == 0 && err == nil {
			err = io.ErrUnexpectedEOF
			break
		}
		data = data[written:]
	}
	socket.writeMu.Unlock()
	if err != nil {
		state.closeDesktop(channel, message.ID)
		_ = state.sendEncrypted(channelID, desktopCloseMessage{Version: protocolVersion, Type: "desktop_close", ID: message.ID, Code: websocket.CloseInternalServerErr, Reason: "Remote desktop write failed"})
	}
}

func (state *connectionState) authenticateChannel(channelID string, channel *browserChannel, plaintext []byte) {
	var message authenticateMessage
	if json.Unmarshal(plaintext, &message) != nil || len(message.Challenge) < 16 || len(message.Challenge) > 256 {
		state.closeChannel(channelID, websocket.ClosePolicyViolation, "Authentication failed", true)
		return
	}
	body, _ := json.Marshal(map[string]string{"token": state.portalToken})
	request, err := http.NewRequestWithContext(state.ctx, http.MethodPost, state.localOrigin+"/api/login", bytes.NewReader(body))
	if err != nil {
		state.closeChannel(channelID, websocket.CloseInternalServerErr, "Local authentication failed", true)
		return
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Origin", state.localOrigin)
	response, err := channel.httpClient.Do(request)
	if err != nil {
		state.closeChannel(channelID, websocket.CloseTryAgainLater, "Local portal is unavailable", true)
		return
	}
	_ = response.Body.Close()
	if response.StatusCode != http.StatusOK {
		state.closeChannel(channelID, websocket.ClosePolicyViolation, "Local authentication failed", true)
		return
	}
	channel.mu.Lock()
	channel.authenticated = true
	if channel.authTimer != nil {
		channel.authTimer.Stop()
	}
	channel.mu.Unlock()
	_ = state.sendEncrypted(channelID, authenticatedMessage{Version: protocolVersion, Type: "authenticated", Challenge: message.Challenge})
}

func (state *connectionState) handleHTTPRequest(channelID string, channel *browserChannel, message httpRequestMessage) {
	if !allowedHTTPRoute(message.Method, message.Path) || len(message.Body) > maxHTTPBody {
		state.sendHTTPError(channelID, message.ID, http.StatusForbidden, "API route is not allowed")
		return
	}
	if message.Method == http.MethodPost && message.Path == "/api/sessions" {
		state.createInteractiveShell(channelID, message)
		return
	}
	target, err := localURL(state.localOrigin, message.Path)
	if err != nil {
		state.sendHTTPError(channelID, message.ID, http.StatusBadRequest, "Invalid API path")
		return
	}
	request, err := http.NewRequestWithContext(state.ctx, message.Method, target, strings.NewReader(message.Body))
	if err != nil {
		state.sendHTTPError(channelID, message.ID, http.StatusBadRequest, "Invalid API request")
		return
	}
	request.Header.Set("Origin", state.localOrigin)
	if message.Body != "" {
		request.Header.Set("Content-Type", "application/json")
	}
	response, err := channel.httpClient.Do(request)
	if err != nil {
		state.sendHTTPError(channelID, message.ID, http.StatusBadGateway, "Local portal did not respond")
		return
	}
	defer response.Body.Close()
	responseBody, err := io.ReadAll(io.LimitReader(response.Body, maxHTTPResponse+1))
	if err != nil || len(responseBody) > maxHTTPResponse {
		state.sendHTTPError(channelID, message.ID, http.StatusBadGateway, "Local portal response was too large")
		return
	}
	_ = state.sendEncrypted(channelID, httpResponseMessage{
		Version: protocolVersion, Type: "http_response", ID: message.ID,
		Status: response.StatusCode, Body: string(responseBody),
	})
}

func (state *connectionState) createInteractiveShell(channelID string, message httpRequestMessage) {
	request, err := remote.DecodeStartRequest(strings.NewReader(message.Body))
	if err != nil {
		state.sendHTTPError(channelID, message.ID, http.StatusBadRequest, err.Error())
		return
	}
	options, err := request.Options()
	if err != nil {
		state.sendHTTPError(channelID, message.ID, http.StatusBadRequest, err.Error())
		return
	}
	created, err := state.control.Create(state.ctx, options)
	if err != nil {
		state.sendHTTPError(channelID, message.ID, http.StatusBadRequest, err.Error())
		return
	}
	_ = visibleterminal.Open(created.ID)
	body, err := json.Marshal(created)
	if err != nil {
		state.sendHTTPError(channelID, message.ID, http.StatusInternalServerError, "could not encode the new session")
		return
	}
	_ = state.sendEncrypted(channelID, httpResponseMessage{
		Version: protocolVersion, Type: "http_response", ID: message.ID,
		Status: http.StatusCreated, Body: string(body),
	})
}

func (state *connectionState) sendHTTPError(channelID, requestID string, status int, message string) {
	body, _ := json.Marshal(map[string]string{"error": message})
	_ = state.sendEncrypted(channelID, httpResponseMessage{
		Version: protocolVersion, Type: "http_response", ID: requestID, Status: status, Body: string(body),
	})
}

func (state *connectionState) openLocalSocket(channelID string, channel *browserChannel, message terminalOpenMessage) {
	target, err := localWebSocketURL(state.localOrigin, "/ws/sessions/"+message.SessionID)
	if err != nil {
		_ = state.sendEncrypted(channelID, terminalCloseMessage{Version: protocolVersion, Type: "terminal_close", ID: message.ID, Code: websocket.ClosePolicyViolation, Reason: "Invalid terminal path"})
		return
	}
	headers := http.Header{"Origin": {state.localOrigin}}
	if originURL, parseErr := url.Parse(state.localOrigin); parseErr == nil {
		for _, cookie := range channel.httpClient.Jar.Cookies(originURL) {
			headers.Add("Cookie", cookie.String())
		}
	}
	connection, response, err := (&websocket.Dialer{HandshakeTimeout: 5 * time.Second}).DialContext(state.ctx, target, headers)
	if err != nil {
		code := websocket.CloseTryAgainLater
		reason := "Local terminal is unavailable"
		if response != nil {
			_ = response.Body.Close()
			if response.StatusCode == http.StatusUnauthorized {
				code = websocket.ClosePolicyViolation
				reason = "Portal authentication expired"
			}
		}
		_ = state.sendEncrypted(channelID, terminalCloseMessage{Version: protocolVersion, Type: "terminal_close", ID: message.ID, Code: code, Reason: reason})
		return
	}
	connection.SetReadLimit(maxTerminalOutput)
	socket := &localSocket{connection: connection}
	channel.mu.Lock()
	if previous := channel.sockets[message.ID]; previous != nil {
		_ = previous.connection.Close()
	}
	channel.sockets[message.ID] = socket
	channel.mu.Unlock()
	_ = state.sendEncrypted(channelID, terminalOpenedMessage{Version: protocolVersion, Type: "terminal_opened", ID: message.ID})
	go state.readLocalSocket(channelID, channel, message.ID, socket)
}

func (state *connectionState) readLocalSocket(channelID string, channel *browserChannel, terminalID string, socket *localSocket) {
	defer state.removeLocalSocket(channel, terminalID, socket)
	for {
		kind, data, err := socket.connection.ReadMessage()
		if err != nil {
			code := websocket.CloseNormalClosure
			reason := "Terminal disconnected"
			var closeError *websocket.CloseError
			if errors.As(err, &closeError) {
				code = normalizeCloseCode(closeError.Code)
				reason = boundedReason(closeError.Text)
			}
			_ = state.sendEncrypted(channelID, terminalCloseMessage{Version: protocolVersion, Type: "terminal_close", ID: terminalID, Code: code, Reason: reason})
			return
		}
		if kind != websocket.TextMessage && kind != websocket.BinaryMessage {
			continue
		}
		if len(data) > maxTerminalOutput {
			_ = state.sendEncrypted(channelID, terminalCloseMessage{Version: protocolVersion, Type: "terminal_close", ID: terminalID, Code: websocket.CloseMessageTooBig, Reason: "Terminal output message is too large"})
			return
		}
		message := terminalDataMessage{Version: protocolVersion, Type: "terminal_data", ID: terminalID, Binary: kind == websocket.BinaryMessage}
		if message.Binary {
			message.Data = base64.RawURLEncoding.EncodeToString(data)
		} else {
			message.Data = string(data)
		}
		if err := state.sendEncrypted(channelID, message); err != nil {
			return
		}
	}
}

func (state *connectionState) writeLocalSocket(channelID string, channel *browserChannel, message terminalDataMessage) {
	channel.mu.Lock()
	socket := channel.sockets[message.ID]
	channel.mu.Unlock()
	if socket == nil {
		return
	}
	var data []byte
	var err error
	if message.Binary {
		data, err = base64.RawURLEncoding.DecodeString(message.Data)
	} else {
		data = []byte(message.Data)
	}
	if err != nil || len(data) > maxTerminalInput {
		state.closeLocalSocket(channel, message.ID, websocket.CloseMessageTooBig, "Terminal input is too large")
		_ = state.sendEncrypted(channelID, terminalCloseMessage{Version: protocolVersion, Type: "terminal_close", ID: message.ID, Code: websocket.CloseMessageTooBig, Reason: "Terminal input is too large"})
		return
	}
	kind := websocket.TextMessage
	if message.Binary {
		kind = websocket.BinaryMessage
	}
	socket.writeMu.Lock()
	err = socket.connection.WriteMessage(kind, data)
	socket.writeMu.Unlock()
	if err != nil {
		state.closeLocalSocket(channel, message.ID, websocket.CloseInternalServerErr, "Terminal write failed")
	}
}

func defaultUploadDirectory() string {
	home, err := os.UserHomeDir()
	if err != nil || strings.TrimSpace(home) == "" {
		return ""
	}
	return filepath.Join(home, "Downloads", "Termlinks Uploads")
}

func validUploadName(name string) bool {
	if name == "" || len(name) > maxUploadNameBytes || !utf8.ValidString(name) || name == "." || name == ".." {
		return false
	}
	if filepath.Base(name) != name || strings.ContainsAny(name, `/\\`) {
		return false
	}
	for _, character := range name {
		if unicode.IsControl(character) {
			return false
		}
	}
	return true
}

func (state *connectionState) uploadError(channelID, id, reason string) {
	_ = state.sendEncrypted(channelID, fileUploadResponse{
		Version: protocolVersion, Type: "file_upload_error", ID: id, Reason: boundedReason(reason),
	})
}

func (state *connectionState) startFileUpload(channelID string, channel *browserChannel, message fileUploadMessage) {
	if !validUploadName(message.Name) || message.Size < 0 || message.Size > maxUploadSize {
		state.uploadError(channelID, message.ID, "Invalid file name or size (maximum 100 MiB)")
		return
	}
	if state.uploadDirectory == "" {
		state.uploadError(channelID, message.ID, "The computer upload directory is unavailable")
		return
	}
	channel.mu.Lock()
	if _, exists := channel.uploads[message.ID]; exists || len(channel.uploads) >= 2 {
		channel.mu.Unlock()
		state.uploadError(channelID, message.ID, "Too many active file uploads")
		return
	}
	channel.mu.Unlock()
	if err := os.MkdirAll(state.uploadDirectory, 0o700); err != nil {
		state.uploadError(channelID, message.ID, "Could not create the upload directory")
		return
	}
	_ = os.Chmod(state.uploadDirectory, 0o700)
	file, err := os.CreateTemp(state.uploadDirectory, ".termlinks-upload-*")
	if err != nil {
		state.uploadError(channelID, message.ID, "Could not create a temporary upload file")
		return
	}
	_ = file.Chmod(0o600)
	upload := &fileUpload{file: file, tempPath: file.Name(), name: message.Name, size: message.Size}
	channel.mu.Lock()
	if _, exists := channel.uploads[message.ID]; exists || len(channel.uploads) >= 2 {
		channel.mu.Unlock()
		cleanupFileUpload(upload)
		state.uploadError(channelID, message.ID, "Too many active file uploads")
		return
	}
	channel.uploads[message.ID] = upload
	channel.mu.Unlock()
	_ = state.sendEncrypted(channelID, fileUploadResponse{Version: protocolVersion, Type: "file_upload_ready", ID: message.ID, Total: message.Size})
}

func (state *connectionState) writeFileUpload(channelID string, channel *browserChannel, message fileUploadMessage) {
	channel.mu.Lock()
	upload := channel.uploads[message.ID]
	channel.mu.Unlock()
	if upload == nil {
		state.uploadError(channelID, message.ID, "File upload is not active")
		return
	}
	data, err := base64.RawURLEncoding.DecodeString(message.Data)
	upload.mu.Lock()
	invalid := upload.closed || err != nil || len(data) > maxUploadChunk || message.Offset != upload.received || upload.received+int64(len(data)) > upload.size
	if !invalid {
		_, err = upload.file.Write(data)
		if err == nil {
			upload.received += int64(len(data))
		}
	}
	received, total := upload.received, upload.size
	upload.mu.Unlock()
	if invalid || err != nil {
		state.removeFileUpload(channel, message.ID, upload)
		cleanupFileUpload(upload)
		state.uploadError(channelID, message.ID, "Invalid file chunk")
		return
	}
	_ = state.sendEncrypted(channelID, fileUploadResponse{
		Version: protocolVersion, Type: "file_upload_progress", ID: message.ID, Received: received, Total: total,
	})
}

func (state *connectionState) finishFileUpload(channelID string, channel *browserChannel, id string) {
	channel.mu.Lock()
	upload := channel.uploads[id]
	if upload != nil {
		delete(channel.uploads, id)
	}
	channel.mu.Unlock()
	if upload == nil {
		state.uploadError(channelID, id, "File upload is not active")
		return
	}
	upload.mu.Lock()
	if upload.closed || upload.received != upload.size {
		upload.mu.Unlock()
		cleanupFileUpload(upload)
		state.uploadError(channelID, id, "File upload is incomplete")
		return
	}
	if err := upload.file.Sync(); err != nil {
		upload.mu.Unlock()
		cleanupFileUpload(upload)
		state.uploadError(channelID, id, "Could not save the uploaded file")
		return
	}
	err := upload.file.Close()
	upload.closed = true
	upload.mu.Unlock()
	if err != nil {
		cleanupFileUpload(upload)
		state.uploadError(channelID, id, "Could not save the uploaded file")
		return
	}
	finalPath, err := reserveUploadPath(upload.tempPath, state.uploadDirectory, upload.name)
	if err != nil {
		cleanupFileUpload(upload)
		state.uploadError(channelID, id, "Could not finalize the uploaded file")
		return
	}
	_ = state.sendEncrypted(channelID, fileUploadResponse{
		Version: protocolVersion, Type: "file_upload_complete", ID: id, Received: upload.received, Total: upload.size, Path: finalPath,
	})
}

func reserveUploadPath(tempPath, directory, name string) (string, error) {
	extension := filepath.Ext(name)
	stem := strings.TrimSuffix(name, extension)
	for suffix := 0; suffix < 10_000; suffix++ {
		candidateName := name
		if suffix > 0 {
			candidateName = fmt.Sprintf("%s (%d)%s", stem, suffix, extension)
		}
		candidate := filepath.Join(directory, candidateName)
		err := os.Link(tempPath, candidate)
		if err == nil {
			if removeErr := os.Remove(tempPath); removeErr != nil {
				_ = os.Remove(candidate)
				return "", removeErr
			}
			return candidate, nil
		}
		if !os.IsExist(err) {
			return "", err
		}
	}
	return "", errors.New("too many files use that name")
}

func (state *connectionState) removeFileUpload(channel *browserChannel, id string, expected *fileUpload) {
	channel.mu.Lock()
	if channel.uploads[id] == expected {
		delete(channel.uploads, id)
	}
	channel.mu.Unlock()
}

func (state *connectionState) cancelFileUpload(channel *browserChannel, id string) {
	channel.mu.Lock()
	upload := channel.uploads[id]
	delete(channel.uploads, id)
	channel.mu.Unlock()
	cleanupFileUpload(upload)
}

func cleanupFileUpload(upload *fileUpload) {
	if upload == nil {
		return
	}
	upload.mu.Lock()
	if !upload.closed {
		_ = upload.file.Close()
		upload.closed = true
	}
	tempPath := upload.tempPath
	upload.mu.Unlock()
	if tempPath != "" {
		_ = os.Remove(tempPath)
	}
}

func (state *connectionState) sendEncrypted(channelID string, value any) error {
	state.channelsMu.Lock()
	channel := state.channels[channelID]
	state.channelsMu.Unlock()
	if channel == nil {
		return errors.New("encrypted channel is closed")
	}
	channel.sendMu.Lock()
	defer channel.sendMu.Unlock()
	channel.mu.Lock()
	sequence := channel.sendSeq
	channel.sendSeq++
	channel.mu.Unlock()
	packet, err := encryptPacket(state.key, channelID, "connector", sequence, value)
	if err != nil {
		return err
	}
	if len(packet) > maxEncryptedPacket {
		return errors.New("encrypted packet is too large")
	}
	return state.sendOuter(encryptedOuterMessage{Type: "e2e_to_browser", ID: channelID, Data: packet})
}

func (state *connectionState) sendOuter(value any) error {
	data, err := json.Marshal(value)
	if err != nil {
		return err
	}
	select {
	case <-state.ctx.Done():
		return state.ctx.Err()
	case state.outgoing <- data:
		return nil
	}
}

func (state *connectionState) closeChannel(id string, code int, reason string, notifyRelay bool) {
	state.channelsMu.Lock()
	channel := state.channels[id]
	delete(state.channels, id)
	state.channelsMu.Unlock()
	if channel != nil {
		channel.mu.Lock()
		if channel.authTimer != nil {
			channel.authTimer.Stop()
		}
		sockets := channel.sockets
		channel.sockets = make(map[string]*localSocket)
		desktops := channel.desktops
		channel.desktops = make(map[string]*desktopSocket)
		windows := channel.windows
		channel.windows = make(map[string]*windowSocket)
		uploads := channel.uploads
		channel.uploads = make(map[string]*fileUpload)
		channel.mu.Unlock()
		for _, socket := range sockets {
			_ = socket.connection.Close()
		}
		for _, socket := range desktops {
			_ = socket.connection.Close()
		}
		for _, socket := range windows {
			socket.cancel()
		}
		for _, upload := range uploads {
			cleanupFileUpload(upload)
		}
	}
	if notifyRelay {
		_ = state.sendOuter(channelCloseMessage{Type: "channel_close", ID: id, Code: code, Reason: reason})
	}
}

func (state *connectionState) closeDesktop(channel *browserChannel, id string) {
	channel.mu.Lock()
	socket := channel.desktops[id]
	delete(channel.desktops, id)
	channel.mu.Unlock()
	if socket != nil {
		_ = socket.connection.Close()
	}
}

func (state *connectionState) removeDesktop(channel *browserChannel, id string, expected *desktopSocket) {
	channel.mu.Lock()
	if channel.desktops[id] == expected {
		delete(channel.desktops, id)
	}
	channel.mu.Unlock()
	_ = expected.connection.Close()
}

func (state *connectionState) closeWindow(channel *browserChannel, id string) {
	channel.mu.Lock()
	socket := channel.windows[id]
	delete(channel.windows, id)
	channel.mu.Unlock()
	if socket != nil {
		socket.cancel()
	}
}

func (state *connectionState) removeWindow(channel *browserChannel, id string, expected *windowSocket) {
	channel.mu.Lock()
	if channel.windows[id] == expected {
		delete(channel.windows, id)
	}
	channel.mu.Unlock()
	expected.cancel()
	expected.mu.Lock()
	expected.capture.Close()
	expected.mu.Unlock()
}

func (state *connectionState) closeAllChannels() {
	state.channelsMu.Lock()
	ids := make([]string, 0, len(state.channels))
	for id := range state.channels {
		ids = append(ids, id)
	}
	state.channelsMu.Unlock()
	for _, id := range ids {
		state.closeChannel(id, websocket.CloseServiceRestart, "Connector disconnected", false)
	}
}

func (state *connectionState) closeLocalSocket(channel *browserChannel, id string, code int, reason string) {
	channel.mu.Lock()
	socket := channel.sockets[id]
	delete(channel.sockets, id)
	channel.mu.Unlock()
	if socket == nil {
		return
	}
	socket.writeMu.Lock()
	_ = socket.connection.WriteControl(websocket.CloseMessage, websocket.FormatCloseMessage(code, reason), time.Now().Add(2*time.Second))
	_ = socket.connection.Close()
	socket.writeMu.Unlock()
}

func (state *connectionState) removeLocalSocket(channel *browserChannel, id string, expected *localSocket) {
	channel.mu.Lock()
	if channel.sockets[id] == expected {
		delete(channel.sockets, id)
	}
	channel.mu.Unlock()
	_ = expected.connection.Close()
}

func deriveKey(token string) [32]byte {
	return sha256.Sum256([]byte(keyContext + token))
}

func encryptPacket(key [32]byte, channelID, direction string, sequence uint32, value any) (string, error) {
	plaintext, err := json.Marshal(value)
	if err != nil {
		return "", err
	}
	block, err := aes.NewCipher(key[:])
	if err != nil {
		return "", err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return "", err
	}
	sequenceBytes := make([]byte, 4)
	binary.BigEndian.PutUint32(sequenceBytes, sequence)
	aad := append([]byte(aadContext+channelID+":"+direction+":"), sequenceBytes...)
	sealed := gcm.Seal(nil, nonce, plaintext, aad)
	packet := append(sequenceBytes, nonce...)
	packet = append(packet, sealed...)
	return base64.RawURLEncoding.EncodeToString(packet), nil
}

func decryptPacket(key [32]byte, channelID, direction string, expectedSequence uint32, packet string) ([]byte, error) {
	encoded, err := base64.RawURLEncoding.DecodeString(packet)
	if err != nil {
		return nil, err
	}
	block, err := aes.NewCipher(key[:])
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	if len(encoded) < 4+gcm.NonceSize()+gcm.Overhead() {
		return nil, errors.New("encrypted packet is too short")
	}
	sequenceBytes := encoded[:4]
	if binary.BigEndian.Uint32(sequenceBytes) != expectedSequence {
		return nil, errors.New("encrypted packet sequence is invalid")
	}
	nonce := encoded[4 : 4+gcm.NonceSize()]
	ciphertext := encoded[4+gcm.NonceSize():]
	aad := append([]byte(aadContext+channelID+":"+direction+":"), sequenceBytes...)
	return gcm.Open(nil, nonce, ciphertext, aad)
}

func connectorURL(relayURL string) (string, error) {
	parsed, err := url.Parse(strings.TrimRight(relayURL, "/"))
	if err != nil || parsed.Scheme != "https" || parsed.Host == "" {
		return "", errors.New("invalid relay URL")
	}
	parsed.Scheme = "wss"
	parsed.Path = "/connector"
	parsed.RawQuery = ""
	parsed.Fragment = ""
	return parsed.String(), nil
}

func localURL(origin, requestPath string) (string, error) {
	parsed, err := url.ParseRequestURI(requestPath)
	if err != nil || parsed.IsAbs() || !strings.HasPrefix(parsed.Path, "/") {
		return "", errors.New("invalid local path")
	}
	return strings.TrimRight(origin, "/") + parsed.RequestURI(), nil
}

func localWebSocketURL(origin, requestPath string) (string, error) {
	target, err := localURL(origin, requestPath)
	if err != nil {
		return "", err
	}
	parsed, _ := url.Parse(target)
	parsed.Scheme = "ws"
	return parsed.String(), nil
}

func allowedHTTPRoute(method, requestPath string) bool {
	parsed, err := url.ParseRequestURI(requestPath)
	if err != nil || parsed.RawQuery != "" {
		return false
	}
	path := parsed.Path
	if method == http.MethodPost && path == "/api/logout" {
		return true
	}
	if method == http.MethodGet && (path == "/api/me" || path == "/api/sessions") {
		return true
	}
	if method == http.MethodPost && path == "/api/sessions" {
		return true
	}
	if method == http.MethodGet && (path == "/api/agents" || path == "/api/projects/suggestions" || path == "/api/workflows") {
		return true
	}
	if method == http.MethodPost && (path == "/api/agents/refresh" || path == "/api/workflows/compile" || path == "/api/workflows") {
		return true
	}
	if strings.HasPrefix(path, "/api/workflows/") {
		parts := strings.Split(strings.TrimPrefix(path, "/api/workflows/"), "/")
		if len(parts) == 1 && method == http.MethodGet {
			return validCoordinatorID(parts[0])
		}
		if len(parts) == 2 && method == http.MethodPost && parts[1] == "cancel" {
			return validCoordinatorID(parts[0])
		}
		if len(parts) == 4 && method == http.MethodPost && parts[1] == "stages" && parts[3] == "input" {
			return validCoordinatorID(parts[0]) && validCoordinatorID(parts[2])
		}
		return false
	}
	if method != http.MethodPost || !strings.HasPrefix(path, "/api/sessions/") || !strings.HasSuffix(path, "/stop") {
		return false
	}
	id := strings.TrimSuffix(strings.TrimPrefix(path, "/api/sessions/"), "/stop")
	return validSessionID(id)
}

func validCoordinatorID(id string) bool {
	if len(id) != 24 {
		return false
	}
	for _, character := range id {
		if (character < '0' || character > '9') && (character < 'a' || character > 'f') {
			return false
		}
	}
	return true
}

func validSessionID(id string) bool {
	if len(id) != 32 {
		return false
	}
	for _, character := range id {
		if (character < '0' || character > '9') && (character < 'a' || character > 'f') {
			return false
		}
	}
	return true
}

func validMessageID(id string) bool {
	if len(id) != 36 {
		return false
	}
	for index, character := range id {
		if index == 8 || index == 13 || index == 18 || index == 23 {
			if character != '-' {
				return false
			}
			continue
		}
		if (character < '0' || character > '9') && (character < 'a' || character > 'f') {
			return false
		}
	}
	return true
}

func normalizeCloseCode(code int) int {
	if code < 1000 || code > 4999 || code == 1004 || code == 1005 || code == 1006 || code == 1015 {
		return websocket.CloseServiceRestart
	}
	return code
}

func boundedReason(reason string) string {
	data := []byte(reason)
	if len(data) <= 120 {
		return reason
	}
	data = data[:120]
	for !utf8.Valid(data) && len(data) > 0 {
		data = data[:len(data)-1]
	}
	return string(data)
}
