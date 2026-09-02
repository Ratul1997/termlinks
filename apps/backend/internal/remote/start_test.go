package remote

import (
	"strings"
	"testing"
)

func TestInteractiveShellOptions(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("SHELL", "/bin/sh")
	request, err := DecodeStartRequest(strings.NewReader(`{"name":" phone shell ","cwd":"~"}`))
	if err != nil {
		t.Fatal(err)
	}
	options, err := request.Options()
	if err != nil {
		t.Fatal(err)
	}
	if options.Name != "phone shell" || len(options.Command) != 1 || options.Command[0] != "/bin/sh" {
		t.Fatalf("unexpected shell options: %#v", options)
	}
	if options.Cwd != home {
		t.Fatalf("home directory = %q, want %q", options.Cwd, home)
	}
}

func TestInteractiveShellRequestValidation(t *testing.T) {
	for _, payload := range []string{
		`{"name":"x","cwd":"/tmp","command":"whoami"}`,
		`{"name":"x","cwd":"/tmp"} {"name":"y"}`,
		`not-json`,
	} {
		if _, err := DecodeStartRequest(strings.NewReader(payload)); err == nil {
			t.Fatalf("accepted invalid payload %q", payload)
		}
	}
	request, err := DecodeStartRequest(strings.NewReader(`{"cwd":"relative/path"}`))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := request.Options(); err == nil {
		t.Fatal("accepted a relative working directory")
	}
}
