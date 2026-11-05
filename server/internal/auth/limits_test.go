package auth

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// withArgonSlots replaces the hashing slots and the key function for one
// test, so concurrency can be observed without spending 19 MiB per call.
func withArgonSlots(t *testing.T, slots int, key func(password, salt []byte, time, memory uint32, threads uint8, keyLen uint32) []byte) {
	t.Helper()
	oldSlots, oldKey := argonSlots, argonKey
	argonSlots, argonKey = make(chan struct{}, slots), key
	t.Cleanup(func() { argonSlots, argonKey = oldSlots, oldKey })
}

func TestArgonConcurrencyIsBounded(t *testing.T) {
	dummyHash() // computed once for good; keep the fake key out of it
	var running, peak atomic.Int32
	withArgonSlots(t, 2, func(_, _ []byte, _, _ uint32, _ uint8, keyLen uint32) []byte {
		n := running.Add(1)
		for {
			p := peak.Load()
			if n <= p || peak.CompareAndSwap(p, n) {
				break
			}
		}
		time.Sleep(5 * time.Millisecond)
		running.Add(-1)
		return make([]byte, keyLen)
	})

	var wg sync.WaitGroup
	for i := range 20 {
		wg.Go(func() {
			var err error
			if i%2 == 0 {
				_, err = HashPassword(t.Context(), "password")
			} else {
				VerifyDummy(t.Context(), "password")
			}
			if err != nil {
				t.Error(err)
			}
		})
	}
	wg.Wait()
	if p := peak.Load(); p > 2 {
		t.Fatalf("%d hashes ran at once, want at most 2", p)
	}
	if p := peak.Load(); p < 1 {
		t.Fatal("no hash ran")
	}
}

func TestArgonWaitRespectsContext(t *testing.T) {
	hash, err := HashPassword(t.Context(), "password")
	if err != nil {
		t.Fatal(err)
	}
	withArgonSlots(t, 1, argonKey)
	argonSlots <- struct{}{} // every slot busy
	defer func() { <-argonSlots }()

	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Millisecond)
	defer cancel()
	if _, err := VerifyPassword(ctx, hash, "password"); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("VerifyPassword: err = %v, want DeadlineExceeded while no slot is free", err)
	}
	if _, err := HashPassword(ctx, "password"); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("HashPassword: err = %v, want DeadlineExceeded while no slot is free", err)
	}
}

func TestDefaultArgonSlots(t *testing.T) {
	if c := cap(argonSlots); c < 2 {
		t.Fatalf("%d hashing slots, want at least 2", c)
	}
}

func TestLimiterAllowForgive(t *testing.T) {
	now := time.Date(2026, 11, 2, 12, 0, 0, 0, time.UTC)
	l := NewLimiter(2, time.Minute)
	l.now = func() time.Time { return now }

	for range 2 {
		if !l.Allow("user:a", "ip:x") {
			t.Fatal("refused within the limit")
		}
	}
	if l.Allow("user:a", "ip:y") {
		t.Fatal("allowed past the limit of one key")
	}
	// The refused call counted nothing for ip:y.
	for range 2 {
		if !l.Allow("ip:y") {
			t.Fatal("ip:y should have its whole budget")
		}
	}
	if l.Allow("ip:y") {
		t.Fatal("ip:y allowed past the limit")
	}
	l.Forgive("user:a", "ip:x")
	if !l.Allow("user:a", "ip:x") {
		t.Fatal("a forgiven attempt must free its place")
	}
	l.Forgive("nobody")
	if l.Blocked("nobody") {
		t.Fatal("forgiving an unknown key must not create a window")
	}
	now = now.Add(time.Minute)
	if !l.Allow("user:a") {
		t.Fatal("still refused after the window")
	}
}

func TestLimiterAllowIsAtomic(t *testing.T) {
	l := NewLimiter(10, time.Minute)
	var allowed atomic.Int32
	var wg sync.WaitGroup
	for range 200 {
		wg.Go(func() {
			if l.Allow("user:alice") {
				allowed.Add(1)
			}
		})
	}
	wg.Wait()
	if n := allowed.Load(); n != 10 {
		t.Fatalf("%d parallel attempts allowed, want exactly 10", n)
	}
}
