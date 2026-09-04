package client

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"strconv"
	"sync"
	"syscall"
	"time"

	"github.com/gorilla/websocket"
	"golang.org/x/term"

	"termlinks/backend/internal/session"
)

type Client struct {
	socket string
	http   *http.Client
}

func New(socket string) *Client {
	dialer := &net.Dialer{Timeout: 2 * time.Second}
	transport := &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return dialer.DialContext(ctx, "unix", socket)
		},
	}
	return &Client{
		socket: socket,
		http:   &http.Client{Transport: transport, Timeout: 3 * time.Second},
	}
}

func (c *Client) Healthy(ctx context.Context) bool {
	request, _ := http.NewRequestWithContext(ctx, http.MethodGet, "http://termlinks.local/v1/health", nil)
	response, err := c.http.Do(request)
	if err != nil {
		return false
	}
	defer response.Body.Close()
	return response.StatusCode == http.StatusOK
}

func (c *Client) Create(ctx context.Context, options session.StartOptions) (session.Info, error) {
	payload, err := json.Marshal(options)
	if err != nil {
		return session.Info{}, err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://termlinks.local/v1/sessions", bytes.NewReader(payload))
	if err != nil {
		return session.Info{}, err
	}
	request.Header.Set("Content-Type", "application/json")
	var created session.Info
	if err := c.doJSON(request, &created); err != nil {
		return session.Info{}, err
	}
	return created, nil
}

func (c *Client) List(ctx context.Context) ([]session.Info, error) {
	request, _ := http.NewRequestWithContext(ctx, http.MethodGet, "http://termlinks.local/v1/sessions", nil)
	var output struct {
		Sessions []session.Info `json:"sessions"`
	}
	if err := c.doJSON(request, &output); err != nil {
		return nil, err
	}
	return output.Sessions, nil
}

func (c *Client) Stop(ctx context.Context, id string) error {
	request, _ := http.NewRequestWithContext(ctx, http.MethodPost, "http://termlinks.local/v1/sessions/"+url.PathEscape(id)+"/stop", nil)
	return c.doJSON(request, nil)
}

func (c *Client) Show(ctx context.Context, id string) (string, error) {
	return c.changeViewer(ctx, id, "show")
}

func (c *Client) Hide(ctx context.Context, id string) (string, error) {
	return c.changeViewer(ctx, id, "hide")
}

func (c *Client) changeViewer(ctx context.Context, id, action string) (string, error) {
	request, _ := http.NewRequestWithContext(ctx, http.MethodPost, "http://termlinks.local/v1/sessions/"+url.PathEscape(id)+"/"+action, nil)
	var output struct {
		Viewer string `json:"viewer"`
	}
	if err := c.doJSON(request, &output); err != nil {
		return "", err
	}
	return output.Viewer, nil
}

// AttachResult describes how an attached session finished. AlreadyExited marks
// a session that was over before the attach started, which is a normal state
// rather than a failure of the attach itself.
type AttachResult struct {
	ExitCode      int
	Signal        string
	AlreadyExited bool
}

// Describe renders the session outcome for humans, e.g. "killed by SIGTERM".
func (r AttachResult) Describe() string {
	if r.Signal != "" {
		return "killed by " + r.Signal
	}
	return fmt.Sprintf("exit code %d", r.ExitCode)
}

func (c *Client) Attach(ctx context.Context, id string) (AttachResult, error) {
	return c.attach(ctx, id, "attach")
}

// AttachViewer is reserved for a native terminal window opened by the daemon.
// Its separate endpoint lets the daemon hide only managed viewers without
// disturbing browser clients or ordinary local attachments.
func (c *Client) AttachViewer(ctx context.Context, id string) (AttachResult, error) {
	return c.attach(ctx, id, "viewer")
}

func (c *Client) attach(ctx context.Context, id, endpoint string) (AttachResult, error) {
	netDialer := &net.Dialer{Timeout: 3 * time.Second}
	dialer := websocket.Dialer{
		NetDialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return netDialer.DialContext(ctx, "unix", c.socket)
		},
		HandshakeTimeout: 3 * time.Second,
	}
	connection, response, err := dialer.DialContext(ctx, "ws://termlinks.local/v1/sessions/"+url.PathEscape(id)+"/"+endpoint, nil)
	if err != nil {
		if response != nil {
			return AttachResult{ExitCode: 1}, fmt.Errorf("attach failed with HTTP %d", response.StatusCode)
		}
		return AttachResult{ExitCode: 1}, err
	}
	defer connection.Close()
	var writeMu sync.Mutex

	inputDone := make(chan struct{})
	if term.IsTerminal(int(os.Stdin.Fd())) {
		state, err := term.MakeRaw(int(os.Stdin.Fd()))
		if err != nil {
			return AttachResult{ExitCode: 1}, fmt.Errorf("enable raw terminal input: %w", err)
		}
		defer term.Restore(int(os.Stdin.Fd()), state)
		sendResize(connection, &writeMu)
		winch := make(chan os.Signal, 1)
		signal.Notify(winch, syscall.SIGWINCH)
		defer signal.Stop(winch)
		go func() {
			for {
				select {
				case <-winch:
					sendResize(connection, &writeMu)
				case <-inputDone:
					return
				}
			}
		}()
	}
	defer close(inputDone)

	go func() {
		buffer := make([]byte, 4096)
		for {
			n, err := os.Stdin.Read(buffer)
			if n > 0 {
				writeMu.Lock()
				writeErr := connection.WriteMessage(websocket.BinaryMessage, buffer[:n])
				writeMu.Unlock()
				if writeErr != nil {
					return
				}
			}
			if err != nil {
				return
			}
		}
	}()

	for {
		messageType, data, err := connection.ReadMessage()
		if err != nil {
			var closeErr *websocket.CloseError
			if errors.As(err, &closeErr) && closeErr.Code == websocket.CloseNormalClosure {
				return AttachResult{}, nil
			}
			return AttachResult{ExitCode: 1}, err
		}
		if messageType == websocket.BinaryMessage {
			if _, err := os.Stdout.Write(data); err != nil {
				return AttachResult{ExitCode: 1}, err
			}
			continue
		}
		var status struct {
			Type          string `json:"type"`
			Running       bool   `json:"running"`
			ExitCode      *int   `json:"exitCode"`
			Signal        string `json:"signal"`
			AlreadyExited bool   `json:"alreadyExited"`
		}
		if json.Unmarshal(data, &status) == nil && status.Type == "status" && !status.Running {
			result := AttachResult{Signal: status.Signal, AlreadyExited: status.AlreadyExited}
			if status.ExitCode != nil {
				result.ExitCode = *status.ExitCode
			}
			return result, nil
		}
	}
}

func (c *Client) doJSON(request *http.Request, output any) error {
	response, err := c.http.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		data, _ := io.ReadAll(io.LimitReader(response.Body, 8192))
		var serverError struct {
			Error string `json:"error"`
		}
		if json.Unmarshal(data, &serverError) == nil && serverError.Error != "" {
			return errors.New(serverError.Error)
		}
		return fmt.Errorf("request failed with HTTP %d", response.StatusCode)
	}
	if output == nil || response.StatusCode == http.StatusNoContent {
		return nil
	}
	return json.NewDecoder(response.Body).Decode(output)
}

func sendResize(connection *websocket.Conn, writeMu *sync.Mutex) {
	cols, rows, err := term.GetSize(int(os.Stdin.Fd()))
	if err != nil || cols < 20 || rows < 5 {
		return
	}
	payload := []byte(`{"type":"resize","cols":` + strconv.Itoa(cols) + `,"rows":` + strconv.Itoa(rows) + `}`)
	writeMu.Lock()
	_ = connection.WriteMessage(websocket.TextMessage, payload)
	writeMu.Unlock()
}
