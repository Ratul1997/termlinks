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

const SessionDuration = 12 * time.Hour

type Manager struct {
	mu       sync.Mutex
	token    [32]byte
	sessions map[[32]byte]time.Time
	attempts map[string][]time.Time
	now      func() time.Time
}

func New(token string) *Manager {
	return &Manager{
		token:    sha256.Sum256([]byte(token)),
		sessions: make(map[[32]byte]time.Time),
		attempts: make(map[string][]time.Time),
		now:      time.Now,
	}
}

func (m *Manager) Login(clientID, candidate string) (string, time.Time, error) {
	now := m.now()
	m.mu.Lock()
	defer m.mu.Unlock()
	for sessionID, expires := range m.sessions {
		if !expires.After(now) {
			delete(m.sessions, sessionID)
		}
	}
	cutoff := now.Add(-time.Minute)
	for id, attempts := range m.attempts {
		kept := attempts[:0]
		for _, attempt := range attempts {
			if attempt.After(cutoff) {
				kept = append(kept, attempt)
			}
		}
		if len(kept) == 0 {
			delete(m.attempts, id)
		} else {
			m.attempts[id] = kept
		}
	}
	recent := m.attempts[clientID][:0]
	for _, attempt := range m.attempts[clientID] {
		if attempt.After(cutoff) {
			recent = append(recent, attempt)
		}
	}
	if len(recent) >= 5 {
		m.attempts[clientID] = recent
		return "", time.Time{}, ErrRateLimited
	}
	recent = append(recent, now)
	m.attempts[clientID] = recent
	candidateHash := sha256.Sum256([]byte(candidate))
	if subtle.ConstantTimeCompare(candidateHash[:], m.token[:]) != 1 {
		return "", time.Time{}, ErrInvalidCredentials
	}
	delete(m.attempts, clientID)
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", time.Time{}, err
	}
	sessionID := base64.RawURLEncoding.EncodeToString(raw)
	expires := now.Add(SessionDuration)
	m.sessions[sha256.Sum256([]byte(sessionID))] = expires
	return sessionID, expires, nil
}

func (m *Manager) Valid(sessionID string) bool {
	if sessionID == "" {
		return false
	}
	now := m.now()
	hash := sha256.Sum256([]byte(sessionID))
	m.mu.Lock()
	defer m.mu.Unlock()
	expires, ok := m.sessions[hash]
	if !ok || !expires.After(now) {
		delete(m.sessions, hash)
		return false
	}
	return true
}

func (m *Manager) Logout(sessionID string) {
	hash := sha256.Sum256([]byte(sessionID))
	m.mu.Lock()
	delete(m.sessions, hash)
	m.mu.Unlock()
}
