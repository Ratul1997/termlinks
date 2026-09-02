//go:build !darwin || !cgo

package windowcapture

type Capture struct{}

func PermissionStatus() Permissions           { return Permissions{} }
func RequestPermissions() Permissions         { return Permissions{} }
func List() ([]Source, error)                 { return nil, ErrUnsupported }
func Open(uint32, int, int) (*Capture, error) { return nil, ErrUnsupported }
func (*Capture) Frame() (Frame, error)        { return Frame{}, ErrUnsupported }
func (*Capture) Pointer(PointerEvent) error   { return ErrUnsupported }
func (*Capture) Key(KeyEvent) error           { return ErrUnsupported }
func (*Capture) Text(string) error            { return ErrUnsupported }
func (*Capture) Clipboard(string) error       { return ErrUnsupported }
func (*Capture) Close()                       {}
