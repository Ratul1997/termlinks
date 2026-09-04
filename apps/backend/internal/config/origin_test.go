package config

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestNormalizePublicOrigin(t *testing.T) {
	valid := map[string]string{
		"https://local.zahidhasan.website":      "https://local.zahidhasan.website",
		"https://local.zahidhasan.website/":     "https://local.zahidhasan.website",
		"  https://LOCAL.Zahidhasan.Website  ":  "https://local.zahidhasan.website",
		"https://local.zahidhasan.website:443":  "https://local.zahidhasan.website",
		"https://local.zahidhasan.website:8443": "https://local.zahidhasan.website:8443",
		"https://a-b.example.co.uk":             "https://a-b.example.co.uk",
	}
	for input, want := range valid {
		got, err := NormalizePublicOrigin(input)
		if err != nil || got != want {
			t.Errorf("NormalizePublicOrigin(%q) = %q, %v; want %q", input, got, err, want)
		}
	}

	invalid := []string{
		"",
		"   ",
		"local.zahidhasan.website",
		"http://local.zahidhasan.website",
		"ws://local.zahidhasan.website",
		"https://local.zahidhasan.website/portal",
		"https://local.zahidhasan.website/?x=1",
		"https://local.zahidhasan.website#fragment",
		"https://user:pass@local.zahidhasan.website",
		"https://*.zahidhasan.website",
		"https://203.0.113.10",
		"https://[2001:db8::1]",
		"https://localhost",
		"https://.zahidhasan.website",
		"https://zahidhasan..website",
		"https://-bad.example.com",
		"https://local.zahidhasan.website:0",
		"https://local.zahidhasan.website:99999",
		"https://exam ple.com",
	}
	for _, input := range invalid {
		if got, err := NormalizePublicOrigin(input); err == nil {
			t.Errorf("NormalizePublicOrigin(%q) = %q, want an error", input, got)
		} else if !errors.Is(err, ErrPublicOriginInvalid) {
			t.Errorf("NormalizePublicOrigin(%q) error = %v, want ErrPublicOriginInvalid", input, err)
		}
	}
}

func TestRelyingPartyIDIsTheExactHostname(t *testing.T) {
	for origin, want := range map[string]string{
		"https://local.zahidhasan.website":      "local.zahidhasan.website",
		"https://local.zahidhasan.website:8443": "local.zahidhasan.website",
	} {
		got, err := RelyingPartyID(origin)
		if err != nil || got != want {
			t.Errorf("RelyingPartyID(%q) = %q, %v; want %q", origin, got, err, want)
		}
	}
	if _, err := RelyingPartyID("https://203.0.113.10"); err == nil {
		t.Fatal("RelyingPartyID accepted an IP address origin")
	}
}

func TestNormalizeClientIPHeader(t *testing.T) {
	for input, want := range map[string]string{
		"CF-Connecting-IP":    "Cf-Connecting-Ip",
		"  cf-connecting-ip ": "Cf-Connecting-Ip",
		"X-Real-IP":           "X-Real-Ip",
	} {
		got, err := NormalizeClientIPHeader(input)
		if err != nil || got != want {
			t.Errorf("NormalizeClientIPHeader(%q) = %q, %v; want %q", input, got, err, want)
		}
	}
	for _, invalid := range []string{"", "   ", "has space", "colon:", "new\nline", strings.Repeat("a", 65)} {
		if _, err := NormalizeClientIPHeader(invalid); !errors.Is(err, ErrClientIPHeaderInvalid) {
			t.Errorf("NormalizeClientIPHeader(%q) error = %v, want ErrClientIPHeaderInvalid", invalid, err)
		}
	}
}

func TestSettingsRoundTripPublicOrigin(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("TERMLINKS_STATE_DIR", dir)
	paths, err := ResolvePaths()
	if err != nil {
		t.Fatal(err)
	}
	if paths.AuthDB != filepath.Join(dir, "auth.db") {
		t.Fatalf("auth database path = %q", paths.AuthDB)
	}
	if err := Ensure(paths); err != nil {
		t.Fatal(err)
	}
	if err := SaveSettings(paths, Settings{Listen: DefaultListen, PublicOrigin: "https://local.zahidhasan.website/"}); err != nil {
		t.Fatal(err)
	}
	loaded, err := LoadSettings(paths)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.PublicOrigin != "https://local.zahidhasan.website" {
		t.Fatalf("public origin = %q", loaded.PublicOrigin)
	}
}

func TestSettingsWithoutPublicOriginStayBackwardCompatible(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("TERMLINKS_STATE_DIR", dir)
	paths, err := ResolvePaths()
	if err != nil {
		t.Fatal(err)
	}
	if err := Ensure(paths); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(paths.Settings, []byte(`{"listen":"127.0.0.1:9100"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	loaded, err := LoadSettings(paths)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Listen != "127.0.0.1:9100" || loaded.PublicOrigin != "" {
		t.Fatalf("settings = %+v", loaded)
	}
	// Saving must not introduce the field for installations that never set it.
	if err := SaveSettings(paths, loaded); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(paths.Settings)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "{\n  \"listen\": \"127.0.0.1:9100\"\n}\n" {
		t.Fatalf("settings file = %q", data)
	}
}

func TestSettingsRejectStoredInvalidPublicOrigin(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("TERMLINKS_STATE_DIR", dir)
	paths, err := ResolvePaths()
	if err != nil {
		t.Fatal(err)
	}
	if err := Ensure(paths); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(paths.Settings, []byte(`{"listen":"127.0.0.1:9100","publicOrigin":"http://oops"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadSettings(paths); err == nil {
		t.Fatal("LoadSettings accepted an invalid stored public origin")
	}
}
