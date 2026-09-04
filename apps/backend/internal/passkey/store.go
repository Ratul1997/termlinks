// Package passkey stores the owner's WebAuthn credentials and runs the
// registration and login ceremonies for the direct HTTPS portal.
package passkey

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/go-webauthn/webauthn/webauthn"

	_ "modernc.org/sqlite"
)

const (
	// MaxCredentials caps enrollment so a single owner cannot fill the database.
	MaxCredentials = 20
	// MaxLabelRunes bounds the user-provided passkey name.
	MaxLabelRunes = 60
	// ownerIDBytes is the length of the stable random WebAuthn user handle.
	ownerIDBytes = 32
)

var (
	ErrNotFound     = errors.New("passkey not found")
	ErrDuplicate    = errors.New("this passkey is already enrolled")
	ErrLimitReached = fmt.Errorf("passkey limit reached (%d)", MaxCredentials)
)

// Record is one enrolled passkey. Only the identifying and timing fields are
// exposed over the API; the credential itself never leaves the daemon.
type Record struct {
	ID         string     `json:"id"`
	Label      string     `json:"label"`
	CreatedAt  time.Time  `json:"createdAt"`
	LastUsedAt *time.Time `json:"lastUsedAt,omitempty"`

	Credential webauthn.Credential `json:"-"`
}

type Store struct {
	db   *sql.DB
	path string
}

// Open prepares the private authentication database that holds the owner ID and
// the enrolled credentials.
func Open(path string) (*Store, error) {
	if path == "" || !filepath.IsAbs(path) {
		return nil, errors.New("authentication database path must be absolute")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, fmt.Errorf("create authentication directory: %w", err)
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("open authentication database: %w", err)
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
		`CREATE TABLE IF NOT EXISTS auth_owner (
			id INTEGER PRIMARY KEY CHECK (id = 1),
			owner_id TEXT NOT NULL
		)`,
		`CREATE TABLE IF NOT EXISTS passkey_credentials (
			credential_id TEXT PRIMARY KEY,
			credential TEXT NOT NULL,
			label TEXT NOT NULL,
			sign_count INTEGER NOT NULL DEFAULT 0,
			created_at TEXT NOT NULL,
			last_used_at TEXT
		)`,
		`CREATE INDEX IF NOT EXISTS idx_passkey_credentials_created
			ON passkey_credentials(created_at)`,
	}
	for _, statement := range statements {
		if _, err := s.db.ExecContext(ctx, statement); err != nil {
			return fmt.Errorf("initialize authentication database: %w", err)
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
			return fmt.Errorf("secure authentication database: %w", err)
		}
	}
	return nil
}

// OwnerID returns the stable random WebAuthn user handle, creating it the first
// time it is needed. Every passkey on this installation belongs to it.
func (s *Store) OwnerID(ctx context.Context) ([]byte, error) {
	var encoded string
	err := s.db.QueryRowContext(ctx, `SELECT owner_id FROM auth_owner WHERE id = 1`).Scan(&encoded)
	if err == nil {
		owner, decodeErr := base64.RawURLEncoding.DecodeString(encoded)
		if decodeErr != nil || len(owner) == 0 {
			return nil, errors.New("stored owner identifier is invalid")
		}
		return owner, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("read owner identifier: %w", err)
	}
	owner := make([]byte, ownerIDBytes)
	if _, err := rand.Read(owner); err != nil {
		return nil, fmt.Errorf("generate owner identifier: %w", err)
	}
	if _, err := s.db.ExecContext(ctx,
		`INSERT INTO auth_owner (id, owner_id) VALUES (1, ?) ON CONFLICT(id) DO NOTHING`,
		base64.RawURLEncoding.EncodeToString(owner)); err != nil {
		return nil, fmt.Errorf("store owner identifier: %w", err)
	}
	return s.OwnerID(ctx)
}

// List returns every enrolled passkey, oldest first.
func (s *Store) List(ctx context.Context) ([]Record, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT credential_id, credential, label, created_at, last_used_at
		 FROM passkey_credentials ORDER BY created_at, credential_id`)
	if err != nil {
		return nil, fmt.Errorf("list passkeys: %w", err)
	}
	defer rows.Close()
	records := make([]Record, 0, 4)
	for rows.Next() {
		var (
			record     Record
			credential string
			created    string
			lastUsed   sql.NullString
		)
		if err := rows.Scan(&record.ID, &credential, &record.Label, &created, &lastUsed); err != nil {
			return nil, fmt.Errorf("read passkey: %w", err)
		}
		if err := json.Unmarshal([]byte(credential), &record.Credential); err != nil {
			return nil, fmt.Errorf("decode stored passkey: %w", err)
		}
		if record.CreatedAt, err = time.Parse(time.RFC3339Nano, created); err != nil {
			return nil, fmt.Errorf("decode passkey timestamp: %w", err)
		}
		if lastUsed.Valid {
			parsed, err := time.Parse(time.RFC3339Nano, lastUsed.String)
			if err != nil {
				return nil, fmt.Errorf("decode passkey timestamp: %w", err)
			}
			record.LastUsedAt = &parsed
		}
		records = append(records, record)
	}
	return records, rows.Err()
}

// Count reports how many passkeys are enrolled.
func (s *Store) Count(ctx context.Context) (int, error) {
	var count int
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM passkey_credentials`).Scan(&count); err != nil {
		return 0, fmt.Errorf("count passkeys: %w", err)
	}
	return count, nil
}

