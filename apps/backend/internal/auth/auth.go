package auth

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"sync"
	"time"
)

var (
	ErrInvalidCredentials = errors.New("invalid credentials")
	ErrRateLimited        = errors.New("too many login attempts")
)

const (
	SessionDuration = 12 * time.Hour

	// TokenAttemptWindow and TokenAttemptMaximum bound portal token guessing.
	TokenAttemptWindow  = time.Minute
	TokenAttemptMaximum = 5
)

// SourceKind records how a browser session was authenticated so that revoking a
// passkey can invalidate exactly the sessions that passkey created.
type SourceKind string

const (
	SourceToken   SourceKind = "token"
	SourcePasskey SourceKind = "passkey"
)

// Source describes the credential a browser session was created with.
type Source struct {
	Kind SourceKind
	// CredentialID is the base64url credential ID for SourcePasskey, empty otherwise.
	CredentialID string
}

type sessionRecord struct {
	expires time.Time
	source  Source
}

type Manager struct {
	mu       sync.Mutex
	token    [32]byte
	sessions map[[32]byte]sessionRecord
	// failures is the portal token budget alone. Passkey ceremonies carry their
	// own budgets so they can never lock the owner out of token recovery.
	failures *Limiter
	now      func() time.Time
}

func New(token string) *Manager {
	return &Manager{
		token:    sha256.Sum256([]byte(token)),
		sessions: make(map[[32]byte]sessionRecord),
		failures: NewLimiter(TokenAttemptMaximum, TokenAttemptWindow),
		now:      time.Now,
	}
}

func (m *Manager) Login(clientID, candidate string) (string, time.Time, error) {
	now := m.now()
	// The attempt is charged the moment it is admitted, so a burst of parallel
	// guesses cannot exceed the budget; a correct token releases it again.
	attempt, allowed := m.failures.AcquireAt(clientID, now)
	if !allowed {
		return "", time.Time{}, ErrRateLimited
	}
	defer attempt.Commit()
	candidateHash := sha256.Sum256([]byte(candidate))
	if subtle.ConstantTimeCompare(candidateHash[:], m.token[:]) != 1 {
		return "", time.Time{}, ErrInvalidCredentials
	}
	attempt.Release()
	m.mu.Lock()
	defer m.mu.Unlock()
	m.pruneLocked(now)
	return m.createLocked(Source{Kind: SourceToken}, now)
}

// Create issues a browser session for an already-verified credential.
func (m *Manager) Create(source Source) (string, time.Time, error) {
	now := m.now()
	m.mu.Lock()
	defer m.mu.Unlock()
	m.pruneLocked(now)
	return m.createLocked(source, now)
}

func (m *Manager) Valid(sessionID string) bool {
	_, ok := m.Session(sessionID)
	return ok
}

// Session returns how a live browser session was authenticated.
func (m *Manager) Session(sessionID string) (Source, bool) {
	if sessionID == "" {
		return Source{}, false
	}
	now := m.now()
	hash := sha256.Sum256([]byte(sessionID))
	m.mu.Lock()
	defer m.mu.Unlock()
	record, ok := m.sessions[hash]
	if !ok || !record.expires.After(now) {
		delete(m.sessions, hash)
		return Source{}, false
	}
	return record.source, true
}

func (m *Manager) Logout(sessionID string) {
	hash := sha256.Sum256([]byte(sessionID))
	m.mu.Lock()
	delete(m.sessions, hash)
	m.mu.Unlock()
}

// RevokeCredential drops every session created by one passkey and reports how many
// were dropped. Token sessions and sessions from other passkeys stay usable.
func (m *Manager) RevokeCredential(credentialID string) int {
	if credentialID == "" {
		return 0
	}
	revoked := 0
	m.mu.Lock()
	defer m.mu.Unlock()
	for hash, record := range m.sessions {
		if record.source.Kind == SourcePasskey && record.source.CredentialID == credentialID {
			delete(m.sessions, hash)
			revoked++
		}
	}
	return revoked
}

func (m *Manager) createLocked(source Source, now time.Time) (string, time.Time, error) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", time.Time{}, err
	}
	sessionID := base64.RawURLEncoding.EncodeToString(raw)
	expires := now.Add(SessionDuration)
	m.sessions[sha256.Sum256([]byte(sessionID))] = sessionRecord{expires: expires, source: source}
	return sessionID, expires, nil
}

func (m *Manager) pruneLocked(now time.Time) {
	for sessionID, record := range m.sessions {
		if !record.expires.After(now) {
			delete(m.sessions, sessionID)
		}
	}
}
