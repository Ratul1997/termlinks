package config

import (
	"os"
	"path/filepath"
	"testing"
)

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
}

func TestResolvePathsRejectsRelativeOverride(t *testing.T) {
	t.Setenv("TERMLINKS_STATE_DIR", filepath.Join("relative", "state"))
	if _, err := ResolvePaths(); err == nil {
		t.Fatal("expected a relative state path to be rejected")
	}
}