// Insert enrolls a verified credential under a label.
func (s *Store) Insert(ctx context.Context, label string, credential webauthn.Credential, createdAt time.Time) (Record, error) {
	label = NormalizeLabel(label)
	encoded, err := json.Marshal(credential)
	if err != nil {
		return Record{}, fmt.Errorf("encode passkey: %w", err)
	}
	id := EncodeCredentialID(credential.ID)
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Record{}, fmt.Errorf("enroll passkey: %w", err)
	}
	defer tx.Rollback()
	var count int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM passkey_credentials`).Scan(&count); err != nil {
		return Record{}, fmt.Errorf("count passkeys: %w", err)
	}
	if count >= MaxCredentials {
		return Record{}, ErrLimitReached
	}
	result, err := tx.ExecContext(ctx,
		`INSERT INTO passkey_credentials (credential_id, credential, label, sign_count, created_at, last_used_at)
		 VALUES (?, ?, ?, ?, ?, NULL) ON CONFLICT(credential_id) DO NOTHING`,
		id, string(encoded), label, int64(credential.Authenticator.SignCount), createdAt.UTC().Format(time.RFC3339Nano))
	if err != nil {
		return Record{}, fmt.Errorf("enroll passkey: %w", err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return Record{}, fmt.Errorf("enroll passkey: %w", err)
	}
	if affected == 0 {
		return Record{}, ErrDuplicate
	}
	if err := tx.Commit(); err != nil {
		return Record{}, fmt.Errorf("enroll passkey: %w", err)
	}
	return Record{ID: id, Label: label, CreatedAt: createdAt.UTC(), Credential: credential}, nil
}

// MarkUsed persists the credential state, including the signature counter, that a
// successful login produced.
//
// The read and the write share one transaction and neither the counter nor the
// last-used time is allowed to move backwards, so a write that lost a race
// cannot undo a newer one and weaken clone detection.
func (s *Store) MarkUsed(ctx context.Context, credential webauthn.Credential, usedAt time.Time) error {
	encoded, err := json.Marshal(credential)
	if err != nil {
		return fmt.Errorf("encode passkey: %w", err)
	}
	id := EncodeCredentialID(credential.ID)
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("record passkey use: %w", err)
	}
	defer tx.Rollback()
	var (
		storedCount int64
		storedUsed  sql.NullString
	)
	err = tx.QueryRowContext(ctx, `SELECT sign_count, last_used_at FROM passkey_credentials WHERE credential_id = ?`, id).
		Scan(&storedCount, &storedUsed)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return fmt.Errorf("record passkey use: %w", err)
	}
	count := int64(credential.Authenticator.SignCount)
	used := usedAt.UTC().Format(time.RFC3339Nano)
	if storedUsed.Valid && storedUsed.String > used {
		used = storedUsed.String
	}
	// A counter below the stored one means another login already recorded a newer
	// one, so keep the stored credential record and only carry the timestamp
	// forward. A zero counter is included: it must never replace a real one.
	if count < storedCount {
		if _, err := tx.ExecContext(ctx, `UPDATE passkey_credentials SET last_used_at = ? WHERE credential_id = ?`, used, id); err != nil {
			return fmt.Errorf("record passkey use: %w", err)
		}
		return tx.Commit()
	}
	if _, err := tx.ExecContext(ctx,
		`UPDATE passkey_credentials SET credential = ?, sign_count = ?, last_used_at = ? WHERE credential_id = ?`,
		string(encoded), count, used, id); err != nil {
		return fmt.Errorf("record passkey use: %w", err)
	}
	return tx.Commit()
}

// Delete removes one enrolled passkey.
func (s *Store) Delete(ctx context.Context, credentialID string) error {
	result, err := s.db.ExecContext(ctx, `DELETE FROM passkey_credentials WHERE credential_id = ?`, credentialID)
	if err != nil {
		return fmt.Errorf("remove passkey: %w", err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("remove passkey: %w", err)
	}
	if affected == 0 {
		return ErrNotFound
	}
	return nil
}

// EncodeCredentialID renders a raw credential ID the way the browser and the API
// exchange it.
func EncodeCredentialID(raw []byte) string {
	return base64.RawURLEncoding.EncodeToString(raw)
}

// ValidCredentialID reports whether a path parameter could name a credential.
func ValidCredentialID(value string) bool {
	if value == "" || len(value) > 512 {
		return false
	}
	raw, err := base64.RawURLEncoding.DecodeString(value)
	return err == nil && len(raw) > 0
}

// NormalizeLabel trims a user-provided passkey name to a single safe line.
func NormalizeLabel(label string) string {
	cleaned := strings.Map(func(character rune) rune {
		if character == '\t' {
			return ' '
		}
		if character < 0x20 || character == 0x7f {
			return -1
		}
		return character
	}, label)
	cleaned = strings.TrimSpace(cleaned)
	if cleaned == "" {
		return "Passkey"
	}
	runes := []rune(cleaned)
	if len(runes) > MaxLabelRunes {
		cleaned = strings.TrimSpace(string(runes[:MaxLabelRunes]))
	}
	return cleaned
}
