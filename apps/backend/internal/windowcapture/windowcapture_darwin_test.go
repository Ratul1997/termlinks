//go:build darwin && cgo

package windowcapture

import (
	"bytes"
	"os"
	"testing"
)

func TestWindowCaptureIntegration(t *testing.T) {
	if os.Getenv("TERMLINKS_WINDOW_INTEGRATION") != "1" {
		t.Skip("set TERMLINKS_WINDOW_INTEGRATION=1 to exercise ScreenCaptureKit")
	}
	permissions := PermissionStatus()
	if !permissions.ScreenRecording {
		t.Skip("Screen Recording permission is not granted")
	}
	sources, err := List()
	if err != nil {
		t.Fatal(err)
	}
	if len(sources) == 0 {
		t.Fatal("no shareable windows")
	}
	capture, err := Open(sources[0].ID, 960, 720)
	if err != nil {
		t.Fatal(err)
	}
	defer capture.Close()
	frame, err := capture.Frame()
	if err != nil {
		t.Fatal(err)
	}
	if frame.Width < 1 || frame.Height < 1 || len(frame.Data) < 4 || !bytes.Equal(frame.Data[:3], []byte{0xff, 0xd8, 0xff}) {
		t.Fatalf("invalid JPEG frame: %dx%d, %d bytes", frame.Width, frame.Height, len(frame.Data))
	}
}
