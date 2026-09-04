package cloudflarepages

import (
	"context"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"
)

func TestEnvironmentIsExplicitAndValidated(t *testing.T) {
	values := map[string]string{}
	lookup := func(name string) (string, bool) { value, ok := values[name]; return value, ok }
	if _, configured, err := fromLookup(lookup); err != nil || configured {
		t.Fatalf("empty environment = configured %v, error %v", configured, err)
	}
	values[TokenEnv] = "cfat_example_token_with_enough_bytes"
	if _, configured, err := fromLookup(lookup); err == nil || !configured || !strings.Contains(err.Error(), AccountEnv) {
		t.Fatalf("partial environment = configured %v, error %v", configured, err)
	}
	values[AccountEnv] = strings.Repeat("a", 32)
	config, configured, err := fromLookup(lookup)
	if err != nil || !configured || config.Project != defaultProject || config.Branch != defaultBranch {
		t.Fatalf("valid environment = %#v, configured %v, error %v", config, configured, err)
	}
	values[ProjectEnv] = "Bad Project"
	if _, _, err := fromLookup(lookup); err == nil || !strings.Contains(err.Error(), ProjectEnv) {
		t.Fatalf("invalid project error = %v", err)
	}
}

func TestEnvironmentIgnoresGenericWranglerCredentials(t *testing.T) {
	values := map[string]string{
		"CLOUDFLARE_API_TOKEN":  "generic-token-must-not-opt-in",
		"CLOUDFLARE_ACCOUNT_ID": strings.Repeat("a", 32),
	}
	lookup := func(name string) (string, bool) { value, ok := values[name]; return value, ok }
	if _, configured, err := fromLookup(lookup); err != nil || configured {
		t.Fatalf("generic Wrangler environment = configured %v, error %v", configured, err)
	}
}

func TestDeploymentStagesPortalAndFunctionWithoutLeakingCustomToken(t *testing.T) {
	portal := fstest.MapFS{
		"dist/index.html": {Data: []byte("portal")},
		"dist/sw.js":      {Data: []byte("worker")},
	}
	functions := fstest.MapFS{
		"bundle/functions/[[path]].js": {Data: []byte("export function onRequest() {}")},
	}
	config := Config{
		APIToken:  "cfat_example_token_with_enough_bytes",
		AccountID: strings.Repeat("b", 32),
		Project:   "my-termlinks",
		Branch:    "main",
	}
	var captured *exec.Cmd
	var capturedExecutable string
	var capturedArguments []string
	command := func(ctx context.Context, executable string, arguments ...string) *exec.Cmd {
		capturedExecutable = executable
		capturedArguments = append([]string(nil), arguments...)
		captured = exec.CommandContext(ctx, "true")
		return captured
	}
	lookPath := func(name string) (string, error) {
		if name == "wrangler" {
			return "/test/wrangler", nil
		}
		return "", fs.ErrNotExist
	}
	if err := deploy(context.Background(), config, portal, functions, io.Discard, io.Discard, lookPath, command); err != nil {
		t.Fatal(err)
	}
	if captured == nil || captured.Dir == "" {
		t.Fatal("deployment command was not created with a staging directory")
	}
	if capturedExecutable != "/test/wrangler" || !containsExact(capturedArguments, "my-termlinks") || !containsExact(capturedArguments, "main") {
		t.Fatalf("deployment command = %q %#v", capturedExecutable, capturedArguments)
	}
	if containsExact(capturedArguments, config.APIToken) || containsExact(capturedArguments, config.AccountID) {
		t.Fatal("deployment credentials must not appear in command arguments")
	}
	for _, expected := range []string{"CLOUDFLARE_API_TOKEN=" + config.APIToken, "CLOUDFLARE_ACCOUNT_ID=" + config.AccountID} {
		if !containsExact(captured.Env, expected) {
			t.Fatalf("deployment environment omitted %s", strings.Split(expected, "=")[0])
		}
	}
	for _, item := range captured.Env {
		if strings.HasPrefix(item, TokenEnv+"=") || strings.HasPrefix(item, AccountEnv+"=") {
			t.Fatalf("custom Termlinks credential leaked alongside Wrangler environment: %s", strings.Split(item, "=")[0])
		}
	}
}

func TestCopyTreePreservesBundledFiles(t *testing.T) {
	source := fstest.MapFS{
		"dist/index.html":     {Data: []byte("portal")},
		"dist/assets/main.js": {Data: []byte("script")},
	}
	destination := filepath.Join(t.TempDir(), "portal")
	if err := copyTree(source, "dist", destination); err != nil {
		t.Fatal(err)
	}
	for name, expected := range map[string]string{"index.html": "portal", "assets/main.js": "script"} {
		contents, err := os.ReadFile(filepath.Join(destination, name))
		if err != nil || string(contents) != expected {
			t.Fatalf("copied %s = %q, %v", name, contents, err)
		}
	}
}

func TestWranglerFallsBackToPinnedNpxPackage(t *testing.T) {
	lookup := func(name string) (string, error) {
		if name == "npx" {
			return "/usr/bin/npx", nil
		}
		return "", fs.ErrNotExist
	}
	executable, arguments, err := wranglerCommand(lookup)
	if err != nil || executable != "/usr/bin/npx" || len(arguments) != 2 || arguments[1] != wranglerPackage {
		t.Fatalf("wrangler fallback = %q %#v, %v", executable, arguments, err)
	}
}

func containsExact(values []string, wanted string) bool {
	for _, value := range values {
		if value == wanted {
			return true
		}
	}
	return false
}
