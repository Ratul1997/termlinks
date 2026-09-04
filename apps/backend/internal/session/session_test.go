package session

import (
	"bytes"
	"syscall"
	"testing"
	"time"
)

func TestInteractiveSessionAndScrollback(t *testing.T) {
	manager := NewManager()
	current, err := manager.Start(StartOptions{
		Command: []string{"/bin/sh", "-c", "printf 'ready\\n'; IFS= read -r value; printf 'received:%s\\n' \"$value\""},
		Cwd:     t.TempDir(),
		Cols:    80,
		Rows:    24,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = current.Stop() })

	initial, updates, cancel := current.Subscribe()
	defer cancel()
	output := append([]byte(nil), initial...)
	output = waitForOutput(t, output, updates, []byte("ready"))
	if err := current.Write([]byte("from-phone\n")); err != nil {
		t.Fatal(err)
	}
	output = waitForOutput(t, output, updates, []byte("received:from-phone"))

	select {
	case <-current.Done():
	case <-time.After(3 * time.Second):
		t.Fatal("command did not exit")
	}
	if info := current.Info(); info.Running || info.ExitCode == nil || *info.ExitCode != 0 {
		t.Fatalf("unexpected final state: %+v", info)
	}
	history, _, detach := current.Subscribe()
	detach()
	if !bytes.Contains(history, []byte("ready")) || !bytes.Contains(history, []byte("received:from-phone")) {
		t.Fatalf("scrollback did not retain output: %q", history)
	}
}

func TestRejectsUnsafeTerminalSizes(t *testing.T) {
	_, err := NewManager().Start(StartOptions{Command: []string{"/bin/echo", "hello"}, Cwd: t.TempDir(), Cols: 10, Rows: 2})
	if err == nil {
		t.Fatal("expected unsafe terminal dimensions to be rejected")
	}
}

func TestEndObserverReceivesAuthoritativeFinalState(t *testing.T) {
	manager := NewManager()
	ended := make(chan Info, 1)
	manager.SetEndObserver(func(info Info) { ended <- info })
	current, err := manager.Start(StartOptions{Command: []string{"/bin/sh", "-c", "exit 7"}, Cwd: t.TempDir(), Cols: 80, Rows: 24})
	if err != nil {
		t.Fatal(err)
	}
	select {
	case info := <-ended:
		if info.ID != current.Info().ID || info.Running || info.EndedAt == nil || info.ExitCode == nil || *info.ExitCode != 7 {
			t.Fatalf("unexpected observer state: %#v", info)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("end observer was not called")
	}
}

func waitForOutput(t *testing.T, output []byte, updates <-chan []byte, expected []byte) []byte {
	t.Helper()
	timer := time.NewTimer(3 * time.Second)
	defer timer.Stop()
	for !bytes.Contains(output, expected) {
		select {
		case chunk := <-updates:
			output = append(output, chunk...)
		case <-timer.C:
			t.Fatalf("timed out waiting for %q in %q", expected, output)
		}
	}
	return output
}

func TestSignalledSessionRecordsSignalAndShellStatus(t *testing.T) {
	manager := NewManager()
	ended := make(chan Info, 1)
	manager.SetEndObserver(func(info Info) { ended <- info })
	current, err := manager.Start(StartOptions{Command: []string{"/bin/sh", "-c", "echo ready; sleep 30"}, Cwd: t.TempDir(), Cols: 80, Rows: 24})
	if err != nil {
		t.Fatal(err)
	}
	initial, updates, detach := current.Subscribe()
	defer detach()
	waitForOutput(t, initial, updates, []byte("ready"))
	if err := current.Stop(); err != nil {
		t.Fatal(err)
	}
	select {
	case info := <-ended:
		if info.Signal != "SIGTERM" {
			t.Fatalf("expected SIGTERM, got %q (info %#v)", info.Signal, info)
		}
		if info.ExitCode == nil || *info.ExitCode != 128+int(syscall.SIGTERM) {
			t.Fatalf("expected shell status %d, got %#v", 128+int(syscall.SIGTERM), info.ExitCode)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("stopped session never reported its final state")
	}
}
