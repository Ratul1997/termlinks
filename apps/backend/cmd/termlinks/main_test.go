package main

import (
	"testing"

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
