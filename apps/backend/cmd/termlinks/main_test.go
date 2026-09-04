package main

import (
	"errors"
	"flag"
	"testing"

	"termlinks/backend/internal/client"
	"termlinks/backend/internal/config"
	"termlinks/backend/internal/session"
)

func TestListenWithPort(t *testing.T) {
	tests := map[string]string{
		"127.0.0.1:8787": "127.0.0.1:9000",
		"[::1]:8787":     "[::1]:9000",
		"localhost:8787": "localhost:9000",
	}
	for input, want := range tests {
		got, err := listenWithPort(input, 9000)
		if err != nil || got != want {
			t.Errorf("listenWithPort(%q, 9000) = %q, %v; want %q", input, got, err, want)
		}
	}
	for _, port := range []int{-1, 0, 65536} {
		if _, err := listenWithPort("127.0.0.1:8787", port); err == nil {
			t.Errorf("listenWithPort accepted invalid port %d", port)
		}
	}
}

func TestFlagWasSetRecognizesAliases(t *testing.T) {
	flags := flag.NewFlagSet("test", flag.ContinueOnError)
	port := flags.Int("port", 0, "")
	flags.IntVar(port, "p", 0, "")
	if err := flags.Parse([]string{"-p", "9000"}); err != nil {
		t.Fatal(err)
	}
	if !flagWasSet(flags, "port", "p") || *port != 9000 {
		t.Fatalf("short port flag was not detected: set=%v port=%d", flagWasSet(flags, "port", "p"), *port)
	}
}

func TestConfigureDaemonPortPersistsAndPreservesHost(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("TERMLINKS_STATE_DIR", dir)
	paths, err := config.ResolvePaths()
	if err != nil {
		t.Fatal(err)
	}
	if err := config.Ensure(paths); err != nil {
		t.Fatal(err)
	}
	if err := config.SaveSettings(paths, config.Settings{Listen: "100.100.10.2:57321"}); err != nil {
		t.Fatal(err)
	}
	if err := configureDaemonPort(paths, 9000); err != nil {
		t.Fatal(err)
	}
	settings, err := config.LoadSettings(paths)
	if err != nil {
		t.Fatal(err)
	}
	if settings.Listen != "100.100.10.2:9000" {
		t.Fatalf("configured listener = %q", settings.Listen)
	}
}

func TestValidateListenSecurityBoundary(t *testing.T) {
	accepted := []string{"127.0.0.1:8787", "[::1]:8787", "192.168.1.10:8787", "100.100.10.2:8787"}
	for _, address := range accepted {
		if err := validateListen(address, false); err != nil {
			t.Errorf("validateListen(%q) unexpectedly failed: %v", address, err)
		}
	}
	rejected := []string{"0.0.0.0:8787", "[::]:8787", "8.8.8.8:8787"}
	for _, address := range rejected {
		if err := validateListen(address, false); err == nil {
			t.Errorf("validateListen(%q) unexpectedly allowed a public bind", address)
		}
	}
	if err := validateListen("0.0.0.0:8787", true); err != nil {
		t.Fatalf("explicit public override failed: %v", err)
	}
}

func TestResolveID(t *testing.T) {
	items := []session.Info{{ID: "abcdef1234"}, {ID: "abc9999999"}}
	if _, err := resolveID(items, "abc"); err == nil {
		t.Fatal("ambiguous prefix was accepted")
	}
	resolved, err := resolveID(items, "abcdef")
	if err != nil || resolved != "abcdef1234" {
		t.Fatalf("resolved = %q, err = %v", resolved, err)
	}
}

func TestListDefaultsToRunningSessions(t *testing.T) {
	items := []session.Info{
		{ID: "running", Running: true},
		{ID: "finished", Running: false},
	}
	running := filterListedSessions(items, false)
	if len(running) != 1 || running[0].ID != "running" {
		t.Fatalf("default list = %#v, want only the running session", running)
	}
	all := filterListedSessions(items, true)
	if len(all) != 2 {
		t.Fatalf("--all list length = %d, want 2", len(all))
	}
}

func TestAttachResultPreservesFailedAndSignalledStatuses(t *testing.T) {
	tests := []struct {
		name   string
		result client.AttachResult
		code   int
		signal string
	}{
		{name: "live failure", result: client.AttachResult{ExitCode: 7}, code: 7},
		{name: "already-finished failure", result: client.AttachResult{ExitCode: 7, AlreadyExited: true}, code: 7},
		{name: "already-finished signal", result: client.AttachResult{ExitCode: 143, Signal: "SIGTERM", AlreadyExited: true}, code: 143, signal: "SIGTERM"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var status exitStatus
			if err := attachResultError(test.result); !errors.As(err, &status) {
				t.Fatalf("attach result error = %v, want exitStatus", err)
			}
			if status.code != test.code || status.signal != test.signal {
				t.Fatalf("exit status = %#v, want code %d signal %q", status, test.code, test.signal)
			}
		})
	}
	if err := attachResultError(client.AttachResult{AlreadyExited: true}); err != nil {
		t.Fatalf("successful finished session returned %v", err)
	}
}

func TestProcessExitCodeClampsUnrepresentableStatuses(t *testing.T) {
	for input, want := range map[int]int{-1: 1, 0: 0, 7: 7, 143: 143, 255: 255, 256: 1} {
		if got := processExitCode(input); got != want {
			t.Errorf("processExitCode(%d) = %d, want %d", input, got, want)
		}
	}
}

func TestDesktopOptInPersistsAndRejectsNonLoopbackTargets(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("TERMLINKS_STATE_DIR", dir)
	paths, err := config.ResolvePaths()
	if err != nil {
		t.Fatal(err)
	}
	if err := config.Ensure(paths); err != nil {
		t.Fatal(err)
	}
	settings := config.CloudSettings{
		RelayURL:       "https://relay.example",
		ConnectorToken: "abcdefghijklmnopqrstuvwxyz1234567890",
		VNCAddress:     config.DefaultVNCAddress,
	}
	if err := config.SaveCloudSettings(paths, settings); err != nil {
		t.Fatal(err)
	}
	if err := setDesktopEnabled(true, "192.168.1.5:5900"); err == nil {
		t.Fatal("non-loopback desktop target was accepted")
	}
	if err := setDesktopEnabled(true, "127.0.0.1:5901"); err != nil {
		t.Fatal(err)
	}
	loaded, err := config.LoadCloudSettings(paths)
	if err != nil {
		t.Fatal(err)
	}
	if !loaded.DesktopEnabled || loaded.VNCAddress != "127.0.0.1:5901" {
		t.Fatalf("desktop opt-in was not persisted: %#v", loaded)
	}
	if err := setDesktopEnabled(false, ""); err != nil {
		t.Fatal(err)
	}
	loaded, err = config.LoadCloudSettings(paths)
	if err != nil || loaded.DesktopEnabled {
		t.Fatalf("desktop opt-out was not persisted: %#v, %v", loaded, err)
	}
}
