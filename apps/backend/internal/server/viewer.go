package server

import (
	"errors"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

const viewerOpenTimeout = 15 * time.Second

var errViewerUnsupported = errors.New("native terminal viewing is unavailable on this computer")

type viewerState struct {
	desired   bool
	openingBy time.Time
	clients   map[*websocket.Conn]struct{}
}

type NativeViewer struct {
	Open  func(string) error
	Close func(string) error
}

// viewerController owns only native viewers. Browser sockets and ordinary
// `termlinks attach` clients are deliberately not registered here, so Hide can
// never detach another client or stop the underlying PTY.
type viewerController struct {
	launch func(string) error
	close  func(string) error
	mu     sync.Mutex
	states map[string]*viewerState
}

func newViewerController(native NativeViewer) *viewerController {
	return &viewerController{launch: native.Open, close: native.Close, states: make(map[string]*viewerState)}
}

func (v *viewerController) status(id string) string {
	v.mu.Lock()
	defer v.mu.Unlock()
	if v.launch == nil {
		return "unsupported"
	}
	state := v.states[id]
	if state == nil {
		return "hidden"
	}
	if v.expireOpening(id, state) {
		return "hidden"
	}
	if len(state.clients) > 0 {
		return "visible"
	}
	if state.desired {
		return "opening"
	}
	delete(v.states, id)
	return "hidden"
}

func (v *viewerController) show(id string) (string, error) {
	v.mu.Lock()
	if v.launch == nil {
		v.mu.Unlock()
		return "unsupported", errViewerUnsupported
	}
	state := v.state(id)
	if v.expireOpening(id, state) {
		state = v.state(id)
	}
	if len(state.clients) > 0 {
		v.mu.Unlock()
		return "visible", nil
	}
	if state.desired {
		v.mu.Unlock()
		return "opening", nil
	}
	state.desired = true
	state.openingBy = time.Now().Add(viewerOpenTimeout)
	v.mu.Unlock()

	if err := v.launch(id); err != nil {
		v.mu.Lock()
		state := v.state(id)
		state.desired = false
		state.openingBy = time.Time{}
		if len(state.clients) == 0 {
			delete(v.states, id)
		}
		v.mu.Unlock()
		return "hidden", err
	}
	v.mu.Lock()
	state = v.state(id)
	stillDesired := state.desired
	v.mu.Unlock()
	if !stillDesired {
		if v.close != nil {
			_ = v.close(id)
		}
		return "hidden", nil
	}
	return "opening", nil
}

func (v *viewerController) hide(id string) (string, error) {
	v.mu.Lock()
	state := v.states[id]
	if state == nil {
		v.mu.Unlock()
		if v.close != nil {
			if err := v.close(id); err != nil {
				return "hidden", err
			}
		}
		if v.launch == nil {
			return "unsupported", nil
		}
		return "hidden", nil
	}
	state.desired = false
	state.openingBy = time.Time{}
	clients := make([]*websocket.Conn, 0, len(state.clients))
	for connection := range state.clients {
		clients = append(clients, connection)
	}
	delete(v.states, id)
	v.mu.Unlock()

	for _, connection := range clients {
		_ = connection.WriteControl(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.CloseNormalClosure, "native viewer hidden"), time.Now().Add(time.Second))
		_ = connection.Close()
	}
	if v.close != nil {
		if err := v.close(id); err != nil {
			return "hidden", err
		}
	}
	if v.launch == nil {
		return "unsupported", nil
	}
	return "hidden", nil
}

func (v *viewerController) register(id string, connection *websocket.Conn) bool {
	v.mu.Lock()
	defer v.mu.Unlock()
	if v.launch == nil {
		return false
	}
	state := v.state(id)
	if v.expireOpening(id, state) {
		return false
	}
	if !state.desired {
		return false
	}
	state.openingBy = time.Time{}
	if state.clients == nil {
		state.clients = make(map[*websocket.Conn]struct{})
	}
	state.clients[connection] = struct{}{}
	return true
}

func (v *viewerController) unregister(id string, connection *websocket.Conn) {
	v.mu.Lock()
	state := v.states[id]
	if state == nil {
		v.mu.Unlock()
		return
	}
	if _, registered := state.clients[connection]; !registered {
		v.mu.Unlock()
		return
	}
	delete(state.clients, connection)
	shouldClose := false
	if len(state.clients) == 0 {
		state.desired = false
		state.openingBy = time.Time{}
		delete(v.states, id)
		shouldClose = true
	}
	v.mu.Unlock()
	if shouldClose && v.close != nil {
		_ = v.close(id)
	}
}

func (v *viewerController) state(id string) *viewerState {
	state := v.states[id]
	if state == nil {
		state = &viewerState{}
		v.states[id] = state
	}
	return state
}

// expireOpening runs with v.mu held. Keeping the controller lock while Close
// removes the timed-out OS window prevents a new Show from overwriting its
// platform handle before the old one is closed.
func (v *viewerController) expireOpening(id string, state *viewerState) bool {
	if state.desired && len(state.clients) == 0 && !state.openingBy.IsZero() && time.Now().After(state.openingBy) {
		state.desired = false
		state.openingBy = time.Time{}
		if v.close != nil {
			_ = v.close(id)
		}
		delete(v.states, id)
		return true
	}
	return false
}
