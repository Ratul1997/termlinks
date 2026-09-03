package terminalhistory

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"termlinks/backend/internal/session"
)

func TestReconcileRecordsExactCloseTimeOnlyOnce(t *testing.T) {
	store := openTestStore(t)
	ctx := context.Background()
	started := time.Date(2026, 9, 3, 12, 0, 0, 0, time.UTC)
	ended := started.Add(5 * time.Minute)
	info := endedSession(1, "build", "/tmp/project", started, ended)

	if err := store.RecordEnded(ctx, info); err != nil {
		t.Fatal(err)
	}
	first := onlyEntry(t, store)
	if first.LastClosedAt == nil || !first.LastClosedAt.Equal(ended) {
		t.Fatalf("last closed = %v, want %v", first.LastClosedAt, ended)
	}
	updated := first.UpdatedAt

	if err := store.RecordEnded(ctx, info); err != nil {
		t.Fatal(err)
	}
	second := onlyEntry(t, store)
	if !second.UpdatedAt.Equal(updated) || !second.LastClosedAt.Equal(ended) {
		t.Fatalf("repeated reconciliation changed timestamps: before=%#v after=%#v", first, second)
	}
}

func TestSessionsWithSameNameAndDirectoryRemainDistinct(t *testing.T) {
	store := openTestStore(t)
	started := time.Date(2026, 9, 3, 12, 0, 0, 0, time.UTC)
	items := []session.Info{
		endedSession(1, "shell", "/tmp/project", started, started.Add(time.Minute)),
		endedSession(2, "shell", "/tmp/project", started.Add(2*time.Minute), started.Add(3*time.Minute)),
	}
	if err := store.Reconcile(context.Background(), items); err != nil {
		t.Fatal(err)
	}
	entries, err := store.List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 2 || entries[0].SourceSessionID == entries[1].SourceSessionID {
		t.Fatalf("distinct sessions were collapsed: %#v", entries)
	}
}

func TestRecentAndFavoriteLimits(t *testing.T) {
	store := openTestStore(t)
	ctx := context.Background()
	started := time.Date(2026, 9, 3, 12, 0, 0, 0, time.UTC)
	ended := make([]session.Info, 0, MaxRecent+2)
	for index := 0; index < MaxRecent+2; index++ {
		begin := started.Add(time.Duration(index) * time.Minute)
		ended = append(ended, endedSession(index+1, fmt.Sprintf("recent-%d", index), "/tmp", begin, begin.Add(time.Second)))
	}
	if err := store.Reconcile(ctx, ended); err != nil {
		t.Fatal(err)
	}
	entries, err := store.List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != MaxRecent {
		t.Fatalf("recent count = %d, want %d", len(entries), MaxRecent)
	}
	for index := 0; index < MaxFavorites; index++ {
		info := session.Info{ID: testID(1000 + index), Name: fmt.Sprintf("favorite-%d", index), Cwd: "/tmp", StartedAt: started, Running: true}
		if _, err := store.SaveSession(ctx, info, true); err != nil {
			t.Fatalf("save favorite %d: %v", index, err)
		}
	}
	extra := session.Info{ID: testID(9999), Name: "too many", Cwd: "/tmp", StartedAt: started, Running: true}
	if _, err := store.SaveSession(ctx, extra, true); !errors.Is(err, ErrFavoriteLimit) {
		t.Fatalf("favorite over limit error = %v, want %v", err, ErrFavoriteLimit)
	}
}

func TestReopenedTerminalKeepsStableRecord(t *testing.T) {
	store := openTestStore(t)
	ctx := context.Background()
	started := time.Date(2026, 9, 3, 12, 0, 0, 0, time.UTC)
	firstEnd := started.Add(time.Minute)
	original := endedSession(1, "project", "/tmp/project", started, firstEnd)
	if err := store.Reconcile(ctx, []session.Info{original}); err != nil {
		t.Fatal(err)
	}
	entry := onlyEntry(t, store)
	reopenedAt := started.Add(2 * time.Minute)
	reopenedID := testID(2)
	if _, err := store.MarkOpened(ctx, entry.ID, reopenedID, reopenedAt); err != nil {
		t.Fatal(err)
	}
	secondEnd := started.Add(4 * time.Minute)
	reopened := endedSession(2, "project", "/tmp/project", reopenedAt, secondEnd)
	if err := store.Reconcile(ctx, []session.Info{original, reopened}); err != nil {
		t.Fatal(err)
	}
	updated := onlyEntry(t, store)
	if updated.ID != entry.ID || updated.ActiveSessionID != "" || updated.LastClosedAt == nil || !updated.LastClosedAt.Equal(secondEnd) {
		t.Fatalf("reopened record was not updated in place: %#v", updated)
	}
}

func TestStorePersistsAcrossRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "terminal-history.db")
	store, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	ended := time.Date(2026, 9, 3, 12, 1, 0, 0, time.UTC)
	if err := store.Reconcile(context.Background(), []session.Info{endedSession(1, "persisted", "/tmp", ended.Add(-time.Minute), ended)}); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if got := onlyEntry(t, store); got.Name != "persisted" {
		t.Fatalf("persisted name = %q", got.Name)
	}
}

func openTestStore(t *testing.T) *Store {
	t.Helper()
	store, err := Open(filepath.Join(t.TempDir(), "terminal-history.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store
}

func onlyEntry(t *testing.T, store *Store) Entry {
	t.Helper()
	entries, err := store.List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Fatalf("entry count = %d, want 1", len(entries))
	}
	return entries[0]
}

func endedSession(id int, name, cwd string, started, ended time.Time) session.Info {
	return session.Info{ID: testID(id), Name: name, Cwd: cwd, StartedAt: started, EndedAt: &ended, Running: false}
}

func testID(value int) string { return fmt.Sprintf("%032x", value) }
