package auth

import (
	"errors"
	"testing"
	"time"
)

func TestLoginLifecycle(t *testing.T) {
	clock := time.Date(2026, 9, 2, 12, 0, 0, 0, time.UTC)
	manager := New("correct-token")
	manager.now = func() time.Time { return clock }

	if _, _, err := manager.Login("phone", "wrong"); !errors.Is(err, ErrInvalidCredentials) {
		t.Fatalf("wrong token error = %v", err)
	}
	sessionID, expires, err := manager.Login("phone", "correct-token")
	if err != nil {
		t.Fatal(err)
	}
	if !expires.Equal(clock.Add(SessionDuration)) || !manager.Valid(sessionID) {
		t.Fatal("new browser session should be valid")
	}
	manager.Logout(sessionID)
	if manager.Valid(sessionID) {
		t.Fatal("logged-out browser session is still valid")
	}

	sessionID, _, err = manager.Login("phone", "correct-token")
	if err != nil {
		t.Fatal(err)
	}
	clock = clock.Add(SessionDuration + time.Second)
	if manager.Valid(sessionID) {
		t.Fatal("expired browser session is still valid")
	}
}

func TestLoginRateLimit(t *testing.T) {
	manager := New("correct-token")
	for attempt := 0; attempt < 5; attempt++ {
		if _, _, err := manager.Login("attacker", "wrong"); !errors.Is(err, ErrInvalidCredentials) {
			t.Fatalf("attempt %d error = %v", attempt, err)
		}
	}
	if _, _, err := manager.Login("attacker", "correct-token"); !errors.Is(err, ErrRateLimited) {
		t.Fatalf("rate-limited login error = %v", err)
	}
	if _, _, err := manager.Login("different-client", "correct-token"); err != nil {
		t.Fatalf("rate limit leaked across clients: %v", err)
	}
}
