//go:build darwin && cgo

package windowcapture

/*
#cgo CFLAGS: -fblocks
#cgo LDFLAGS: -framework Foundation -framework AppKit -framework CoreGraphics -framework ScreenCaptureKit -framework ImageIO -framework ApplicationServices
#include <stdlib.h>
#include "windowcapture_darwin.h"
*/
import "C"

import (
	"encoding/json"
	"errors"
	"fmt"
	"runtime"
	"unsafe"
)

type Capture struct {
	handle unsafe.Pointer
}

func PermissionStatus() Permissions {
	return Permissions{
		Supported:       C.tl_window_supported() != 0,
		ScreenRecording: C.tl_screen_recording_allowed() != 0,
		Accessibility:   C.tl_accessibility_allowed() != 0,
	}
}

func RequestPermissions() Permissions {
	C.tl_request_permissions()
	return PermissionStatus()
}

func List() ([]Source, error) {
	var raw *C.char
	var nativeError *C.char
	if C.tl_window_sources(&raw, &nativeError) == 0 {
		return nil, nativeFailure(nativeError)
	}
	defer C.free(unsafe.Pointer(raw))
	var sources []Source
	if err := json.Unmarshal([]byte(C.GoString(raw)), &sources); err != nil {
		return nil, fmt.Errorf("decode macOS window list: %w", err)
	}
	return sources, nil
}

func Open(id uint32, maxWidth, maxHeight int) (*Capture, error) {
	var nativeError *C.char
	handle := C.tl_window_open(C.uint32_t(id), C.int(maxWidth), C.int(maxHeight), &nativeError)
	if handle == nil {
		return nil, nativeFailure(nativeError)
	}
	capture := &Capture{handle: handle}
	runtime.SetFinalizer(capture, (*Capture).Close)
	return capture, nil
}

func (capture *Capture) Frame() (Frame, error) {
	if capture == nil || capture.handle == nil {
		return Frame{}, errors.New("window capture is closed")
	}
	var data *C.uchar
	var length C.size_t
	var width C.int
	var height C.int
	var nativeError *C.char
	if C.tl_window_frame(capture.handle, &data, &length, &width, &height, &nativeError) == 0 {
		return Frame{}, nativeFailure(nativeError)
	}
	defer C.free(unsafe.Pointer(data))
	return Frame{Data: C.GoBytes(unsafe.Pointer(data), C.int(length)), Width: int(width), Height: int(height)}, nil
}

func (capture *Capture) Pointer(event PointerEvent) error {
	if capture == nil || capture.handle == nil {
		return errors.New("window capture is closed")
	}
	action := C.CString(event.Action)
	defer C.free(unsafe.Pointer(action))
	if C.tl_window_pointer(capture.handle, action, C.double(event.X), C.double(event.Y), C.int(event.Button), C.double(event.DeltaX), C.double(event.DeltaY)) == 0 {
		return errors.New("macOS rejected the pointer event; allow Termlinks in Accessibility settings")
	}
	return nil
}

func (capture *Capture) Key(event KeyEvent) error {
	if capture == nil || capture.handle == nil {
		return errors.New("window capture is closed")
	}
	code := C.CString(event.Code)
	defer C.free(unsafe.Pointer(code))
	if C.tl_window_key(capture.handle, code, boolInt(event.Down), boolInt(event.Shift), boolInt(event.Ctrl), boolInt(event.Alt), boolInt(event.Meta)) == 0 {
		return errors.New("macOS rejected the keyboard event; allow Termlinks in Accessibility settings")
	}
	return nil
}

func (capture *Capture) Text(text string) error {
	if capture == nil || capture.handle == nil {
		return errors.New("window capture is closed")
	}
	value := C.CString(text)
	defer C.free(unsafe.Pointer(value))
	if C.tl_window_text(capture.handle, value) == 0 {
		return errors.New("macOS rejected the text event; allow Termlinks in Accessibility settings")
	}
	return nil
}

func (capture *Capture) Clipboard(text string) error {
	if capture == nil || capture.handle == nil {
		return errors.New("window capture is closed")
	}
	value := C.CString(text)
	defer C.free(unsafe.Pointer(value))
	if C.tl_window_clipboard(capture.handle, value) == 0 {
		return errors.New("macOS rejected the clipboard update")
	}
	return nil
}

func (capture *Capture) Close() {
	if capture == nil || capture.handle == nil {
		return
	}
	C.tl_window_close(capture.handle)
	capture.handle = nil
	runtime.SetFinalizer(capture, nil)
}

func boolInt(value bool) C.int {
	if value {
		return 1
	}
	return 0
}

func nativeFailure(nativeError *C.char) error {
	if nativeError == nil {
		return errors.New("macOS window capture failed")
	}
	defer C.free(unsafe.Pointer(nativeError))
	return errors.New(C.GoString(nativeError))
}
