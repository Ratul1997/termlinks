package windowcapture

import "errors"

var ErrUnsupported = errors.New("selected-window sharing requires macOS 14 or newer")

type Source struct {
	ID          uint32 `json:"id"`
	Title       string `json:"title"`
	Application string `json:"application"`
	BundleID    string `json:"bundleId,omitempty"`
	Width       int    `json:"width"`
	Height      int    `json:"height"`
}

type Permissions struct {
	Supported       bool `json:"supported"`
	ScreenRecording bool `json:"screenRecording"`
	Accessibility   bool `json:"accessibility"`
}

type Frame struct {
	Data   []byte
	Width  int
	Height int
}

type PointerEvent struct {
	Action string
	X      float64
	Y      float64
	Button int
	DeltaX float64
	DeltaY float64
}

type KeyEvent struct {
	Code  string
	Down  bool
	Shift bool
	Ctrl  bool
	Alt   bool
	Meta  bool
}
