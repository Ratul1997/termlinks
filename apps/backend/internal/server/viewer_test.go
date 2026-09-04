package server

import (
	"sync"
	"testing"
	"time"
)

func TestHideDuringNativeLaunchClosesLateWindow(t *testing.T) {
	launchStarted := make(chan struct{})
	releaseLaunch := make(chan struct{})
	closed := make(chan string, 1)
	var launchMu sync.Mutex
	launchFinished := false
	viewers := newViewerController(NativeViewer{
		Open: func(string) error {
			close(launchStarted)
			<-releaseLaunch
			launchMu.Lock()
			launchFinished = true
			launchMu.Unlock()
			return nil
		},
		Close: func(id string) error {
			launchMu.Lock()
			finished := launchFinished
			launchMu.Unlock()
			if finished {
				closed <- id
			}
			return nil
		},
	})
	showDone := make(chan string, 1)
	go func() {
		status, _ := viewers.show("0123456789abcdef0123456789abcdef")
		showDone <- status
	}()
	<-launchStarted
	status, err := viewers.hide("0123456789abcdef0123456789abcdef")
	if err != nil || status != "hidden" {
		t.Fatalf("hide during launch = %q, %v", status, err)
	}
	close(releaseLaunch)
	if status = <-showDone; status != "hidden" {
		t.Fatalf("late show status = %q, want hidden", status)
	}
	select {
	case id := <-closed:
		if id != "0123456789abcdef0123456789abcdef" {
			t.Fatalf("closed viewer = %q", id)
		}
	case <-time.After(time.Second):
		t.Fatal("late native window was not closed")
	}
}

func TestOpeningTimeoutClosesOrphanedNativeWindow(t *testing.T) {
	closed := make(chan string, 1)
	viewers := newViewerController(NativeViewer{
		Open: func(string) error { return nil },
		Close: func(id string) error {
			closed <- id
			return nil
		},
	})
	id := "0123456789abcdef0123456789abcdef"
	if status, err := viewers.show(id); err != nil || status != "opening" {
		t.Fatalf("show = %q, %v", status, err)
	}
	viewers.mu.Lock()
	viewers.states[id].openingBy = time.Now().Add(-time.Second)
	viewers.mu.Unlock()
	if status := viewers.status(id); status != "hidden" {
		t.Fatalf("expired viewer status = %q", status)
	}
	select {
	case closedID := <-closed:
		if closedID != id {
			t.Fatalf("closed viewer = %q", closedID)
		}
	case <-time.After(time.Second):
		t.Fatal("timed-out native viewer window was not closed")
	}
}
