package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestDefaultListenUsesPrivateDynamicPort(t *testing.T) {
	if DefaultListen != "127.0.0.1:57321" {
		t.Fatalf("DefaultListen = %q", DefaultListen)
	}
}

func TestCloudSettingsPersistPrivately(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("TERMLINKS_STATE_DIR", dir)
	paths, err := ResolvePaths()
	if err != nil {
		t.Fatal(err)
	}
	if err := Ensure(paths); err != nil {
		t.Fatal(err)
	}
	want := CloudSettings{
		RelayURL: "https://relay.example.workers.dev/", ConnectorToken: "abcdefghijklmnopqrstuvwxyz1234567890",
		DesktopEnabled: true, VNCAddress: "127.0.0.1:5901",
	}
	if err := SaveCloudSettings(paths, want); err != nil {
		t.Fatal(err)
	}
	got, err := LoadCloudSettings(paths)
	if err != nil {
		t.Fatal(err)
	}
	if got.RelayURL != "https://relay.example.workers.dev" || got.ConnectorToken != want.ConnectorToken || !got.DesktopEnabled || got.VNCAddress != want.VNCAddress {
		t.Fatalf("cloud settings = %#v", got)
	}
	info, err := os.Stat(paths.Cloud)
	if err != nil {
		t.Fatal(err)
	}
	if permissions := info.Mode().Perm(); permissions != 0o600 {
		t.Fatalf("cloud settings permissions = %o, want 600", permissions)
	}
}

func TestValidateCloudSettings(t *testing.T) {
	token := "abcdefghijklmnopqrstuvwxyz1234567890"
	for _, relayURL := range []string{"http://relay.example", "https://user@relay.example", "https://relay.example/path", "https://relay.example?token=secret"} {
		if err := ValidateCloudSettings(CloudSettings{RelayURL: relayURL, ConnectorToken: token}); err == nil {
			t.Errorf("accepted unsafe relay URL %q", relayURL)
		}
	}
	if err := ValidateCloudSettings(CloudSettings{RelayURL: "https://relay.example", ConnectorToken: token}); err != nil {
		t.Fatalf("valid cloud settings rejected: %v", err)
	}
	for _, address := range []string{"192.168.1.10:5900", "example.com:5900", "127.0.0.1:0", "127.0.0.1"} {
		if err := ValidateCloudSettings(CloudSettings{RelayURL: "https://relay.example", ConnectorToken: token, DesktopEnabled: true, VNCAddress: address}); err == nil {
			t.Errorf("accepted unsafe VNC address %q", address)
		}
	}
	for _, address := range []string{"127.0.0.1:5900", "[::1]:5900", "localhost:5900"} {
		if err := ValidateCloudSettings(CloudSettings{RelayURL: "https://relay.example", ConnectorToken: token, DesktopEnabled: true, VNCAddress: address}); err != nil {
			t.Errorf("rejected safe VNC address %q: %v", address, err)
		}
	}
}

func TestLegacyCloudSettingsDefaultToLoopbackDesktopTarget(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("TERMLINKS_STATE_DIR", dir)
	paths, err := ResolvePaths()
	if err != nil {
		t.Fatal(err)
	}
	if err := Ensure(paths); err != nil {
		t.Fatal(err)
	}
	legacy := []byte(`{"relayUrl":"https://relay.example","connectorToken":"abcdefghijklmnopqrstuvwxyz1234567890"}`)
	if err := os.WriteFile(paths.Cloud, legacy, 0o600); err != nil {
		t.Fatal(err)
	}
	settings, err := LoadCloudSettings(paths)
	if err != nil {
		t.Fatal(err)
	}
	if settings.DesktopEnabled || settings.VNCAddress != DefaultVNCAddress {
		t.Fatalf("legacy desktop settings = enabled %v, address %q", settings.DesktopEnabled, settings.VNCAddress)
	}
}

func TestTokenPersistsWithPrivatePermissions(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("TERMLINKS_STATE_DIR", dir)
	paths, err := ResolvePaths()
	if err != nil {
		t.Fatal(err)
	}
	if err := Ensure(paths); err != nil {
		t.Fatal(err)
	}
	first, err := LoadOrCreateToken(paths)
	if err != nil {
		t.Fatal(err)
	}
	second, err := LoadOrCreateToken(paths)
	if err != nil {
		t.Fatal(err)
	}
	if first != second || len(first) < 32 {
		t.Fatal("authentication token was not persisted")
	}
	info, err := os.Stat(paths.Token)
	if err != nil {
		t.Fatal(err)
	}
	if permissions := info.Mode().Perm(); permissions != 0o600 {
		t.Fatalf("token permissions = %o, want 600", permissions)
	}
	dirInfo, err := os.Stat(paths.Dir)
	if err != nil {
		t.Fatal(err)
	}
	if permissions := dirInfo.Mode().Perm(); permissions != 0o700 {
		t.Fatalf("state directory permissions = %o, want 700", permissions)
	}
	for _, directory := range []string{paths.WorkflowArtifacts, paths.WorkflowWorktrees} {
		info, err := os.Stat(directory)
		if err != nil {
			t.Fatal(err)
		}
		if permissions := info.Mode().Perm(); permissions != 0o700 {
			t.Fatalf("workflow directory permissions = %o, want 700", permissions)
		}
	}
}

func TestResolvePathsRejectsRelativeOverride(t *testing.T) {
	t.Setenv("TERMLINKS_STATE_DIR", filepath.Join("relative", "state"))
	if _, err := ResolvePaths(); err == nil {
		t.Fatal("expected a relative state path to be rejected")
	}
}
