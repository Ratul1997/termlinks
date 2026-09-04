package main

import (
	"flag"
	"io"
	"os"
	"strings"
	"testing"

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

// captureOutput runs a CLI command with standard output redirected so the test
// can assert on what the owner sees.
func captureOutput(t *testing.T, command func() error) (string, error) {
	t.Helper()
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	original := os.Stdout
	os.Stdout = writer
	runErr := command()
	os.Stdout = original
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	output, err := io.ReadAll(reader)
	if err != nil {
		t.Fatal(err)
	}
	return string(output), runErr
}

func TestAuthConfigureStoresOriginAndStatusReportsIt(t *testing.T) {
	t.Setenv("TERMLINKS_STATE_DIR", t.TempDir())
	paths, err := config.ResolvePaths()
	if err != nil {
		t.Fatal(err)
	}

	output, err := captureOutput(t, func() error { return authStatus() })
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output, "not configured") {
		t.Fatalf("status before configuration = %q", output)
	}

	output, err = captureOutput(t, func() error {
		return runAuth([]string{"configure", "--origin", "https://Local.Example.COM/"})
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output, "https://local.example.com") || !strings.Contains(output, "relying party ID local.example.com") {
		t.Fatalf("configure output = %q", output)
	}
	settings, err := config.LoadSettings(paths)
	if err != nil {
		t.Fatal(err)
	}
	if settings.PublicOrigin != "https://local.example.com" || settings.Listen != config.DefaultListen {
		t.Fatalf("settings = %+v", settings)
	}

	output, err = captureOutput(t, func() error { return authStatus() })
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"https://local.example.com", "local.example.com", "Enrolled keys:   0", "Client IP header: none"} {
		if !strings.Contains(output, want) {
			t.Fatalf("status output %q is missing %q", output, want)
		}
	}
}

func TestAuthConfigureStoresATrustedClientIPHeader(t *testing.T) {
	t.Setenv("TERMLINKS_STATE_DIR", t.TempDir())
	paths, err := config.ResolvePaths()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := captureOutput(t, func() error {
		return runAuth([]string{"configure", "--origin", "https://local.example.com", "--client-ip-header", "cf-connecting-ip"})
	}); err != nil {
		t.Fatal(err)
	}
	settings, err := config.LoadSettings(paths)
	if err != nil {
		t.Fatal(err)
	}
	if settings.TrustedClientIPHeader != "Cf-Connecting-Ip" {
		t.Fatalf("trusted client IP header = %q", settings.TrustedClientIPHeader)
	}
	output, err := captureOutput(t, func() error { return authStatus() })
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output, "Client IP header: Cf-Connecting-Ip") {
		t.Fatalf("status output = %q", output)
	}

	// Reconfiguring the origin alone keeps the header; --no-client-ip-header drops it.
	if _, err := captureOutput(t, func() error {
		return runAuth([]string{"configure", "--origin", "https://local.example.com"})
	}); err != nil {
		t.Fatal(err)
	}
	if settings, err = config.LoadSettings(paths); err != nil || settings.TrustedClientIPHeader != "Cf-Connecting-Ip" {
		t.Fatalf("header = %q, err = %v", settings.TrustedClientIPHeader, err)
	}
	if _, err := captureOutput(t, func() error {
		return runAuth([]string{"configure", "--origin", "https://local.example.com", "--no-client-ip-header"})
	}); err != nil {
		t.Fatal(err)
	}
	if settings, err = config.LoadSettings(paths); err != nil || settings.TrustedClientIPHeader != "" {
		t.Fatalf("header = %q, err = %v", settings.TrustedClientIPHeader, err)
	}
}

func TestAuthConfigureWarnsWhenTheHostnameChanges(t *testing.T) {
	t.Setenv("TERMLINKS_STATE_DIR", t.TempDir())
	if _, err := captureOutput(t, func() error {
		return runAuth([]string{"configure", "--origin", "https://first.example.com"})
	}); err != nil {
		t.Fatal(err)
	}
	output, err := captureOutput(t, func() error {
		return runAuth([]string{"configure", "--origin", "https://second.example.com"})
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output, "no longer work") {
		t.Fatalf("hostname change output = %q", output)
	}
}

func TestAuthConfigureRejectsInvalidUsage(t *testing.T) {
	t.Setenv("TERMLINKS_STATE_DIR", t.TempDir())
	invocations := [][]string{
		{"configure"},
		{"configure", "--origin", "http://local.example.com"},
		{"configure", "--origin", "https://local.example.com", "extra"},
		{"configure", "--origin", "https://192.0.2.10"},
		{"configure", "--origin", "https://local.example.com", "--client-ip-header", "bad header"},
		{"configure", "--origin", "https://local.example.com", "--client-ip-header", "CF-Connecting-IP", "--no-client-ip-header"},
		{"nonsense"},
	}
	for _, args := range invocations {
		if err := runAuth(args); err == nil {
			t.Fatalf("runAuth(%v) accepted an invalid invocation", args)
		}
	}
	paths, err := config.ResolvePaths()
	if err != nil {
		t.Fatal(err)
	}
	settings, err := config.LoadSettings(paths)
	if err != nil {
		t.Fatal(err)
	}
	if settings.PublicOrigin != "" {
		t.Fatalf("a rejected configuration stored %q", settings.PublicOrigin)
	}
}
