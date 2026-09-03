package terminalhistory

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	_ "modernc.org/sqlite"

	"termlinks/backend/internal/session"
)

const (
	MaxFavorites = 100
	MaxRecent    = 10
	maxNameRunes = 80
	maxCwdBytes  = 4096
)

var (
	ErrNotFound      = errors.New("saved terminal not found")
	ErrFavoriteLimit = fmt.Errorf("favorite limit reached (%d)", MaxFavorites)
)

type Entry struct {
	ID              string     `json:"id"`
	SourceSessionID string     `json:"sourceSessionId"`
	ActiveSessionID string     `json:"activeSessionId,omitempty"`
	Name            string     `json:"name"`
	Cwd             string     `json:"cwd"`
	Favorite        bool       `json:"favorite"`
	CreatedAt       time.Time  `json:"createdAt"`
	UpdatedAt       time.Time  `json:"updatedAt"`
	LastOpenedAt    time.Time  `json:"lastOpenedAt"`
	LastClosedAt    *time.Time `json:"lastClosedAt,omitempty"`
}

type Store struct {
	db   *sql.DB
	path string
}

func Open(path string) (*Store, error) {
	if path == "" || !filepath.IsAbs(path) {
		return nil, errors.New("terminal history database path must be absolute")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, fmt.Errorf("create terminal history directory: %w", err)
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("open terminal history database: %w", err)
	}
	db.SetMaxOpenConns(1)
	store := &Store{db: db, path: path}
	if err := store.initialize(context.Background()); err != nil {
		_ = db.Close()
		return nil, err
	}
	if err := store.secureFiles(); err != nil {
		_ = db.Close()
		return nil, err
	}
	return store, nil
}

func (s *Store) initialize(ctx context.Context) error {
	statements := []string{
		`PRAGMA journal_mode = WAL`,
		`PRAGMA synchronous = NORMAL`,
		`PRAGMA busy_timeout = 5000`,
		`CREATE TABLE IF NOT EXISTS saved_terminals (
			id TEXT PRIMARY KEY,
			source_session_id TEXT NOT NULL UNIQUE,
			active_session_id TEXT NOT NULL DEFAULT '',
			name TEXT NOT NULL,
			cwd TEXT NOT NULL,
			favorite INTEGER NOT NULL DEFAULT 0,
			created_at TEXT NOT NULL,
			updated_at TEXT NOT NULL,
			last_opened_at TEXT NOT NULL,
			last_closed_at TEXT
		)`,
		`CREATE UNIQUE INDEX IF NOT EXISTS idx_saved_terminals_active
			ON saved_terminals(active_session_id) WHERE active_session_id != ''`,
		`CREATE INDEX IF NOT EXISTS idx_saved_terminals_activity
			ON saved_terminals(favorite DESC, last_closed_at DESC, last_opened_at DESC)`,
	}
	for _, statement := range statements {
		if _, err := s.db.ExecContext(ctx, statement); err != nil {
			return fmt.Errorf("initialize terminal history database: %w", err)
		}
	}
	return nil
}

func (s *Store) Close() error {
	_, _ = s.db.Exec(`PRAGMA wal_checkpoint(TRUNCATE)`)
	err := s.db.Close()
	if secureErr := s.secureFiles(); err == nil {
		err = secureErr
	}
	return err
}

func (s *Store) secureFiles() error {
	for _, name := range []string{s.path, s.path + "-wal", s.path + "-shm"} {
		if err := os.Chmod(name, 0o600); err != nil && !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("secure terminal history database: %w", err)
		}
	}
	return nil
}

func (s *Store) Reconcile(ctx context.Context, sessions []session.Info) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	known := make(map[string]struct{}, len(sessions))
	for _, info := range sessions {
		known[info.ID] = struct{}{}
		if info.Running || info.EndedAt == nil {
			continue
		}
		if err := reconcileEnded(ctx, tx, info); err != nil {
			return err
		}
	}
	if err := clearMissingActiveSessions(ctx, tx, known); err != nil {
		return err
	}
	if err := pruneRecent(ctx, tx); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) RecordEnded(ctx context.Context, info session.Info) error {
	if info.Running || info.EndedAt == nil {
		return errors.New("terminal session has not ended")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := reconcileEnded(ctx, tx, info); err != nil {
		return err
	}
	if err := pruneRecent(ctx, tx); err != nil {
		return err
	}
	return tx.Commit()
}

func reconcileEnded(ctx context.Context, tx *sql.Tx, info session.Info) error {
	name, cwd, err := safeMetadata(info)
	if err != nil {
		return err
	}
	var id, activeID string
	var closed sql.NullString
	err = tx.QueryRowContext(ctx, `SELECT id,active_session_id,last_closed_at FROM saved_terminals
		WHERE source_session_id=? OR active_session_id=? LIMIT 1`, info.ID, info.ID).Scan(&id, &activeID, &closed)
	if errors.Is(err, sql.ErrNoRows) {
		now := info.EndedAt.UTC()
		id, err = newID()
		if err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, `INSERT INTO saved_terminals
			(id,source_session_id,name,cwd,created_at,updated_at,last_opened_at,last_closed_at)
			VALUES(?,?,?,?,?,?,?,?)`, id, info.ID, name, cwd, encodeTime(info.StartedAt), encodeTime(now), encodeTime(info.StartedAt), encodeTime(now))
		return err
	}
	if err != nil {
		return err
	}
	if closed.Valid && activeID != info.ID {
		return nil
	}
	ended := encodeTime(*info.EndedAt)
	_, err = tx.ExecContext(ctx, `UPDATE saved_terminals SET active_session_id=CASE WHEN active_session_id=? THEN '' ELSE active_session_id END,
		last_closed_at=?,updated_at=? WHERE id=?`, info.ID, ended, ended, id)
	return err
}

