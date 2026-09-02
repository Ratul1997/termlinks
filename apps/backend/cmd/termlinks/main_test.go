package main

import (
	"testing"

	"termlinks/backend/internal/config"
	"termlinks/backend/internal/session"
)

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
