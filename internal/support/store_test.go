package support

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestMemoryRoutesAreScopedAndExpire(t *testing.T) {
	s := NewMemory()
	ctx := context.Background()
	now := time.Now()
	s.now = func() time.Time { return now }
	key := RouteKey(-100, 5)
	_, err := s.Update(ctx, key, func(r *Record) error {
		*r = Record{Kind: "route", UserID: 42, SourceMessageID: 3, ExpiresAt: now.Add(MessageLifetime)}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, found, err := s.Get(ctx, RouteKey(-101, 5)); err != nil || found {
		t.Fatal("cross-group route", err)
	}
	if r, found, err := s.Get(ctx, key); err != nil || !found || r.UserID != 42 {
		t.Fatal("missing route", err)
	}
	now = now.Add(MessageLifetime)
	if _, found, err := s.Get(ctx, key); err != nil || found {
		t.Fatal("expired route", err)
	}
}
func TestMemoryConcurrentDeliveryClaimDoesNotRepeat(t *testing.T) {
	s := NewMemory()
	ctx := context.Background()
	key := DeliveryKey("customer", 42, 8)
	var claims atomic.Int32
	var wg sync.WaitGroup
	for range 20 {
		wg.Go(func() {
			_, err := s.Update(ctx, key, func(r *Record) error {
				if r.Status != "" {
					return ErrDuplicate
				}
				*r = Record{Kind: "delivery", UserID: 42, SourceMessageID: 8, Token: "token", Status: "posting", ExpiresAt: time.Now().Add(MessageLifetime)}
				claims.Add(1)
				return nil
			})
			if err != nil && !errors.Is(err, ErrDuplicate) {
				t.Error(err)
			}
		})
	}
	wg.Wait()
	if claims.Load() != 1 {
		t.Fatal("duplicate claim", claims.Load())
	}
}
func TestMemoryRejectsInvalidAndBoundedMetadata(t *testing.T) {
	s := NewMemory()
	ctx := context.Background()
	for _, key := range []string{"mode:-1", "route:42:1", "delivery:staff:42:1", "delivery:customer:-100:1", "route:-100:0", "mode:01"} {
		if _, _, err := s.Get(ctx, key); !errors.Is(err, ErrInvalid) {
			t.Fatal("invalid key accepted", key)
		}
	}
	_, err := s.Update(ctx, ModeKey(42), func(r *Record) error {
		*r = Record{Kind: "mode", UserID: 42, Status: "active", ExpiresAt: time.Now().Add(2 * ModeLifetime)}
		return nil
	})
	if !errors.Is(err, ErrInvalid) {
		t.Fatal("mode retention accepted", err)
	}
}
