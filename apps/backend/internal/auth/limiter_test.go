package auth

import (
	"sync"
	"testing"
	"time"
)

func TestLimiterAdmitsAtMostTheBudgetUnderAParallelBurst(t *testing.T) {
	const budget = 5
	limiter := NewLimiter(budget, time.Minute)

	// Every request checks and charges in one step, so a burst that all arrives
	// before any failure is recorded still cannot exceed the budget.
	var (
		start    = make(chan struct{})
		wait     sync.WaitGroup
		mu       sync.Mutex
		admitted int
	)
	for attempt := 0; attempt < 200; attempt++ {
		wait.Add(1)
		go func() {
			defer wait.Done()
			<-start
			reservation, ok := limiter.Acquire("burst")
			if !ok {
				return
			}
			reservation.Commit()
			mu.Lock()
			admitted++
			mu.Unlock()
		}()
	}
	close(start)
	wait.Wait()
	if admitted != budget {
		t.Fatalf("admitted %d attempts, want %d", admitted, budget)
	}
	if _, ok := limiter.Acquire("burst"); ok {
		t.Fatal("an exhausted client was admitted again")
	}
	if _, ok := limiter.Acquire("someone-else"); !ok {
		t.Fatal("one client's burst blocked another")
	}
}

func TestLimiterReleasesSuccessfulAttempts(t *testing.T) {
	limiter := NewLimiter(2, time.Minute)
	for attempt := 0; attempt < 50; attempt++ {
		reservation, ok := limiter.Acquire("owner")
		if !ok {
			t.Fatalf("attempt %d was rejected even though every earlier one succeeded", attempt)
		}
		reservation.Release()
	}

	// A failure charges the budget, and a later success forgives it.
	first, _ := limiter.Acquire("owner")
	first.Commit()
	second, ok := limiter.Acquire("owner")
	if !ok {
		t.Fatal("the second attempt should still fit the budget")
	}
	second.Release()
	if _, ok := limiter.Acquire("owner"); !ok {
		t.Fatal("a success did not forgive the earlier failure")
	}
}

func TestLimiterResolutionIsIdempotent(t *testing.T) {
	limiter := NewLimiter(1, time.Minute)
	reservation, ok := limiter.Acquire("owner")
	if !ok {
		t.Fatal("the first attempt should be admitted")
	}
	// The handlers pair `defer Commit()` with a Release on the success path.
	reservation.Release()
	reservation.Commit()
	if _, ok := limiter.Acquire("owner"); !ok {
		t.Fatal("a committed-after-release reservation re-charged the budget")
	}
	var absent *Reservation
	absent.Commit()
	absent.Release()
}

func TestLimiterWindowExpires(t *testing.T) {
	limiter := NewLimiter(2, time.Minute)
	start := time.Now()
	for attempt := 0; attempt < 2; attempt++ {
		reservation, ok := limiter.AcquireAt("owner", start)
		if !ok {
			t.Fatalf("attempt %d was rejected", attempt)
		}
		reservation.Commit()
	}
	if _, ok := limiter.AcquireAt("owner", start.Add(30*time.Second)); ok {
		t.Fatal("the budget was not enforced inside the window")
	}
	if _, ok := limiter.AcquireAt("owner", start.Add(2*time.Minute)); !ok {
		t.Fatal("the budget did not recover after the window")
	}
}

func TestManagerLoginBurstCannotExceedTheTokenBudget(t *testing.T) {
	manager := New("correct-token")
	var (
		start = make(chan struct{})
		wait  sync.WaitGroup
		mu    sync.Mutex
		rate  int
	)
	for attempt := 0; attempt < 100; attempt++ {
		wait.Add(1)
		go func() {
			defer wait.Done()
			<-start
			if _, _, err := manager.Login("burst", "wrong"); err == ErrRateLimited {
				mu.Lock()
				rate++
				mu.Unlock()
			}
		}()
	}
	close(start)
	wait.Wait()
	if rate != 100-TokenAttemptMaximum {
		t.Fatalf("%d of 100 parallel guesses were rate-limited, want %d", rate, 100-TokenAttemptMaximum)
	}
	if _, _, err := manager.Login("burst", "correct-token"); err != ErrRateLimited {
		t.Fatalf("a spent budget still accepted a login: %v", err)
	}
}