func clearMissingActiveSessions(ctx context.Context, tx *sql.Tx, known map[string]struct{}) error {
	rows, err := tx.QueryContext(ctx, `SELECT id,active_session_id FROM saved_terminals WHERE active_session_id != ''`)
	if err != nil {
		return err
	}
	type active struct{ id, sessionID string }
	missing := make([]active, 0)
	for rows.Next() {
		var item active
		if err := rows.Scan(&item.id, &item.sessionID); err != nil {
			rows.Close()
			return err
		}
		if _, ok := known[item.sessionID]; !ok {
			missing = append(missing, item)
		}
	}
	if err := rows.Close(); err != nil {
		return err
	}
	for _, item := range missing {
		if _, err := tx.ExecContext(ctx, `UPDATE saved_terminals SET active_session_id='' WHERE id=?`, item.id); err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) List(ctx context.Context) ([]Entry, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id,source_session_id,active_session_id,name,cwd,favorite,
		created_at,updated_at,last_opened_at,last_closed_at FROM saved_terminals
		ORDER BY favorite DESC,MAX(updated_at,last_opened_at,COALESCE(last_closed_at,'')) DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	entries := make([]Entry, 0)
	for rows.Next() {
		entry, err := scanEntry(rows)
		if err != nil {
			return nil, err
		}
		entries = append(entries, entry)
	}
	return entries, rows.Err()
}

func (s *Store) Get(ctx context.Context, id string) (Entry, error) {
	entry, err := scanEntry(s.db.QueryRowContext(ctx, `SELECT id,source_session_id,active_session_id,name,cwd,favorite,
		created_at,updated_at,last_opened_at,last_closed_at FROM saved_terminals WHERE id=?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return Entry{}, ErrNotFound
	}
	return entry, err
}

func (s *Store) SaveSession(ctx context.Context, info session.Info, favorite bool) (Entry, error) {
	name, cwd, err := safeMetadata(info)
	if err != nil {
		return Entry{}, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Entry{}, err
	}
	defer tx.Rollback()
	entry, found, err := findBySession(ctx, tx, info.ID)
	if err != nil {
		return Entry{}, err
	}
	if favorite && (!found || !entry.Favorite) {
		if err := enforceFavoriteLimit(ctx, tx); err != nil {
			return Entry{}, err
		}
	}
	now := time.Now().UTC()
	if !found {
		id, err := newID()
		if err != nil {
			return Entry{}, err
		}
		_, err = tx.ExecContext(ctx, `INSERT INTO saved_terminals
			(id,source_session_id,name,cwd,favorite,created_at,updated_at,last_opened_at)
			VALUES(?,?,?,?,?,?,?,?)`, id, info.ID, name, cwd, boolInt(favorite), encodeTime(now), encodeTime(now), encodeTime(now))
		entry.ID = id
	} else {
		_, err = tx.ExecContext(ctx, `UPDATE saved_terminals SET favorite=?,updated_at=? WHERE id=?`, boolInt(favorite), encodeTime(now), entry.ID)
	}
	if err != nil {
		return Entry{}, err
	}
	if err := pruneRecent(ctx, tx); err != nil {
		return Entry{}, err
	}
	if err := tx.Commit(); err != nil {
		return Entry{}, err
	}
	return s.Get(ctx, entry.ID)
}

func (s *Store) Update(ctx context.Context, id string, name *string, favorite *bool) (Entry, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Entry{}, err
	}
	defer tx.Rollback()
	entry, err := scanEntry(tx.QueryRowContext(ctx, `SELECT id,source_session_id,active_session_id,name,cwd,favorite,
		created_at,updated_at,last_opened_at,last_closed_at FROM saved_terminals WHERE id=?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return Entry{}, ErrNotFound
	}
	if err != nil {
		return Entry{}, err
	}
	if favorite != nil && *favorite && !entry.Favorite {
		if err := enforceFavoriteLimit(ctx, tx); err != nil {
			return Entry{}, err
		}
	}
	if name != nil {
		entry.Name = *name
	}
	if favorite != nil {
		entry.Favorite = *favorite
	}
	_, err = tx.ExecContext(ctx, `UPDATE saved_terminals SET name=?,favorite=?,updated_at=? WHERE id=?`,
		entry.Name, boolInt(entry.Favorite), encodeTime(time.Now().UTC()), id)
	if err != nil {
		return Entry{}, err
	}
	if err := pruneRecent(ctx, tx); err != nil {
		return Entry{}, err
	}
	if err := tx.Commit(); err != nil {
		return Entry{}, err
	}
	return s.Get(ctx, id)
}

func (s *Store) Delete(ctx context.Context, id string) error {
	result, err := s.db.ExecContext(ctx, `DELETE FROM saved_terminals WHERE id=?`, id)
	if err != nil {
		return err
	}
	changed, _ := result.RowsAffected()
	if changed == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *Store) MarkOpened(ctx context.Context, id, sessionID string, openedAt time.Time) (Entry, error) {
	result, err := s.db.ExecContext(ctx, `UPDATE saved_terminals SET active_session_id=?,last_opened_at=?,updated_at=? WHERE id=?`,
		sessionID, encodeTime(openedAt), encodeTime(openedAt), id)
	if err != nil {
		return Entry{}, err
	}
	changed, _ := result.RowsAffected()
	if changed == 0 {
		return Entry{}, ErrNotFound
	}
	return s.Get(ctx, id)
}

func (s *Store) RenameSession(ctx context.Context, sessionID, name string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE saved_terminals SET name=?,updated_at=?
		WHERE source_session_id=? OR active_session_id=?`, name, encodeTime(time.Now().UTC()), sessionID, sessionID)
	return err
}

func findBySession(ctx context.Context, tx *sql.Tx, sessionID string) (Entry, bool, error) {
	entry, err := scanEntry(tx.QueryRowContext(ctx, `SELECT id,source_session_id,active_session_id,name,cwd,favorite,
		created_at,updated_at,last_opened_at,last_closed_at FROM saved_terminals
		WHERE source_session_id=? OR active_session_id=? LIMIT 1`, sessionID, sessionID))
	if errors.Is(err, sql.ErrNoRows) {
		return Entry{}, false, nil
	}
	return entry, err == nil, err
}

func enforceFavoriteLimit(ctx context.Context, tx *sql.Tx) error {
	var count int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM saved_terminals WHERE favorite=1`).Scan(&count); err != nil {
		return err
	}
	if count >= MaxFavorites {
		return ErrFavoriteLimit
	}
	return nil
}

func pruneRecent(ctx context.Context, tx *sql.Tx) error {
	_, err := tx.ExecContext(ctx, `DELETE FROM saved_terminals WHERE favorite=0 AND id NOT IN (
		SELECT id FROM saved_terminals WHERE favorite=0
		ORDER BY MAX(updated_at,last_opened_at,COALESCE(last_closed_at,'')) DESC LIMIT ?
	)`, MaxRecent)
	return err
}

type rowScanner interface{ Scan(...any) error }

func scanEntry(row rowScanner) (Entry, error) {
	var entry Entry
	var favorite int
	var created, updated, opened string
	var closed sql.NullString
	err := row.Scan(&entry.ID, &entry.SourceSessionID, &entry.ActiveSessionID, &entry.Name, &entry.Cwd, &favorite,
		&created, &updated, &opened, &closed)
	if err != nil {
		return Entry{}, err
	}
	entry.Favorite = favorite != 0
	entry.CreatedAt, err = decodeTime(created)
	if err != nil {
		return Entry{}, err
	}
	entry.UpdatedAt, err = decodeTime(updated)
	if err != nil {
		return Entry{}, err
	}
	entry.LastOpenedAt, err = decodeTime(opened)
	if err != nil {
		return Entry{}, err
	}
	if closed.Valid {
		value, err := decodeTime(closed.String)
		if err != nil {
			return Entry{}, err
		}
		entry.LastClosedAt = &value
	}
	return entry, nil
}

func newID() (string, error) {
	data := make([]byte, 16)
	if _, err := rand.Read(data); err != nil {
		return "", err
	}
	return hex.EncodeToString(data), nil
}

func encodeTime(value time.Time) string { return value.UTC().Format(time.RFC3339Nano) }

func decodeTime(value string) (time.Time, error) { return time.Parse(time.RFC3339Nano, value) }

func boolInt(value bool) int {
	if value {
		return 1
	}
	return 0
}

func ValidID(id string) bool {
	if len(id) != 32 {
		return false
	}
	for _, character := range id {
		if (character < '0' || character > '9') && (character < 'a' || character > 'f') {
			return false
		}
	}
	return true
}

func safeMetadata(info session.Info) (string, string, error) {
	name := strings.TrimSpace(strings.ReplaceAll(info.Name, "\x00", ""))
	if name == "" {
		name = "Terminal"
	}
	runes := []rune(name)
	if len(runes) > maxNameRunes {
		name = string(runes[:maxNameRunes])
	}
	if info.Cwd == "" || len(info.Cwd) > maxCwdBytes || strings.ContainsRune(info.Cwd, 0) {
		return "", "", errors.New("terminal working directory is invalid")
	}
	return name, info.Cwd, nil
}
